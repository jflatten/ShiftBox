package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"regexp"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

const (
	tunnelHandshakeTimeout = 10 * time.Second
	maxTunnelFrame         = 16 << 10
)

var tunnelServiceKey = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// TunnelService declares a service bound to a loopback address in the guest.
type TunnelService struct {
	Key       string   `json:"key"`
	Target    string   `json:"target"`
	Protocols []string `json:"protocols"`
}

// TunnelRegistration is the first frame on the guest's control stream.
type TunnelRegistration struct {
	Type            string          `json:"type"`
	ProtocolVersion string          `json:"protocolVersion"`
	Generation      int64           `json:"workspaceGeneration"`
	Services        []TunnelService `json:"services"`
}

// StreamHeader identifies the service and request using a single stream.
type StreamHeader struct {
	ServiceKey string `json:"serviceKey"`
	RequestID  string `json:"requestId"`
	Protocol   string `json:"protocol"`
}

// TunnelAuthority comes from the current controller registration for a TLS leaf.
type TunnelAuthority struct {
	WorkspaceID, OwnerID string
	Generation           int64
	ExpiresAt            time.Time
}

// Tunnels stores active reverse sessions, indexed by authenticated workspace.
// The registry also tracks these connections for immediate revocation.
type Tunnels struct {
	Registry *Registry
	mu       sync.Mutex
	active   map[string]*activeTunnel
}

type activeTunnel struct {
	session   *yamux.Session
	allowlist *ServiceAllowlist
	authority TunnelAuthority
	check     func(context.Context) (TunnelAuthority, error)
}

// Serve authenticates and registers a guest-initiated TLS/Yamux connection.
func (t *Tunnels) Serve(ctx context.Context, conn *tls.Conn, protocol string,
	authenticate func(context.Context, *x509.Certificate) (TunnelAuthority, error),
) error {
	defer conn.Close()
	if t.Registry == nil || authenticate == nil || protocol == "" {
		return errors.New("guest tunnel listener is incompletely configured")
	}
	handshake, cancel := context.WithTimeout(ctx, tunnelHandshakeTimeout)
	defer cancel()
	if err := conn.HandshakeContext(handshake); err != nil {
		return err
	}
	state := conn.ConnectionState()
	if state.Version < tls.VersionTLS13 || len(state.VerifiedChains) == 0 || len(state.VerifiedChains[0]) == 0 {
		return errors.New("guest tunnel requires verified TLS identity")
	}
	leaf := state.VerifiedChains[0][0]
	auth, err := authenticate(handshake, leaf)
	if err != nil {
		return err
	}
	if auth.WorkspaceID == "" || auth.OwnerID == "" || auth.Generation < 1 {
		return errors.New("invalid guest tunnel registration authority")
	}
	auth.ExpiresAt = auth.ExpiresAt.UTC()
	if leaf.NotAfter.Before(auth.ExpiresAt) {
		auth.ExpiresAt = leaf.NotAfter
	}
	if !time.Now().Before(auth.ExpiresAt) {
		return errors.New("guest tunnel identity expired")
	}
	session, err := yamux.Server(conn, TunnelYamuxConfig())
	if err != nil {
		return err
	}
	defer session.Close()
	control, err := session.AcceptStreamWithContext(handshake)
	if err != nil {
		return err
	}
	defer control.Close()
	_ = control.SetDeadline(time.Now().Add(tunnelHandshakeTimeout))
	var registration TunnelRegistration
	if err := ReadTunnelFrame(control, &registration); err != nil || registration.Type != "register" ||
		registration.ProtocolVersion != protocol || registration.Generation != auth.Generation {
		return errors.New("invalid guest tunnel registration")
	}
	services, err := ValidateTunnelServices(registration.Services)
	if err != nil {
		return err
	}
	allowlist, err := NewServiceAllowlist(auth.WorkspaceID, auth.Generation, services)
	if err != nil {
		return err
	}
	entry := &activeTunnel{session: session, allowlist: allowlist, authority: auth,
		check: func(ctx context.Context) (TunnelAuthority, error) { return authenticate(ctx, leaf) }}
	unregister, err := t.Registry.Register(Scope{UserID: auth.OwnerID, WorkspaceID: auth.WorkspaceID}, func() error {
		return entry.authorize(handshake)
	}, session.Close)
	if err != nil {
		return err
	}
	defer unregister()
	t.mu.Lock()
	old := t.active[auth.WorkspaceID]
	if t.active == nil {
		t.active = make(map[string]*activeTunnel)
	}
	t.active[auth.WorkspaceID] = entry
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		if t.active[auth.WorkspaceID] == entry {
			delete(t.active, auth.WorkspaceID)
		}
		t.mu.Unlock()
	}()
	if old != nil {
		_ = old.session.Close()
	}
	if err := WriteTunnelFrame(control, struct{ Accepted bool }{true}); err != nil {
		return err
	}
	_ = control.Close()
	expires := time.NewTimer(time.Until(auth.ExpiresAt))
	defer expires.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-session.CloseChan():
		return nil
	case <-expires.C:
		return errors.New("guest tunnel identity expired")
	}
}

