package contract_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/imagecache"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	_ "modernc.org/sqlite"
)

const (
	builtContractAPI          = "/internal/v1/built-images"
	builtContractInstallation = "99999999-0000-4000-8000-000000000013"
	builtContractRepository   = "https://github.com/example/project"
	builtContractConfigPath   = ".devcontainer/devcontainer.json"
	builtContractConfig       = "{\n  \"image\": \"ubuntu:24.04\"\n}\n"
	builtContractDispatch     = int64(7)
)

// TestBuiltImageCacheNegotiatesExactConfigAndScope verifies that a project can
// reuse the exact config across commits and branches while other projects and
// platforms cannot resolve it or use its private registry references.
func TestBuiltImageCacheNegotiatesExactConfigAndScope(t *testing.T) {
	fixture := newBuiltContractFixture(t)
	image := newBuiltContractImage(t, "first build")
	publication := fixture.start(t, "first")
	fixture.upload(t, "first", publication, image)
	fixture.succeed(t, "first")
	if resumed := fixture.start(t, "first"); resumed != publication {
		t.Fatalf("resume after launching = %+v, want the same bounded attempt %+v", resumed, publication)
	}
	fixture.finish(t, "first", publication, "promoted")
	fixture.expect(t, "first", http.MethodPost, builtContractAPI+"/publications", "application/json",
		builtContractNegotiation(t, "first", fixture.source.configSHA), http.StatusNoContent)

	firstHit := fixture.lookup(t, "second", fixture.source.configSHA, http.StatusOK)
	fixture.assertPull(t, "second", firstHit, image)
	fixture.source.revisions["main"] = strings.Repeat("2", 40)
	fixture.assertPull(t, "second", fixture.lookup(t, "second", fixture.source.configSHA, http.StatusOK), image)
	fixture.moveProjectRef(t, "release")
	fixture.assertPull(t, "second", fixture.lookup(t, "second", fixture.source.configSHA, http.StatusOK), image)
	fixture.lookup(t, "second", digest.FromString(strings.TrimSpace(builtContractConfig)).Encoded(), http.StatusNoContent)
	fixture.lookup(t, "other-project", fixture.source.configSHA, http.StatusNoContent)
	fixture.lookup(t, "other-platform", fixture.source.configSHA, http.StatusNoContent)

	hit := fixture.lookup(t, "second", fixture.source.configSHA, http.StatusOK)
	repo, _ := builtContractReference(t, hit)
	for _, path := range []string{
		"/manifests/" + image.manifest.ID.String(), "/blobs/" + image.config.ID.String(), "/blobs/" + image.layer.ID.String(),
		"/blobs/" + digest.FromString("nonexistent").String(),
	} {
		fixture.expect(t, "other-project", http.MethodGet, "/v2/"+repo+path, "", nil, http.StatusForbidden)
		fixture.expect(t, "other-platform", http.MethodHead, "/v2/"+repo+path, "", nil, http.StatusForbidden)
	}
	other := fixture.start(t, "other-project")
	destination, _ := builtContractReference(t, other.Image)
	query := url.Values{"from": {repo}, "mount": {image.layer.ID.String()}}
	mount := fixture.request(t, "other-project", http.MethodPost, "/v2/"+destination+"/blobs/uploads/?"+query.Encode(), "", nil, 0)
	if mount.Code != http.StatusAccepted && mount.Code != http.StatusForbidden {
		t.Fatalf("cross-scope mount = %d, want regular upload admission or authorization denial", mount.Code)
	}
	fixture.expect(t, "other-project", http.MethodHead, "/v2/"+destination+"/blobs/"+image.layer.ID.String(), "", nil, http.StatusNotFound)
}

