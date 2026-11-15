package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/release"
	"github.com/jflatten/ShiftBox/internal/secret"
	"github.com/jflatten/ShiftBox/internal/web"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// GuestListenerConfig assigns distinct binds and the guest-reachable TLS host.
type GuestListenerConfig struct {
	ActivityPolicy    *workspace.ActivityPolicy
	EnrollmentAddress string
	PollAddress       string
	PublicHost        string
	GitHub            *identity.GitHub
	Secrets           *secret.Service
	TunnelAddress     string
	Tunnels           *proxy.Tunnels
	Checkpoint        *backup.WriteFence
	GuestUpdates      func(context.Context, string) (release.GuestUpdate, error)
}

// GuestListeners owns the initial enrollment and authenticated poll listeners.
type GuestListeners struct {
	EnrollURL     string
	PollURL       string
	TunnelAddress string
	enroll        guestListener
	poll          guestListener
	tunnel        *guestTunnelListener
	closed        chan struct{}
	closeOnce     sync.Once
}

type guestListener struct {
	network net.Listener
	server  *http.Server
}

// ListenGuests prepares the listeners and derives the endpoints for enrollment
// media, including before UTM creates its shared network. A partial bind is
// closed before returning an error.
func ListenGuests(config GuestListenerConfig, enrollment *guest.Enrollment, authority *guest.Authority,
	compatibility web.GuestCompatibility, now func() time.Time,
) (*GuestListeners, error) {
	if enrollment == nil || authority == nil || now == nil || compatibility.Protocol == "" || compatibility.CurrentRelease == "" ||
		config.EnrollmentAddress == "" || config.PollAddress == "" ||
		(config.TunnelAddress == "") != (config.Tunnels == nil) ||
		config.Tunnels != nil && config.Tunnels.Registry == nil {
		return nil, errors.New("internal guest listeners are incompletely configured")
	}
	certificates := &internalServerCertificate{authority: authority, name: config.PublicHost, now: now}
	if _, err := certificates.current(nil); err != nil {
		return nil, err
	}
	enroll, err := listenGuest(config.EnrollmentAddress, guest.EnrollmentTLS(tls.Certificate{}),
		checkpointRequest(config.Checkpoint, web.GuestEnrollmentHandler(compatibility, enrollment.Enroll, now)), certificates)
	if err != nil {
		return nil, err
	}
	internal := http.NewServeMux()
	internal.Handle(guest.RenewalPath, web.GuestRenewalHandler(enrollment.Renew, now))
	if config.GuestUpdates != nil {
		internal.Handle(guest.UpdateDownloadPath, web.GuestUpdateHandler(func(ctx context.Context, leaf *x509.Certificate, stamp time.Time) (string, error) {
			if config.Tunnels != nil {
				if err := registerGuestPollConnection(ctx, config.Tunnels.Registry, func() (guest.Contact, error) {
					return enrollment.Authenticate(ctx, leaf, now())
				}); err != nil {
					return "", err
				}
			}
			return enrollment.UpdateRelease(ctx, leaf, stamp)
		}, config.GuestUpdates, now))
	}
	if config.GitHub != nil {
		internal.Handle(guest.GitAccessTokenPath, web.GuestGitTokenHandler(enrollment.Authenticate, config.GitHub.AccessToken, now))
	}
	if config.Secrets != nil {
		internal.Handle("POST "+guest.SecretResolvePrefix, web.GuestSecretHandler(
			func(ctx context.Context, leaf *x509.Certificate, stamp time.Time, name string) ([]byte, string, error) {
				return enrollment.ResolveSecret(ctx, leaf, stamp, config.Secrets, name)
			}, now))
	}
	poll, err := listenGuest(config.PollAddress, guest.ServerTLS(tls.Certificate{}, authority), checkpointRequest(config.Checkpoint, internal), certificates)
	if err != nil {
		_ = enroll.network.Close()
		return nil, err
	}
	if config.Tunnels != nil {
		trackGuestPollConnections(poll.server)
	}
	listeners := &GuestListeners{
		EnrollURL: guestURL(config.PublicHost, enroll.network, "/internal/v1/enroll"),
		PollURL:   guestURL(config.PublicHost, poll.network, "/internal/v1/guest/poll"),
		enroll:    enroll, poll: poll, closed: make(chan struct{}),
	}
	if config.Tunnels != nil {
		tunnel, err := listenGuestTunnel(config.TunnelAddress, authority, certificates, config.Tunnels, compatibility.Protocol,
			func(ctx context.Context, leaf *x509.Certificate) (proxy.TunnelAuthority, error) {
				var contact guest.Contact
				err := withCheckpointActivity(ctx, config.Checkpoint, func() error {
					var err error
					contact, err = enrollment.Authenticate(ctx, leaf, now())
					return err
				})
				if err != nil {
					return proxy.TunnelAuthority{}, err
				}
				if contact.DesiredState != "running" {
					return proxy.TunnelAuthority{}, guest.ErrGuestUnauthorized
				}
				return proxy.TunnelAuthority{WorkspaceID: contact.WorkspaceID, OwnerID: contact.OwnerID,
					Generation: contact.Generation, ExpiresAt: leaf.NotAfter}, nil
			})
		if err != nil {
			listeners.Close()
			return nil, err
		}
		listeners.tunnel = tunnel
		listeners.TunnelAddress = guestAddress(config.PublicHost, tunnel.network)
	}
	exchange := func(ctx context.Context, leaf *x509.Certificate, stamp time.Time, report guest.PollReport) (guest.Contact, *guest.PollOperation, error) {
		report.ActivityPolicy = config.ActivityPolicy
		if config.Tunnels != nil {
			if err := registerGuestPollConnection(ctx, config.Tunnels.Registry, func() (guest.Contact, error) {
				return enrollment.AuthenticatePoll(ctx, leaf, stamp)
			}); err != nil {
				return guest.Contact{}, nil, err
			}
			report.IDEConnected = config.Tunnels.Registry.HasIDEConnection
		}
		return enrollment.Exchange(ctx, leaf, stamp, report)
	}
	internal.Handle("/internal/v1/guest/poll", web.GuestPollHandler(compatibility, exchange, now, listeners.TunnelAddress))
	return listeners, nil
}

