package app

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/imagecache"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// TestCacheStatusIncludesPublishedDevcontainer verifies that the administrator
// inventory resolves a promoted slot to a project name and retained layer sizes.
func TestCacheStatusIncludesPublishedDevcontainer(t *testing.T) {
	fixture := newBuiltRebuildFixture(t)
	image := fixture.publishInitial(t)
	fixture.exec(t, `UPDATE project_templates SET name='Python tools' WHERE id='project-first'`)
	status, err := fixture.service.cache.status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range status.CachedImages {
		if item.Digest == image.id && item.Reference == "Python tools" && item.Source == "devcontainer" &&
			item.Availability == "complete" && !item.AddedAt.IsZero() && len(item.LayerBytes) == 1 &&
			item.LayerBytes[0] != nil && *item.LayerBytes[0] == strconv.Itoa(len(image.layer)) {
			return
		}
	}
	t.Errorf("status().CachedImages = %+v, want the complete Python tools image %s", status.CachedImages, image.id)
}

// TestBuiltImageRebuildPromotesCurrentOrigin holds an old read grant across
// replacement and verifies that promotion uses the project's current ref.
func TestBuiltImageRebuildPromotesCurrentOrigin(t *testing.T) {
	fixture := newBuiltRebuildFixture(t)
	old := fixture.publishInitial(t)
	fixture.revise(t, "release")
	reader := fixture.addReader(t)
	address, err := fixture.service.lookup(t.Context(), reader, "launch-reader", builtAuthorityDispatch, fixture.configSHA)
	if err != nil || !strings.HasSuffix(address, "@"+old.id.String()) {
		t.Fatalf("lookup before rebuild = %q, %v, want prior manifest %s", address, err, old.id)
	}
	_, readScope, _ := strings.Cut(address, "/"+builtImageRepositoryPrefix)
	readScope, _, _ = strings.Cut(readScope, "@")

	replacement := newBuiltRebuildImage(t, "rebuilt on the current release ref")
	attempt := fixture.begin(t, fixture.configSHA)
	fixture.stage(t, attempt, replacement)
	if disposition, err := fixture.service.finish(t.Context(), fixture.contact, attempt.AttemptID); disposition != "" || !errors.Is(err, web.ErrBuiltImagePromotionPending) {
		t.Errorf("finish before rebuild succeeds = %q, %v, want retryable pending response", disposition, err)
	}
	fixture.requireCurrent(t, old.id, fixture.source.versions["main"].Revision)
	fixture.succeed(t, "fixture-rebuild")
	disposition, err := fixture.service.finish(t.Context(), fixture.contact, attempt.AttemptID)
	if err != nil || disposition != "promoted" {
		t.Fatalf("finish explicit rebuild = %q, %v, want promoted", disposition, err)
	}
	fixture.requireCurrent(t, replacement.id, fixture.source.versions["release"].Revision)
	if fixture.source.lastRef != "release" || fixture.source.lastPath != builtAuthorityConfig {
		t.Errorf("remote comparison used %q %q, want current release ref and %q", fixture.source.lastRef, fixture.source.lastPath, builtAuthorityConfig)
	}
	_, transfer, err := fixture.service.transfer(t.Context(), reader, readScope)
	if err != nil {
		t.Fatalf("old read grant after replacement = %v, want retained reader authority", err)
	}
	defer transfer.close()
	object, _, err := transfer.lease.Open(digest.FromBytes(old.layer))
	if err != nil {
		t.Fatalf("open old pinned layer after replacement = %v, want success", err)
	}
	data, err := io.ReadAll(object)
	object.Close()
	if err != nil || !bytes.Equal(data, old.layer) {
		t.Errorf("old pinned layer after replacement = %d bytes, %v, want original %d bytes", len(data), err, len(old.layer))
	}
}