// TestBuiltImageCacheFencesExistingTransfers verifies live dispatch, membership,
// GitHub and policy checks on grants admitted before authority changed.
func TestBuiltImageCacheFencesExistingTransfers(t *testing.T) {
	fixture := newBuiltContractFixture(t)
	image := newBuiltContractImage(t, "published before revocation")
	publication := fixture.start(t, "first")
	fixture.upload(t, "first", publication, image)
	fixture.finish(t, "first", publication, "promoted")
	imagePath := func(t *testing.T) string {
		t.Helper()
		hit := fixture.lookup(t, "second", fixture.source.configSHA, http.StatusOK)
		repo, ref := builtContractReference(t, hit)
		return "/v2/" + repo + "/manifests/" + ref
	}

	readPath := imagePath(t)
	fixture.exec(t, `UPDATE github_authorizations SET state='revoked',version=5 WHERE user_id='built-bob'`)
	fixture.expect(t, "second", http.MethodHead, readPath, "", nil, http.StatusForbidden)
	fixture.exec(t, `UPDATE github_authorizations SET state='connected',version=6 WHERE user_id='built-bob'`)
	readPath = imagePath(t)
	fixture.exec(t, `UPDATE users SET membership_state='disabled' WHERE id='built-bob'`)
	fixture.expect(t, "second", http.MethodHead, readPath, "", nil, http.StatusForbidden)
	fixture.exec(t, `UPDATE users SET membership_state='active' WHERE id='built-bob'`)
	readPath = imagePath(t)
	fixture.exec(t, `UPDATE guest_dispatches SET dispatch_version=8 WHERE operation_id='built-launch-second'`)
	fixture.expect(t, "second", http.MethodHead, readPath, "", nil, http.StatusForbidden)
	fixture.exec(t, `UPDATE guest_dispatches SET dispatch_version=7 WHERE operation_id='built-launch-second'`)
	readPath = imagePath(t)
	staging := fixture.start(t, "other-project")
	location := fixture.openUpload(t, "other-project", staging)
	fixture.exec(t, `UPDATE installations SET built_image_enabled=0,built_image_generation=2,built_image_version=2,built_image_purge_cutoff=1`)
	fixture.expect(t, "second", http.MethodGet, readPath, "", nil, http.StatusForbidden)
	fixture.expect(t, "other-project", http.MethodPut, builtContractDigestURL(t, location, image.layer.ID),
		"application/octet-stream", image.layer.Body, http.StatusForbidden)
	fixture.exec(t, `UPDATE installations SET built_image_enabled=1,built_image_generation=3,built_image_version=3`)
	fixture.expect(t, "second", http.MethodHead, readPath, "", nil, http.StatusForbidden)
	fixture.lookup(t, "second", fixture.source.configSHA, http.StatusNoContent)
}

