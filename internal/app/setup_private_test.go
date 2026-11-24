package app

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"golang.org/x/crypto/bcrypt"
)

type privateSetupAdmin struct {
	calls int
	fail  bool
	hash  []byte
}

func (a *privateSetupAdmin) Ensure(_ context.Context, _, _ string, hash []byte) error {
	a.calls++
	a.hash = hash
	if a.fail {
		return errors.New("fixture identity interruption")
	}
	return nil
}

// TestPrivateOwnerClaimFencesRetries verifies that only a confirmed password
// creates an owner and the same password survives a retry without a reset.
func TestPrivateOwnerClaimFencesRetries(t *testing.T) {
	const origin = "https://shiftbox.install.test:8443"
	const claim = "valid-test-claim-which-is-at-least-32-bytes"
	const password = "known-initial-password"
	admin := &privateSetupAdmin{fail: true}
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "bootstrap.db")
	seed := database.BootstrapSeed{ID: uuid.NewString(), Organization: "ShiftBox", Origin: origin,
		IDEPattern: "{workspace}.ide.install.test", PreviewPattern: "{service}--{workspace}.preview.install.test",
		Release: "development", MinGuestRelease: "development", Now: time.Now()}
	newHandler := func() (http.Handler, *sql.DB) {
		t.Helper()
		db, id, err := database.OpenBootstrap(t.Context(), path, seed)
		if err != nil {
			t.Fatal(err)
		}
		gate, err := NewPrivateSetupClaim(claim)
		if err != nil {
			t.Fatal(err)
		}
		handler, err := NewSetupHandler(SetupHTTP{DB: db, Cipher: cipher, Admin: admin, InstallationID: id, Origin: origin,
			Cloud:   &CloudSetup{ManagementOrigin: origin, IDEHostPattern: seed.IDEPattern, PreviewHostPattern: seed.PreviewPattern, PrivateClaim: gate},
			Journal: operation.NewJournal(db, time.Now, uuid.NewString), Now: time.Now, NewID: uuid.NewString})
		if err != nil {
			t.Fatal(err)
		}
		return handler, db
	}

	handler, db := newHandler()
	requestWithConfirmation := func(host, requestOrigin, candidate, email, credential, confirmation string, secure bool) *httptest.ResponseRecorder {
		t.Helper()
		fields := map[string]string{"email": email, "claimCode": candidate, "initialPassword": credential}
		if confirmation != "" {
			fields["confirmPassword"] = confirmation
		}
		body, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "http://"+host+"/api/v1/setup/administrator", strings.NewReader(string(body)))
		if secure {
			r.TLS = &tls.ConnectionState{}
		}
		r.Header.Set("Origin", requestOrigin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", uuid.NewString())
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), claim) || strings.Contains(w.Body.String(), password) {
			t.Fatal("private setup returned credential material")
		}
		return w
	}
	request := func(host, requestOrigin, candidate, email, credential string, secure bool) *httptest.ResponseRecorder {
		t.Helper()
		return requestWithConfirmation(host, requestOrigin, candidate, email, credential, credential, secure)
	}
	const host = "shiftbox.install.test:8443"
	const owner = "owner@example.test"
	for _, unauthorized := range []*httptest.ResponseRecorder{
		request(host, origin, "", owner, password, true),
		request(host, origin, "incorrect", owner, password, true),
		request(host, "https://other.test", claim, owner, password, true),
		request("guest.install.test:8443", origin, claim, owner, password, true),
		request(host, origin, claim, owner, password, false),
	} {
		if unauthorized.Code != http.StatusForbidden {
			t.Fatalf("initial claim denial status = %d, want 403", unauthorized.Code)
		}
	}
	for _, confirmation := range []string{"", "different-confirmation"} {
		w := requestWithConfirmation(host, origin, claim, owner, password, confirmation, true)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "setup.password_mismatch") {
			t.Fatalf("unconfirmed password status = %d, want password mismatch: %s", w.Code, w.Body)
		}
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM users").Scan(&count); err != nil || count != 0 {
		t.Fatalf("owner count before valid claim = %d (%v), want 0", count, err)
	}
	if admin.calls != 0 {
		t.Fatalf("identity calls before valid claim = %d, want 0", admin.calls)
	}
	if w := request(host, origin, claim, owner, password, true); w.Code < 400 {
		t.Fatalf("interrupted owner creation status = %d, want an error", w.Code)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	handler, db = newHandler()
	t.Cleanup(func() { db.Close() })
	admin.fail = false
	for _, conflict := range []*httptest.ResponseRecorder{
		request(host, origin, claim, "other@example.test", password, true),
		request(host, origin, claim, owner, "different-initial-password", true),
	} {
		if conflict.Code != http.StatusConflict {
			t.Fatalf("changed owner retry = %d, want 409", conflict.Code)
		}
	}
	if w := request(host, origin, claim, owner, password, true); w.Code != http.StatusCreated {
		t.Fatalf("resumed owner status = %d, want 201: %s", w.Code, w.Body)
	}
	if bcrypt.CompareHashAndPassword(admin.hash, []byte(password)) != nil {
		t.Fatal("resumed owner does not have the original password")
	}
	var mustChange, ready bool
	var ciphertextID sql.NullString
	if err := db.QueryRow(`SELECT u.must_change_password, b.credential_ready, b.password_ciphertext_id
		FROM bootstrap_administrator b JOIN users u ON u.id=b.user_id`).Scan(&mustChange, &ready, &ciphertextID); err != nil {
		t.Fatal(err)
	}
	if mustChange || !ready || ciphertextID.Valid {
		t.Fatalf("owner credential state = (must change: %v, ready: %v, ciphertext: %v)", mustChange, ready, ciphertextID.Valid)
	}
	status := httptest.NewRecorder()
	handler.ServeHTTP(status, httptest.NewRequest("GET", origin+"/api/v1/setup/status", nil))
	var progress struct {
		CompletedSteps []string
		AvailableSteps []string
	}
	if err := json.Unmarshal(status.Body.Bytes(), &progress); err != nil {
		t.Fatal(err)
	}
	if status.Code != http.StatusOK || strings.Join(progress.CompletedSteps, ",") != "administrator" ||
		strings.Join(progress.AvailableSteps, ",") != "data_profile" {
		t.Fatalf("private setup did not skip the password reset: %d %s", status.Code, status.Body)
	}
	if w := request(host, origin, claim, owner, password, true); w.Code != http.StatusConflict {
		t.Fatalf("used claim status = %d, want 409", w.Code)
	}
	if _, err := db.Exec("UPDATE installations SET state='blocked' WHERE id=$1", seed.ID); err != nil {
		t.Fatal(err)
	}
	if w := request(host, origin, claim, owner, password, true); w.Code != http.StatusConflict {
		t.Fatalf("retired claim status = %d, want 409", w.Code)
	}
	if err := db.QueryRow("SELECT count(*) FROM users").Scan(&count); err != nil || count != 1 {
		t.Fatalf("owner count = %d (%v), want 1", count, err)
	}
}