// TestBuiltImageRebuildUploadsAfterConfigure keeps an admitted OCI upload
// working while the IDE and remaining hooks run, then promotes after success.
func TestBuiltImageRebuildUploadsAfterConfigure(t *testing.T) {
	fixture := newBuiltRebuildFixture(t)
	previous := fixture.publishInitial(t)
	attempt := fixture.begin(t, fixture.configSHA)
	fixture.exec(t, `UPDATE operation_steps SET status='succeeded',finished_at=$1
		WHERE operation_id='fixture-rebuild' AND step_key='configure'`, builtAuthorityStamp)
	fixture.exec(t, `UPDATE operations SET current_step='verify_ide' WHERE id='fixture-rebuild'`)
	replacement := newBuiltRebuildImage(t, "upload while the IDE is starting")
	fixture.stage(t, attempt, replacement)
	fixture.exec(t, `UPDATE operations SET current_step='finish_setup' WHERE id='fixture-rebuild'`)
	if disposition, err := fixture.service.finish(t.Context(), fixture.contact, attempt.AttemptID); disposition != "" || !errors.Is(err, web.ErrBuiltImagePromotionPending) {
		t.Errorf("finish during remaining setup = %q, %v, want retryable pending response", disposition, err)
	}
	fixture.requireCurrent(t, previous.id, fixture.source.versions["main"].Revision)
	fixture.succeed(t, "fixture-rebuild")
	if disposition, err := fixture.service.finish(t.Context(), fixture.contact, attempt.AttemptID); err != nil || disposition != "promoted" {
		t.Fatalf("finish after rebuild succeeds = %q, %v, want promoted", disposition, err)
	}
	fixture.requireCurrent(t, replacement.id, fixture.source.versions["main"].Revision)
}

// TestBuiltImageRebuildPreservesImageWhenOriginChanges covers stale config
// bytes and a project ref change while its old commit is being checked remotely.
func TestBuiltImageRebuildPreservesImageWhenOriginChanges(t *testing.T) {
	for _, scenario := range []string{"stale retained config", "origin config changed", "configured ref changed during read"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newBuiltRebuildFixture(t)
			old := fixture.publishInitial(t)
			buildConfig := fixture.configSHA
			if scenario == "stale retained config" {
				buildConfig = digest.FromString("retained workspace's old exact config").Encoded()
			}
			attempt := fixture.begin(t, buildConfig)
			fixture.stage(t, attempt, newBuiltRebuildImage(t, scenario))
			fixture.succeed(t, "fixture-rebuild")
			switch scenario {
			case "origin config changed":
				current := fixture.source.versions["main"]
				current.ConfigSHA256 = digest.FromString("new exact config bytes").Encoded()
				current.Revision = strings.Repeat("d", 40)
				fixture.source.versions["main"] = current
			case "configured ref changed during read":
				fixture.source.afterRead = func() { fixture.revise(t, "release") }
			}
			disposition, err := fixture.service.finish(t.Context(), fixture.contact, attempt.AttemptID)
			if disposition != "discarded" && !errors.Is(err, guest.ErrGuestUnauthorized) {
				t.Errorf("finish after %s = %q, %v, want discard or fenced authority", scenario, disposition, err)
			}
			fixture.requireCurrent(t, old.id, strings.Repeat("a", 40))
		})
	}
}