// TestBuiltImageCachePublishesFirstCompleteCandidate verifies that a partial
// upload and two overlapping misses expose only the first complete eligible
// attempt. A repeated finish returns the committed disposition.
func TestBuiltImageCachePublishesFirstCompleteCandidate(t *testing.T) {
	fixture := newBuiltContractFixture(t)
	older := fixture.start(t, "first")
	winner := fixture.start(t, "second")
	firstImage := newBuiltContractImage(t, "started first")
	secondImage := newBuiltContractImage(t, "finished first")
	location := fixture.openUpload(t, "first", older)
	response := fixture.request(t, "first", http.MethodPut, builtContractDigestURL(t, location, firstImage.layer.ID),
		"application/octet-stream", firstImage.layer.Body[:20], int64(len(firstImage.layer.Body)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("interrupted blob PUT = %d, want %d", response.Code, http.StatusBadRequest)
	}
	repo, _ := builtContractReference(t, older.Image)
	fixture.expect(t, "first", http.MethodHead, "/v2/"+repo+"/blobs/"+firstImage.layer.ID.String(), "", nil, http.StatusNotFound)
	fixture.lookup(t, "second", fixture.source.configSHA, http.StatusNoContent)

	fixture.upload(t, "first", older, firstImage)
	fixture.upload(t, "second", winner, secondImage)
	fixture.finish(t, "second", winner, "promoted")
	fixture.finish(t, "second", winner, "promoted")
	fixture.finish(t, "first", older, "discarded")
	hit := fixture.lookup(t, "first", fixture.source.configSHA, http.StatusOK)
	fixture.assertPull(t, "first", hit, secondImage)
	if _, ref := builtContractReference(t, hit); ref == firstImage.manifest.ID.String() {
		t.Fatalf("launch hit %s, want first successful publication %s", ref, secondImage.manifest.ID)
	}
}

type builtContractFixture struct {
	db      *sql.DB
	seed    *sql.Tx
	handler http.Handler
	source  *builtContractSource
	stamp   string
}

type builtContractSource struct {
	db        *sql.DB
	configSHA string
	revisions map[string]string
}

func (g *builtContractSource) AuthorizeProjectSource(ctx context.Context, owner, repository string) (int64, error) {
	canonical, ok := identity.CanonicalGitHubRepository(repository)
	if !ok || canonical != builtContractRepository+".git" {
		return 0, identity.ErrGitHubRepositoryDenied
	}
	var version int64
	err := g.db.QueryRowContext(ctx, `SELECT version FROM github_authorizations WHERE user_id=$1 AND state='connected'`, owner).Scan(&version)
	if err != nil {
		return 0, identity.ErrGitHubRepositoryDenied
	}
	return version, nil
}

func (g *builtContractSource) ReadProjectConfig(ctx context.Context, owner, repository, ref, configPath string) (identity.GitHubOriginConfig, error) {
	version, err := g.AuthorizeProjectSource(ctx, owner, repository)
	if err != nil || configPath != builtContractConfigPath || g.revisions[ref] == "" {
		return identity.GitHubOriginConfig{}, identity.ErrGitHubRepositoryDenied
	}
	return identity.GitHubOriginConfig{Repository: builtContractRepository + ".git", Revision: g.revisions[ref],
		ConfigSHA256: g.configSHA, AuthorizationVersion: version}, nil
}

func (g *builtContractSource) AuthorizationViewInTx(ctx context.Context, tx *sql.Tx, owner string) (identity.GitHubAuthorizationView, error) {
	view := identity.GitHubAuthorizationView{Provider: "github.com"}
	err := tx.QueryRowContext(ctx, `SELECT state,version FROM github_authorizations WHERE user_id=$1`, owner).Scan(&view.State, &view.Version)
	return view, err
}

func newBuiltContractFixture(t *testing.T) *builtContractFixture {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open(t.Context(), database.Config{Profile: "sqlite", SQLitePath: filepath.Join(dir, "built-cache.db")})
	if err != nil {
		t.Fatal(err)
	}
	state := &app.ControllerState{DB: db, Installation: app.ActiveInstallation{InstallationID: builtContractInstallation}}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Errorf("closing built cache fixture: %v", err)
		}
	})
	if err := database.Migrate(t.Context(), db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	fixture := &builtContractFixture{db: db, stamp: now.Format(time.RFC3339Nano), source: &builtContractSource{db: db,
		configSHA: digest.FromString(builtContractConfig).Encoded(), revisions: map[string]string{"main": strings.Repeat("1", 40), "release": strings.Repeat("3", 40)}}}
	fixture.seed, err = db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seed := fixture.seed
	t.Cleanup(func() { _ = seed.Rollback() })
	fixture.exec(t, `INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,
		preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
		VALUES($1,'active','ShiftBox','sqlite','https://management.test','{workspace}.ide.test','{service}--{workspace}.preview.test','test','test',$2,$2,1)`,
		builtContractInstallation, fixture.stamp)
	for _, user := range []string{"built-admin", "built-alice", "built-bob"} {
		fixture.exec(t, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
			VALUES($1,$2,'member','active','local','setup',$3,$3,1)`, user, user+"@example.test", fixture.stamp)
		fixture.exec(t, `INSERT INTO github_authorizations(user_id,app_id,provider_user_id,state,nonce,ciphertext,access_expires_at,
			refresh_expires_at,updated_at,version) VALUES($1,'fixture-app',$1,'connected',$2,$2,'','',$3,4)`, user, []byte{}, fixture.stamp)
	}
	fixture.exec(t, `INSERT INTO resource_profiles(id,name,cpu_count,memory_mib,disk_mib,enabled,version)
		VALUES('built-profile','Built cache fixture',2,1024,40,1,1)`)
	for _, arch := range []string{"amd64", "arm64"} {
		fixture.exec(t, `INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,
			owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state)
			VALUES($1,$1,$2,'test','test','1','[]','{}','{}','{}',$3,'fixture.utm','built-admin',$4,'available')`,
			"artifact-"+arch, arch, strings.Repeat("a", 64), fixture.stamp)
	}
	for _, project := range []string{"built-shared", "built-other"} {
		fixture.exec(t, `INSERT INTO project_templates(id,owner_user_id,name,state,current_revision,created_at,updated_at,version)
			VALUES($1,'built-admin',$1,'active',1,$2,$2,1)`, project, fixture.stamp)
		fixture.exec(t, `INSERT INTO template_revisions(template_id,revision,repository_url,repository_ref,devcontainer_path,resource_profile_id,
			platform_artifact_id,secret_names_json,created_by,created_at)
			VALUES($1,1,$2,'main',$3,'built-profile','artifact-amd64','[]','built-admin',$4)`, project, builtContractRepository, builtContractConfigPath, fixture.stamp)
		for _, member := range []string{"built-alice", "built-bob"} {
			fixture.exec(t, `INSERT INTO template_grants(template_id,user_id,can_launch,project_secret_warning_ack_at,granted_by,granted_at)
				VALUES($1,$2,1,$3,'built-admin',$3)`, project, member, fixture.stamp)
		}
	}
	contacts := map[string]guest.Contact{}
	for _, workspace := range []struct{ suffix, owner, project, arch string }{
		{"first", "built-alice", "built-shared", "amd64"}, {"second", "built-bob", "built-shared", "amd64"},
		{"other-project", "built-bob", "built-other", "amd64"}, {"other-platform", "built-bob", "built-shared", "arm64"},
	} {
		id, operation := "built-workspace-"+workspace.suffix, "built-launch-"+workspace.suffix
		fixture.exec(t, `INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,
			repository_url,repository_ref,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,
			observed_state,generation,ownership_marker,provider_instance_id,created_at,updated_at,version)
			VALUES($1,$2,$3,$4,1,$5,$6,'main',$7,2,1024,40,'{"mode":"shared"}','running','running',2,'cache-fixture',$1,$8,$8,1)`,
			id, builtContractInstallation, workspace.owner, workspace.project, "artifact-"+workspace.arch, builtContractRepository, builtContractConfigPath, fixture.stamp)
		fixture.exec(t, `INSERT INTO guest_registrations(workspace_id,generation,state,certificate_serial,certificate_not_after,public_key_fingerprint)
			VALUES($1,2,'active','01',$2,$3)`, id, now.Add(time.Hour).Format(time.RFC3339Nano), strings.Repeat("a", 64))
		fixture.exec(t, `INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,current_step,created_at,
			started_at,version,originating_action) VALUES($1,$2,'provision',2,$3,$1,'running','configure',$4,$4,7,'workspace.launch')`,
			operation, id, workspace.owner, fixture.stamp)
		fixture.exec(t, `INSERT INTO operation_steps(operation_id,step_index,step_key,label,status,started_at)
			VALUES($1,0,'configure','Configure workspace','active',$2)`, operation, fixture.stamp)
		fixture.exec(t, `INSERT INTO guest_dispatches(operation_id,dispatch_version,status) VALUES($1,$2,'running')`, operation, builtContractDispatch)
		contacts[workspace.suffix] = guest.Contact{WorkspaceID: id, OwnerID: workspace.owner, Generation: 2, Repository: builtContractRepository, DesiredState: "running"}
	}
	if err := fixture.seed.Commit(); err != nil {
		t.Fatal(err)
	}
	fixture.seed = nil
	// Keep this transport fixture independent of the test runner's ordinary
	// disk utilization. Storage pressure is exercised by the storage tests.
	settings := imagecache.Settings{StoragePath: imagecache.DefaultPath(dir, ""), MaxBytes: imagecache.Bytes(16 << 20),
		VolumeHighPercent: 98, VolumeLowPercent: 97}
	configured, err := json.Marshal(imagecache.Configuration{Version: 1, Desired: settings,
		Effective: imagecache.EffectiveSettings{Version: 1, Settings: settings}})
	if err != nil {
		t.Fatal(err)
	}
	fixture.exec(t, `UPDATE installations SET image_cache_settings_json=$1`, string(configured))
	clock := func() time.Time { return time.Now().UTC() }
	if err := state.ConfigureImageCache(t.Context(), dir, "", "https://built.example:9443", clock); err != nil {
		t.Fatal(err)
	}
	fixture.handler = state.BuiltImageHandler(&proxy.Registry{}, fixture.source,
		func(_ context.Context, leaf *x509.Certificate, _ time.Time) (guest.Contact, error) {
			if leaf != nil {
				if contact, ok := contacts[leaf.Subject.CommonName]; ok {
					return contact, nil
				}
			}
			return guest.Contact{}, guest.ErrGuestUnauthorized
		}, clock)
	return fixture
}

func (f *builtContractFixture) exec(t *testing.T, statement string, args ...any) {
	t.Helper()
	exec := f.db.ExecContext
	if f.seed != nil {
		exec = f.seed.ExecContext
	}
	if _, err := exec(t.Context(), statement, args...); err != nil {
		t.Fatalf("seed/update cache fixture (%s, %v): %v", statement, args, err)
	}
}

func (f *builtContractFixture) succeed(t *testing.T, guest string) {
	t.Helper()
	operation := "built-launch-" + guest
	f.exec(t, `UPDATE guest_dispatches SET status='succeeded',container_id=$1 WHERE operation_id=$2`, strings.Repeat("b", 64), operation)
	f.exec(t, `UPDATE operation_steps SET status='succeeded',finished_at=$1 WHERE operation_id=$2`, f.stamp, operation)
	f.exec(t, `UPDATE operations SET status='succeeded',finished_at=$1,current_step=NULL WHERE id=$2`, f.stamp, operation)
}

func (f *builtContractFixture) moveProjectRef(t *testing.T, ref string) {
	t.Helper()
	f.exec(t, `INSERT INTO template_revisions(template_id,revision,repository_url,repository_ref,devcontainer_path,resource_profile_id,
		platform_artifact_id,secret_names_json,created_by,created_at)
		SELECT template_id,2,repository_url,$1,devcontainer_path,resource_profile_id,platform_artifact_id,secret_names_json,created_by,created_at
		FROM template_revisions WHERE template_id='built-shared' AND revision=1`, ref)
	f.exec(t, `UPDATE project_templates SET current_revision=2,version=2 WHERE id='built-shared'`)
}

func (f *builtContractFixture) request(t *testing.T, guest, method, path, mediaType string, body []byte, length int64) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequestWithContext(t.Context(), method, "https://built.example:9443"+path, bytes.NewReader(body))
	request.ContentLength = length
	if mediaType != "" {
		request.Header.Set("Content-Type", mediaType)
	}
	leaf := &x509.Certificate{Subject: pkix.Name{CommonName: guest}, NotAfter: time.Now().Add(time.Hour)}
	request.TLS = &tls.ConnectionState{Version: tls.VersionTLS13, PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}}}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

func (f *builtContractFixture) expect(t *testing.T, guest, method, path, mediaType string, body []byte, want int) *httptest.ResponseRecorder {
	t.Helper()
	response := f.request(t, guest, method, path, mediaType, body, int64(len(body)))
	if response.Code != want {
		t.Fatalf("%s %s as %s = %d %s, want %d", method, path, guest, response.Code, response.Body.String(), want)
	}
	return response
}

func builtContractNegotiation(t *testing.T, guestName, hash string) []byte {
	t.Helper()
	data, err := json.Marshal(struct {
		OperationID     string `json:"operationId"`
		DispatchVersion int64  `json:"dispatchVersion"`
		ConfigSHA256    string `json:"configSha256"`
	}{OperationID: "built-launch-" + guestName, DispatchVersion: builtContractDispatch, ConfigSHA256: hash})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (f *builtContractFixture) lookup(t *testing.T, guest, hash string, want int) string {
	t.Helper()
	body := builtContractNegotiation(t, guest, hash)
	response := f.expect(t, guest, http.MethodPost, builtContractAPI+"/lookup", "application/json", body, want)
	if want == http.StatusNoContent {
		if response.Body.Len() != 0 {
			t.Fatalf("cache miss body = %q, want empty", response.Body.String())
		}
		return ""
	}
	var result struct {
		Image string `json:"image"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Image == "" {
		t.Fatalf("cache hit = %s (%v), want immutable image reference", response.Body.String(), err)
	}
	return result.Image
}

