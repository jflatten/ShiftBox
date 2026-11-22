package guest

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/jflatten/ShiftBox/internal/proxy"
)

// ServeTunnel registers services on an outbound guest mTLS session and serves
// only the registered local targets until cancellation or disconnection.
func ServeTunnel(ctx context.Context, address string, config *tls.Config, protocol string, generation int64, services []proxy.TunnelService) error {
	return serveTunnel(ctx, address, config, protocol, generation, services, nil)
}

func serveTunnel(ctx context.Context, address string, config *tls.Config, protocol string, generation int64, services []proxy.TunnelService,
	sshHandler func(context.Context, net.Conn),
) error {
	if config == nil || config.InsecureSkipVerify || config.MinVersion < tls.VersionTLS13 ||
		len(config.Certificates) != 1 || protocol == "" || generation < 1 {
		return errors.New("invalid guest tunnel configuration")
	}
	allowed, err := proxy.ValidateTunnelServices(services)
	if err != nil {
		return err
	}
	allowlist, err := proxy.NewServiceAllowlist("local", generation, allowed)
	if err != nil {
		return err
	}
	if allowlist.Allows("local", generation, proxy.SSHService, proxy.SSHService) && sshHandler == nil {
		return errors.New("SSH tunnel service has no server")
	}
	targets := make(map[string]string, len(services))
	for _, service := range services {
		targets[service.Key] = service.Target
	}
	handshake, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	dialer := tls.Dialer{Config: config}
	conn, err := dialer.DialContext(handshake, "tcp", address)
	if err != nil {
		return err
	}
	defer conn.Close()
	session, err := yamux.Client(conn, proxy.TunnelYamuxConfig())
	if err != nil {
		return err
	}
	runCtx, stop := context.WithCancel(ctx)
	var streams sync.WaitGroup
	defer func() {
		stop()
		_ = session.Close()
		streams.Wait()
	}()
	closeOnCancel := context.AfterFunc(runCtx, func() { _ = session.Close() })
	defer closeOnCancel()
	if err := registerTunnel(session, proxy.TunnelRegistration{
		Type: "register", ProtocolVersion: protocol, Generation: generation, Services: services,
	}); err != nil {
		return err
	}
	slots := make(chan struct{}, 32)
	for {
		stream, err := session.AcceptStreamWithContext(runCtx)
		if err != nil {
			return err
		}
		select {
		case slots <- struct{}{}:
			streams.Go(func() {
				defer func() { <-slots }()
				serveTunnelStream(runCtx, stream, generation, allowlist, targets, sshHandler)
			})
		default:
			stream.Close()
		}
	}
}

func registerTunnel(session *yamux.Session, registration proxy.TunnelRegistration) error {
	control, err := session.OpenStream()
	if err != nil {
		return err
	}
	defer control.Close()
	_ = control.SetDeadline(time.Now().Add(10 * time.Second))
	if err := proxy.WriteTunnelFrame(control, registration); err != nil {
		return err
	}
	var reply struct{ Accepted bool }
	if err := proxy.ReadTunnelFrame(control, &reply); err != nil || !reply.Accepted {
		return errors.New("guest tunnel registration was not accepted")
	}
	return nil
}

// serveTunnelStream takes the dial address from the guest's registered
// services. The incoming stream can only name a service in this generation.
func serveTunnelStream(ctx context.Context, stream *yamux.Stream, generation int64, allowlist *proxy.ServiceAllowlist,
	targets map[string]string, sshHandler func(context.Context, net.Conn),
) {
	defer stream.Close()
	_ = stream.SetReadDeadline(time.Now().Add(10 * time.Second))
	var header proxy.StreamHeader
	if err := proxy.ReadTunnelFrame(stream, &header); err != nil || !proxy.ValidStreamHeader(header) ||
		!allowlist.Allows("local", generation, header.ServiceKey, header.Protocol) {
		return
	}
	_ = stream.SetReadDeadline(time.Time{})
	if header.ServiceKey == proxy.SSHService {
		sshHandler(ctx, stream)
		return
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	target := targets[header.ServiceKey]
	network := "tcp"
	if proxy.IsWorkbenchSocket(header.ServiceKey, target) {
		network = "unix"
	}
	local, err := dialer.DialContext(ctx, network, target)
	if err != nil {
		return
	}
	defer local.Close()
	cancel := context.AfterFunc(ctx, func() { _ = local.Close() })
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(local, stream)
		if conn, ok := local.(interface{ CloseWrite() error }); ok {
			_ = conn.CloseWrite()
		}
	}()
	_, _ = io.Copy(stream, local)
	_ = stream.SetReadDeadline(time.Now())
	_ = local.Close()
	<-done
}