// TestBuiltImageRebuildPreservesImageAfterInterruptedReplacement confirms that
// a truncated layer cannot displace the previously complete published image.
func TestBuiltImageRebuildPreservesImageAfterInterruptedReplacement(t *testing.T) {
	fixture := newBuiltRebuildFixture(t)
	previous := fixture.publishInitial(t)
	attempt := fixture.begin(t, fixture.configSHA)
	replacement := newBuiltRebuildImage(t, "interrupted rebuild")
	_, transfer, err := fixture.service.transfer(t.Context(), fixture.contact, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	err = transfer.cache.PutBuiltBlob(t.Context(), transfer.scope, digest.FromBytes(replacement.layer), int64(len(replacement.layer)), bytes.NewReader(replacement.layer[:8]))
	transfer.close()
	if !errors.Is(err, imagecache.ErrBuiltInvalid) {
		t.Errorf("truncated rebuild layer = %v, want invalid bytes", err)
	}
	fixture.succeed(t, "fixture-rebuild")
	if disposition, err := fixture.service.finish(t.Context(), fixture.contact, attempt.AttemptID); err != nil || disposition != "discarded" {
		t.Errorf("finish interrupted rebuild = %q, %v, want discarded", disposition, err)
	}
	fixture.requireCurrent(t, previous.id, strings.Repeat("a", 40))
}

// TestBuiltImageRebuildDeclinesReplacementUnderPressure demonstrates that a
// replacement cannot evict its prior image to make room for the staged layer.
func TestBuiltImageRebuildDeclinesReplacementUnderPressure(t *testing.T) {
	for _, scenario := range []string{"active attempt", "resumed after restart with stale config"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newBuiltRebuildFixture(t)
			previous := fixture.publishInitial(t)
			usage := fixture.runtime.active.Status(time.Now()).Usage
			used, err := strconv.ParseInt(*usage.RetainedBytes, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			fixture.settings.MaxBytes = imagecache.Bytes(used + 64)
			fixture.runtime.active.SetLimits(fixture.settings, 0)
			config := fixture.configSHA
			if scenario == "resumed after restart with stale config" {
				config = digest.FromString("old retained config").Encoded()
			}
			attempt := fixture.begin(t, config)
			if err := fixture.runtime.active.FitBudget(t.Context(), used-1, 0); err == nil {
				t.Error("previous closure was evictable immediately after replacement admission")
			}
			if scenario == "resumed after restart with stale config" {
				fixture.restart(t, attempt.AttemptID)
			}
			replacement := newBuiltRebuildImage(t, "separate layer")
			_, transfer, err := fixture.service.transfer(t.Context(), fixture.contact, attempt.AttemptID)
			if err != nil {
				t.Fatal(err)
			}
			err = transfer.cache.PutBuiltBlob(t.Context(), transfer.scope, digest.FromBytes(replacement.layer), int64(len(replacement.layer)), bytes.NewReader(replacement.layer))
			transfer.close()
			if !errors.Is(err, imagecache.ErrBuiltStorageFull) {
				t.Errorf("put replacement layer under pressure = %v, want storage refusal", err)
			}
			fixture.requireCurrent(t, previous.id, strings.Repeat("a", 40))
			fixture.succeed(t, "fixture-rebuild")
			if disposition, err := fixture.service.finish(t.Context(), fixture.contact, attempt.AttemptID); err != nil || disposition != "discarded" {
				t.Errorf("finish after storage refusal = %q, %v, want discarded", disposition, err)
			}
			if err := fixture.runtime.active.FitBudget(t.Context(), used-1, 0); err != nil {
				t.Errorf("previous closure stayed pinned after terminal discard: %v", err)
			}
		})
	}
}

// TestBuiltImageRebuildPreservesImageAfterFailure checks that a complete staged
// image from a guest dispatch cannot publish when its workspace rebuild fails.
func TestBuiltImageRebuildPreservesImageAfterFailure(t *testing.T) {
	fixture := newBuiltRebuildFixture(t)
	previous := fixture.publishInitial(t)
	attempt := fixture.begin(t, fixture.configSHA)
	fixture.stage(t, attempt, newBuiltRebuildImage(t, "complete image from the failed rebuild"))
	fixture.reportDispatchSuccess(t, "fixture-rebuild")
	if disposition, err := fixture.service.finish(t.Context(), fixture.contact, attempt.AttemptID); disposition != "" || !errors.Is(err, web.ErrBuiltImagePromotionPending) {
		t.Errorf("finish after guest dispatch succeeds but rebuild is still running = %q, %v, want retryable pending response", disposition, err)
	}
	fixture.exec(t, `UPDATE operations SET status='failed',finished_at=$1 WHERE id='fixture-rebuild'`, builtAuthorityStamp)
	if disposition, err := fixture.service.finish(t.Context(), fixture.contact, attempt.AttemptID); disposition != "discarded" && !errors.Is(err, guest.ErrGuestUnauthorized) {
		t.Errorf("finish after failed rebuild = %q, %v, want discard or denial", disposition, err)
	}
	fixture.requireCurrent(t, previous.id, strings.Repeat("a", 40))
}

type builtRebuildFixture struct {
	db        *sql.DB
	runtime   *imageCacheRuntime
	settings  imagecache.Settings
	service   *builtImageService
	source    *builtRebuildSource
	contact   guest.Contact
	configSHA string
}