func listenGuest(address string, config *tls.Config, handler http.Handler, certificates *internalServerCertificate) (guestListener, error) {
	listener, err := listenGuestNetwork(address, net.Listen)
	if err != nil {
		return guestListener{}, errors.New("cannot bind internal guest listener")
	}
	config.Certificates = nil
	config.GetCertificate = certificates.current
	server := &http.Server{
		Handler: handler, TLSConfig: config, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 20 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: time.Minute,
		ErrorLog: log.New(io.Discard, "", 0),
	}
	return guestListener{network: listener, server: server}, nil
}

func guestURL(host string, listener net.Listener, path string) string {
	return (&url.URL{Scheme: "https", Host: guestAddress(host, listener), Path: path}).String()
}

func guestAddress(host string, listener net.Listener) string {
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	return net.JoinHostPort(host, port)
}

// Serve runs internal listeners until shutdown or listener failure.
func (s *GuestListeners) Serve(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 3)
	count := 2
	for _, endpoint := range []guestListener{s.enroll, s.poll} {
		go func() { done <- endpoint.server.ServeTLS(endpoint.network, "", "") }()
	}
	if s.tunnel != nil {
		count++
		go func() { done <- s.tunnel.serve(runCtx) }()
	}
	var primary error
	select {
	case <-ctx.Done():
		primary = ctx.Err()
	case <-s.closed:
		primary = net.ErrClosed
	case primary = <-done:
		count--
	}
	cancel()
	s.Close()
	for range count {
		<-done
	}
	return primary
}

// Close releases the reserved listeners and active connections.
func (s *GuestListeners) Close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		for _, endpoint := range []guestListener{s.enroll, s.poll} {
			_ = endpoint.server.Close()
			_ = endpoint.network.Close()
		}
		if s.tunnel != nil {
			_ = s.tunnel.network.Close()
		}
	})
}

type internalServerCertificate struct {
	mu        sync.Mutex
	authority *guest.Authority
	name      string
	now       func() time.Time
	leaf      *tls.Certificate
}

func (s *internalServerCertificate) current(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.leaf != nil && !now.Before(s.leaf.Leaf.NotBefore) &&
		now.Before(s.leaf.Leaf.NotBefore.Add(4*time.Hour)) && now.Before(s.leaf.Leaf.NotAfter) {
		return s.leaf, nil
	}
	leaf, err := s.authority.ServerLeaf([]string{s.name}, now)
	if err != nil {
		return nil, err
	}
	s.leaf = &leaf
	return s.leaf, nil
}
