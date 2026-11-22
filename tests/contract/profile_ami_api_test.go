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
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestEC2ProfileImageAdmission exercises profile-owned image snapshots through
// HTTP: profile changes affect new launches, retries and existing workspaces
// retain their original image, and an unbound profile cannot use a template AMI.
func TestEC2ProfileImageAdmission(t *testing.T) {
	db, err := database.Open(t.Context(), database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "profile-launch.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(t.Context(), db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	const origin = "https://management.test"
	templateID, profileID := uuid.NewString(), uuid.NewString()
	token, now := strings.Repeat("A", 43), time.Now().UTC()
	seedWorkspaceLaunch(t, db, templateID, token, now)
	for _, id := range []string{"image-a", "image-b"} {
		_, err := db.Exec(`INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,
		 owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state,provider_kind,ec2_image_json)
		 SELECT $1,$1,architecture,platform_version,guest_release,protocol_version,owned_resources_json,digests_json,provenance_json,
		 manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state,'ec2',$2 FROM platform_artifacts WHERE id='artifact'`, id, `{"amiId":"ami-`+id+`","minimumRootDiskGiB":32}`)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`UPDATE users SET role='administrator' WHERE id='member'`,
		`UPDATE capacity_budgets SET cpu_limit=20,memory_mib_limit=65536,disk_mib_limit=1048576`,
		`UPDATE resource_profiles SET cpu_count=2,memory_mib=4096,disk_mib=32768,instance_type='t3.medium',platform_artifact_id='image-a' WHERE id='profile'`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	clock := func() time.Time { return now }
	policy := web.WorkspaceLaunchPolicy{Protocols: []string{"1"}, EC2VerifiedAMIs: []string{"ami-image-a", "ami-image-b"},
		Capabilities: provider.Capabilities{Name: "ec2", Create: true, Start: true, Architecture: "amd64", NetworkModes: []string{"shared"},
			Minimum: provider.Resources{DiskBytes: 32 << 30}, Maximum: provider.Resources{DiskBytes: 2047 << 30},
			InstanceTypes: []provider.InstanceType{{Name: "t3.medium", Architecture: "amd64", CPUCount: 2, MemoryMiB: 4096}}}}
	journal := operation.NewJournal(db, clock, uuid.NewString)
	launch := web.WorkspaceLaunchHandler(db, origin, clock, journal, policy)
	request := func(handler http.Handler, path, key, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, origin+path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		req.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("POST %s = %d %s, want %d", path, response.Code, response.Body, want)
		}
		return response
	}
	body := `{"templateId":"` + templateID + `","templateRevision":1}`
	first := request(&launch, "/api/v1/workspaces", "profile-image-first", body, 202)
	if _, err := db.Exec(`UPDATE resource_profiles SET platform_artifact_id='image-b',version=version+1 WHERE id='profile'`); err != nil {
		t.Fatal(err)
	}
	if replay := request(&launch, "/api/v1/workspaces", "profile-image-first", body, 202); replay.Body.String() != first.Body.String() {
		t.Errorf("launch replay=%s,want original %s", replay.Body, first.Body)
	}
	request(&launch, "/api/v1/workspaces", "profile-image-second", body, 202)
	var a, b int
	if err := db.QueryRow(`SELECT SUM(CASE WHEN platform_artifact_id='image-a' THEN 1 ELSE 0 END),SUM(CASE WHEN platform_artifact_id='image-b' THEN 1 ELSE 0 END) FROM workspaces`).Scan(&a, &b); err != nil || a != 1 || b != 1 {
		t.Errorf("workspace image snapshots=%d/%d (%v),want one each", a, b, err)
	}
	if _, err := db.Exec(`UPDATE workspaces SET platform_artifact_id='image-b' WHERE platform_artifact_id='image-a'`); err == nil {
		t.Error("existing workspace AMI changed; want immutable snapshot")
	}
	if _, err := db.Exec(`UPDATE resource_profiles SET platform_artifact_id=NULL WHERE id='profile'`); err != nil {
		t.Fatal(err)
	}
	blocked := request(&launch, "/api/v1/workspaces", "profile-image-unbound", body, 409)
	if !strings.Contains(blocked.Body.String(), "resource profile needs a compatible AMI") {
		t.Errorf("unbound profile error=%s,want profile AMI guidance", blocked.Body)
	}

	// AWS template creation takes just the selected profile, even if an old
	// independent image is supplied by an outdated client.
	if _, err := db.Exec(`INSERT INTO resource_profiles(id,name,cpu_count,memory_mib,disk_mib,enabled,version,instance_type,platform_artifact_id)
	 VALUES($1,'Bound profile',2,4096,32768,1,1,'t3.medium','image-b')`, profileID); err != nil {
		t.Fatal(err)
	}
	_, create, _ := web.TemplateHandlers(db, origin, clock, uuid.NewString)
	templateBody := `{"name":"Profile template","repositoryUrl":"https://github.com/example/repo","resourceProfileId":"` + profileID + `"}`
	created := request(&create, "/api/v1/templates", "profile-template-create", templateBody, 201)
	var view struct {
		PlatformArtifactID string `json:"platformArtifactId"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &view); err != nil || view.PlatformArtifactID != "image-b" {
		t.Errorf("created template image=%q(%v),want profile image-b", view.PlatformArtifactID, err)
	}

	// The catalog supplies image compatibility facts, but hides profiles whose
	// AMI must be assigned by an infrastructure administrator first.
	catalog := web.TemplateCatalogHandler(db, origin, clock, policy)
	req := httptest.NewRequest(http.MethodGet, origin+"/api/v1/templates/catalog", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
	response := httptest.NewRecorder()
	catalog.ServeHTTP(response, req)
	var choices struct {
		ProviderKind     string
		ResourceProfiles []struct{ ID, PlatformArtifactID string }
		PlatformImages   []struct {
			Architecture, AMIID string
			MinimumDiskGiB      int
		}
	}
	if err := json.Unmarshal(response.Body.Bytes(), &choices); err != nil || response.Code != 200 || choices.ProviderKind != "ec2" ||
		len(choices.ResourceProfiles) != 1 || choices.ResourceProfiles[0].ID != profileID || choices.ResourceProfiles[0].PlatformArtifactID != "image-b" ||
		len(choices.PlatformImages) != 2 || choices.PlatformImages[0].Architecture != "amd64" || choices.PlatformImages[0].AMIID != "ami-image-a" || choices.PlatformImages[0].MinimumDiskGiB != 32 {
		t.Errorf("AMI catalog=%+v (%v), status=%d; want verified image facts and only the bound profile", choices, err, response.Code)
	}

	// A family profile keeps its saved selection while validation of a patch
	// changes only subsequent admissions, including after a launch retry.
	baseArtifactID, patchArtifactID := uuid.NewString(), uuid.NewString()
	for _, patch := range []struct {
		id, state string
		created   int64
	}{{baseArtifactID, "available", 100}, {patchArtifactID, "validating", 200}} {
		_, err := db.Exec(`INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,
			owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,
			state,provider_kind,ec2_image_json,ec2_family,ec2_created_at)
			SELECT $1,$1,architecture,platform_version,guest_release,protocol_version,owned_resources_json,digests_json,provenance_json,
			manifest_json,manifest_sha256,$1,imported_by,imported_at,$2,'ec2',$3,'ubuntu',$4 FROM platform_artifacts WHERE id='artifact'`,
			patch.id, patch.state, `{"amiId":"ami-`+patch.id+`","minimumRootDiskGiB":32,"family":"ubuntu"}`, patch.created)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, createProfile := web.CapacityHandlers(db, origin, clock, uuid.NewString, policy)
	request(&createProfile, "/api/v1/admin/resource-profiles", "family-profile-create",
		`{"name":"Ubuntu family profile","instanceType":"t3.medium","diskMiB":32768,"amiFamily":"ubuntu"}`, 201)
	var familyProfileID string
	if err := db.QueryRow("SELECT id FROM resource_profiles WHERE name='Ubuntu family profile' AND ec2_ami_family='ubuntu'").Scan(&familyProfileID); err != nil {
		t.Fatal(err)
	}
	created = request(&create, "/api/v1/templates", "family-template-create",
		`{"name":"Family template","repositoryUrl":"https://github.com/example/repo","resourceProfileId":"`+familyProfileID+`"}`, 201)
	var familyTemplate struct{ ID string }
	if err := json.Unmarshal(created.Body.Bytes(), &familyTemplate); err != nil || familyTemplate.ID == "" {
		t.Fatalf("create family template = %q, %v; want template ID", created.Body.String(), err)
	}
	familyLaunch := `{"templateId":"` + familyTemplate.ID + `","templateRevision":1}`
	old := request(&launch, "/api/v1/workspaces", "family-launch-before", familyLaunch, 202)
	if _, err := db.Exec("UPDATE platform_artifacts SET state='available' WHERE id=$1", patchArtifactID); err != nil {
		t.Fatal(err)
	}
	request(&launch, "/api/v1/workspaces", "family-launch-after", familyLaunch, 202)
	if retry := request(&launch, "/api/v1/workspaces", "family-launch-before", familyLaunch, 202); retry.Body.String() != old.Body.String() {
		t.Error("family launch replay changed after publishing a patch")
	}
	for key, want := range map[string]string{"family-launch-before": baseArtifactID, "family-launch-after": patchArtifactID} {
		var image string
		var singleBoot bool
		err := db.QueryRow(`SELECT w.platform_artifact_id,b.operation_id IS NOT NULL FROM operations o
			JOIN workspaces w ON w.id=o.workspace_id LEFT JOIN ec2_launch_enrollment b ON b.operation_id=o.id
			WHERE o.request_key=$1`, key).Scan(&image, &singleBoot)
		if err != nil || image != want || !singleBoot {
			t.Errorf("launch %q = %q, single boot %t, %v; want %q, single boot", key, image, singleBoot, err, want)
		}
	}

	// Retiring an older image must preserve workspaces still bound to it.
	managedID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,
		owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,
		state,provider_kind,ec2_image_json,ec2_family,ec2_created_at)
		SELECT $1,'unused-release',architecture,platform_version,guest_release,protocol_version,owned_resources_json,digests_json,provenance_json,
		manifest_json,manifest_sha256,'ami-unused',imported_by,imported_at,state,provider_kind,ec2_image_json,ec2_family,50
		FROM platform_artifacts WHERE id=$2`, managedID, baseArtifactID); err != nil {
		t.Fatal(err)
	}
	retire := web.RetireAMIHandler(db, origin, clock)
	retireStatus := func(id string) int {
		retireRequest := httptest.NewRequest(http.MethodDelete, origin+"/api/v1/admin/platform-artifacts/"+id, nil)
		retireRequest.SetPathValue("artifactId", id)
		retireRequest.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		retireRequest.Header.Set("Origin", origin)
		retireRequest.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		retired := httptest.NewRecorder()
		retire.ServeHTTP(retired, retireRequest)
		return retired.Code
	}
	if status := retireStatus(baseArtifactID); status != http.StatusConflict {
		t.Errorf("retire image bound to an existing workspace = %d, want 409", status)
	}
	if status := retireStatus(managedID); status != http.StatusNoContent {
		t.Errorf("retire unused family image = %d, want 204", status)
	}
}
