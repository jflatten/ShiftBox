package guest_test

import (
	"bytes"
	"crypto/x509"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/provider"
)

// TestGuestRenewal proves a response can be recovered with the same new
// key while its predecessor is retired from normal guest traffic.
func TestGuestRenewal(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "guest.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	const install = "00000000-0000-4000-8000-000000000001"
	if _, err := db.ExecContext(ctx, `
	INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
	VALUES('`+install+`','active','Fixture','sqlite','https://management.test','ide.test','preview.test','release','release','now','now',1);
	INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
	VALUES('owner','a@example.test','member','active','local','setup','now','now',1);
	INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state)
	VALUES('artifact','release','amd64','test','test','1','[]','{}','{}','{}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','fixture.utm','owner','now','available');
	INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,generation,ownership_marker,provider_instance_id,created_at,updated_at,version)
	VALUES('work','`+install+`','owner','template',1,'artifact','https://github.com/example/test','devcontainer.json',2,1024,40,'{}','running','running',1,'marker','vm','now','now',1);
	INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,created_at,version)
	VALUES('op','work','provision',1,'owner','fixture-enrollment','running','now',1);
	INSERT INTO guest_registrations(workspace_id,generation,state) VALUES('work',1,'eligible');`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	ca, err := app.EnsureInstallationCA(t.TempDir(), install, cipher, now)
	if err != nil {
		t.Fatal(err)
	}
	enrollment := guest.NewEnrollment(db, ca)
	ref := provider.WorkspaceRef{InstallationID: install, WorkspaceID: "work", Generation: 1, OwnershipMarker: "marker", InstanceID: "vm"}
	token, err := enrollment.Mint(ctx, "op", provider.Inventory{Ref: ref, InstanceID: "vm", Power: provider.Stopped, ObservedAt: now}, now)
	if err != nil {
		t.Fatal(err)
	}
	originalCSR := fixtureCSR(t)
	initial, err := enrollment.Enroll(ctx, token, originalCSR, now)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(initial)
	if err != nil {
		t.Fatal(err)
	}
	replacementCSR := fixtureCSR(t)
	if _, err := enrollment.Renew(ctx, leaf, replacementCSR, now.Add(4*time.Hour-time.Second)); !errors.Is(err, guest.ErrGuestUnauthorized) {
		t.Errorf("early renewal = %v, want denial", err)
	}
	now = now.Add(4 * time.Hour)
	if _, err := enrollment.Renew(ctx, leaf, originalCSR, now); !errors.Is(err, guest.ErrGuestUnauthorized) {
		t.Errorf("renewing with the existing key = %v, want denial", err)
	}
	issued, err := enrollment.Renew(ctx, leaf, replacementCSR, now)
	if err != nil {
		t.Fatal(err)
	}
	next, err := x509.ParseCertificate(issued)
	if err != nil {
		t.Fatal(err)
	}
	if next.SerialNumber.Cmp(leaf.SerialNumber) == 0 || len(next.URIs) != 1 || len(leaf.URIs) != 1 ||
		next.URIs[0].String() != leaf.URIs[0].String() || !next.NotAfter.Equal(now.Add(8*time.Hour)) {
		t.Fatal("replacement did not preserve the controller-owned identity with a fresh serial")
	}
	if _, err := enrollment.Authenticate(ctx, leaf, now); !errors.Is(err, guest.ErrGuestUnauthorized) {
		t.Errorf("old leaf ordinary authorization = %v, want denial", err)
	}
	if _, err := enrollment.Authenticate(ctx, next, now); err != nil {
		t.Fatalf("new leaf ordinary authorization = %v, want success", err)
	}
	replay, err := guest.NewEnrollment(db, ca).Renew(ctx, leaf, replacementCSR, now)
	if err != nil || !bytes.Equal(issued, replay) {
		t.Errorf("renewal retry = %v, want the identical certificate", err)
	}
	if _, err := enrollment.Renew(ctx, leaf, fixtureCSR(t), now); !errors.Is(err, guest.ErrGuestUnauthorized) {
		t.Errorf("different-key retry = %v, want denial", err)
	}
	if _, err := enrollment.Renew(ctx, leaf, replacementCSR, leaf.NotAfter); !errors.Is(err, guest.ErrGuestUnauthorized) {
		t.Errorf("retry with expired predecessor = %v, want denial", err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE guest_registrations SET state='revoked' WHERE workspace_id='work'"); err != nil {
		t.Fatal(err)
	}
	if _, err := enrollment.Renew(ctx, leaf, replacementCSR, now); !errors.Is(err, guest.ErrGuestUnauthorized) {
		t.Errorf("retry after revocation = %v, want denial", err)
	}
}
