package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
	"golang.org/x/oauth2"
)

const (
	externalAttemptCookie = "__Host-shiftbox_external_login"
	externalStatePrefix   = "external."
	externalAttemptMaxAge = 5 * time.Minute
)

type externalLogin struct {
	db           *sql.DB
	installation string
	login        *SetupLogin
	registry     *proxy.Registry
	connector    func(context.Context) (identity.ConnectorIdentity, error)
	now          func() time.Time
	newID        func() string
	stateKey     [32]byte
}

type externalAttempt struct {
	verifier, connectorID, issuerBinding, sessionID, nonce string
	startedAt                                              time.Time
}

func (f externalLogin) begin(w http.ResponseWriter, connector identity.ConnectorIdentity, sessionID string) (string, time.Time) {
	verifier := oauth2.GenerateVerifier()
	now := f.now().Truncate(time.Second)
	expires := now.Add(externalAttemptMaxAge)
	value := strings.Join([]string{
		verifier, connector.BrokerID(), loginBinding("issuer", connector.Issuer), sessionID, strconv.FormatInt(now.Unix(), 10),
	}, ":")
	value += ":" + f.signAttempt(value)
	http.SetCookie(w, &http.Cookie{Name: externalAttemptCookie, Value: value, Path: "/", HttpOnly: true,
		Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: int(externalAttemptMaxAge.Seconds())})
	cfg := f.login.Config
	cfg.Scopes = append(append([]string(nil), cfg.Scopes...), "email", "profile")
	location := cfg.AuthCodeURL(externalStatePrefix+loginBinding("state", value),
		oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("nonce", loginBinding("nonce", value)),
		oauth2.SetAuthURLParam("connector_id", connector.BrokerID()), oauth2.SetAuthURLParam("prompt", "login"))
	return location, expires
}

func (f externalLogin) signAttempt(value string) string {
	mac := hmac.New(sha256.New, f.stateKey[:])
	mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (f externalLogin) readAttempt(r *http.Request) (externalAttempt, bool) {
	cookies := r.CookiesNamed(externalAttemptCookie)
	query := r.URL.Query()
	state := query["state"]
	if len(cookies) != 1 || len(state) != 1 || len(query["code"]) != 1 || query.Get("code") == "" {
		return externalAttempt{}, false
	}
	value := cookies[0].Value
	separator := strings.LastIndexByte(value, ':')
	if separator < 0 || !hmac.Equal([]byte(value[separator+1:]), []byte(f.signAttempt(value[:separator]))) {
		return externalAttempt{}, false
	}
	if subtle.ConstantTimeCompare([]byte(state[0]), []byte(externalStatePrefix+loginBinding("state", value))) != 1 {
		return externalAttempt{}, false
	}
	parts := strings.Split(value[:separator], ":")
	if len(parts) != 5 || len(parts[0]) != 43 || parts[1] == "" || len(parts[2]) != 43 ||
		(parts[3] != "" && !web.ValidUUID(parts[3])) {
		return externalAttempt{}, false
	}
	seconds, err := strconv.ParseInt(parts[4], 10, 64)
	start := time.Unix(seconds, 0)
	if err != nil || seconds <= 0 || start.After(f.now()) || !f.now().Before(start.Add(externalAttemptMaxAge)) {
		return externalAttempt{}, false
	}
	return externalAttempt{verifier: parts[0], connectorID: parts[1], issuerBinding: parts[2], sessionID: parts[3],
		startedAt: start, nonce: loginBinding("nonce", value)}, true
}

func (f externalLogin) start(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	connector, err := f.connector(r.Context())
	if err != nil {
		externalSignInError(w, r, err)
		return
	}
	location, _ := f.begin(w, connector, "")
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (f externalLogin) finish(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.SetCookie(w, &http.Cookie{Name: externalAttemptCookie, Path: "/", HttpOnly: true,
		Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	attempt, ok := f.readAttempt(r)
	if !ok {
		externalSignInError(w, r, identity.ErrInvalidIdentity)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	connector, err := f.connector(ctx)
	if err != nil || connector.BrokerID() != attempt.connectorID || loginBinding("issuer", connector.Issuer) != attempt.issuerBinding {
		externalSignInError(w, r, identity.ErrInvalidIdentity)
		return
	}
	verified, err := f.login.Provider.ExchangeExternal(ctx, f.login.Config, r.URL.Query().Get("code"), attempt.verifier, attempt.nonce, connector, attempt.startedAt)
	if err != nil {
		externalSignInError(w, r, err)
		return
	}
	var session auth.Session
	var token string
	if attempt.sessionID == "" {
		session, token, err = issueExternalSession(ctx, f.db, f.installation, verified, f.now(), f.newID)
	} else {
		session, token, err = reauthorizeExternalSession(ctx, f.db, f.registry, attempt.sessionID, verified, f.now(), f.newID)
	}
	if err != nil {
		externalSignInError(w, r, err)
		return
	}
	if err := web.SetSessionCookie(w, token, session.ExpiresAt); err != nil {
		web.WriteProblem(w, web.RequestID(ctx), err)
		return
	}
	http.Redirect(w, r, workspacesHome, http.StatusSeeOther)
}

func (f externalLogin) reauthorizationHandler(origin string) web.AuthorizedHandler {
	return web.AuthorizedHandler{
		DB: f.db, Origin: origin, Now: f.now,
		Route: web.ManagementRoute{Method: http.MethodPost, Path: "/session/external-reauthorization", OperationID: "beginExternalReauthorization", Action: "session.external_reauthorize"},
		Resolve: func(context.Context, *sql.Tx, *http.Request, auth.Principal) (web.AuthorizationTarget, error) {
			return web.AuthorizationTarget{Action: "session.external_reauthorize"}, nil
		},
		Apply: func(_ context.Context, _ *sql.Tx, _ *http.Request, principal auth.Principal, _ web.AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				connector, err := f.connector(r.Context())
				if err != nil {
					web.WriteProblem(w, web.RequestID(r.Context()), web.Problem{Status: 503, Code: "auth.external_unavailable", Title: "Organizational sign-in is unavailable"})
					return
				}
				location, expires := f.begin(w, connector, principal.Session.ID)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(struct {
					AuthorizationURL string    `json:"authorizationUrl"`
					ExpiresAt        time.Time `json:"expiresAt"`
				}{location, expires})
			}), nil
		},
	}
}

func externalSignInError(w http.ResponseWriter, r *http.Request, err error) {
	problem := web.Problem{Status: 401, Code: "auth.sign_in_failed", Title: "Organizational sign-in could not be completed"}
	if errors.Is(err, errMembershipPending) {
		problem = web.Problem{Status: 403, Code: "auth.membership_pending", Title: "Your membership is awaiting administrator approval"}
	}
	web.WriteProblem(w, web.RequestID(r.Context()), problem)
}
