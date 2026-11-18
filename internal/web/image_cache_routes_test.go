package web_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/imagecache"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/web"
)

const imageCacheRoutePath = "/api/v1/admin/image-cache"

type imageCacheView struct {
	Version   int64               `json:"version"`
	Desired   imagecache.Settings `json:"desired"`
	Effective struct {
		Version int64 `json:"version"`
		imagecache.Settings
	} `json:"effective"`
}

// TestImageCacheRoutesRequireCurrentAuthority checks which roles may inspect
// and save cache settings, and rejects writes with invalid CSRF or versions.
func TestImageCacheRoutesRequireCurrentAuthority(t *testing.T) {
	db := keepAliveDatabase(t)
	defaultPath := filepath.Join(t.TempDir(), "registry")
	principal, request := imageCacheRequestFixture(t, db, defaultPath)
	valid := imageCacheBody(defaultPath, `"1024"`, 80, 70)

	tests := []struct {
		name         string
		method       string
		role         string
		installation string
		version      string
		csrf         bool
		want         int
	}{
		{name: "member read", method: http.MethodGet, role: "member", installation: "active", want: http.StatusForbidden},
		{name: "member write", method: http.MethodPut, role: "member", installation: "active", version: `"1"`, csrf: true, want: http.StatusForbidden},
		{name: "read-only context read", method: http.MethodGet, role: "administrator", installation: "blocked", want: http.StatusOK},
		{name: "read-only context write", method: http.MethodPut, role: "administrator", installation: "blocked", version: `"1"`, csrf: true, want: http.StatusForbidden},
		{name: "missing CSRF", method: http.MethodPut, role: "administrator", installation: "active", version: `"1"`, want: http.StatusForbidden},
		{name: "missing version", method: http.MethodPut, role: "administrator", installation: "active", csrf: true, want: http.StatusPreconditionRequired},
		{name: "stale version", method: http.MethodPut, role: "administrator", installation: "active", version: `"0"`, csrf: true, want: http.StatusPreconditionFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			principal.User.Role = test.role
			principal.InstallationState = test.installation
			body := ""
			if test.method == http.MethodPut {
				body = valid
			}
			response := request(test.method, body, test.version, test.csrf)
			if response.Code != test.want {
				t.Errorf("%s %s (%s) = %d %s, want %d", test.method, imageCacheRoutePath, test.name, response.Code, response.Body, test.want)
			}
		})
	}
}

