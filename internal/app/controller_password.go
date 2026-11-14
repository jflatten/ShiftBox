package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
)

func activePasswordHandler(db *sql.DB, origin string, registry *proxy.Registry, passwords setupPasswords, now func() time.Time) web.AuthorizedHandler {
	store := auth.NewStore(db)
	var change sync.Mutex
	return web.AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: web.ManagementRoute{Method: http.MethodPut, Path: "/session/password", OperationID: "changeOwnPassword", Action: "session.change_password"},
		Resolve: func(context.Context, *sql.Tx, *http.Request, auth.Principal) (web.AuthorizationTarget, error) {
			return web.AuthorizationTarget{Action: "session.change_password"}, nil
		},
		Apply: func(_ context.Context, _ *sql.Tx, r *http.Request, actor auth.Principal, _ web.AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			if passwords == nil {
				return nil, passwordServiceUnavailable()
			}
			var input passwordChange
			if err := web.DecodeJSON(nil, r, &input, 4096); err != nil {
				return nil, err
			}
			if !input.valid() {
				return nil, web.Problem{Status: 400, Code: "password.invalid", Title: "Choose a different password with at least 12 characters and at most 72 UTF-8 bytes"}
			}
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				change.Lock()
				defer change.Unlock()
				ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
				defer cancel()
				err := replaceActivePassword(ctx, db, store, registry, passwords, now, r, actor.User, input)
				if err != nil {
					web.WriteProblem(w, web.RequestID(r.Context()), err)
					return
				}
				web.ClearSessionCookie(w)
				w.WriteHeader(http.StatusNoContent)
			}), nil
		},
	}
}

func replaceActivePassword(ctx context.Context, db *sql.DB, store *auth.Store, registry *proxy.Registry, passwords setupPasswords,
	now func() time.Time, r *http.Request, user auth.User, input passwordChange,
) error {
	valid, err := passwords.PasswordVerified(ctx, user.PrimaryEmail, input.Current)
	if err != nil {
		return passwordServiceUnavailable()
	}
	if !valid {
		return web.Problem{Status: 403, Code: "password.current_invalid", Title: "Current password is incorrect"}
	}
	token, err := web.SessionToken(r)
	if err != nil {
		return err
	}
	// Dex owns the credential. Recheck authority and revoke before making its
	// separate write, so an uncertain write cannot leave old sessions authorized.
	err = database.Transaction(ctx, db, func(tx *sql.Tx) error {
		current, err := store.Principal(ctx, tx, token)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && (current.User.ID != user.ID || current.User.PrimaryEmail != user.PrimaryEmail ||
			!auth.Authorize(current, "session.change_password", auth.Resource{}, nil, now()).Allowed)) {
			return web.Problem{Status: 401, Code: "auth.required", Title: "Sign in to continue"}
		}
		if err != nil {
			return err
		}
		return store.RevokeUserSessions(ctx, tx, user.ID, now(), "password_change")
	})
	if err != nil {
		return err
	}
	match := func(scope proxy.Scope) bool { return scope.UserID == user.ID }
	if err := registry.CloseMatching(match); err != nil {
		return passwordServiceUnavailable()
	}
	if err := passwords.ReplacePassword(ctx, user.PrimaryEmail, input.New); err != nil {
		return passwordServiceUnavailable()
	}

	// Complete the management record even if the client loses the response.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	err = database.Transaction(ctx, db, func(tx *sql.Tx) error {
		stamp := now()
		_, err := tx.ExecContext(ctx, `UPDATE users SET must_change_password=0,version=version+1,updated_at=$1
			WHERE id=$2 AND credential_mode='local' AND membership_state='active' AND primary_email=$3
			AND EXISTS(SELECT 1 FROM installations WHERE singleton=1 AND state='active')`,
			stamp.UTC().Format(time.RFC3339Nano), user.ID, user.PrimaryEmail)
		if err != nil {
			return err
		}
		if err := store.RevokeUserSessions(ctx, tx, user.ID, stamp, "password_change"); err != nil {
			return err
		}
		_, err = audit.Append(ctx, tx, audit.Event{OccurredAt: stamp, ActorType: "user", ActorID: user.ID,
			Action: "session.change_password", ResourceType: "user", ResourceID: user.ID, Outcome: "allowed", ReasonCode: "credential_changed"})
		return err
	})
	if err != nil {
		return err
	}
	return registry.CloseMatching(match)
}

func passwordServiceUnavailable() web.Problem {
	return web.Problem{Status: 503, Code: "password.unavailable", Title: "Password change could not be completed",
		Detail: "Sign in again to continue."}
}
