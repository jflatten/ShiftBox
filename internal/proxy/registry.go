package proxy

import (
	"errors"
	"sync"
)

// Scope identifies the authority attached to a management, IDE, or preview connection.
type Scope struct {
	UserID      string
	SessionID   string
	WorkspaceID string
	GrantID     string
	IDE         bool
}

type connection struct {
	scope  Scope
	close  func() error
	mu     sync.Mutex
	closed bool
}

// Registry tracks live connections. Its zero value is ready for concurrent use.
type Registry struct {
	mu          sync.Mutex
	next        uint64
	connections map[uint64]*connection
}

// HasIDEConnection reports whether an authenticated IDE request is still open.
func (r *Registry) HasIDEConnection(workspaceID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hasIDEConnection(workspaceID)
}

// IfIDEDisconnected fences a short database commitment against new browser
// IDE connections. The callback must not call the registry or a provider.
func (r *Registry) IfIDEDisconnected(workspaceID string, commit func() error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hasIDEConnection(workspaceID) {
		return nil
	}
	return commit()
}

func (r *Registry) hasIDEConnection(workspaceID string) bool {
	for _, entry := range r.connections {
		if entry.scope.WorkspaceID == workspaceID && entry.scope.IDE {
			return true
		}
	}
	return false
}

// Register checks current durable authority under the admission lock. Revocation
// must commit first and then call CloseMatching, closing the check/register race.
// The returned function unregisters an already-finished connection.
func (r *Registry) Register(scope Scope, authorize func() error, close func() error) (func(), error) {
	if authorize == nil || close == nil {
		return nil, errors.New("connection registration requires authorization and closure")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := authorize(); err != nil {
		return nil, err
	}
	if r.connections == nil {
		r.connections = make(map[uint64]*connection)
	}
	r.next++
	id := r.next
	r.connections[id] = &connection{scope: scope, close: close}
	return func() { r.mu.Lock(); delete(r.connections, id); r.mu.Unlock() }, nil
}

// CloseMatching waits for matching close callbacks and retains failures for retry.
// Callbacks run outside the registry lock so handlers can unregister on shutdown.
func (r *Registry) CloseMatching(match func(Scope) bool) error {
	r.mu.Lock()
	selected := make(map[uint64]*connection)
	for id, entry := range r.connections {
		if match(entry.scope) {
			selected[id] = entry
		}
	}
	r.mu.Unlock()
	var failures []error
	for id, entry := range selected {
		entry.mu.Lock()
		var err error
		if !entry.closed {
			err = entry.close()
			entry.closed = err == nil
		}
		entry.mu.Unlock()
		if err != nil {
			failures = append(failures, err)
			continue
		}
		r.mu.Lock()
		delete(r.connections, id)
		r.mu.Unlock()
	}
	return errors.Join(failures...)
}
