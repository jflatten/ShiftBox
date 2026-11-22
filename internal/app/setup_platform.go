package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/release"
	"github.com/jflatten/ShiftBox/internal/web"
)

func (s SetupHTTP) platformHandler() http.Handler {
	var validation sync.Mutex
	return s.setupMethod(http.MethodPost, func(ctx context.Context, tx *sql.Tx, _ *http.Request, actor auth.Principal, _ web.AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
		var current, previous string
		err := tx.QueryRowContext(ctx, `SELECT active_release,min_guest_release FROM installations i
			WHERE id=$1 AND state='configuring' AND config_generation>0
			AND EXISTS(SELECT 1 FROM browser_certificates WHERE state='active')`, s.InstallationID).Scan(&current, &previous)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, web.Problem{Status: 409, Code: "setup.certificate_required", Title: "Validate the browser certificate before importing the platform image"}
		}
		if err != nil {
			return nil, err
		}
		if s.Platform == nil {
			return nil, web.Problem{Status: 409, Code: "setup.platform_source_required", Title: "Start local setup with a platform import folder and UTM settings"}
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !validation.TryLock() {
				setupProblem(w, r, web.Problem{Status: 409, Code: "setup.platform_check_running", Title: "A platform image check is already running"})
				return
			}
			defer validation.Unlock()
			if err := s.Platform(r.Context(), actor.User.ID, current, previous); err != nil {
				problem := web.Problem{Status: 409, Code: "setup.platform_validation_failed", Title: "Platform image validation failed", Detail: "Check UTM, the guest address, and the workspace resource bounds, then retry."}
				if errors.Is(err, release.ErrInvalidArtifact) || errors.Is(err, release.ErrCatalogConflict) {
					problem.Detail = "Check the local bundle and its manifest. Use a release compatible with this installation."
				}
				setupProblem(w, r, problem)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}), nil
	})
}
