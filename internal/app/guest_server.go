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
	"strings"
	"sync"
	"time"

	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/launchlog"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/release"
	"github.com/jflatten/ShiftBox/internal/secret"
	"github.com/jflatten/ShiftBox/internal/web"
	"github.com/jflatten/ShiftBox/internal/workspace"
	"github.com/jflatten/ShiftBox/internal/workspacelog"
)

// GuestListenerConfig assigns distinct binds and the guest-reachable TLS host.
type GuestListenerConfig struct {
	ActivityPolicy        *workspace.ActivityPolicy
	EnrollmentAddress     string
	PollAddress           string
	PublicHost            string
	GitHub                *identity.GitHub
	Secrets               *secret.Service
	WorkspaceLogs         *workspacelog.Store
	TunnelAddress         string
	Tunnels               *proxy.Tunnels
	Checkpoint            *backup.WriteFence
	ImageCache            http.Handler
	BuiltImages           http.Handler
	OfferImageCache       func(context.Context, guest.Contact) string
	ImageCacheContext     func(context.Context, guest.Contact) context.Context
	GuestUpdates          func(context.Context, string) (release.GuestUpdate, error)
	EC2Bootstrap          *guest.EC2Bootstrap
	EC2Region, EC2Account string
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
	initial := http.NewServeMux()
	initial.Handle("/internal/v1/enroll", web.GuestEnrollmentHandler(compatibility, enrollment.Enroll, now))
	if config.EC2Bootstrap != nil {
		initial.Handle("/internal/v1/enroll/ec2/", web.GuestEC2BootstrapHandler(compatibility, config.EC2Bootstrap))
	}
	enroll, err := listenGuest(config.EnrollmentAddress, guest.EnrollmentTLS(tls.Certificate{}),
		checkpointRequest(config.Checkpoint, initial), certificates)
	if err != nil {
		return nil, err
	}
	internal := http.NewServeMux()
	if config.WorkspaceLogs != nil {
		internal.Handle(workspacelog.GuestPath, web.GuestWorkspaceLogHandler(enrollment.AuthorizeWorkspaceLog, config.WorkspaceLogs, now))
	}
	internal.Handle(launchlog.GuestPath, web.GuestLaunchTimingHandler(enrollment.RecordLaunchTiming, now))
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
		internal.Handle("GET "+guest.SecretListPath, web.GuestSecretListHandler(
			func(ctx context.Context, leaf *x509.Certificate, stamp time.Time) ([]secret.Key, error) {
				return enrollment.ListSecrets(ctx, leaf, stamp, config.Secrets)
			}, now))
		internal.Handle("POST "+guest.SecretResolvePrefix, web.GuestSecretHandler(
			func(ctx context.Context, leaf *x509.Certificate, stamp time.Time, name string) ([]byte, string, error) {
				return enrollment.ResolveSecret(ctx, leaf, stamp, config.Secrets, name)
			}, now))
	}
	var pollHandler http.Handler = checkpointRequest(config.Checkpoint, internal)
	if config.BuiltImages != nil {
		internal.Handle(web.BuiltImageAPIPath+"/", config.BuiltImages)
	}
	if config.ImageCache != nil || config.BuiltImages != nil {
		cacheRoutes := http.NewServeMux()
		var registry *proxy.Registry
		if config.Tunnels != nil {
			registry = config.Tunnels.Registry
		}
		publicCache := http.NotFoundHandler()
		if config.ImageCache != nil {
			publicCache = guestImageCache(config.ImageCache, enrollment, registry, config.Checkpoint, now, config.ImageCacheContext)
		}
		privateCache := http.NotFoundHandler()
		if config.BuiltImages != nil {
			privateCache = checkpointRequest(config.Checkpoint, config.BuiltImages)
		}
		cacheRoutes.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == strings.TrimSuffix(web.BuiltImageRegistryPath, "/") ||
				strings.HasPrefix(r.URL.Path, web.BuiltImageRegistryPath) {
				privateCache.ServeHTTP(w, r)
				return
			}
			publicCache.ServeHTTP(w, r)
		})
		cacheRoutes.Handle("/", pollHandler)
		pollHandler = cacheRoutes
	}
	poll, err := listenGuest(config.PollAddress, guest.ServerTLS(tls.Certificate{}, authority), pollHandler, certificates)
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
		contact, command, err := enrollment.Exchange(ctx, leaf, stamp, report)
		if err == nil && config.OfferImageCache != nil && contact.DesiredState == "running" {
			contact.ImageCacheOffer = config.OfferImageCache(ctx, contact)
		}
		return contact, command, err
	}
	pollRoute := web.GuestPollHandler(compatibility, exchange, now, listeners.TunnelAddress)
	internal.HandleFunc("/internal/v1/guest/poll", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(guest.LaunchTimingOfferHeader, "1")
		w.Header().Set(guest.DetailedConfigureTimingHeader, "1")
		if config.WorkspaceLogs != nil {
			w.Header().Set(guest.WorkspaceLogOfferHeader, workspacelog.Format)
		}
		if config.BuiltImages != nil {
			w.Header().Set(guest.BuiltImageOfferHeader, "1")
		}
		pollRoute.ServeHTTP(w, r)
	})
	return listeners, nil
}

// BuiltImageHandler binds negotiation and private OCI transfers to the same
// enrollment, connection registry, and storage as the workspace runtime.
func (s *ControllerState) BuiltImageHandler(registry *proxy.Registry, source builtImageSource,
	authenticate func(context.Context, *x509.Certificate, time.Time) (guest.Contact, error), now func() time.Time,
) http.Handler {
	if s == nil || s.imageCache == nil || source == nil || registry == nil || authenticate == nil || now == nil {
		return http.NotFoundHandler()
	}
	service := newBuiltImageService(s.imageCache, source, registry, now)
	return web.GuestBuiltImageCacheHandler(web.GuestBuiltImageOperations{
		Authenticate: authenticate,
		Lookup:       service.lookup,
		Begin: func(ctx context.Context, contact guest.Contact, id string, version int64, sha string) (web.BuiltImagePublication, error) {
			publication, err := service.begin(ctx, contact, id, version, sha)
			return web.BuiltImagePublication(publication), err
		},
		Finish: service.finish,
		Transfer: func(ctx context.Context, contact guest.Contact, id string) (context.Context, web.BuiltImageTransfer, error) {
			transferCtx, transfer, err := service.transfer(ctx, contact, id)
			if err != nil {
				return nil, web.BuiltImageTransfer{}, err
			}
			return transferCtx, web.BuiltImageTransfer{
				Cache: transfer.cache, Scope: transfer.scope, Lease: transfer.lease,
				Manifest: transfer.manifest, Close: transfer.close,
			}, nil
		},
	}, now)
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
