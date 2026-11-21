package contract_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
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
	if _, err := db.ExecContext(ctx, `INSERT INTO operational_conditions(condition_key,severity,detail,observed_at)
		VALUES('storage.low_space','warning','Free volume space before creating a workspace.',$1)`, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if response := read(http.StatusOK); !strings.Contains(response.Body.String(), `"key":"storage.low_space"`) {
		t.Fatal("the administrator infrastructure response omitted the persisted storage condition")
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
	budget, profile := web.CapacityHandlers(db, origin, func() time.Time { return now }, uuid.NewString, web.WorkspaceLaunchPolicy{Capabilities: provider.Capabilities{
		Minimum: provider.Resources{CPU: 1, MemoryBytes: 512 << 20, DiskBytes: 20 << 20},
		Maximum: provider.Resources{CPU: 8, MemoryBytes: 4096 << 20, DiskBytes: 40 << 20},
	}})
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
	request(&budget, `"1"`, csrf, strings.Replace(limits, ":400", ":-1", 1), 400)
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

// TestUnlimitedCapacityAPI verifies independent unlimited limits survive a
// versioned write and read, while invalid and stale writes preserve the budget.
func TestUnlimitedCapacityAPI(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "unlimited.db")})
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
	if _, err := db.ExecContext(ctx, "UPDATE users SET role='administrator' WHERE id='member'"); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return now }
	budget, _ := web.CapacityHandlers(db, origin, clock, uuid.NewString, web.WorkspaceLaunchPolicy{})
	status := web.InfrastructureStatusHandler(db, origin, clock, provider.Capabilities{})
	type limits struct {
		CPUCount  int   `json:"cpuCount"`
		MemoryMiB int64 `json:"memoryMiB"`
		DiskMiB   int64 `json:"diskMiB"`
	}
	request := func(body, version string, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, origin+"/api/v1/admin/capacity", strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		req.Header.Set("If-Match", version)
		response := httptest.NewRecorder()
		budget.ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("PUT capacity(%s, %s) = %d %s, want %d", body, version, response.Code, response.Body, want)
		}
	}
	for i, want := range []limits{
		{MemoryMiB: 1024, DiskMiB: 40},
		{CPUCount: 2, DiskMiB: 40},
		{CPUCount: 2, MemoryMiB: 1024},
		{},
	} {
		body, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		version := strconv.Quote(strconv.Itoa(i + 1))
		request(string(body), version, http.StatusNoContent)
		request(string(body), version, http.StatusPreconditionFailed)
		req := httptest.NewRequest(http.MethodGet, origin+"/api/v1/admin/infrastructure", nil)
		req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		response := httptest.NewRecorder()
		status.ServeHTTP(response, req)
		var view struct {
			Capacity limits
			Version  int
		}
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil || response.Code != http.StatusOK ||
			view.Capacity != want || view.Version != i+2 || response.Header().Get("ETag") != strconv.Quote(strconv.Itoa(i+2)) {
			t.Fatalf("GET capacity after %s = %+v, status %d (%v), want %+v, version %d", body, view, response.Code, err, want, i+2)
		}
	}
	for _, invalid := range []string{
		`{"cpuCount":-1,"memoryMiB":0,"diskMiB":0}`,
		`{"cpuCount":0,"memoryMiB":-1,"diskMiB":0}`,
		`{"cpuCount":0,"memoryMiB":0,"diskMiB":-1}`,
		`{"cpuCount":0.5,"memoryMiB":0,"diskMiB":0}`,
		`{"cpuCount":"Unlimited","memoryMiB":0,"diskMiB":0}`,
	} {
		request(invalid, `"5"`, http.StatusBadRequest)
	}
	var saved limits
	var version int
	if err := db.QueryRowContext(ctx, "SELECT cpu_limit,memory_mib_limit,disk_mib_limit,version FROM capacity_budgets").
		Scan(&saved.CPUCount, &saved.MemoryMiB, &saved.DiskMiB, &version); err != nil || saved != (limits{}) || version != 5 {
		t.Errorf("capacity after invalid writes = %+v, version %d (%v), want unlimited and version 5", saved, version, err)
	}
}

