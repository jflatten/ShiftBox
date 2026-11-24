package web

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
)

func helperDownloadHandler(db *sql.DB, origin string, now func() time.Time, route ManagementRoute, image []byte) http.Handler {
	download := AuthorizedHandler{
		DB: db, Origin: origin, Now: now, Route: route,
		Resolve: func(_ context.Context, _ *sql.Tx, _ *http.Request, principal auth.Principal) (AuthorizationTarget, error) {
			resource := auth.Resource{Type: "workspace", OwnerID: principal.User.ID}
			return AuthorizationTarget{Action: route.Action, Resource: resource}, nil
		},
		Apply: func(_ context.Context, _ *sql.Tx, _ *http.Request, _ auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if len(image) == 0 {
					WriteProblem(w, RequestID(r.Context()), Problem{Status: http.StatusNotFound, Code: "ssh.helper_unavailable", Title: "The macOS helper is unavailable on this installation"})
					return
				}
				w.Header().Set("Content-Type", "application/x-apple-diskimage")
				w.Header().Set("Content-Disposition", `attachment; filename="ShiftBox-Local-Helper.dmg"`)
				w.Header().Set("X-Content-Type-Options", "nosniff")
				http.ServeContent(w, r, "ShiftBox-Local-Helper.dmg", time.Time{}, bytes.NewReader(image))
			}), nil
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			WriteProblem(w, RequestID(r.Context()), sshDenied)
			return
		}
		download.ServeHTTP(w, r)
	})
}
