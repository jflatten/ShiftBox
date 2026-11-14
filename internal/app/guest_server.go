package app

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/web"
)

// GuestListenerConfig assigns distinct binds and the guest-reachable TLS host.
type GuestListenerConfig struct {
	EnrollmentAddress string
	PollAddress       string
	PublicHost        string
}

// GuestListeners owns the initial enrollment and authenticated poll listeners.
type GuestListeners struct {
	EnrollURL string
	PollURL   string
	enroll    guestListener
	poll      guestListener
}

type guestListener struct {
	network net.Listener
	server  *http.Server
}

// ListenGuests reserves both listeners and derives the endpoints for enrollment
// media. A partial bind is closed before returning an error.
func ListenGuests(config GuestListenerConfig, enrollment *guest.Enrollment, authority *guest.Authority,
	compatibility web.GuestCompatibility, now func() time.Time,
) (*GuestListeners, error) {
	if enrollment == nil || authority == nil || now == nil || compatibility.Protocol == "" || compatibility.CurrentRelease == "" ||
		config.EnrollmentAddress == "" || config.PollAddress == "" {
		return nil, errors.New("internal guest listeners are incompletely configured")
	}
	certificates := &internalServerCertificate{authority: authority, name: config.PublicHost, now: now}
	if _, err := certificates.current(nil); err != nil {
		return nil, err
	}
	enroll, err := listenGuest(config.EnrollmentAddress, guest.EnrollmentTLS(tls.Certificate{}),
		web.GuestEnrollmentHandler(compatibility, enrollment.Enroll, now), certificates)
	if err != nil {
		return nil, err
	}
	poll, err := listenGuest(config.PollAddress, guest.ServerTLS(tls.Certificate{}, authority),
		web.GuestPollHandler(compatibility, enrollment.Exchange, now), certificates)
	if err != nil {
		_ = enroll.network.Close()
		return nil, err
	}
	return &GuestListeners{
		EnrollURL: guestURL(config.PublicHost, enroll.network, "/internal/v1/enroll"),
		PollURL:   guestURL(config.PublicHost, poll.network, "/internal/v1/guest/poll"),
		enroll:    enroll, poll: poll,
	}, nil
}

func listenGuest(address string, config *tls.Config, handler http.Handler, certificates *internalServerCertificate) (guestListener, error) {
	listener, err := net.Listen("tcp", address)
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
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	return (&url.URL{Scheme: "https", Host: net.JoinHostPort(host, port), Path: path}).String()
}

// Serve runs both listeners until shutdown or listener failure.
func (s *GuestListeners) Serve(ctx context.Context) error {
	done := make(chan error, 2)
	for _, endpoint := range []guestListener{s.enroll, s.poll} {
		go func() { done <- endpoint.server.ServeTLS(endpoint.network, "", "") }()
	}
	var primary error
	select {
	case <-ctx.Done():
		primary = ctx.Err()
	case primary = <-done:
		s.Close()
		<-done
		return primary
	}
	s.Close()
	<-done
	<-done
	return primary
}

// Close releases the reserved listeners and active connections.
func (s *GuestListeners) Close() {
	for _, endpoint := range []guestListener{s.enroll, s.poll} {
		_ = endpoint.server.Close()
		_ = endpoint.network.Close()
	}
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
