package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/auth"
)

var auditAction = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)

// ListAuditEventsHandler serves bounded, redacted administrator history.
func ListAuditEventsHandler(db *sql.DB, origin string, now func() time.Time) AuthorizedHandler {
	return AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodGet, Path: "/admin/audit", OperationID: "listAuditEvents", Action: "audit.list"},
		Resolve: func(context.Context, *sql.Tx, *http.Request, auth.Principal) (AuthorizationTarget, error) {
			return AuthorizationTarget{Action: "audit.list"}, nil
		},
		Apply: listAuditEvents,
	}
}

func listAuditEvents(ctx context.Context, tx *sql.Tx, r *http.Request, _ auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	badFilter := Problem{Status: 400, Code: "request.invalid_filter", Title: "Check the audit filters"}
	if err != nil {
		return nil, badFilter
	}
	action, resource := query.Get("action"), query.Get("resourceId")
	if (action != "" && !auditAction.MatchString(action)) || (resource != "" && !ValidUUID(resource)) {
		return nil, badFilter
	}
	var before int64
	if cursor := query.Get("cursor"); cursor != "" {
		before, err = strconv.ParseInt(cursor, 10, 64)
		if err != nil || before <= 0 || strconv.FormatInt(before, 10) != cursor {
			return nil, badFilter
		}
	}
	var installation string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM installations WHERE singleton=1").Scan(&installation); err != nil {
		return nil, err
	}
	items, next, err := audit.List(ctx, tx, installation, action, resource, before)
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if next > 0 {
			params := url.Values{"cursor": {strconv.FormatInt(next, 10)}}
			if action != "" {
				params.Set("action", action)
			}
			if resource != "" {
				params.Set("resourceId", resource)
			}
			w.Header().Set("Link", "</api/v1/admin/audit?"+params.Encode()+`>; rel="next"`)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(items)
	}), nil
}