type builtContractPublication struct {
	AttemptID string    `json:"attemptId"`
	Image     string    `json:"image"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (f *builtContractFixture) start(t *testing.T, guest string) builtContractPublication {
	t.Helper()
	body := builtContractNegotiation(t, guest, f.source.configSHA)
	response := f.expect(t, guest, http.MethodPost, builtContractAPI+"/publications", "application/json", body, http.StatusCreated)
	var result builtContractPublication
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.AttemptID == "" || !result.ExpiresAt.After(time.Now()) {
		t.Fatalf("publication admission = %s (%v), want bounded attempt and image", response.Body.String(), err)
	}
	if _, ref := builtContractReference(t, result.Image); ref != "candidate" {
		t.Fatalf("staging reference = %s, want candidate tag", result.Image)
	}
	return result
}

func (f *builtContractFixture) finish(t *testing.T, guest string, publication builtContractPublication, want string) {
	t.Helper()
	endpoint := builtContractAPI + "/publications/" + publication.AttemptID + "/finish"
	response := f.expect(t, guest, http.MethodPost, endpoint, "", nil, http.StatusOK)
	var result struct {
		Disposition string `json:"disposition"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Disposition != want {
		t.Fatalf("finish %s = %s (%v), want %s", publication.AttemptID, response.Body.String(), err, want)
	}
}

