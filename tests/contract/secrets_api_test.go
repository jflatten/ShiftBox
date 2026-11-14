package contract_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/secret"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestSecretMetadataAPI checks owner scope enforcement, explicit empty writes,
// delete acknowledgement and that management responses contain only metadata.
func TestSecretMetadataAPI(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "secrets.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	const origin = "https://management.test"
	const member = "00000000-0000-4000-8000-000000000004"
	const installation = "00000000-0000-4000-8000-000000000001"
	const token = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	now := time.Now().UTC()
	seedWorkspaceLaunch(t, db, "00000000-0000-4000-8000-000000000003", token, now)
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES($1,'member-id@example.test','member','active','local','setup','now','now',1)`, member); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE sessions SET user_id=$1 WHERE id='launch-session'", member); err != nil {
		t.Fatal(err)
	}
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	for _, handler := range web.SecretHandlers(db, origin, secret.NewService(cipher), func() time.Time { return now }) {
		mux.Handle(handler.Route.Method+" /api/v1"+handler.Route.Path, &handler)
	}
	request := func(method, path, body, acknowledgement string, want int) string {
		t.Helper()
		r := httptest.NewRequest(method, origin+"/api/v1/secrets"+path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		if acknowledgement != "" {
			r.Header.Set("X-Acknowledge-Fallback", acknowledgement)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want || strings.Contains(w.Body.String(), "secret-fixture") {
			t.Fatalf("%s %s = %d, want %d and no plaintext", method, path, w.Code, want)
		}
		return w.Body.String()
	}
	personal := "/user/" + member + "/TOKEN"
	request("PUT", "/organization/"+installation+"/TOKEN", `{"value":"secret-fixture"}`, "", 403)
	request("PUT", personal, `{"value":null}`, "", 400)
	request("PUT", personal, `{"value":"secret-fixture"}`, "", 204)
	request("PUT", personal, `{"value":""}`, "", 204)
	list := "?scope=user&scopeId=" + member
	var entries []secret.Metadata
	if err := json.Unmarshal([]byte(request("GET", list, "", "", 200)), &entries); err != nil ||
		len(entries) != 1 || !entries[0].ExplicitEmpty || entries[0].Name != "TOKEN" {
		t.Fatalf("secret metadata = %+v, %v, want explicit empty TOKEN", entries, err)
	}
	request("DELETE", personal, "", "", 400)
	request("DELETE", personal, "", "acknowledged", 204)
	if got := request("GET", list, "", "", 200); strings.TrimSpace(got) != "[]" {
		t.Fatal("deleted secret remained in the metadata list")
	}
}