// TestResourceProfileCRUD protects assigned profiles, rejects stale writes,
// and verifies that new profiles are listed with their saved resources.
func TestResourceProfileCRUD(t *testing.T) {
	const origin = "https://management.test"
	const collection = "/api/v1/admin/resource-profiles"
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "profiles.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	clock := func() time.Time { return now }
	token := strings.Repeat("A", 43)
	seedWorkspaceLaunch(t, db, uuid.NewString(), token, now)
	caps := provider.Capabilities{
		Minimum: provider.Resources{CPU: 1, MemoryBytes: 512 << 20, DiskBytes: 20 << 20},
		Maximum: provider.Resources{CPU: 8, MemoryBytes: 4096 << 20, DiskBytes: 80 << 20},
	}
	_, create := web.CapacityHandlers(db, origin, clock, func() string { return "new-profile" }, web.WorkspaceLaunchPolicy{Capabilities: caps})
	mux := http.NewServeMux()
	mux.Handle(create.Route.Method+" /api/v1"+create.Route.Path, &create)
	for _, handler := range web.ResourceProfileHandlers(db, origin, clock, web.WorkspaceLaunchPolicy{Capabilities: caps}) {
		mux.Handle(handler.Route.Method+" /api/v1"+handler.Route.Path, &handler)
	}
	request := func(method, path, version, csrf, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, origin+path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		req.Header.Set("Origin", origin)
		req.Header.Set("If-Match", version)
		req.Header.Set("X-CSRF-Token", csrf)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("%s %s = %d %s, want %d", method, path, response.Code, response.Body, want)
		}
		return response
	}
	const original = `{"name":"Large","cpuCount":4,"memoryMiB":2048,"diskMiB":80}`
	const edited = `{"name":"Medium","cpuCount":3,"memoryMiB":1536,"diskMiB":60,"enabled":false}`
	item := collection + "/new-profile"
	csrf := auth.CSRFToken(token)
	request(http.MethodGet, collection, "", "", "", 403)
	request(http.MethodPut, item, `"1"`, csrf, edited, 403)
	request(http.MethodDelete, item, `"1"`, csrf, "", 403)
	if _, err := db.ExecContext(ctx, "UPDATE users SET role='administrator' WHERE id='member'"); err != nil {
		t.Fatal(err)
	}
	request(http.MethodPost, collection, "", csrf, original, 201)
	type profileView struct {
		ID, Name                              string
		CPUCount, MemoryMiB, DiskMiB, Version int
		Enabled                               bool
	}
	readNew := func() profileView {
		t.Helper()
		response := request(http.MethodGet, collection, "", "", "", 200)
		var profiles []profileView
		if err := json.Unmarshal(response.Body.Bytes(), &profiles); err != nil {
			t.Fatal(err)
		}
		for _, entry := range profiles {
			if entry.ID == "new-profile" {
				return entry
			}
		}
		return profileView{}
	}
	if got := readNew(); got.Name != "Large" || got.CPUCount != 4 || got.MemoryMiB != 2048 || got.DiskMiB != 80 || got.Version != 1 || !got.Enabled {
		t.Fatalf("created profile = %+v", got)
	}
	request(http.MethodPut, item, `"1"`, "invalid", edited, 403)
	request(http.MethodPut, item, "", csrf, edited, 428)
	request(http.MethodPut, item, `"1"`, csrf, strings.Replace(edited, "Medium", "Small", 1), 409)
	request(http.MethodPut, item, `"1"`, csrf, strings.Replace(edited, ":60", ":81", 1), 409)
	request(http.MethodPut, item, `"1"`, csrf, edited, 204)
	request(http.MethodPut, item, `"1"`, csrf, edited, 412)
	request(http.MethodDelete, item, `"1"`, csrf, "", 412)
	if got := readNew(); got.Name != "Medium" || got.CPUCount != 3 || got.MemoryMiB != 1536 || got.DiskMiB != 60 || got.Version != 2 || got.Enabled {
		t.Fatalf("edited profile = %+v", got)
	}
	blocked := request(http.MethodDelete, collection+"/profile", `"1"`, csrf, "", 409)
	if !strings.Contains(blocked.Body.String(), "infrastructure.resource_profile_in_use") {
		t.Fatalf("expected assigned profile conflict, got %s", blocked.Body)
	}
	request(http.MethodDelete, item, `"2"`, csrf, "", 204)
	request(http.MethodDelete, item, `"2"`, csrf, "", 404)
	if got := readNew(); got.ID != "" {
		t.Fatalf("deleted profile still listed = %+v", got)
	}
}