func (f *builtContractFixture) openUpload(t *testing.T, guest string, publication builtContractPublication) string {
	t.Helper()
	repo, _ := builtContractReference(t, publication.Image)
	response := f.expect(t, guest, http.MethodPost, "/v2/"+repo+"/blobs/uploads/", "", nil, http.StatusAccepted)
	if location := response.Header().Get("Location"); strings.HasPrefix(location, "/v2/"+repo+"/blobs/uploads/") {
		return location
	}
	t.Fatalf("upload Location = %q, want relative location within %s", response.Header().Get("Location"), repo)
	return ""
}

func builtContractDigestURL(t *testing.T, location string, id digest.Digest) string {
	t.Helper()
	endpoint, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	query := endpoint.Query()
	query.Set("digest", id.String())
	endpoint.RawQuery = query.Encode()
	return endpoint.String()
}

func builtContractReference(t *testing.T, image string) (repository, reference string) {
	t.Helper()
	host, path, ok := strings.Cut(image, "/")
	if !ok || host != "built.example:9443" || !strings.HasPrefix(path, "shiftbox-built/") {
		t.Fatalf("image reference = %s, want direct guest registry private namespace", image)
	}
	if repository, reference, ok := strings.Cut(path, "@"); ok {
		return repository, reference
	}
	if repository, reference, ok := strings.Cut(path, ":"); ok {
		return repository, reference
	}
	t.Fatalf("image reference = %s, want tagged or digested image", image)
	return "", ""
}

