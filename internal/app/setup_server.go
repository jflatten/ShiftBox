package app

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"
)

// BootstrapServer owns a loopback-only listener and its local HTTPS lifecycle.
// Origin is the actual address, including the OS-selected port when zero is used.
type BootstrapServer struct {
	Origin   string
	listener net.Listener
	server   *http.Server
}

// ListenBootstrap reserves only an explicit loopback address, then constructs
// handlers with its exact HTTPS origin. It never exposes an HTTP fallback.
func ListenBootstrap(address string, certificate tls.Certificate, handler func(string) (http.Handler, error)) (*BootstrapServer, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("setup listener requires a loopback IP and port")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() || ip.Zone() != "" {
		return nil, errors.New("setup listener must bind a loopback IP")
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return nil, errors.New("setup listener port is invalid")
	}
	if len(certificate.Certificate) == 0 || certificate.PrivateKey == nil || handler == nil {
		return nil, errors.New("setup TLS material and handler are required")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(ip.String(), port))
	if err != nil {
		return nil, errors.New("cannot bind local setup listener")
	}
	origin := "https://" + listener.Addr().String()
	// Browser origin serialization omits the default HTTPS port.
	if _, actualPort, _ := net.SplitHostPort(listener.Addr().String()); actualPort == "443" {
		origin = "https://" + ip.String()
		if ip.Is6() {
			origin = "https://[" + ip.String() + "]"
		}
	}
	next, err := handler(origin)
	if err != nil || next == nil {
		listener.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("setup handler is missing")
	}
	server := &http.Server{Handler: next, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}}, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, ErrorLog: log.New(io.Discard, "", 0)}
	return &BootstrapServer{Origin: origin, listener: listener, server: server}, nil
}

// Serve runs until cancellation or listener failure. Its shutdown goroutine is
// always joined; a stalled connection is closed after the five-second grace period.
func (s *BootstrapServer) Serve(ctx context.Context) error {
	shutdown, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-shutdown.Done()
		grace, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

// Close releases both active requests and an unserved reserved listener.
func (s *BootstrapServer) Close() error {
	err := s.server.Close()
	_ = s.listener.Close()
	return err
}