type builtRebuildSource struct {
	versions          map[string]identity.GitHubOriginConfig
	lastRef, lastPath string
	afterRead         func()
}

func (s *builtRebuildSource) AuthorizeProjectSource(_ context.Context, owner, repository string) (int64, error) {
	if owner != builtAuthorityOwner || repository != builtAuthorityRepository {
		return 0, identity.ErrGitHubRepositoryDenied
	}
	return 4, nil
}

func (s *builtRebuildSource) ReadProjectConfig(ctx context.Context, owner, repository, ref, path string) (identity.GitHubOriginConfig, error) {
	if _, err := s.AuthorizeProjectSource(ctx, owner, repository); err != nil {
		return identity.GitHubOriginConfig{}, err
	}
	s.lastRef, s.lastPath = ref, path
	origin, exists := s.versions[ref]
	if !exists || path != builtAuthorityConfig {
		return identity.GitHubOriginConfig{}, identity.ErrGitHubRepositoryDenied
	}
	if s.afterRead != nil {
		s.afterRead()
	}
	return origin, nil
}

func (s *builtRebuildSource) AuthorizationViewInTx(ctx context.Context, tx *sql.Tx, owner string) (identity.GitHubAuthorizationView, error) {
	var view identity.GitHubAuthorizationView
	err := tx.QueryRowContext(ctx, `SELECT state,version FROM github_authorizations WHERE user_id=$1`, owner).Scan(&view.State, &view.Version)
	return view, err
}

func newBuiltRebuildFixture(t *testing.T) *builtRebuildFixture {
	t.Helper()
	db := seedBuiltImageAuthority(t)
	dir := t.TempDir()
	settings := imagecache.Settings{StoragePath: imagecache.DefaultPath(dir, ""), MaxBytes: imagecache.Bytes(16 << 20),
		VolumeHighPercent: 98, VolumeLowPercent: 97}
	configuration := imagecache.Configuration{Version: 1, Desired: settings,
		Effective: imagecache.EffectiveSettings{Version: 1, Settings: settings}}
	encoded, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &builtRebuildFixture{db: db, settings: settings, configSHA: digest.FromString("exact project config bytes\n").Encoded(),
		contact: guest.Contact{WorkspaceID: "workspace-first", OwnerID: builtAuthorityOwner,
			Repository: builtAuthorityRepository, Generation: builtAuthorityGeneration, DesiredState: "running"}}
	fixture.exec(t, `UPDATE installations SET image_cache_settings_json=$1`, string(encoded))
	fixture.runtime, err = newImageCacheRuntime(t.Context(), db, dir, "", "https://guest.test:8444", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	registry := &proxy.Registry{}
	t.Cleanup(func() {
		if err := registry.CloseMatching(func(scope proxy.Scope) bool { return scope.BuiltImageGeneration > 0 }); err != nil {
			t.Error(err)
		}
		if err := fixture.runtime.Close(); err != nil {
			t.Errorf("close rebuild cache runtime: %v", err)
		}
	})
	fixture.source = &builtRebuildSource{versions: make(map[string]identity.GitHubOriginConfig)}
	for ref, commit := range map[string]string{"main": strings.Repeat("a", 40), "release": strings.Repeat("b", 40)} {
		fixture.source.versions[ref] = identity.GitHubOriginConfig{Repository: builtAuthorityRepository + ".git",
			Revision: commit, ConfigSHA256: fixture.configSHA, AuthorizationVersion: 4}
	}
	fixture.service = newBuiltImageService(fixture.runtime, fixture.source, registry, time.Now)
	return fixture
}

func (f *builtRebuildFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatalf("rebuild fixture update %s: %v", query, err)
	}
}