func (f *builtContractFixture) upload(t *testing.T, guest string, publication builtContractPublication, image builtContractImage) {
	t.Helper()
	repo, _ := builtContractReference(t, publication.Image)
	for _, blob := range []builtContractObject{image.config, image.layer} {
		f.expect(t, guest, http.MethodHead, "/v2/"+repo+"/blobs/"+blob.ID.String(), "", nil, http.StatusNotFound)
		location := f.openUpload(t, guest, publication)
		result := f.expect(t, guest, http.MethodPut, builtContractDigestURL(t, location, blob.ID), "application/octet-stream", blob.Body, http.StatusCreated)
		if got := result.Header().Get("Docker-Content-Digest"); got != blob.ID.String() {
			t.Fatalf("committed blob digest = %s, want %s", got, blob.ID)
		}
	}
	manifestPath := "/v2/" + repo + "/manifests/candidate"
	accept := f.expect(t, guest, http.MethodPut, manifestPath, v1.MediaTypeImageManifest, image.manifest.Body, http.StatusCreated)
	if got := accept.Header().Get("Docker-Content-Digest"); got != image.manifest.ID.String() {
		t.Fatalf("committed manifest digest = %s, want %s", got, image.manifest.ID)
	}
}

func (f *builtContractFixture) assertPull(t *testing.T, guest, hit string, image builtContractImage) {
	t.Helper()
	repo, ref := builtContractReference(t, hit)
	if ref != image.manifest.ID.String() {
		t.Fatalf("pull image reference = %s, want %s", ref, image.manifest.ID)
	}
	prefix := "/v2/" + repo
	manifest := f.expect(t, guest, http.MethodGet, prefix+"/manifests/"+ref, "", nil, http.StatusOK)
	if !bytes.Equal(manifest.Body.Bytes(), image.manifest.Body) {
		t.Errorf("pull manifest bytes do not match committed digest %s", image.manifest.ID)
	}
	for _, blob := range []builtContractObject{image.config, image.layer} {
		response := f.expect(t, guest, http.MethodGet, prefix+"/blobs/"+blob.ID.String(), "", nil, http.StatusOK)
		if !bytes.Equal(response.Body.Bytes(), blob.Body) {
			t.Errorf("pull blob bytes do not match committed digest %s", blob.ID)
		}
	}
}

