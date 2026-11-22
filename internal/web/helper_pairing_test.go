package web_test

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestHelperPairingBindsBrowserApproval verifies cookie and CSRF requirements,
// exact numeric loopback, proof, expiry, and one successful concurrent exchange.
func TestHelperPairingBindsBrowserApproval(t *testing.T) {
	db, now := readySSHWorkspace(t)
	const origin = "https://management.test"
	api := web.NewWorkspaceSSHAPI(db, nil, nil, nil, func() time.Time { return now }, origin, nil)
	session, token, err := auth.NewSession("pairing-browser", "user", "normal", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(t.Context(), db, func(tx *sql.Tx) error {
		return auth.NewStore(db).SaveSession(t.Context(), tx, session)
	}); err != nil {
		t.Fatal(err)
	}
	request := func(path, cookie, csrf string, payload map[string]string) *httptest.ResponseRecorder {
		data, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, origin+"/api/v1/ssh/helper/"+path, bytes.NewReader(data))
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: cookie})
			req.Header.Set("Origin", origin)
			req.Header.Set("X-CSRF-Token", csrf)
		}
		res := httptest.NewRecorder()
		api.ServeHTTP(res, req)
		return res
	}
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	const redirect = "http://127.0.0.1:47777/callback"
	const state = "012345678901234567890123456789"
	approval := map[string]string{"state": state, "challenge": base64.RawURLEncoding.EncodeToString(sum[:]), "redirectUri": redirect}
	if res := request("approve", "", "", approval); res.Code != http.StatusUnauthorized {
		t.Fatalf("unapproved browser status = %d, want 401", res.Code)
	}
	if res := request("approve", token, "wrong", approval); res.Code != http.StatusForbidden {
		t.Fatalf("invalid CSRF status = %d, want 403", res.Code)
	}
	approve := func() string {
		t.Helper()
		res := request("approve", token, auth.CSRFToken(token), approval)
		var body struct{ CallbackURL string }
		err := json.Unmarshal(res.Body.Bytes(), &body)
		if res.Code != http.StatusOK || err != nil {
			t.Fatalf("approval = %d %s, %v, want callback", res.Code, res.Body, err)
		}
		callback, err := url.Parse(body.CallbackURL)
		if err != nil || callback.Scheme != "http" || callback.Host != "127.0.0.1:47777" || callback.Path != "/callback" ||
			callback.Query().Get("state") != state || callback.Query().Get("code") == "" {
			t.Fatalf("callback = %s, %v, want exact loopback address and bound code", body.CallbackURL, err)
		}
		return callback.Query().Get("code")
	}
	approval["redirectUri"] = "http://localhost:47777/callback"
	if res := request("approve", token, auth.CSRFToken(token), approval); res.Code != http.StatusBadRequest {
		t.Fatalf("nonnumeric loopback = %d, want 400", res.Code)
	}
	approval["redirectUri"] = redirect
	code := approve()
	exchange := func(code, state, verifier, callback string) *httptest.ResponseRecorder {
		return request("exchange", "", "", map[string]string{"code": code, "state": state, "verifier": verifier, "redirectUri": callback})
	}
	for _, res := range []*httptest.ResponseRecorder{
		exchange(code, state, strings.Repeat("x", 43), redirect),
		exchange(code, state, verifier, "http://127.0.0.1:47778/callback"),
		exchange(code, strings.Repeat("s", 29), verifier, redirect),
	} {
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("mismatched exchange = %d %s, want 401", res.Code, res.Body)
		}
	}
	var group sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		group.Go(func() { results <- exchange(code, state, verifier, redirect) })
	}
	group.Wait()
	close(results)
	var successes int
	for res := range results {
		if res.Code == http.StatusUnauthorized {
			continue
		}
		var body struct {
			Credential string
			AccountID  string
			ExpiresAt  time.Time
		}
		err := json.Unmarshal(res.Body.Bytes(), &body)
		if res.Code != http.StatusOK || err != nil || body.Credential == "" || body.AccountID != "user" || !body.ExpiresAt.Equal(session.ExpiresAt) {
			t.Fatalf("exchange = %d %s, %v, want scoped helper credential", res.Code, res.Body, err)
		}
		successes++
	}
	if successes != 1 {
		t.Fatalf("successful exchanges = %d, want one", successes)
	}
	code = approve()
	now = now.Add(10 * time.Minute)
	if res := exchange(code, state, verifier, redirect); res.Code != http.StatusUnauthorized {
		t.Fatalf("expired exchange = %d, want 401", res.Code)
	}
	code = approve()
	if err := database.Transaction(t.Context(), db, func(tx *sql.Tx) error {
		return auth.NewStore(db).RevokeSession(t.Context(), tx, session.ID, now, "logout")
	}); err != nil {
		t.Fatal(err)
	}
	if res := exchange(code, state, verifier, redirect); res.Code != http.StatusUnauthorized {
		t.Fatalf("exchange after browser logout = %d, want 401", res.Code)
	}
}