// Open creates one stream to an advertised service after checking current guest
// authority. Callers must separately authorize the user's browser request.
func (t *Tunnels) Open(ctx context.Context, workspace string, generation int64, header StreamHeader) (net.Conn, error) {
	t.mu.Lock()
	entry := t.active[workspace]
	t.mu.Unlock()
	if entry == nil || !ValidStreamHeader(header) || !entry.allowlist.Allows(workspace, generation, header.ServiceKey, header.Protocol) {
		return nil, errors.New("guest tunnel service unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := entry.authorize(ctx); err != nil {
		if ctx.Err() == nil {
			_ = entry.session.Close()
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stream, err := entry.session.OpenStream()
	if err != nil {
		return nil, err
	}
	_ = stream.SetWriteDeadline(time.Now().Add(tunnelHandshakeTimeout))
	if err := WriteTunnelFrame(stream, header); err != nil {
		stream.Close()
		return nil, err
	}
	_ = stream.SetWriteDeadline(time.Time{})
	return stream, nil
}

func (t *activeTunnel) authorize(ctx context.Context) error {
	current, err := t.check(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	if current.WorkspaceID != t.authority.WorkspaceID || current.Generation != t.authority.Generation ||
		current.OwnerID != t.authority.OwnerID || !now.Before(t.authority.ExpiresAt) || !now.Before(current.ExpiresAt) {
		return errors.New("guest tunnel authority changed")
	}
	return nil
}

// ValidateTunnelServices admits only named HTTP and WebSocket loopback targets.
func ValidateTunnelServices(input []TunnelService) ([]Service, error) {
	if len(input) == 0 || len(input) > 64 {
		return nil, errors.New("invalid guest tunnel service count")
	}
	services := make([]Service, 0, len(input))
	for _, service := range input {
		target, err := netip.ParseAddrPort(service.Target)
		if err != nil || !target.Addr().IsLoopback() || target.Port() == 0 ||
			len(service.Key) > 63 || !tunnelServiceKey.MatchString(service.Key) || len(service.Protocols) > 2 {
			return nil, errors.New("invalid guest tunnel service")
		}
		services = append(services, Service{Key: service.Key, Protocols: service.Protocols})
	}
	return services, nil
}

// ValidStreamHeader bounds request metadata before it crosses the tunnel.
func ValidStreamHeader(h StreamHeader) bool {
	return len(h.ServiceKey) <= 63 && tunnelServiceKey.MatchString(h.ServiceKey) &&
		len(h.RequestID) <= 128 && tunnelServiceKey.MatchString(h.RequestID) &&
		(h.Protocol == "http" || h.Protocol == "websocket")
}

// TunnelYamuxConfig sets shared heartbeat and stream bounds.
func TunnelYamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.AcceptBacklog = 32
	cfg.StreamOpenTimeout = tunnelHandshakeTimeout
	cfg.StreamCloseTimeout = tunnelHandshakeTimeout
	cfg.LogOutput = io.Discard
	return cfg
}

// ReadTunnelFrame reads one bounded length-prefixed JSON header.
func ReadTunnelFrame(r io.Reader, target any) error {
	var size uint32
	if err := binary.Read(r, binary.BigEndian, &size); err != nil || size == 0 || size > maxTunnelFrame {
		return errors.New("invalid guest tunnel frame size")
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	// Decode into a strict schema without consuming following HTTP bytes.
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid guest tunnel frame")
	}
	return nil
}

// WriteTunnelFrame writes one bounded length-prefixed JSON header.
func WriteTunnelFrame(w io.Writer, frame any) error {
	data, err := json.Marshal(frame)
	if err != nil || len(data) > maxTunnelFrame {
		return errors.New("invalid guest tunnel frame")
	}
	if err := binary.Write(w, binary.BigEndian, uint32(len(data))); err != nil {
		return err
	}
	n, err := w.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}
