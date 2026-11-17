package web

import (
	"database/sql"
	"io"
	"net/http"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// humanActivity serves the listener and input reports only after service
// authority has been checked. Preview origins never receive this capability.
func (s serviceProxy) humanActivity(w http.ResponseWriter, r *http.Request, authority proxy.OriginAuthority, cookie, origin string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if (authority.Scope.Service != "ide" && authority.Scope.Service != "repair") || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == proxy.HumanActivityScriptPath {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, proxy.HumanActivityScript())
		}
		return
	}
	if r.Method != http.MethodPost || r.Header.Get("Origin") != origin ||
		len(r.Header.Values("X-ShiftBox-Activity")) != 1 || r.Header.Get("X-ShiftBox-Activity") != "input" {
		writeServiceDenial(w, r, proxy.ErrOriginGrant)
		return
	}
	var input struct{}
	if err := DecodeJSON(w, r, &input, 64); err != nil {
		WriteProblem(w, RequestID(r.Context()), err)
		return
	}
	err := database.Transaction(r.Context(), s.db, func(tx *sql.Tx) error {
		ctx := r.Context()
		resource, generation, err := serviceWorkspace(ctx, tx, authority.Scope.WorkspaceID, authority.Scope.Service)
		if err != nil {
			return err
		}
		stamp := s.now()
		current, err := proxy.ReadOriginCookie(ctx, tx, authority.Scope, cookie, stamp)
		if err != nil {
			return err
		}
		if current != authority || generation != authority.Scope.Generation {
			return proxy.ErrOriginGrant
		}
		if _, err := authorizeService(ctx, tx, current, resource, stamp); err != nil {
			return err
		}
		return workspace.RecordHumanActivity(ctx, tx, authority.Scope.WorkspaceID, generation, stamp)
	})
	if err != nil {
		writeServiceDenial(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
