package contract_test

import (
	"encoding/json"
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

// TestInfrastructureStatus checks that an administrator sees the current
// budget and its edit version, while an ordinary member cannot read them.
func TestInfrastructureStatus(t *testing.T) {
	const origin = "https://management.test"
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "infrastructure.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	token := strings.Repeat("A", 43)
	seedWorkspaceLaunch(t, db, uuid.NewString(), token, now)
	handler := web.InfrastructureStatusHandler(db, origin, func() time.Time { return now }, provider.Capabilities{
		Name: "utm", Version: "5.0.8", Architecture: "amd64", NetworkModes: []string{"shared"},
	})
	read := func(want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, origin+"/api/v1/admin/infrastructure", nil)
		req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("infrastructure status = %d %s, want %d", response.Code, response.Body, want)
		}
		return response
	}
	read(http.StatusForbidden)
	if _, err := db.ExecContext(ctx, "UPDATE users SET role='administrator' WHERE id='member'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE capacity_budgets SET cpu_limit=4, version=2"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO protected_values(id,value_json) VALUES('fixture','{}')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO browser_certificates(id,chain_pem,private_key_ciphertext_id,
		not_before,not_after,fingerprint_sha256,state) VALUES('fixture','fixture-chain','fixture',$1,$2,'fixture','active')`,
		now.Add(-time.Hour).Format(time.RFC3339Nano), now.Add(30*24*time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	response := read(http.StatusOK)
	var view struct {
		Provider struct {
			Name, Version, Architecture string
			NetworkModes                []string
		}
		Capacity   struct{ CPUCount, MemoryMiB, DiskMiB int }
		Version    int
		Conditions []struct {
			Key, Severity string
			Active        bool
		}
	}
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil ||
		view.Provider.Name != "utm" || view.Provider.Version != "5.0.8" || view.Provider.Architecture != "amd64" ||
		len(view.Provider.NetworkModes) != 1 || view.Provider.NetworkModes[0] != "shared" ||
		view.Capacity.CPUCount != 4 || view.Capacity.MemoryMiB != 1024 || view.Capacity.DiskMiB != 40 || view.Version != 2 ||
		len(view.Conditions) != 1 || view.Conditions[0].Key != "browser_certificate.expiring" ||
		view.Conditions[0].Severity != "warning" || !view.Conditions[0].Active ||
		response.Header().Get("ETag") != `"2"` {
		t.Fatalf("infrastructure status = %+v (%v), ETag %q, want UTM fixture and version 2 capacity", view, err, response.Header().Get("ETag"))
	}
	if strings.Contains(response.Body.String(), "fixture-chain") {
		t.Fatal("infrastructure status exposed the stored certificate chain")
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM capacity_budgets"); err != nil {
		t.Fatal(err)
	}
	read(http.StatusServiceUnavailable)
}

// TestNetworkPolicyAdministration rejects restrictions that the pinned
// provider cannot enforce and persists only the admitted unrestricted policy.
func TestNetworkPolicyAdministration(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "network.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	const origin = "https://management.test"
	now := time.Now().UTC()
	token := strings.Repeat("A", 43)
	seedWorkspaceLaunch(t, db, uuid.NewString(), token, now)
	handler := web.NetworkPolicyHandler(db, origin, func() time.Time { return now }, uuid.NewString,
		provider.Capabilities{Name: "utm", Version: "5.0.8", NetworkModes: []string{"shared"}})
	request := func(body string, want int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, origin+"/api/v1/admin/network-policies", strings.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("create network policy %s = %d %s, want %d", body, w.Code, w.Body.String(), want)
		}
	}
	const unrestricted = `{"name":"Outbound access","mode":"all"}`
	request(unrestricted, 403)
	if _, err := db.ExecContext(ctx, "UPDATE users SET role='administrator' WHERE id='member'"); err != nil {
		t.Fatal(err)
	}
	request(`{"name":"Offline","mode":"no_network"}`, 409)
	request(`{"name":"Restricted","mode":"allowlist","rules":[{"destination":"192.0.2.0/24","transport":"tcp","port":443}]}`, 409)
	request(`{"name":"Broken","mode":"allowlist","rules":[{"destination":"arbitrary.example","transport":"tcp","port":443}]}`, 400)
	request(unrestricted, 201)
	request(unrestricted, 409)
	var name, mode, version string
	if err := db.QueryRowContext(ctx, "SELECT name,mode,provider_capability_version FROM network_policies").Scan(&name, &mode, &version); err != nil ||
		name != "Outbound access" || mode != "all" || version != "5.0.8" {
		t.Fatalf("stored policy = %q/%q/%q (%v), want Outbound access/all/5.0.8", name, mode, version, err)
	}
}

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
