// Package workspacelog holds the recent, process-local output of guest setup commands.
package workspacelog

import (
	"slices"
	"sync"
	"time"
)

const (
	// GuestPath is served only by the authenticated guest HTTPS listener.
	GuestPath = "/internal/v1/guest/workspace-logs"
	// Format identifies guests that suppress CLI trace diagnostics.
	Format = "2"
	// BatchLimit bounds the bytes of text in a single guest upload.
	BatchLimit = 8 << 10

	historyLimit   = 128
	workspaceLimit = 32
)

// Batch is one ordered chunk of console output.
type Batch struct {
	ID   int64  `json:"-"`
	Text string `json:"text"`
	key  string
}

// Store retains a bounded tail for recently logging workspaces. Its zero value
// is ready to use and can be shared between the guest and management listeners.
type Store struct {
	mu      sync.Mutex
	lastID  int64
	history map[string][]Batch
}

// Append records a batch once, including retries of an upload whose reply was lost.
func (s *Store) Append(workspaceID, key, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	history := s.history[workspaceID]
	for _, batch := range history {
		if batch.key == key {
			return
		}
	}
	if s.history == nil {
		s.history = make(map[string][]Batch)
	}
	if len(history) == 0 && len(s.history) == workspaceLimit {
		var oldestWorkspace string
		oldestID := s.lastID + 1
		for id, batches := range s.history {
			if latest := batches[len(batches)-1].ID; latest < oldestID {
				oldestWorkspace, oldestID = id, latest
			}
		}
		delete(s.history, oldestWorkspace)
	}
	if len(history) == historyLimit {
		copy(history, history[1:])
		history = history[:historyLimit-1]
	}
	// Start new streams above old cursors after a normal controller restart.
	s.lastID = max(s.lastID+1, time.Now().UnixMicro())
	s.history[workspaceID] = append(history, Batch{ID: s.lastID, Text: text, key: key})
}

// After returns a copy of the workspace's retained batches after the given cursor.
func (s *Store) After(workspaceID string, cursor int64) []Batch {
	s.mu.Lock()
	defer s.mu.Unlock()
	history := s.history[workspaceID]
	index, found := slices.BinarySearchFunc(history, cursor, func(batch Batch, cursor int64) int {
		switch {
		case batch.ID < cursor:
			return -1
		case batch.ID > cursor:
			return 1
		default:
			return 0
		}
	})
	if found {
		index++
	}
	return slices.Clone(history[index:])
}
