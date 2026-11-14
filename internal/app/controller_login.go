package app

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
)

// NewActiveLoginHandler serves local Dex sign-in for the active installation.
// It requires direct HTTPS on the configured management callback host.
func NewActiveLoginHandler(db *sql.DB, installation string, login *SetupLogin, now func() time.Time, newID func() string) (http.Handler, error) {
	return activeLoginHandler(db, installation, login, nil, now, newID)
}

// NewActiveIdentityHandlers supplies the external browser endpoints and the
// guarded reauthorization entry point alongside the local Dex sign-in.
func NewActiveIdentityHandlers(db *sql.DB, installation string, login *SetupLogin, registry *proxy.Registry,
	connector func(context.Context) (identity.ConnectorIdentity, error), now func() time.Time, newID func() string,
) (http.Handler, web.AuthorizedHandler, error) {
	if registry == nil || connector == nil {
		return nil, web.AuthorizedHandler{}, errors.New("external sign-in is incompletely wired")
	}
	f := externalLogin{db: db, installation: installation, login: login, registry: registry, connector: connector, now: now, newID: newID}
	rand.Read(f.stateKey[:])
	handler, err := activeLoginHandler(db, installation, login, &f, now, newID)
	if err != nil {
		return nil, web.AuthorizedHandler{}, err
	}
	origin := strings.TrimSuffix(login.Config.RedirectURL, "/auth/callback")
	return handler, f.reauthorizationHandler(origin), nil
}

func activeLoginHandler(db *sql.DB, installation string, login *SetupLogin, external *externalLogin, now func() time.Time, newID func() string) (http.Handler, error) {
	if db == nil || installation == "" || login == nil || login.Provider == nil || now == nil || newID == nil {
		return nil, errors.New("active sign-in is incompletely wired")
	}
	callback, err := url.Parse(login.Config.RedirectURL)
	if err != nil || callback.Scheme != "https" || callback.Hostname() == "" || callback.User != nil ||
		callback.Path != "/auth/callback" || callback.RawPath != "" || callback.RawQuery != "" ||
		callback.ForceQuery || callback.Fragment != "" {
		return nil, errors.New("active sign-in requires a management HTTPS callback")
	}
	store := auth.NewStore(db)
	flow := loginFlow{
		login: login, cookie: "__Host-shiftbox_login", cookiePath: "/", redirect: "/",
		deny: func(w http.ResponseWriter, r *http.Request) {
			web.WriteProblem(w, web.RequestID(r.Context()), web.Problem{
				Status: http.StatusUnauthorized, Code: "auth.sign_in_failed", Title: "Sign-in could not be completed",
			})
		},
		issue: func(ctx context.Context, verified identity.VerifiedIdentity) (auth.Session, string, error) {
			var session auth.Session
			var token string
			err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
				var passwordChange bool
				err := tx.QueryRowContext(ctx, `SELECT u.must_change_password
					FROM users u CROSS JOIN installations i
					WHERE i.id=$1 AND i.state='active' AND u.id=$2 AND u.credential_mode='local'
					  AND u.membership_state='active' AND u.role IN ('administrator','member')`,
					installation, verified.Subject).Scan(&passwordChange)
				if err != nil {
					return err
				}
				mode := "normal"
				if passwordChange {
					mode = "password_change"
				}
				session, token, err = auth.NewSession(newID(), verified.Subject, mode, now(), 30*time.Minute)
				if err != nil {
					return err
				}
				return store.SaveSession(ctx, tx, session)
			})
			return session, token, err
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/login", flow.start)
	mux.HandleFunc("GET /auth/callback", func(w http.ResponseWriter, r *http.Request) {
		if external != nil && strings.HasPrefix(r.URL.Query().Get("state"), externalStatePrefix) {
			external.finish(w, r)
			return
		}
		flow.finish(w, r)
	})
	if external != nil {
		mux.HandleFunc("GET /auth/external", external.start)
	}
	return web.WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || r.Host != callback.Host {
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusMisdirectedRequest)
			return
		}
		mux.ServeHTTP(w, r)
	})), nil
}
