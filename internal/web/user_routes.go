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
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

type membershipChange struct {
	Role  *string `json:"role"`
	State *string `json:"membershipState"`
}

// UpdateMembershipHandler applies versioned membership and role changes.
func UpdateMembershipHandler(db *sql.DB, origin string, now func() time.Time, registry *proxy.Registry, journal *operation.Journal) AuthorizedHandler {
	store := auth.NewStore(db)
	return AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodPatch, Path: "/admin/users/{userId}", OperationID: "updateMembership", Resolver: "membership_update"},
		Lookup: func(ctx context.Context, tx *sql.Tx, token string) (auth.Principal, error) {
			// Serialize role changes before reading actor authority or checking
			// that an administrator will remain available.
			if _, err := tx.ExecContext(ctx, "UPDATE installations SET version=version WHERE singleton=1"); err != nil {
				return auth.Principal{}, err
			}
			return store.Principal(ctx, tx, token)
		},
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
			change := target.membershipChange
			disabling := target.Action == "user.disable"
			if !disabling && change.Role == nil {
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
			stale := Problem{Status: 412, Code: "request.version_mismatch", Title: "Refresh the membership before saving changes"}
			if user.Version != version {
				return nil, stale
			}
			nextState, reason := user.MembershipState, "role_changed"
			role := user.Role
			if change.Role != nil {
				role = *change.Role
			}
			if disabling {
				nextState, reason = *change.State, "membership_disabled"
			} else if target.Action == "user.approve" {
				if user.MembershipState != "pending" {
					return nil, Problem{Status: 409, Code: "user.approval_unavailable", Title: "Only pending memberships can be approved"}
				}
				nextState, reason = "active", "membership_approved"
			} else if user.MembershipState != "active" {
				return nil, Problem{Status: 409, Code: "user.change_unavailable", Title: "Only active accounts can change roles"}
			} else if user.Role == *change.Role {
				return membershipResponse(user), nil
			}
			demotion := user.Role == "administrator" && role == "member" && user.MembershipState == "active"
			if (disabling || demotion) && registry == nil {
				return nil, errors.New("connection registry is unavailable")
			}
			if user.Role == "administrator" && user.MembershipState == "active" && (disabling || demotion) {
				var another bool
				if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id<>$1 AND role='administrator'
					AND membership_state='active' AND must_change_password=0)`, id).Scan(&another); err != nil {
					return nil, err
				}
				if !another {
					return nil, Problem{Status: 409, Code: "user.last_administrator", Title: "Keep at least one active administrator with sign-in access"}
				}
			}
			stamp := now()
			result, err := tx.ExecContext(ctx, `UPDATE users SET role=$1,membership_state=$2,version=version+1,updated_at=$3
				WHERE id=$4 AND version=$5`, role, nextState, stamp.UTC().Format(time.RFC3339Nano), id, version)
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
				ResourceType: "user", ResourceID: id, Outcome: "allowed", ReasonCode: reason,
			}); err != nil {
				return nil, err
			}
			user.Role, user.MembershipState, user.Version = role, nextState, version+1
			var owned map[string]bool
			response := membershipResponse(user)
			if disabling {
				if journal == nil {
					return nil, errors.New("workspace stop journal is unavailable")
				}
				if err := store.RevokeUserSessions(ctx, tx, id, stamp, reason); err != nil {
					return nil, err
				}
				// Retire outstanding enrollment offers before closing live
				// connections. Historical consumed grants are kept for audit.
				if _, err := tx.ExecContext(ctx, `UPDATE enrollment_grants SET state='invalidated'
					WHERE state='issued' AND workspace_id IN (SELECT id FROM workspaces WHERE owner_user_id=$1)`, id); err != nil {
					return nil, err
				}
				ids, stops, err := workspace.QueueOffboardingStops(ctx, tx, journal, id, actor.User.ID, user.Version, stamp)
				if err != nil {
					return nil, err
				}
				owned = make(map[string]bool, len(ids))
				for _, workspaceID := range ids {
					owned[workspaceID] = true
				}
				response = membershipJSON(user.Version, struct {
					auth.User
					StopOperations []workspace.OffboardingStop `json:"stopOperations"`
				}{user, stops})
			}
			if !demotion && !disabling {
				return response, nil
			}
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if err := registry.CloseMatching(func(scope proxy.Scope) bool { return scope.UserID == id || owned[scope.WorkspaceID] }); err != nil {
					WriteProblem(w, RequestID(r.Context()), Problem{Status: 503, Code: "user.connection_closure_pending",
						Title: "Membership changed but some connections could not be closed"})
					return
				}
				response.ServeHTTP(w, r)
			}), nil
		},
	}
}

func membershipResponse(user auth.User) http.Handler {
	return membershipJSON(user.Version, user)
}

func membershipJSON(version int64, body any) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(version, 10)))
		_ = json.NewEncoder(w).Encode(body)
	})
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
