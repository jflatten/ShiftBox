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
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/imagecache"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/web"
)

const imageCacheRoutePath = "/api/v1/admin/image-cache"
const builtImagePolicyRoutePath = imageCacheRoutePath + "/built-images"
const builtImagePolicyAuditAction = "infrastructure.image_cache.built_images.update"

type builtImagePolicyView struct {
	Enabled      bool   `json:"enabled"`
	Version      int64  `json:"version"`
	Availability string `json:"availability"`
}

type imageCacheView struct {
	BuiltImages builtImagePolicyView `json:"builtImages"`
	Version     int64                `json:"version"`
	Desired     imagecache.Settings  `json:"desired"`
	Effective   struct {
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
	want := imagecache.Settings{StoragePath: defaultPath, MaxBytes: imagecache.Bytes(14 << 30), VolumeHighPercent: 80, VolumeLowPercent: 70}

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

// TestBuiltImagePolicyRouteRequiresAuthority checks the independent setting's
// read permission, mutation permission, exact Origin/CSRF, and own precondition.
func TestBuiltImagePolicyRouteRequiresAuthority(t *testing.T) {
	db := keepAliveDatabase(t)
	defaultPath := filepath.Join(t.TempDir(), "registry")
	principal, request := builtImagePolicyRequestFixture(t, db, defaultPath, nil)
	const body = `{"enabled":false}`
	tests := []struct {
		name, method, role, installation, version string
		alter                                     func(*http.Request)
		want                                      int
	}{
		{name: "member read", method: http.MethodGet, role: "member", installation: "active", want: http.StatusForbidden},
		{name: "member write", method: http.MethodPut, role: "member", installation: "active", version: `"1"`, want: http.StatusForbidden},
		{name: "blocked administrator may view", method: http.MethodGet, role: "administrator", installation: "blocked", want: http.StatusOK},
		{name: "blocked administrator cannot save", method: http.MethodPut, role: "administrator", installation: "blocked", version: `"1"`, want: http.StatusForbidden},
		{name: "missing CSRF", method: http.MethodPut, role: "administrator", installation: "active", version: `"1"`,
			alter: func(r *http.Request) { r.Header.Del("X-CSRF-Token") }, want: http.StatusForbidden},
		{name: "untrusted Origin", method: http.MethodPut, role: "administrator", installation: "active", version: `"1"`,
			alter: func(r *http.Request) { r.Header.Set("Origin", "https://preview.test") }, want: http.StatusForbidden},
		{name: "missing precondition", method: http.MethodPut, role: "administrator", installation: "active", want: http.StatusPreconditionRequired},
		{name: "weak precondition", method: http.MethodPut, role: "administrator", installation: "active", version: `W/"1"`, want: http.StatusBadRequest},
		{name: "stale precondition", method: http.MethodPut, role: "administrator", installation: "active", version: `"0"`, want: http.StatusPreconditionFailed},
	}
	for _, test := range tests {
		// Every failed guard must reject before changing durable policy.
		t.Run(test.name, func(t *testing.T) {
			principal.User.Role, principal.InstallationState = test.role, test.installation
			response := request(test.method, body, test.version, test.alter)
			if response.Code != test.want {
				t.Errorf("built-image %s (%s) = %d %s, want %d", test.method, test.name, response.Code, response.Body, test.want)
			}
		})
	}
	principal.User.Role, principal.InstallationState = "administrator", "active"
	initial := readImageCacheView(t, request(http.MethodGet, "", "", nil), `"1"`)
	want := builtImagePolicyView{Enabled: true, Version: 1, Availability: "available"}
	if initial.BuiltImages != want {
		t.Errorf("GET %s after rejected requests builtImages = %+v, want %+v", imageCacheRoutePath, initial.BuiltImages, want)
	}
	assertBuiltImagePolicyAudit(t, db, principal, 0)
}

// TestBuiltImagePolicyRouteValidatesBoolean ensures explicit booleans are
// required and that validation failures leave the On default available.
func TestBuiltImagePolicyRouteValidatesBoolean(t *testing.T) {
	db := keepAliveDatabase(t)
	principal, request := builtImagePolicyRequestFixture(t, db, filepath.Join(t.TempDir(), "registry"), nil)
	for _, body := range []string{`{}`, `{"enabled":null}`, `{"enabled":"false"}`, `{"enabled":0}`, `{"enabled":false,"purgeCutoff":0}`} {
		response := request(http.MethodPut, body, `"1"`, nil)
		if response.Code != http.StatusBadRequest {
			t.Errorf("PUT %s (%s) = %d %s, want %d", builtImagePolicyRoutePath, body, response.Code, response.Body, http.StatusBadRequest)
		}
	}
	want := builtImagePolicyView{Enabled: true, Version: 1, Availability: "available"}
	if got := readImageCacheView(t, request(http.MethodGet, "", "", nil), `"1"`).BuiltImages; got != want {
		t.Errorf("GET %s after invalid payloads builtImages = %+v, want %+v", imageCacheRoutePath, got, want)
	}
	assertBuiltImagePolicyAudit(t, db, principal, 0)
}

// TestBuiltImagePolicyRoutePersistsBeforeFencing checks that a successful
// disable commits the durable state and audit before notifying the runtime,
// and that a new handler still reads the saved version after reopening SQLite.
func TestBuiltImagePolicyRoutePersistsBeforeFencing(t *testing.T) {
	db := keepAliveDatabase(t)
	var databaseNumber int
	var databaseName, databasePath string
	if err := db.QueryRowContext(t.Context(), "PRAGMA database_list").Scan(&databaseNumber, &databaseName, &databasePath); err != nil {
		t.Fatalf("read SQLite fixture path: %v", err)
	}
	defaultPath := filepath.Join(t.TempDir(), "registry")
	var saveCalls int
	principal, request := builtImagePolicyRequestFixture(t, db, defaultPath, func(saved imagecache.BuiltPolicy) {
		saveCalls++
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		current, err := imagecache.ReadBuiltPolicy(ctx, db)
		if err != nil || current != saved || current.Enabled || current.Version != 2 || current.Generation != 2 ||
			current.PurgeCutoff != (sql.NullInt64{Int64: 1, Valid: true}) {
			t.Errorf("ReadBuiltPolicy at save callback = %+v, %v, want committed disabled version/generation 2 and cutoff 1 from %+v",
				current, err, saved)
		}
		var auditCount int
		err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_events WHERE action=$1", builtImagePolicyAuditAction).Scan(&auditCount)
		if err != nil || auditCount != 1 {
			t.Errorf("policy audit at save callback count = %d, %v, want one committed event", auditCount, err)
		}
	})
	initial := readImageCacheView(t, request(http.MethodGet, "", "", nil), `"1"`)
	disabled := builtImagePolicyView{Enabled: false, Version: 2, Availability: "disabled"}
	assertBuiltImagePolicyResponse(t, request(http.MethodPut, `{"enabled":false}`, `"1"`, nil), `"2"`, disabled)
	if saveCalls != 1 {
		t.Errorf("save callback called %d times when the setting changed, want 1", saveCalls)
	}
	assertBuiltImagePolicyResponse(t, request(http.MethodPut, `{"enabled":false}`, `"2"`, nil), `"2"`, disabled)
	assertBuiltImagePolicyAudit(t, db, principal, 1)
	stale := request(http.MethodPut, `{"enabled":true}`, `"1"`, nil)
	if stale.Code != http.StatusPreconditionFailed {
		t.Errorf("stale PUT %s = %d %s, want %d", builtImagePolicyRoutePath, stale.Code, stale.Body, http.StatusPreconditionFailed)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close SQLite fixture before reload: %v", err)
	}
	db, err := database.Open(t.Context(), database.Config{Profile: "sqlite", SQLitePath: databasePath})
	if err != nil {
		t.Fatalf("reopen SQLite fixture: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	principal, request = builtImagePolicyRequestFixture(t, db, defaultPath, nil)
	reloaded := readImageCacheView(t, request(http.MethodGet, "", "", nil), `"1"`)
	if reloaded.BuiltImages != disabled {
		t.Errorf("GET %s after reload builtImages = %+v, want %+v", imageCacheRoutePath, reloaded.BuiltImages, disabled)
	}
	if diff := cmp.Diff(initial.Desired, reloaded.Desired); diff != "" {
		t.Errorf("built-image update changed public cache settings (-want +got):\n%s", diff)
	}
	assertBuiltImagePolicyAudit(t, db, principal, 1)
	enabled := builtImagePolicyView{Enabled: true, Version: 3, Availability: "available"}
	assertBuiltImagePolicyResponse(t, request(http.MethodPut, `{"enabled":true}`, `"2"`, nil), `"3"`, enabled)
	disabled.Version = 4
	assertBuiltImagePolicyResponse(t, request(http.MethodPut, `{"enabled":false}`, `"3"`, nil), `"4"`, disabled)
	policy, err := imagecache.ReadBuiltPolicy(t.Context(), db)
	if err != nil || policy.PurgeCutoff != (sql.NullInt64{Int64: 1, Valid: true}) {
		t.Errorf("second disable during pending cleanup saved purge cutoff %v, %v, want oldest cutoff 1", policy.PurgeCutoff, err)
	}
	assertBuiltImagePolicyAudit(t, db, principal, 3)
}

// TestBuiltImagePolicyRouteRollsBackIfAuditFails verifies that a rejected audit
// insert cannot persist policy or trigger runtime fencing.
func TestBuiltImagePolicyRouteRollsBackIfAuditFails(t *testing.T) {
	db := keepAliveDatabase(t)
	if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER reject_cache_audit BEFORE INSERT ON audit_events
		WHEN NEW.action='infrastructure.image_cache.built_images.update' BEGIN
			SELECT RAISE(ABORT, 'injected cache audit failure');
		END`); err != nil {
		t.Fatalf("install audit fault: %v", err)
	}
	called := false
	principal, request := builtImagePolicyRequestFixture(t, db, filepath.Join(t.TempDir(), "registry"), func(imagecache.BuiltPolicy) { called = true })
	response := request(http.MethodPut, `{"enabled":false}`, `"1"`, nil)
	if response.Code != http.StatusInternalServerError || called {
		t.Errorf("audit failure PUT %s = %d %s, callback %t, want 500 and no callback", builtImagePolicyRoutePath, response.Code, response.Body, called)
	}
	want := builtImagePolicyView{Enabled: true, Version: 1, Availability: "available"}
	if got := readImageCacheView(t, request(http.MethodGet, "", "", nil), `"1"`).BuiltImages; got != want {
		t.Errorf("GET %s after audit failure builtImages = %+v, want %+v", imageCacheRoutePath, got, want)
	}
	assertBuiltImagePolicyAudit(t, db, principal, 0)
}

func builtImagePolicyRequestFixture(t *testing.T, db *sql.DB, defaultPath string, afterSave func(imagecache.BuiltPolicy)) (*auth.Principal, func(string, string, string, func(*http.Request)) *httptest.ResponseRecorder) {
	t.Helper()
	session, principal, token := sessionHandlerFixture(t)
	principal.User.Role = "administrator"
	if err := db.QueryRowContext(t.Context(), "SELECT id FROM installations WHERE singleton=1").Scan(&principal.InstallationID); err != nil {
		t.Fatalf("read cache fixture installation ID: %v", err)
	}
	get, _ := web.ImageCacheHandlers(db, session.Origin, session.Now, nil, defaultPath, func(context.Context) (imagecache.RuntimeStatus, error) {
		return imagecache.RuntimeStatus{BuiltImageAvailability: "available"}, nil
	})
	put := web.BuiltImagePolicyHandler(db, session.Origin, session.Now, afterSave, func() string { return "available" })
	lookup := func(_ context.Context, _ *sql.Tx, _ string) (auth.Principal, error) { return *principal, nil }
	get.Lookup, put.Lookup = lookup, lookup
	handler := http.NewServeMux()
	handler.Handle("GET "+imageCacheRoutePath, &get)
	handler.Handle("PUT "+builtImagePolicyRoutePath, &put)
	return principal, func(method, body, version string, alter func(*http.Request)) *httptest.ResponseRecorder {
		path := imageCacheRoutePath
		if method == http.MethodPut {
			path = builtImagePolicyRoutePath
		}
		r := httptest.NewRequest(method, session.Origin+path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		if method == http.MethodPut {
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", session.Origin)
			r.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		}
		if version != "" {
			r.Header.Set("If-Match", version)
		}
		if alter != nil {
			alter(r)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		return response
	}
}

func assertBuiltImagePolicyResponse(t *testing.T, response *httptest.ResponseRecorder, etag string, want builtImagePolicyView) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("PUT %s = %d %s, want %d", builtImagePolicyRoutePath, response.Code, response.Body, http.StatusOK)
	}
	if got := response.Header().Get("ETag"); got != etag {
		t.Errorf("PUT %s ETag = %q, want %q", builtImagePolicyRoutePath, got, etag)
	}
	var got builtImagePolicyView
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || got != want {
		t.Errorf("PUT %s policy = %+v, %v, want %+v", builtImagePolicyRoutePath, got, err, want)
	}
}

func assertBuiltImagePolicyAudit(t *testing.T, db *sql.DB, principal *auth.Principal, want int) {
	t.Helper()
	var total, correctlyScoped int
	err := db.QueryRowContext(t.Context(), `SELECT COUNT(*),COUNT(CASE WHEN actor_type='user' AND actor_id=$2 AND
		resource_type='installation' AND resource_id=$3 AND outcome='allowed' THEN 1 END)
		FROM audit_events WHERE action=$1`, builtImagePolicyAuditAction, principal.User.ID, principal.InstallationID).
		Scan(&total, &correctlyScoped)
	if err != nil || total != want || correctlyScoped != want {
		t.Errorf("built-image policy audit for %s = %d total, %d correctly scoped, %v, want %d of each", principal.User.ID, total, correctlyScoped, err, want)
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
