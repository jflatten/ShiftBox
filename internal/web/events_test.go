package web_test

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/testsupport"
	"github.com/jflatten/ShiftBox/internal/web"
	_ "modernc.org/sqlite"
)

func eventFixture(t *testing.T) (*operation.Journal, *sql.DB, operation.Operation) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "events.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	var counter atomic.Uint32
	j := operation.NewJournal(db, time.Now, func() string { return testsupport.ID(counter.Add(1)) })
	op, err := j.Create(ctx, operation.Request{Actor: "alice", Key: "event-request-0001", Type: "provision", Generation: 1, Steps: []operation.Step{{Key: "create", Label: "Create VM"}}})
	if err != nil {
		t.Fatal(err)
	}
	return j, db, op
}

func readEvent(t *testing.T, reader *bufio.Reader) (string, string) {
	t.Helper()
	id, data := "", ""
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE event: %v", err)
		}
		if strings.HasPrefix(line, "id: ") {
			id = strings.TrimSpace(strings.TrimPrefix(line, "id: "))
		}
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		}
		if line == "\n" && data != "" {
			return id, data
		}
	}
}

// TestEventReplayPreservesGaps returns ordered persisted IDs so clients can refetch on a sequence gap.
func TestEventReplayPreservesGaps(t *testing.T) {
	j, db, op := eventFixture(t)
	ctx := context.Background()
	claim, err := j.Claim(ctx, op.ID, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.StartStep(ctx, claim, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM operation_events WHERE operation_id=$1 AND event_id=2`, op.ID); err != nil {
		t.Fatal(err)
	}
	var registry proxy.Registry
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/operations/{operationId}/events", web.OperationEvents(j, &registry, func(r *http.Request, id string) (proxy.Scope, error) {
		return proxy.Scope{UserID: "alice", SessionID: "session-one"}, nil
	}, 5*time.Millisecond))
	server := httptest.NewServer(mux)
	defer server.Close()
	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/operations/"+op.ID+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Last-Event-ID", "1")
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("SSE response = %d %s", response.StatusCode, response.Header.Get("Content-Type"))
	}
	id, data := readEvent(t, bufio.NewReader(response.Body))
	if id != "3" {
		t.Errorf("replayed event = %q, want actual persisted ID 3", id)
	}
	var event struct {
		EventID          string              `json:"eventId"`
		OperationID      string              `json:"operationId"`
		OperationVersion int64               `json:"operationVersion"`
		OccurredAt       time.Time           `json:"occurredAt"`
		Operation        operation.Operation `json:"operation"`
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		t.Fatal(err)
	}
	if event.EventID != id || event.OperationID != op.ID || event.OperationVersion != 3 || event.Operation.Steps[0].Status != "active" || event.OccurredAt.IsZero() {
		t.Errorf("replay snapshot = %+v", event)
	}
}

// TestEventRevocationClosesStream checks both explicit closure and current-permission rechecks on real HTTP.
func TestEventRevocationClosesStream(t *testing.T) {
	for _, direct := range []bool{true, false} {
		// An open response must end without waiting for its session or certificate expiry.
		t.Run(fmt.Sprintf("registry=%t", direct), func(t *testing.T) {
			j, _, op := eventFixture(t)
			var registry proxy.Registry
			var permitted atomic.Bool
			permitted.Store(true)
			mux := http.NewServeMux()
			mux.Handle("GET /api/v1/operations/{operationId}/events", web.OperationEvents(j, &registry, func(r *http.Request, id string) (proxy.Scope, error) {
				if !permitted.Load() {
					return proxy.Scope{}, web.Problem{Status: 403, Code: "auth.denied", Title: "Access revoked"}
				}
				return proxy.Scope{UserID: "alice", SessionID: "session-one"}, nil
			}, 5*time.Millisecond))
			server := httptest.NewServer(mux)
			defer server.Close()
			client := &http.Client{Timeout: 3 * time.Second}
			response, err := client.Get(server.URL + "/api/v1/operations/" + op.ID + "/events")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			reader := bufio.NewReader(response.Body)
			readEvent(t, reader)
			permitted.Store(false)
			if direct {
				if err := registry.CloseMatching(func(scope proxy.Scope) bool { return scope.SessionID == "session-one" }); err != nil {
					t.Fatal(err)
				}
			}
			_, err = io.ReadAll(reader)
			if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("revoked stream did not close: %v", err)
			}
		})
	}
}

// TestInvalidEventCursor rejects malformed and future cursors instead of silently missing history.
func TestInvalidEventCursor(t *testing.T) {
	j, _, op := eventFixture(t)
	var registry proxy.Registry
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/operations/{operationId}/events", web.OperationEvents(j, &registry, func(*http.Request, string) (proxy.Scope, error) { return proxy.Scope{UserID: "alice"}, nil }, time.Second))
	for _, cursor := range []string{"-1", "not-an-id", "999"} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/operations/"+op.ID+"/events", nil)
		request.Header.Set("Last-Event-ID", cursor)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code < 400 {
			t.Errorf("cursor %q returned %d, want rejection", cursor, response.Code)
		}
	}
}
