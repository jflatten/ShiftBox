package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
)

const resetPasswordAction = "user.reset_password"

func resetPasswordHandler(db *sql.DB, origin string, registry *proxy.Registry, dex passwordHashResetter, now func() time.Time, change *sync.Mutex) web.AuthorizedHandler {
	return web.AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: web.ManagementRoute{Method: http.MethodPost, Path: "/admin/users/{userId}/password-reset", OperationID: "resetLocalUserPassword", Action: resetPasswordAction},
		Resolve: func(_ context.Context, _ *sql.Tx, r *http.Request, _ auth.Principal) (web.AuthorizationTarget, error) {
			id := r.PathValue("userId")
			if !web.ValidUUID(id) {
				return web.AuthorizationTarget{}, web.Problem{Status: 400, Code: "request.invalid_id", Title: "Invalid user identifier"}
			}
			return web.AuthorizationTarget{Action: resetPasswordAction, Resource: auth.Resource{Type: "user", ID: id}}, nil
		},
		Apply: func(_ context.Context, _ *sql.Tx, r *http.Request, actor auth.Principal, target web.AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			version, err := web.RequireVersion(r)
			if err != nil {
				return nil, err
			}
			if dex == nil || registry == nil {
				return nil, resetPasswordUnavailable()
			}
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				change.Lock()
				defer change.Unlock()
				ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
				defer cancel()
				password, user, err := resetLocalPassword(ctx, db, registry, dex, now, r, target.Resource.ID, version)
				if err != nil {
					web.WriteProblem(w, web.RequestID(ctx), err)
					return
				}
				if actor.User.ID == user.ID {
					web.ClearSessionCookie(w)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(user.Version, 10)))
				_ = json.NewEncoder(w).Encode(struct {
					User              auth.User `json:"user"`
					TemporaryPassword string    `json:"temporaryPassword"`
				}{user, password})
			}), nil
		},
	}
}

func resetLocalPassword(
	ctx context.Context,
	db *sql.DB,
	registry *proxy.Registry,
	dex passwordHashResetter,
	now func() time.Time,
	r *http.Request,
	userID string,
	version int64,
) (string, auth.User, error) {
	token, err := web.SessionToken(r)
	if err != nil {
		return "", auth.User{}, err
	}
	store := auth.NewStore(db)
	var user auth.User
	var event audit.Event
	err = database.Transaction(ctx, db, func(tx *sql.Tx) error {
		// Membership updates use the same lock when checking the last
		// usable administrator. The Dex write happens after this commits.
		if _, err := tx.ExecContext(ctx, "UPDATE installations SET version=version WHERE singleton=1"); err != nil {
			return err
		}
		stamp := now()
		actor, err := store.Principal(ctx, tx, token)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !auth.Authorize(actor, "session.view", auth.Resource{}, nil, stamp).Allowed) {
			return web.Problem{Status: 401, Code: "auth.required", Title: "Sign in to continue"}
		}
		if err != nil {
			return err
		}
		if !auth.Authorize(actor, resetPasswordAction, auth.Resource{Type: "user", ID: userID}, nil, stamp).Allowed {
			return web.Problem{Status: 403, Code: "auth.forbidden", Title: "This action is not permitted"}
		}
		user, err = store.User(ctx, tx, userID)
		if errors.Is(err, sql.ErrNoRows) {
			return web.Problem{Status: 404, Code: "user.not_found", Title: "User not found"}
		}
		if err != nil {
			return err
		}
		if err := checkPasswordResetTarget(ctx, tx, user, version); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE users SET must_change_password=1,version=version+1,updated_at=$1 WHERE id=$2",
			stamp.UTC().Format(time.RFC3339Nano), user.ID); err != nil {
			return err
		}
		user.MustChangePassword = true
		user.Version++
		if err := revokePasswordResetAccess(ctx, tx, store, user.ID, stamp); err != nil {
			return err
		}
		event = audit.Event{OccurredAt: stamp, ActorType: "user", ActorID: actor.User.ID, Action: resetPasswordAction,
			ResourceType: "user", ResourceID: user.ID, Outcome: "started", ReasonCode: "sessions_revoked"}
		_, err = audit.Append(ctx, tx, event)
		return err
	})
	if err != nil {
		return "", auth.User{}, err
	}
	match := func(scope proxy.Scope) bool { return scope.UserID == user.ID }
	if err := registry.CloseMatching(match); err != nil {
		return "", auth.User{}, resetPasswordUnavailable()
	}
	password, hash, err := identity.NewTemporaryPassword()
	if err != nil {
		return "", auth.User{}, resetPasswordUnavailable()
	}
	defer clear(hash)
	if err := dex.ResetPasswordHash(ctx, user.PrimaryEmail, user.ID, hash); err != nil {
		return "", auth.User{}, resetPasswordUnavailable()
	}

	// Revoke any restricted sessions issued during the Dex write. Complete
	// the audit even if the browser disconnects before receiving delivery.
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	err = database.Transaction(finish, db, func(tx *sql.Tx) error {
		stamp := now()
		if err := revokePasswordResetAccess(finish, tx, store, user.ID, stamp); err != nil {
			return err
		}
		event.OccurredAt, event.Outcome, event.ReasonCode = stamp, "allowed", "temporary_password_reissued"
		_, err := audit.Append(finish, tx, event)
		return err
	})
	if err != nil {
		return "", auth.User{}, err
	}
	if err := registry.CloseMatching(match); err != nil {
		return "", auth.User{}, resetPasswordUnavailable()
	}
	return password, user, nil
}

func checkPasswordResetTarget(ctx context.Context, tx *sql.Tx, user auth.User, version int64) error {
	if user.Version != version {
		return web.Problem{Status: 412, Code: "request.version_mismatch", Title: "Refresh the membership before resetting the password"}
	}
	if user.CredentialMode != "local" || user.MembershipState != "active" {
		return web.Problem{Status: 409, Code: "user.reset_unavailable", Title: "Only active local accounts can have their passwords reset"}
	}
	if user.Role == "administrator" {
		var another bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id<>$1 AND role='administrator'
			AND membership_state='active' AND must_change_password=0)`, user.ID).Scan(&another); err != nil {
			return err
		}
		if !another {
			return web.Problem{Status: 409, Code: "user.last_administrator", Title: "Keep at least one active administrator with sign-in access"}
		}
	}
	return nil
}

func revokePasswordResetAccess(ctx context.Context, tx *sql.Tx, store *auth.Store, userID string, now time.Time) error {
	if err := store.RevokeUserSessions(ctx, tx, userID, now, "administrator_password_reset"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "UPDATE origin_grants SET revoked_at=$1 WHERE user_id=$2 AND revoked_at IS NULL",
		now.UTC().Format(time.RFC3339Nano), userID)
	return err
}

func resetPasswordUnavailable() web.Problem {
	return web.Problem{Status: 503, Code: "user.password_reset_unavailable", Title: "Password reset could not be completed",
		Detail: "Refresh the membership before trying again."}
}
