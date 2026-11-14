package app

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
	"golang.org/x/oauth2"
)

// SetupLogin carries the discovered Dex provider and its private client configuration.
type SetupLogin struct {
	Provider *identity.OIDC
	Config   oauth2.Config
	Issuer   string
}

const setupLoginCookie = "__Secure-shiftbox-setup-login"

func setupLoginValue(label, verifier string) string {
	sum := sha256.Sum256([]byte(label + ":" + verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (s SetupHTTP) startLogin(w http.ResponseWriter, r *http.Request) {
	verifier := oauth2.GenerateVerifier()
	http.SetCookie(w, &http.Cookie{Name: setupLoginCookie, Value: verifier, Path: "/auth/callback",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: 300})
	location := s.Login.Config.AuthCodeURL(setupLoginValue("state", verifier),
		oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("nonce", setupLoginValue("nonce", verifier)))
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (s SetupHTTP) finishLogin(w http.ResponseWriter, r *http.Request) {
	cookies := r.CookiesNamed(setupLoginCookie)
	http.SetCookie(w, &http.Cookie{Name: setupLoginCookie, Path: "/auth/callback", HttpOnly: true,
		Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	query := r.URL.Query()
	code, state := query["code"], query["state"]
	if len(cookies) != 1 || len(cookies[0].Value) != 43 || len(code) != 1 || code[0] == "" ||
		len(state) != 1 || subtle.ConstantTimeCompare([]byte(state[0]), []byte(setupLoginValue("state", cookies[0].Value))) != 1 {
		setupLoginDenied(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	verified, err := s.Login.Provider.ExchangeLocal(ctx, s.Login.Config, code[0], cookies[0].Value,
		setupLoginValue("nonce", cookies[0].Value), s.Login.Issuer)
	if err != nil {
		setupLoginDenied(w, r)
		return
	}
	var session auth.Session
	var token string
	err = database.Transaction(ctx, s.DB, func(tx *sql.Tx) error {
		var id string
		err := tx.QueryRowContext(ctx, `SELECT u.id FROM bootstrap_administrator b
			JOIN users u ON u.id=b.user_id JOIN installations i ON i.id=b.installation_id
			WHERE b.installation_id=$1 AND b.user_id=$2 AND b.credential_ready=1
			AND i.state IN ('bootstrap','configuring') AND u.role='administrator'
			AND u.membership_state='active' AND u.credential_mode='local'`, s.InstallationID, verified.Subject).Scan(&id)
		if err != nil {
			return err
		}
		session, token, err = auth.NewSession(s.NewID(), id, "bootstrap_restricted", s.Now(), 30*time.Minute)
		if err != nil {
			return err
		}
		return auth.NewStore(s.DB).SaveSession(ctx, tx, session)
	})
	if err != nil {
		setupLoginDenied(w, r)
		return
	}
	if err := web.SetSessionCookie(w, token, session.ExpiresAt); err != nil {
		web.WriteProblem(w, web.RequestID(r.Context()), err)
		return
	}
	http.Redirect(w, r, "/api/v1/session", http.StatusSeeOther)
}

func setupLoginDenied(w http.ResponseWriter, r *http.Request) {
	web.WriteProblem(w, web.RequestID(r.Context()), web.Problem{Status: 401, Code: "setup.sign_in_failed", Title: "Start local sign-in again"})
}

func (s SetupHTTP) sessionHandler() *web.SessionHandler {
	handler := web.NewSessionHandler(s.DB, s.Origin, &proxy.Registry{}, s.Now)
	lookup := handler.Lookup
	handler.Lookup = func(ctx context.Context, token string) (auth.Principal, error) {
		principal, err := lookup(ctx, token)
		if err != nil {
			return principal, err
		}
		if principal.Session.Mode != "bootstrap_restricted" ||
			(principal.InstallationState != "bootstrap" && principal.InstallationState != "configuring") {
			return auth.Principal{}, sql.ErrNoRows
		}
		principal.LocalBootstrap = true
		return principal, nil
	}
	return handler
}
