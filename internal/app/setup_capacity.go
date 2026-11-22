package app

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/web"
)

func (s SetupHTTP) capacityHandler() http.Handler {
	return s.setupMutation(func(ctx context.Context, tx *sql.Tx, r *http.Request, actor auth.Principal, _ web.AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
		var ready bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM installations i JOIN browser_certificates c ON c.state='active'
			WHERE i.id=$1 AND i.state='configuring' AND i.config_generation>0)`, s.InstallationID).Scan(&ready); err != nil {
			return nil, err
		}
		if !ready {
			return nil, web.Problem{Status: 409, Code: "setup.certificate_required", Title: "Validate the browser certificate before setting capacity"}
		}
		return web.ApplyCapacityBudget(ctx, tx, r, s.InstallationID, actor.User.ID, s.Now())
	})
}
