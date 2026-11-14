package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/web"
	"golang.org/x/crypto/bcrypt"
)

type setupPasswords interface {
	PasswordVerified(context.Context, string, string) (bool, error)
	ReplacePassword(context.Context, string, string) error
}

func (s SetupHTTP) passwordHandler(sessions *web.SessionHandler, passwords setupPasswords) http.Handler {
	var change sync.Mutex
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		change.Lock()
		defer change.Unlock()
		token, err := web.SessionToken(r)
		if err != nil {
			setupProblem(w, r, err)
			return
		}
		principal, err := sessions.Lookup(r.Context(), token)
		if errors.Is(err, sql.ErrNoRows) {
			setupLoginDenied(w, r)
			return
		}
		if err != nil {
			setupProblem(w, r, err)
			return
		}
		if !principal.User.MustChangePassword || !auth.Authorize(principal, "session.change_password", auth.Resource{}, nil, s.Now()).Allowed {
			setupLoginDenied(w, r)
			return
		}
		web.MutationGuard(s.Origin, func(*http.Request) (auth.Session, error) {
			return principal.Session, nil
		}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.changePassword(w, r, principal, passwords)
		})).ServeHTTP(w, r)
	})
}

func (s SetupHTTP) changePassword(w http.ResponseWriter, r *http.Request, principal auth.Principal, passwords setupPasswords) {
	var input passwordChange
	if err := web.DecodeJSON(w, r, &input, 4096); err != nil {
		setupProblem(w, r, err)
		return
	}
	if !input.valid() {
		setupProblem(w, r, web.Problem{Status: 400, Code: "setup.invalid_password", Title: "Choose a different password with at least 12 characters and at most 72 UTF-8 bytes"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var initialHash string
	err := s.DB.QueryRowContext(ctx, `SELECT password_hash FROM bootstrap_administrator
		WHERE installation_id=$1 AND user_id=$2 AND credential_ready=1`,
		s.InstallationID, principal.User.ID).Scan(&initialHash)
	if err != nil {
		setupProblem(w, r, err)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(initialHash), []byte(input.New)) == nil {
		setupProblem(w, r, web.Problem{Status: 400, Code: "setup.invalid_password", Title: "Choose a password different from the temporary password"})
		return
	}
	email := principal.User.PrimaryEmail
	valid, err := passwords.PasswordVerified(ctx, email, input.Current)
	if err != nil {
		setupProblem(w, r, err)
		return
	}
	if valid {
		if err := passwords.ReplacePassword(ctx, email, input.New); err != nil {
			setupProblem(w, r, err)
			return
		}
	} else {
		// Dex may have committed before the controller could record the change.
		// Reconcile only the original temporary password and the exact new password.
		if bcrypt.CompareHashAndPassword([]byte(initialHash), []byte(input.Current)) == nil {
			valid, err = passwords.PasswordVerified(ctx, email, input.New)
			if err != nil {
				setupProblem(w, r, err)
				return
			}
		}
		if !valid {
			setupProblem(w, r, web.Problem{Status: 403, Code: "setup.current_password_invalid", Title: "Current password is incorrect"})
			return
		}
	}
	err = database.Transaction(ctx, s.DB, func(tx *sql.Tx) error {
		now := s.Now().UTC().Format(time.RFC3339Nano)
		result, err := tx.ExecContext(ctx, `UPDATE users SET must_change_password=0, version=version+1, updated_at=$1
			WHERE id=$2 AND must_change_password=1 AND version=$3 AND EXISTS (
				SELECT 1 FROM installations WHERE id=$4 AND state IN ('bootstrap','configuring')
			)`, now, principal.User.ID, principal.User.Version, s.InstallationID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrSetupConflict
		}
		_, err = tx.ExecContext(ctx, `UPDATE sessions SET revoked_at=$1, revocation_reason='password_change'
			WHERE user_id=$2 AND revoked_at IS NULL`, now, principal.User.ID)
		return err
	})
	if err != nil {
		setupProblem(w, r, err)
		return
	}
	web.ClearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

type passwordChange struct {
	Current string `json:"currentPassword"`
	New     string `json:"newPassword"`
}

func (p passwordChange) valid() bool {
	return p.Current != "" && len(p.Current) <= 72 && len(p.New) <= 72 &&
		utf8.RuneCountInString(p.New) >= 12 && p.New != p.Current
}
