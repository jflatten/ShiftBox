package contract_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/operation"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type setupPasswordAPI struct {
	hash    []byte
	calls   int
	failure error
}

func (a *setupPasswordAPI) Ensure(_ context.Context, _ string, _ string, hash []byte) error {
	a.hash = append([]byte(nil), hash...)
	a.calls++
	return a.failure
}

// TestProtectedAdministratorAPI proves first-owner creation, journal completion, one-time delivery, and safe status/retry behavior through the protected HTTP surface.
func TestProtectedAdministratorAPI(t *testing.T) {
	ctx := context.Background()
	db, installation, err := database.OpenBootstrap(ctx, filepath.Join(t.TempDir(), "setup.db"), database.BootstrapSeed{
		ID:           "00000000-0000-4000-8000-000000000001",
		Organization: "ShiftBox", Origin: "https://127.0.0.1:8443",
		IDEPattern: "{workspace}.ide.test", PreviewPattern: "{service}--{workspace}.preview.test",
		Release: "release", MinGuestRelease: "release", Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher, _ := cryptostore.New(make([]byte, 32))
	api := &setupPasswordAPI{}
	journal := operation.NewJournal(db, time.Now, func() string { return "00000000-0000-4000-8000-000000000003" })
	handler, err := app.NewSetupHandler(app.SetupHTTP{DB: db, InstallationID: installation, Origin: "https://127.0.0.1:8443", Cipher: cipher, Admin: api, Now: time.Now, NewID: func() string { return "00000000-0000-4000-8000-000000000002" }, Journal: journal})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://127.0.0.1:8443/api/v1/setup/"+path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:12345"
		r.TLS = &tls.ConnectionState{}
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "initial-owner-request")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if got := request("POST", "administrator", `{"email":"owner@example.test"}`, "https://attacker.test").Code; got != 403 {
		t.Fatalf("foreign Origin = %d, want 403", got)
	}
	if got := request("POST", "administrator", `{"email":"owner@example.test","role":"administrator"}`, "https://127.0.0.1:8443").Code; got != 400 {
		t.Fatalf("unknown field = %d, want 400", got)
	}
	api.failure = identity.ErrPasswordAdministration
	unavailable := request("POST", "administrator", `{"email":"owner@example.test"}`, "https://127.0.0.1:8443")
	if unavailable.Code != 503 {
		t.Fatalf("Dex outage status = %d, want 503", unavailable.Code)
	}
	api.failure = nil
	response := request("POST", "administrator", `{"email":"owner@example.test"}`, "https://127.0.0.1:8443")
	if response.Code != 201 {
		t.Fatalf("create administrator status = %d, want 201", response.Code)
	}
	var result struct {
		AdministratorID   string              `json:"administratorId"`
		TemporaryPassword string              `json:"temporaryPassword"`
		Operation         operation.Operation `json:"operation"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword(api.hash, []byte(result.TemporaryPassword)); err != nil {
		t.Fatal("HTTP delivered credential does not match Dex intent")
	}
	if result.Operation.Status != "succeeded" || result.AdministratorID == "" {
		t.Fatal("missing completed setup evidence")
	}
	status := request("GET", "status", "", "")
	if status.Code != 200 || strings.Contains(status.Body.String(), result.TemporaryPassword) || strings.Contains(status.Body.String(), string(api.hash)) {
		t.Fatal("status failed or exposed credential")
	}
	repeated := request("POST", "administrator", `{"email":"owner@example.test"}`, "https://127.0.0.1:8443")
	if repeated.Code != 409 || strings.Contains(repeated.Body.String(), result.TemporaryPassword) || api.calls != 2 {
		t.Fatal("retry redisplayed or reset credential")
	}
}