// TestEC2ResourceProfileShape verifies that the server derives compute from an
// offered type, rejects unsupported disks, and reads legacy EC2 allocations.
func TestEC2ResourceProfileShape(t *testing.T) {
	const origin = "https://management.test"
	const collection = "/api/v1/admin/resource-profiles"
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "ec2-profiles.db")})
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
	if _, err := db.ExecContext(ctx, "UPDATE users SET role='administrator' WHERE id='member'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE resource_profiles SET cpu_count=2,memory_mib=4096,disk_mib=32768"); err != nil {
		t.Fatal(err)
	}
	caps := provider.Capabilities{Name: "ec2", Architecture: "amd64", Minimum: provider.Resources{DiskBytes: 32 << 30},
		Maximum:       provider.Resources{DiskBytes: 2047 << 30},
		InstanceTypes: []provider.InstanceType{{Name: "m6i.xlarge", Architecture: "amd64", CPUCount: 4, MemoryMiB: 16384}}}

	if _, err := db.ExecContext(ctx, `INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,
	 owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state,provider_kind,ec2_image_json)
	 VALUES('ec2-image','EC2 fixture','amd64','test','release','1','[]','{}','{}','{}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','fixture','member','now','available','ec2','{"amiId":"ami-profile","minimumRootDiskGiB":32}')`); err != nil {
		t.Fatal(err)
	}
	policy := web.WorkspaceLaunchPolicy{Capabilities: caps, Protocols: []string{"1"}, EC2VerifiedAMIs: []string{"ami-profile"}}
	clock := func() time.Time { return now }
	_, create := web.CapacityHandlers(db, origin, clock, func() string { return "ec2-profile" }, policy)
	mux := http.NewServeMux()
	mux.Handle("POST "+collection, &create)
	for _, handler := range web.ResourceProfileHandlers(db, origin, clock, policy) {
		mux.Handle(handler.Route.Method+" /api/v1"+handler.Route.Path, &handler)
	}
	request := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, origin+path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		req.Header.Set("Origin", origin)
		req.Header.Set("If-Match", `"1"`)
		req.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		result := httptest.NewRecorder()
		mux.ServeHTTP(result, req)
		if result.Code != want {
			t.Fatalf("%s %s = %d %s, want %d", method, path, result.Code, result.Body, want)
		}
		return result
	}
	request(http.MethodPost, collection, `{"name":"Large","instanceType":"unavailable.example","diskMiB":65536}`, 409)
	request(http.MethodPost, collection, `{"name":"Large","instanceType":"m6i.xlarge","platformArtifactId":"ec2-image","diskMiB":32769}`, 409)
	request(http.MethodPost, collection, `{"name":"Large","instanceType":"m6i.xlarge","platformArtifactId":"ec2-image","diskMiB":31744}`, 409)
	request(http.MethodPost, collection, `{"name":"Large","instanceType":"m6i.xlarge","diskMiB":65536}`, 409)
	request(http.MethodPost, collection, `{"name":"Large","instanceType":"m6i.xlarge","platformArtifactId":"missing-image","diskMiB":65536}`, 409)
	request(http.MethodPost, collection, `{"name":"Large","instanceType":"m6i.xlarge","platformArtifactId":"ec2-image","diskMiB":65536,"cpuCount":1,"memoryMiB":1}`, 201)
	request(http.MethodPut, collection+"/ec2-profile", `{"name":"Large","instanceType":"m6i.xlarge","platformArtifactId":"ec2-image","diskMiB":98304,"enabled":false}`, 204)

	var profiles []struct {
		ID, InstanceType, PlatformArtifactID string
		CPUCount, MemoryMiB, DiskMiB         int
		Enabled                              bool
	}
	view := request(http.MethodGet, collection, "", 200)
	if err := json.Unmarshal(view.Body.Bytes(), &profiles); err != nil || len(profiles) != 2 {
		t.Fatalf("profiles = %+v, %v; want two profiles", profiles, err)
	}
	current, legacy := profiles[0], profiles[1]
	if current.PlatformArtifactID != "ec2-image" || current.InstanceType != "m6i.xlarge" || current.CPUCount != 4 || current.MemoryMiB != 16384 ||
		current.DiskMiB != 98304 || current.Enabled || legacy.InstanceType != "t3.medium" {
		t.Fatalf("profiles = %+v; want edited m6i.xlarge and legacy t3.medium", profiles)
	}
}

// TestLaunchLogRetentionAPI checks authorization, validation, optimistic concurrency, and readback.
func TestLaunchLogRetentionAPI(t *testing.T) {
	const origin = "https://management.test"
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "retention.db")})
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
	update := web.LaunchLogRetentionHandler(db, origin, func() time.Time { return now })
	read := web.InfrastructureStatusHandler(db, origin, func() time.Time { return now }, provider.Capabilities{})
	mux := http.NewServeMux()
	mux.Handle("PUT /api/v1/admin/launch-log-retention", &update)
	mux.Handle("GET /api/v1/admin/infrastructure", &read)
	request := func(method, path, version, body string, want int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, origin+path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		req.Header.Set("If-Match", version)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("request %s %s = %d %s, want %d", method, path, response.Code, response.Body, want)
		}
		return response.Body.Bytes()
	}
	const settings = "/api/v1/admin/launch-log-retention"
	request("PUT", settings, `"1"`, `{"days":14}`, 403)
	if _, err := db.ExecContext(ctx, "UPDATE users SET role='administrator' WHERE id='member'"); err != nil {
		t.Fatal(err)
	}
	const status = "/api/v1/admin/infrastructure"
	if got := string(request("GET", status, "", "", 200)); !strings.Contains(got, `"launchLogRetention":{"days":30,"version":1}`) {
		t.Fatalf("default retention missing: %s", got)
	}
	request("PUT", settings, "", `{"days":14}`, 428)
	request("PUT", settings, `"1"`, `{"days":0}`, 400)
	request("PUT", settings, `"1"`, `{"days":14.5}`, 400)
	request("PUT", settings, `"1"`, `{"days":14}`, 204)
	request("PUT", settings, `"1"`, `{"days":21}`, 412)
	if got := string(request("GET", status, "", "", 200)); !strings.Contains(got, `"launchLogRetention":{"days":14,"version":2}`) {
		t.Fatalf("saved retention missing: %s", got)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_events WHERE action='settings.launch_log_retention.update'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit records = %d, %v, want 1", count, err)
	}
}
