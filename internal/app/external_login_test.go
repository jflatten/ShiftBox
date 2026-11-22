package app

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/identity"
	"golang.org/x/oauth2"
)

// TestExternalAuthorizationAttempt checks that the returned state and nonce
// bind to a short-lived, browser-only attempt and its original session.
func TestExternalAuthorizationAttempt(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	f := externalLogin{
		login: &SetupLogin{Config: oauth2.Config{ClientID: "fixture", Endpoint: oauth2.Endpoint{AuthURL: "https://manage.test/dex/auth"}}},
		now:   func() time.Time { return now }, stateKey: [32]byte{1},
	}
	connector := identity.ConnectorIdentity{ID: "company", Issuer: "https://upstream.test"}
	id := uuid.NewString()
	w := httptest.NewRecorder()
	location, expires := f.begin(w, connector, id)
	u, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	params := u.Query()
	if params.Get("prompt") != "login" || params.Get("connector_id") != connector.ID || params.Get("code_challenge_method") != "S256" ||
		params.Get("scope") != "email profile" || params.Get("nonce") == "" || params.Get("state") == "" ||
		!expires.Equal(now.Add(5*time.Minute)) {
		t.Fatal("authorization URL lacks forced connector selection, PKCE or a five-minute bound")
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].Domain != "" {
		t.Fatal("external authorization attempt cookie lacks origin protection")
	}
	r := httptest.NewRequest("GET", "https://manage.test/auth/callback?code=fixture&state="+url.QueryEscape(params.Get("state")), nil)
	r.AddCookie(cookies[0])
	attempt, ok := f.readAttempt(r)
	if !ok || attempt.sessionID != id || attempt.connectorID != connector.ID || !attempt.startedAt.Equal(now) ||
		params.Get("code_challenge") != oauth2.S256ChallengeFromVerifier(attempt.verifier) {
		t.Fatal("callback did not recover the exact session and PKCE binding")
	}
	forged := strings.Replace(cookies[0].Value, id, uuid.NewString(), 1)
	forgedRequest := httptest.NewRequest("GET", "https://manage.test/auth/callback?code=fixture&state="+externalStatePrefix+loginBinding("state", forged), nil)
	cookie := *cookies[0]
	cookie.Value = forged
	forgedRequest.AddCookie(&cookie)
	if _, ok := f.readAttempt(forgedRequest); ok {
		t.Fatal("a forged cookie and matching public state changed the target session")
	}
	now = expires
	if _, ok := f.readAttempt(r); ok {
		t.Fatal("expired external authorization attempt accepted")
	}
}
