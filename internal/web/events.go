package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/proxy"
)

// OperationAuthorizer checks the current session and the operation's bound resource.
// It returns the exact authority scope used to register live-connection revocation.
type OperationAuthorizer func(*http.Request, string) (proxy.Scope, error)

type operationEvent struct {
	EventID          string              `json:"eventId"`
	OperationID      string              `json:"operationId"`
	OperationVersion int64               `json:"operationVersion"`
	EventType        string              `json:"eventType"`
	OccurredAt       time.Time           `json:"occurredAt"`
	Operation        operation.Operation `json:"operation"`
}

// OperationEvents streams durable ordered snapshots and rechecks authority while idle.
// Registry closure interrupts blocked writes and waits for the stream handler to exit.
func OperationEvents(journal *operation.Journal, registry *proxy.Registry, authorize OperationAuthorizer, interval time.Duration) http.Handler {
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("operationId")
		if !ValidUUID(id) {
			WriteProblem(w, RequestID(r.Context()), Problem{Status: 400, Code: "request.invalid_id", Title: "Invalid operation identifier"})
			return
		}
		scope, err := authorize(r, id)
		if err != nil {
			WriteProblem(w, RequestID(r.Context()), err)
			return
		}
		if _, err := journal.Get(r.Context(), id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				err = Problem{Status: 404, Code: "operation.not_found", Title: "Operation not found"}
			}
			WriteProblem(w, RequestID(r.Context()), err)
			return
		}
		last, err := eventCursor(r)
		if err != nil {
			WriteProblem(w, RequestID(r.Context()), err)
			return
		}
		latest, err := journal.LatestEventID(r.Context(), id)
		if err != nil {
			WriteProblem(w, RequestID(r.Context()), err)
			return
		}
		if last > latest {
			WriteProblem(w, RequestID(r.Context()), Problem{Status: 409, Code: "operation.invalid_event_cursor", Title: "Refresh this operation", Retryable: true})
			return
		}

		control := http.NewResponseController(w)
		ctx, release, err := registerEventStream(r.Context(), control, registry, scope, func() (proxy.Scope, error) {
			return authorize(r, id)
		})
		if err != nil {
			WriteProblem(w, RequestID(r.Context()), err)
			return
		}
		defer release()
		r = r.WithContext(ctx)
		setEventStreamHeaders(w)
		if err := writeEventChunk(ctx, control, w, ": connected\n\n"); err != nil {
			return
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		heartbeat := time.Now()
		for {
			if ctx.Err() != nil {
				return
			}
			currentScope, err := authorize(r, id)
			if err != nil || currentScope != scope {
				return
			}
			events, err := journal.Events(ctx, id, last)
			if err != nil {
				return
			}
			for _, event := range events {
				currentScope, err := authorize(r, id)
				if err != nil || currentScope != scope {
					return
				}
				// Persisted gaps are not renumbered: the UI detects them and refetches.
				payload, err := json.Marshal(operationEvent{EventID: strconv.FormatInt(event.ID, 10), OperationID: id, OperationVersion: event.Operation.Version, EventType: "operation.updated", OccurredAt: event.OccurredAt, Operation: event.Operation})
				if err != nil {
					return
				}
				chunk := fmt.Sprintf("id: %d\nevent: operation.updated\ndata: %s\n\n", event.ID, payload)
				if err := writeEventChunk(ctx, control, w, chunk); err != nil {
					return
				}
				last = event.ID
				heartbeat = time.Now()
			}
			current, err := journal.Get(ctx, id)
			if err != nil {
				return
			}
			if current.Status == "succeeded" || current.Status == "failed" || current.Status == "cancelled" {
				latest, err := journal.LatestEventID(ctx, id)
				if err != nil {
					return
				}
				if last >= latest {
					return
				}
			}
			if len(events) == 1000 {
				continue
			}
			if time.Since(heartbeat) >= 15*time.Second {
				if err := writeEventChunk(ctx, control, w, ": heartbeat\n\n"); err != nil {
					return
				}
				heartbeat = time.Now()
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}

func registerEventStream(ctx context.Context, control *http.ResponseController, registry *proxy.Registry,
	scope proxy.Scope, authorize func() (proxy.Scope, error),
) (context.Context, func(), error) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	unregister, err := registry.Register(scope, func() error {
		current, err := authorize()
		if err != nil {
			return err
		}
		if current != scope {
			return Problem{Status: 403, Code: "auth.changed", Title: "Access changed"}
		}
		return nil
	}, func() error {
		cancel()
		if err := control.SetWriteDeadline(time.Now()); err != nil {
			select {
			case <-done:
				return nil
			default:
				return err
			}
		}
		<-done
		return nil
	})
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return ctx, func() {
		close(done)
		cancel()
		unregister()
	}, nil
}

func setEventStreamHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
}

func eventCursor(r *http.Request) (int64, error) {
	values := r.Header.Values("Last-Event-ID")
	if len(values) == 0 {
		return 0, nil
	}
	invalid := Problem{Status: 400, Code: "request.invalid_event_cursor", Title: "Invalid event cursor"}
	if len(values) != 1 || values[0] == "" {
		return 0, invalid
	}
	for _, character := range values[0] {
		if character < '0' || character > '9' {
			return 0, invalid
		}
	}
	id, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil {
		return 0, invalid
	}
	return id, nil
}

func writeEventChunk(ctx context.Context, control *http.ResponseController, w http.ResponseWriter, chunk string) error {
	if err := control.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	// Recheck after changing the deadline so revocation cannot be undone by a racing writer.
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := fmt.Fprint(w, chunk); err != nil {
		return err
	}
	if err := control.Flush(); err != nil {
		return err
	}
	// An HTTP/2 write deadline aborts the stream even while it waits for new
	// events. Limit the write and flush without imposing an idle timeout.
	return control.SetWriteDeadline(time.Time{})
}
