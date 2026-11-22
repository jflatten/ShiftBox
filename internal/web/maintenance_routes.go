package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// MaintenanceHandlers lists and schedules administrator maintenance campaigns.
func MaintenanceHandlers(db *sql.DB, origin string, now func() time.Time, journal *operation.Journal) (AuthorizedHandler, AuthorizedHandler) {
	resolve := func(action string) func(context.Context, *sql.Tx, *http.Request, auth.Principal) (AuthorizationTarget, error) {
		return func(ctx context.Context, tx *sql.Tx, _ *http.Request, _ auth.Principal) (AuthorizationTarget, error) {
			resource := auth.Resource{Type: "installation"}
			err := tx.QueryRowContext(ctx, "SELECT id FROM installations WHERE singleton=1 AND state='active'").Scan(&resource.ID)
			return AuthorizationTarget{Action: action, Resource: resource}, err
		}
	}
	list := AuthorizedHandler{DB: db, Origin: origin, Now: now, Resolve: resolve("infrastructure.view"),
		Route: ManagementRoute{Method: http.MethodGet, Path: "/admin/maintenance", OperationID: "listMaintenanceCampaigns", Action: "infrastructure.view"},
		Apply: func(ctx context.Context, tx *sql.Tx, _ *http.Request, _ auth.Principal, target AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			items, err := workspace.ListMaintenance(ctx, tx, target.Resource.ID)
			if err != nil {
				return nil, err
			}
			return noticeJSON(http.StatusOK, items, ""), nil
		}}
	create := AuthorizedHandler{DB: db, Origin: origin, Now: now, Resolve: resolve("maintenance.manage"),
		Route: ManagementRoute{Method: http.MethodPost, Path: "/admin/maintenance", OperationID: "createMaintenanceCampaign", Action: "maintenance.manage"},
		Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, actor auth.Principal, target AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			key, err := IdempotencyKey(r)
			if err != nil {
				return nil, err
			}
			var input struct {
				ReleaseID string    `json:"releaseId"`
				Deadline  time.Time `json:"deadline"`
			}
			if err := DecodeJSON(nil, r, &input, 4096); err != nil {
				return nil, err
			}
			op, err := workspace.ScheduleMaintenance(ctx, tx, journal, target.Resource.ID, actor.User.ID, key, input.ReleaseID, input.Deadline, now())
			switch {
			case errors.Is(err, workspace.ErrMaintenanceInvalid):
				return nil, Problem{Status: 400, Code: "maintenance.invalid_campaign", Title: "Enter a release and a future deadline"}
			case errors.Is(err, workspace.ErrMaintenanceUnavailable):
				return nil, Problem{Status: 409, Code: "maintenance.unavailable", Title: "Choose an available release for the active controller and finish any open campaign"}
			case errors.Is(err, operation.ErrConflict):
				return nil, Problem{Status: 409, Code: "maintenance.request_conflict", Title: "This request identifier is already in use"}
			case err != nil:
				return nil, err
			}
			return noticeJSON(http.StatusAccepted, op, ""), nil
		}}
	return list, create
}