func (f *builtRebuildFixture) restart(t *testing.T, attemptID string) {
	t.Helper()
	f.service.releasePrevious(attemptID)
	dir, endpoint, registry := f.runtime.installationDir, f.runtime.guestEndpoint, f.service.registry
	if err := f.runtime.Close(); err != nil {
		t.Fatal(err)
	}
	runtime, err := newImageCacheRuntime(t.Context(), f.db, dir, "", endpoint, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	f.runtime = runtime
	f.runtime.active.SetLimits(f.settings, 0)
	f.service = newBuiltImageService(f.runtime, f.source, registry, time.Now)
}

func (f *builtRebuildFixture) revise(t *testing.T, ref string) {
	t.Helper()
	f.exec(t, `INSERT INTO template_revisions(template_id,revision,repository_url,repository_ref,devcontainer_path,
		resource_profile_id,platform_artifact_id,secret_names_json,created_by,created_at)
		SELECT template_id,3,repository_url,$1,devcontainer_path,resource_profile_id,platform_artifact_id,secret_names_json,created_by,created_at
		FROM template_revisions WHERE template_id='project-first' AND revision=2`, ref)
	f.exec(t, `UPDATE project_templates SET current_revision=3,version=3 WHERE id='project-first'`)
}

func (f *builtRebuildFixture) publishInitial(t *testing.T) builtRebuildImage {
	t.Helper()
	image := newBuiltRebuildImage(t, "published first image")
	publication, err := f.service.begin(t.Context(), f.contact, "launch-first", builtAuthorityDispatch, f.configSHA)
	if err != nil || publication.AttemptID == "" {
		t.Fatalf("begin initial fixture publication = %+v, %v, want an attempt", publication, err)
	}
	f.stage(t, publication, image)
	if disposition, err := f.service.finish(t.Context(), f.contact, publication.AttemptID); err != nil || disposition != "promoted" {
		t.Fatalf("publish initial image = %q, %v, want promoted", disposition, err)
	}
	f.succeed(t, "launch-first")
	return image
}

func (f *builtRebuildFixture) addReader(t *testing.T) guest.Contact {
	t.Helper()
	f.exec(t, `INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,
		repository_url,repository_ref,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,
		observed_state,generation,ownership_marker,provider_instance_id,created_at,updated_at,version)
		SELECT 'workspace-reader',installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,repository_url,
			repository_ref,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,
			observed_state,generation,'reader-fixture','workspace-reader',created_at,updated_at,version
		FROM workspaces WHERE id='workspace-first'`)
	f.exec(t, `INSERT INTO guest_registrations(workspace_id,generation,state,certificate_serial,certificate_not_after,public_key_fingerprint)
		SELECT 'workspace-reader',generation,state,certificate_serial,certificate_not_after,public_key_fingerprint
		FROM guest_registrations WHERE workspace_id='workspace-first'`)
	f.exec(t, `INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,current_step,created_at,started_at,version,originating_action)
		VALUES('launch-reader','workspace-reader','provision',$1,$2,'launch-reader','running','configure',$3,$3,7,'workspace.launch')`,
		builtAuthorityGeneration, builtAuthorityOwner, builtAuthorityStamp)
	f.addDispatch(t, "launch-reader")
	reader := f.contact
	reader.WorkspaceID = "workspace-reader"
	return reader
}

func (f *builtRebuildFixture) begin(t *testing.T, configSHA string) builtImagePublication {
	t.Helper()
	const operation = "fixture-rebuild"
	created := builtImageTime(time.Now())
	f.exec(t, `INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,current_step,created_at,started_at,version,originating_action)
		VALUES($1,'workspace-first','rebuild',$2,$3,$1,'running','configure',$4,$4,7,'workspace.rebuild')`,
		operation, builtAuthorityGeneration, builtAuthorityOwner, created)
	f.addDispatch(t, operation)
	publication, err := f.service.begin(t.Context(), f.contact, operation, builtAuthorityDispatch, configSHA)
	if err != nil || publication.AttemptID == "" {
		t.Fatalf("begin rebuild publication = %+v, %v, want a bounded attempt", publication, err)
	}
	return publication
}

func (f *builtRebuildFixture) addDispatch(t *testing.T, operation string) {
	t.Helper()
	f.exec(t, `INSERT INTO operation_steps(operation_id,step_index,step_key,label,status,started_at)
		VALUES($1,0,'configure','Configure workspace','active',$2)`, operation, builtAuthorityStamp)
	f.exec(t, `INSERT INTO guest_dispatches(operation_id,dispatch_version,status) VALUES($1,$2,'running')`, operation, builtAuthorityDispatch)
}

func (f *builtRebuildFixture) reportDispatchSuccess(t *testing.T, operation string) {
	t.Helper()
	f.exec(t, `UPDATE guest_dispatches SET status='succeeded',container_id=$1 WHERE operation_id=$2`, strings.Repeat("b", 64), operation)
}

func (f *builtRebuildFixture) succeed(t *testing.T, operation string) {
	t.Helper()
	f.reportDispatchSuccess(t, operation)
	f.exec(t, `UPDATE operation_steps SET status='succeeded',finished_at=$1 WHERE operation_id=$2`, builtAuthorityStamp, operation)
	f.exec(t, `UPDATE operations SET status='succeeded',finished_at=$1,current_step=NULL WHERE id=$2`, builtAuthorityStamp, operation)
}

func (f *builtRebuildFixture) stage(t *testing.T, publication builtImagePublication, image builtRebuildImage) {
	t.Helper()
	ctx, transfer, err := f.service.transfer(t.Context(), f.contact, publication.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	defer transfer.close()
	for _, body := range [][]byte{image.layer, image.config} {
		if err := transfer.cache.PutBuiltBlob(ctx, transfer.scope, digest.FromBytes(body), int64(len(body)), bytes.NewReader(body)); err != nil {
			t.Fatalf("stage fixture blob: %v", err)
		}
	}
	id, err := transfer.cache.PutBuiltManifest(ctx, transfer.scope, v1.MediaTypeImageManifest, int64(len(image.manifest)), bytes.NewReader(image.manifest))
	if err != nil || id != image.id {
		t.Fatalf("stage fixture manifest = %s, %v, want %s", id, err, image.id)
	}
}

func (f *builtRebuildFixture) requireCurrent(t *testing.T, expected digest.Digest, origin string) {
	t.Helper()
	var manifest digest.Digest
	var revision string
	var scope imagecache.BuiltScope
	err := f.db.QueryRowContext(t.Context(), `SELECT manifest_digest,origin_revision,storage_scope,policy_generation FROM built_image_slots
		WHERE project_id='project-first' AND target_os='linux' AND target_arch='amd64'`).Scan(&manifest, &revision, &scope.ID, &scope.Generation)
	if err != nil || manifest != expected || revision != origin {
		t.Fatalf("current built image = %s at %s, %v, want %s at %s", manifest, revision, err, expected, origin)
	}
	lease, err := f.runtime.active.AcquireBuiltImage(scope, manifest, v1.Platform{OS: "linux", Architecture: "amd64"})
	if err != nil {
		t.Fatalf("acquire current built image %s = %v, want a complete closure", manifest, err)
	}
	lease.Close()
}

type builtRebuildImage struct {
	id                      digest.Digest
	manifest, config, layer []byte
}

func newBuiltRebuildImage(t *testing.T, label string) builtRebuildImage {
	t.Helper()
	marshal := func(value any) []byte {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var layerBytes bytes.Buffer
	archive := tar.NewWriter(&layerBytes)
	contents := strings.Repeat(label, 128)
	if err := archive.WriteHeader(&tar.Header{Name: "fixture", Mode: 0o644, Size: int64(len(contents))}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(archive, contents); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	layer := layerBytes.Bytes()
	layerID := digest.FromBytes(layer)
	config := marshal(v1.Image{Platform: v1.Platform{OS: "linux", Architecture: "amd64"},
		RootFS: v1.RootFS{Type: "layers", DiffIDs: []digest.Digest{layerID}}})
	manifest := marshal(v1.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: v1.MediaTypeImageManifest,
		Config: v1.Descriptor{Digest: digest.FromBytes(config), Size: int64(len(config)), MediaType: v1.MediaTypeImageConfig},
		Layers: []v1.Descriptor{{Digest: layerID, Size: int64(len(layer)), MediaType: v1.MediaTypeImageLayer}}})
	return builtRebuildImage{id: digest.FromBytes(manifest), manifest: manifest, config: config, layer: layer}
}
