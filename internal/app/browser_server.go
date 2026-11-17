package app

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
)

// BrowserServer owns the browser HTTPS listener for an active installation.
type BrowserServer struct {
	Address  string
	listener net.Listener
	server   *http.Server
}

// ListenBrowser mounts management and workspace services on their own origins.
// It uses the browser certificate already validated by controller preflight.
func ListenBrowser(address string, state *ControllerState, management http.Handler, tunnels *proxy.Tunnels, now func() time.Time, fence *backup.WriteFence) (*BrowserServer, error) {
	if address == "" || state == nil || state.DB == nil || tunnels == nil || tunnels.Registry == nil || now == nil {
		return nil, errors.New("browser listener is incompletely configured")
	}
	if _, err := state.browserCertificateAt(now()); err != nil {
		return nil, err
	}
	settings := state.Installation.Settings
	hosts, err := config.NewServiceHosts(settings.ManagementOrigin, settings.IDEHostPattern, settings.PreviewHostPattern)
	if err != nil {
		return nil, err
	}
	service := web.ServiceProxyHandler(state.DB, hosts, tunnels.Registry, now, tunnels.Forward)
	entry := checkpointRequest(fence, service)
	workspaceService := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == web.ServiceEntryPath || r.URL.Path == proxy.HumanActivityPath {
			entry.ServeHTTP(w, r)
			return
		}
		service.ServeHTTP(w, r)
	})
	router, err := web.NewBrowserRouter(hosts, CheckpointManagement(fence, management), workspaceService)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, errors.New("cannot bind browser HTTPS listener")
	}
	server := &http.Server{
		Handler: router, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, SessionTicketsDisabled: true,
			GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return state.browserCertificateAt(now()) }},
		ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, ErrorLog: log.New(io.Discard, "", 0),
	}
	return &BrowserServer{Address: listener.Addr().String(), listener: listener, server: server}, nil
}

// Serve runs until cancellation or listener failure and waits for shutdown.
func (s *BrowserServer) Serve(ctx context.Context) error {
	shutdown, stop := context.WithCancel(ctx)
	defer stop()
	s.server.BaseContext = func(net.Listener) context.Context { return shutdown }
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-shutdown.Done()
		grace, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := s.server.Shutdown(grace); err != nil {
			_ = s.server.Close()
		}
	}()
	err := s.server.ServeTLS(s.listener, "", "")
	stop()
	<-done
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// Close releases a reserved listener, including before Serve.
func (s *BrowserServer) Close() error {
	err := s.server.Close()
	_ = s.listener.Close()
	return err
}
