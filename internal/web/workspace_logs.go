package web

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/workspacelog"
)

// GuestWorkspaceLogHandler accepts bounded console output from the guest
// identified by the verified TLS certificate and its current dispatch.
func GuestWorkspaceLogHandler(authorize func(context.Context, *x509.Certificate, time.Time, string, int64) (string, error),
	logs *workspacelog.Store, now func() time.Time,
) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+workspacelog.GuestPath, func(w http.ResponseWriter, r *http.Request) {
		requestID := RequestID(r.Context())
		denied := Problem{Status: 403, Code: "guest.identity_denied", Title: "Guest log upload is not authorized"}
		if r.TLS == nil || r.TLS.Version < tls.VersionTLS13 || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.VerifiedChains[0]) == 0 {
			WriteProblem(w, requestID, denied)
			return
		}
		var report struct {
			Format          string `json:"format"`
			OperationID     string `json:"operationId"`
			DispatchVersion int64  `json:"dispatchVersion"`
			BatchID         string `json:"batchId"`
			Text            string `json:"text"`
		}
		if err := DecodeJSON(w, r, &report, 6*workspacelog.BatchLimit+256); err != nil {
			WriteProblem(w, requestID, err)
			return
		}
		if report.Format != workspacelog.Format || !ValidUUID(report.OperationID) || report.DispatchVersion < 1 || !ValidUUID(report.BatchID) ||
			len(report.Text) > workspacelog.BatchLimit || strings.TrimSpace(report.Text) == "" {
			WriteProblem(w, requestID, Problem{Status: 400, Code: "guest.invalid_workspace_log", Title: "Invalid workspace log batch"})
			return
		}
		id, err := authorize(r.Context(), r.TLS.VerifiedChains[0][0], now(), report.OperationID, report.DispatchVersion)
		switch {
		case errors.Is(err, guest.ErrGuestUnauthorized):
			WriteProblem(w, requestID, denied)
		case err != nil:
			WriteProblem(w, requestID, err)
		default:
			logs.Append(id, report.BatchID, report.Text)
			w.WriteHeader(http.StatusNoContent)
		}
	})
	return WithRequestID(mux)
}

// WorkspaceLogsHandler streams retained and incoming guest output exclusively
// to the workspace owner. Project hooks can print data private to that owner.
func WorkspaceLogsHandler(db *sql.DB, origin string, now func() time.Time, registry *proxy.Registry, logs *workspacelog.Store) AuthorizedHandler {
	return AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route:   ManagementRoute{Method: http.MethodGet, Path: "/workspaces/{workspaceId}/logs", OperationID: "streamWorkspaceLogs", Action: "workspace.view"},
		Resolve: resolveWorkspaceLogs,
		Apply: func(_ context.Context, _ *sql.Tx, r *http.Request, principal auth.Principal, target AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			cursor, err := eventCursor(r)
			if err != nil {
				return nil, err
			}
			scope := proxy.Scope{UserID: principal.User.ID, SessionID: principal.Session.ID, WorkspaceID: target.Resource.ID}
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				streamWorkspaceLogs(w, r, registry, logs, scope, cursor, func(r *http.Request) (proxy.Scope, error) {
					return authorizeManagementStream(r, db, now(), resolveWorkspaceLogs)
				})
			}), nil
		},
	}
}

func resolveWorkspaceLogs(ctx context.Context, tx *sql.Tx, r *http.Request, principal auth.Principal) (AuthorizationTarget, error) {
	id := r.PathValue("workspaceId")
	if !ValidUUID(id) {
		return AuthorizationTarget{}, Problem{Status: 400, Code: "request.invalid_id", Title: "Invalid workspace identifier"}
	}
	var owner string
	err := tx.QueryRowContext(ctx, `SELECT owner_user_id FROM workspaces WHERE id=$1 AND desired_state<>'deleted'`, id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && owner != principal.User.ID) {
		return AuthorizationTarget{}, Problem{Status: 404, Code: "workspace.not_found", Title: "Workspace not found"}
	}
	if err != nil {
		return AuthorizationTarget{}, err
	}
	return AuthorizationTarget{Action: "workspace.view", Resource: auth.Resource{Type: "workspace", ID: id, OwnerID: owner}}, nil
}

func streamWorkspaceLogs(w http.ResponseWriter, r *http.Request, registry *proxy.Registry, logs *workspacelog.Store,
	scope proxy.Scope, cursor int64, authorize func(*http.Request) (proxy.Scope, error),
) {
	control := http.NewResponseController(w)
	ctx, release, err := registerEventStream(r.Context(), control, registry, scope, func() (proxy.Scope, error) {
		return authorize(r)
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
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	lastWrite := time.Now()
	for {
		batches := logs.After(scope.WorkspaceID, cursor)
		if len(batches) > 0 || time.Since(lastWrite) >= 15*time.Second {
			current, err := authorize(r)
			if err != nil || scope != current {
				return
			}
			if len(batches) == 0 && writeEventChunk(ctx, control, w, ": heartbeat\n\n") != nil {
				return
			}
			for _, batch := range batches {
				payload, err := json.Marshal(batch)
				if err != nil || writeEventChunk(ctx, control, w, fmt.Sprintf("id: %d\nevent: workspace.log\ndata: %s\n\n", batch.ID, payload)) != nil {
					return
				}
				cursor = batch.ID
			}
			lastWrite = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
