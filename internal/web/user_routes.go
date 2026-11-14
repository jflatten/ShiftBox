package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/auth"
)

type membershipChange struct {
	Role  *string `json:"role"`
	State *string `json:"membershipState"`
}

// UpdateMembershipHandler approves a pending membership with the selected role.
func UpdateMembershipHandler(db *sql.DB, origin string, now func() time.Time) AuthorizedHandler {
	store := auth.NewStore(db)
	return AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodPatch, Path: "/admin/users/{userId}", OperationID: "updateMembership", Resolver: "membership_update"},
		Resolve: func(_ context.Context, _ *sql.Tx, r *http.Request, _ auth.Principal) (AuthorizationTarget, error) {
			id := r.PathValue("userId")
			if !ValidUUID(id) {
				return AuthorizationTarget{}, Problem{Status: 400, Code: "request.invalid_id", Title: "Invalid user identifier"}
			}
			var change membershipChange
			if err := DecodeJSON(nil, r, &change, 4096); err != nil {
				return AuthorizationTarget{}, err
			}
			target, err := MembershipChangeTarget(id, change.Role, change.State)
			target.membershipChange = &change
			return target, err
		},
		Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, actor auth.Principal, target AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			if target.Action != "user.approve" {
				return nil, Problem{Status: 409, Code: "user.change_unavailable", Title: "This membership change is unavailable"}
			}
			change := target.membershipChange
			if change.Role == nil {
				return nil, Problem{Status: 400, Code: "user.role_required", Title: "Choose a role before approving membership"}
			}
			version, err := RequireVersion(r)
			if err != nil {
				return nil, err
			}
			id := target.Resource.ID
			user, err := store.User(ctx, tx, id)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, Problem{Status: 404, Code: "user.not_found", Title: "User not found"}
			}
			if err != nil {
				return nil, err
			}
			stale := Problem{Status: 412, Code: "request.version_mismatch", Title: "Refresh the membership before approving"}
			if user.Version != version {
				return nil, stale
			}
			if user.MembershipState != "pending" {
				return nil, Problem{Status: 409, Code: "user.approval_unavailable", Title: "Only pending memberships can be approved"}
			}
			stamp := now()
			result, err := tx.ExecContext(ctx, `UPDATE users SET role=$1,membership_state='active',version=version+1,updated_at=$2
				WHERE id=$3 AND version=$4 AND membership_state='pending'`, *change.Role, stamp.UTC().Format(time.RFC3339Nano), id, version)
			if err != nil {
				return nil, err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return nil, err
			}
			if count != 1 {
				return nil, stale
			}
			if _, err := audit.Append(ctx, tx, audit.Event{
				OccurredAt: stamp, ActorType: "user", ActorID: actor.User.ID, Action: target.Action,
				ResourceType: "user", ResourceID: id, Outcome: "allowed", ReasonCode: "membership_approved",
			}); err != nil {
				return nil, err
			}
			user.Role, user.MembershipState, user.Version = *change.Role, "active", version+1
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(user.Version, 10)))
				_ = json.NewEncoder(w).Encode(user)
			}), nil
		},
	}
}

// ListUsersHandler returns membership records to authorized administrators.
// It does not read Dex identities or credential records.
func ListUsersHandler(db *sql.DB, origin string, now func() time.Time) AuthorizedHandler {
	return AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodGet, Path: "/admin/users", OperationID: "listUsers", Action: "user.list"},
		Resolve: func(context.Context, *sql.Tx, *http.Request, auth.Principal) (AuthorizationTarget, error) {
			return AuthorizationTarget{Action: "user.list"}, nil
		},
		Apply: listUsers,
	}
}

func listUsers(ctx context.Context, tx *sql.Tx, _ *http.Request, _ auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,primary_email,display_name,role,membership_state,
		credential_mode,must_change_password,version FROM users ORDER BY primary_email,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []auth.User{}
	for rows.Next() {
		var user auth.User
		if err := rows.Scan(&user.ID, &user.PrimaryEmail, &user.DisplayName, &user.Role,
			&user.MembershipState, &user.CredentialMode, &user.MustChangePassword, &user.Version); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(users)
	}), nil
}
