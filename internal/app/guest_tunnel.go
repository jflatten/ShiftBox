package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"sync"

	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/proxy"
)

type guestTunnelListener struct {
	network      net.Listener
	tls          *tls.Config
	tunnels      *proxy.Tunnels
	protocol     string
	authenticate func(context.Context, *x509.Certificate) (proxy.TunnelAuthority, error)
}

func listenGuestTunnel(address string, authority *guest.Authority, certificates *internalServerCertificate,
	tunnels *proxy.Tunnels, protocol string, authenticate func(context.Context, *x509.Certificate) (proxy.TunnelAuthority, error),
) (*guestTunnelListener, error) {
	listener, err := listenGuestNetwork(address, net.Listen)
	if err != nil {
		return nil, errors.New("cannot bind internal guest tunnel listener")
	}
	cfg := guest.ServerTLS(tls.Certificate{}, authority)
	cfg.Certificates = nil
	cfg.GetCertificate = certificates.current
	return &guestTunnelListener{network: listener, tls: cfg, tunnels: tunnels, protocol: protocol, authenticate: authenticate}, nil
}

func (l *guestTunnelListener) serve(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	var connections sync.WaitGroup
	defer func() {
		cancel()
		connections.Wait()
	}()
	stop := context.AfterFunc(ctx, func() { _ = l.network.Close() })
	defer stop()
	for {
		conn, err := l.network.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		connections.Go(func() {
			// A guest's disconnect or failed handshake cannot stop the listener.
			_ = l.tunnels.Serve(runCtx, tls.Server(conn, l.tls), l.protocol, l.authenticate)
		})
	}
}