type builtContractImage struct {
	manifest, config, layer builtContractObject
}

type builtContractObject struct {
	ID   digest.Digest
	Body []byte
}

func newBuiltContractImage(t *testing.T, contents string) builtContractImage {
	t.Helper()
	object := func(data []byte) builtContractObject {
		return builtContractObject{ID: digest.FromBytes(data), Body: data}
	}
	marshal := func(value any) []byte {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var layerTar bytes.Buffer
	archive := tar.NewWriter(&layerTar)
	if err := archive.WriteHeader(&tar.Header{Name: "fixture", Mode: 0o644, Size: int64(len(contents))}); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Write([]byte(contents)); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	layer := object(layerTar.Bytes())
	config := object(marshal(v1.Image{Platform: v1.Platform{OS: "linux", Architecture: "amd64"},
		RootFS: v1.RootFS{Type: "layers", DiffIDs: []digest.Digest{layer.ID}}}))
	manifest := object(marshal(v1.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: v1.MediaTypeImageManifest,
		Config: v1.Descriptor{Digest: config.ID, Size: int64(len(config.Body)), MediaType: v1.MediaTypeImageConfig},
		Layers: []v1.Descriptor{{Digest: layer.ID, Size: int64(len(layer.Body)), MediaType: v1.MediaTypeImageLayer}}}))
	return builtContractImage{manifest: manifest, config: config, layer: layer}
}
