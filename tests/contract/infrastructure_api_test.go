package contract_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestCapacityAdministration checks that only administrators can set budgets
// or create profiles, that stale edits fail, and that provider limits apply.
func TestCapacityAdministration(t *testing.T) {
	const origin = "https://management.test"
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "capacity.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	token := strings.Repeat("A", 43)
	seedWorkspaceLaunch(t, db, uuid.NewString(), token, now)
	budget, profile := web.CapacityHandlers(db, origin, func() time.Time { return now }, uuid.NewString, provider.Capabilities{
		Minimum: provider.Resources{CPU: 1, MemoryBytes: 512 << 20, DiskBytes: 20 << 20},
		Maximum: provider.Resources{CPU: 8, MemoryBytes: 4096 << 20, DiskBytes: 40 << 20},
	})
	request := func(h *web.AuthorizedHandler, version, csrf, body string, want int) {
		t.Helper()
		r := httptest.NewRequest(h.Route.Method, origin+"/api/v1"+h.Route.Path, strings.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("If-Match", version)
		r.Header.Set("X-CSRF-Token", csrf)
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s returned %d %s, want %d", h.Route.Path, w.Code, w.Body.String(), want)
		}
	}
	const limits = `{"cpuCount":4,"memoryMiB":2048,"diskMiB":400}`
	const small = `{"name":"Small alternate","cpuCount":2,"memoryMiB":1024,"diskMiB":40}`
	csrf := auth.CSRFToken(token)
	request(&budget, `"1"`, csrf, limits, 403)
	request(&profile, "", csrf, small, 403)
	if _, err := db.ExecContext(ctx, "UPDATE users SET role='administrator' WHERE id='member'"); err != nil {
		t.Fatal(err)
	}
	request(&budget, `"1"`, "invalid", limits, 403)
	request(&budget, "", csrf, limits, 428)
	request(&budget, `"0"`, csrf, limits, 412)
	request(&budget, `"1"`, csrf, strings.Replace(limits, ":400", ":0", 1), 400)
	request(&budget, `"1"`, csrf, limits, 204)
	request(&budget, `"1"`, csrf, limits, 412)
	request(&profile, "", csrf, strings.Replace(small, ":40", ":400", 1), 409)
	request(&profile, "", csrf, small, 201)
	request(&profile, "", csrf, small, 409)
	var version, disk, count int
	err = db.QueryRowContext(ctx, `SELECT version,disk_mib_limit,
		(SELECT COUNT(*) FROM resource_profiles WHERE name='Small alternate' AND enabled=1)
		FROM capacity_budgets`).Scan(&version, &disk, &count)
	if err != nil || version != 2 || disk != 400 || count != 1 {
		t.Fatalf("stored capacity = version %d, disk %d, profiles %d, error %v", version, disk, count, err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM capacity_budgets"); err != nil {
		t.Fatal(err)
	}
	request(&budget, `"1"`, csrf, limits, 412)
	request(&budget, `"0"`, csrf, limits, 204)
	request(&budget, `"0"`, csrf, limits, 412)
}