// TestImageCacheRoutesRejectInvalidSettings verifies that malformed budgets,
// thresholds, or paths leave the current settings and version intact.
func TestImageCacheRoutesRejectInvalidSettings(t *testing.T) {
	defaultPath := filepath.Join(t.TempDir(), "registry")
	principal, request := imageCacheRequestFixture(t, keepAliveDatabase(t), defaultPath)
	principal.User.Role = "administrator"
	want := imagecache.Settings{StoragePath: defaultPath, VolumeHighPercent: 80, VolumeLowPercent: 70}

	tests := []struct {
		name string
		body string
	}{
		{name: "enabled without budget", body: imageCacheBody(defaultPath, "null", 80, 70)},
		{name: "zero budget", body: imageCacheBody(defaultPath, `"0"`, 80, 70)},
		{name: "negative budget", body: imageCacheBody(defaultPath, `"-1"`, 80, 70)},
		{name: "fractional budget", body: imageCacheBody(defaultPath, `"1.5"`, 80, 70)},
		{name: "overflowing budget", body: imageCacheBody(defaultPath, `"9223372036854775808"`, 80, 70)},
		{name: "budget encoded as a number", body: imageCacheBody(defaultPath, "1024", 80, 70)},
		{name: "zero low-water threshold", body: imageCacheBody(defaultPath, `"1024"`, 80, 0)},
		{name: "equal thresholds", body: imageCacheBody(defaultPath, `"1024"`, 80, 80)},
		{name: "reversed thresholds", body: imageCacheBody(defaultPath, `"1024"`, 70, 80)},
		{name: "high-water threshold is 100 percent", body: imageCacheBody(defaultPath, `"1024"`, 100, 70)},
		{name: "relative storage path", body: imageCacheBody("registry", `"1024"`, 80, 70)},
		{name: "storage path traversal", body: imageCacheBody(defaultPath+"/../registry-other", `"1024"`, 80, 70)},
		{name: "unknown field", body: strings.TrimSuffix(imageCacheBody(defaultPath, `"1024"`, 80, 70), "}") + `,"upstream":"https://registry.test"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := request(http.MethodPut, test.body, `"1"`, true)
			if response.Code != http.StatusBadRequest {
				t.Errorf("PUT %s (%s) = %d %s, want %d", imageCacheRoutePath, test.body, response.Code, response.Body, http.StatusBadRequest)
			}
		})
	}

	view := readImageCacheView(t, request(http.MethodGet, "", "", false), `"1"`)
	if diff := cmp.Diff(want, view.Desired); diff != "" {
		t.Errorf("GET %s after invalid saves has different desired settings (-want +got):\n%s", imageCacheRoutePath, diff)
	}
	if diff := cmp.Diff(want, view.Effective.Settings); diff != "" {
		t.Errorf("GET %s after invalid saves has different effective settings (-want +got):\n%s", imageCacheRoutePath, diff)
	}
	if view.Version != 1 || view.Effective.Version != 1 {
		t.Errorf("GET %s after invalid saves has versions %d/%d, want 1/1", imageCacheRoutePath, view.Version, view.Effective.Version)
	}
}

// TestImageCacheRoutesPersistStagedPathAfterRestart verifies that an accepted
// move journals durable intent and preserves the old path throughout admission.
func TestImageCacheRoutesPersistStagedPathAfterRestart(t *testing.T) {
	db := keepAliveDatabase(t)
	var sequence int
	var databaseName, databasePath string
	if err := db.QueryRowContext(t.Context(), "PRAGMA database_list").Scan(&sequence, &databaseName, &databasePath); err != nil {
		t.Fatalf("read SQLite fixture path: %v", err)
	}
	base := t.TempDir()
	defaultPath := filepath.Join(base, "registry")
	oldBlob := filepath.Join(defaultPath, "fixture-blob")
	if err := os.Mkdir(defaultPath, 0o700); err != nil {
		t.Fatalf("create cache fixture directory: %v", err)
	}
	if err := os.WriteFile(oldBlob, []byte("existing cache bytes"), 0o600); err != nil {
		t.Fatalf("create cache fixture blob: %v", err)
	}
	principal, request := imageCacheRequestFixture(t, db, defaultPath)
	principal.User.Role = "administrator"
	initial := readImageCacheView(t, request(http.MethodGet, "", "", false), `"1"`)

	nextPath := filepath.Join(base, "registry-next")
	maxBytes := "9223372036854775807"
	response := request(http.MethodPut, imageCacheBody(nextPath, `"`+maxBytes+`"`, 80, 70), `"1"`, true)
	if response.Code != http.StatusAccepted {
		t.Fatalf("PUT %s (move to %s) = %d %s, want %d", imageCacheRoutePath, nextPath, response.Code, response.Body, http.StatusAccepted)
	}
	var accepted struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil || accepted.ID == "" {
		t.Fatalf("PUT %s (move to %s) operation ID = %q, %v, want an operation ID", imageCacheRoutePath, nextPath, accepted.ID, err)
	}
	var auditCount int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM audit_events WHERE actor_id = $1", principal.User.ID).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Errorf("accepted cache move wrote %d audit records for actor %q with error %v, want 1 record and no error", auditCount, principal.User.ID, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close SQLite fixture before restart: %v", err)
	}
	db, err := database.Open(t.Context(), database.Config{Profile: "sqlite", SQLitePath: databasePath})
	if err != nil {
		t.Fatalf("reopen SQLite fixture: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(t.Context(), db, "sqlite"); err != nil {
		t.Fatalf("migrate reopened SQLite fixture: %v", err)
	}
	journal := operation.NewJournal(db, nil, nil)
	if op, err := journal.Get(t.Context(), accepted.ID); err != nil || op.OriginatingAction != "infrastructure.image_cache" || op.Status != "pending" || len(op.Steps) == 0 {
		t.Errorf("restart read operation %q = %+v, %v, want a pending infrastructure.image_cache operation with progress steps", accepted.ID, op, err)
	}
	principal, request = imageCacheRequestFixture(t, db, defaultPath)
	principal.User.Role = "administrator"
	view := readImageCacheView(t, request(http.MethodGet, "", "", false), `"2"`)
	want := imagecache.Settings{Enabled: true, StoragePath: nextPath, MaxBytes: &maxBytes, VolumeHighPercent: 80, VolumeLowPercent: 70}
	if diff := cmp.Diff(want, view.Desired); diff != "" {
		t.Errorf("GET %s after restart has different desired settings (-want +got):\n%s", imageCacheRoutePath, diff)
	}
	if diff := cmp.Diff(initial.Effective, view.Effective); diff != "" {
		t.Errorf("GET %s during a staged move changed effective settings (-want +got):\n%s", imageCacheRoutePath, diff)
	}
	if data, err := os.ReadFile(oldBlob); err != nil || string(data) != "existing cache bytes" {
		t.Errorf("old cache blob during a staged move = %q, %v, want existing cache bytes and no error", data, err)
	}
}

func imageCacheBody(path, maxBytes string, high, low int) string {
	return fmt.Sprintf(`{"enabled":true,"storagePath":%q,"maxBytes":%s,"volumeHighPercent":%d,"volumeLowPercent":%d}`, path, maxBytes, high, low)
}

func imageCacheRequestFixture(t *testing.T, db *sql.DB, defaultPath string) (*auth.Principal, func(string, string, string, bool) *httptest.ResponseRecorder) {
	t.Helper()
	session, principal, token := sessionHandlerFixture(t)
	if err := db.QueryRowContext(t.Context(), "SELECT id FROM installations WHERE singleton = 1").Scan(&principal.InstallationID); err != nil {
		t.Fatalf("read cache fixture installation ID: %v", err)
	}
	journal := operation.NewJournal(db, session.Now, uuid.NewString)
	get, put := web.ImageCacheHandlers(db, session.Origin, session.Now, journal, defaultPath, nil)
	lookup := func(_ context.Context, _ *sql.Tx, _ string) (auth.Principal, error) { return *principal, nil }
	get.Lookup, put.Lookup = lookup, lookup
	handler := http.NewServeMux()
	handler.Handle("GET "+imageCacheRoutePath, &get)
	handler.Handle("PUT "+imageCacheRoutePath, &put)
	return principal, func(method, body, version string, csrf bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, session.Origin+imageCacheRoutePath, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		if method == http.MethodPut {
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", session.Origin)
		}
		if version != "" {
			r.Header.Set("If-Match", version)
		}
		if csrf {
			r.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		return response
	}
}

func readImageCacheView(t *testing.T, response *httptest.ResponseRecorder, etag string) imageCacheView {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s, want %d", imageCacheRoutePath, response.Code, response.Body, http.StatusOK)
	}
	if got := response.Header().Get("ETag"); got != etag {
		t.Errorf("GET %s ETag = %q, want %q", imageCacheRoutePath, got, etag)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("GET %s Cache-Control = %q, want no-store", imageCacheRoutePath, got)
	}
	var view imageCacheView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode GET %s: %v", imageCacheRoutePath, err)
	}
	return view
}

// TestWorkspaceImageCacheDetailPreservesResolution verifies that warning recovery
// changes verification evidence while preserving the startup digest and Running state.
func TestWorkspaceImageCacheDetailPreservesResolution(t *testing.T) {
	db := keepAliveDatabase(t)
	const workspaceID = "00000000-0000-4000-8000-000000000035"
	if _, err := db.ExecContext(t.Context(), "UPDATE workspaces SET desired_state='running',observed_state='running' WHERE id=$1", workspaceID); err != nil {
		t.Fatal(err)
	}
	session, principal, token := sessionHandlerFixture(t)
	_, handler := web.WorkspaceReadHandlers(db, session.Origin, session.Now, nil)
	handler.Lookup = func(context.Context, *sql.Tx, string) (auth.Principal, error) { return *principal, nil }
	read := func() *imagecache.WorkspaceFreshness {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, session.Origin+"/api/v1/workspaces/"+workspaceID, nil)
		request.SetPathValue("workspaceId", workspaceID)
		request.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("workspace detail status = %d %s", response.Code, response.Body)
		}
		var detail struct {
			ObservedState string                         `json:"observedState"`
			ImageCache    *imagecache.WorkspaceFreshness `json:"imageCache"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		if detail.ObservedState != "running" {
			t.Errorf("observed state = %q, want running", detail.ObservedState)
		}
		return detail.ImageCache
	}
	oldDigest, newDigest := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
	stale := imagecache.Observation{Origin: "registry.example.test", Repository: "example/image", Reference: "latest", Digest: oldDigest, Stale: true}
	if err := imagecache.RecordWorkspaceFreshness(t.Context(), db, workspaceID, 1, stale, false); err != nil {
		t.Fatal(err)
	}
	before := read()
	if before == nil || !before.WarningActive || before.WarningMessage != imagecache.StaleWarning {
		t.Fatalf("outage cache view = %+v, want persisted warning", before)
	}
	fresh := stale
	fresh.Digest, fresh.Stale, fresh.Verified = newDigest, false, true
	if err := imagecache.RecordWorkspaceFreshness(t.Context(), db, workspaceID, 1, fresh, true); err != nil {
		t.Fatal(err)
	}
	after := read()
	if after == nil || after.WarningActive || !after.UpdateAvailable || len(after.References) != 1 ||
		after.References[0].ResolvedDigest != oldDigest || after.References[0].VerifiedDigest != newDigest {
		t.Fatalf("recovered cache view = %+v, want preserved startup digest and separate current upstream digest", after)
	}
	if _, err := db.ExecContext(t.Context(), "UPDATE workspaces SET generation=2 WHERE id=$1", workspaceID); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != nil {
		t.Errorf("new workspace generation exposed old cache evidence: %+v", got)
	}
}
