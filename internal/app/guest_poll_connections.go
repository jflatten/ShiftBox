package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"

	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/proxy"
)

type pollConnectionKey struct{}

type pollConnection struct {
	transport  net.Conn
	mu         sync.Mutex
	closed     bool
	unregister func()
}

func trackGuestPollConnections(server *http.Server) {
	var connections sync.Map
	server.ConnContext = func(ctx context.Context, transport net.Conn) context.Context {
		entry := &pollConnection{transport: transport}
		connections.Store(transport, entry)
		return context.WithValue(ctx, pollConnectionKey{}, entry)
	}
	server.ConnState = func(transport net.Conn, state http.ConnState) {
		if state != http.StateClosed && state != http.StateHijacked {
			return
		}
		if value, ok := connections.LoadAndDelete(transport); ok {
			entry := value.(*pollConnection)
			entry.mu.Lock()
			defer entry.mu.Unlock()
			entry.closed = true
			if entry.unregister != nil {
				entry.unregister()
			}
		}
	}
}

func registerGuestPollConnection(ctx context.Context, registry *proxy.Registry, authenticate func() (guest.Contact, error)) error {
	entry, ok := ctx.Value(pollConnectionKey{}).(*pollConnection)
	if !ok {
		return errors.New("guest poll transport missing from request")
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.closed {
		return net.ErrClosed
	}
	if entry.unregister != nil {
		// Exchange still verifies the leaf and current authority on every poll.
		return nil
	}
	contact, err := authenticate()
	if err != nil {
		return err
	}
	entry.unregister, err = registry.Register(proxy.Scope{UserID: contact.OwnerID, WorkspaceID: contact.WorkspaceID, GuestPoll: true}, func() error {
		current, err := authenticate()
		if err != nil {
			return err
		}
		if current.OwnerID != contact.OwnerID || current.WorkspaceID != contact.WorkspaceID || current.Generation != contact.Generation {
			return guest.ErrGuestUnauthorized
		}
		return nil
	}, func() error {
		err := entry.transport.Close()
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	})
	return err
}
