package web

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
)

// RetireAMIHandler removes an unused family image from future selection while
// preserving its historical catalog identity and AWS resources.
func RetireAMIHandler(db *sql.DB, origin string, now func() time.Time) AuthorizedHandler {
	return AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodDelete, Path: "/admin/platform-artifacts/{artifactId}", OperationID: "retireAMI", Action: "infrastructure.manage"},
		Resolve: func(_ context.Context, _ *sql.Tx, r *http.Request, _ auth.Principal) (AuthorizationTarget, error) {
			if !ValidUUID(r.PathValue("artifactId")) {
				return AuthorizationTarget{}, invalidAuthorizationFields()
			}
			return AuthorizationTarget{Action: "infrastructure.manage"}, nil
		},
		Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, _ auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			// Serialize with workspace admission. Its final insert repeats the
			// retirement predicate after acquiring the same installation lock.
			if _, err := tx.ExecContext(ctx, "UPDATE installations SET version=version WHERE singleton=1 AND state='active'"); err != nil {
				return nil, err
			}
			result, err := tx.ExecContext(ctx, `UPDATE platform_artifacts SET ec2_retired=1
				WHERE id=$1 AND provider_kind='ec2' AND ec2_family<>'' AND state='available' AND is_default=0
				  AND NOT EXISTS (SELECT 1 FROM workspaces WHERE platform_artifact_id=$1 AND observed_state<>'deleted')`, r.PathValue("artifactId"))
			if err := checkResourceProfileWrite(result, err, Problem{Status: 409, Code: "infrastructure.ami_retirement_conflict",
				Title: "Only unused AMI family images can be retired"}); err != nil {
				return nil, err
			}
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), nil
		},
	}
}
