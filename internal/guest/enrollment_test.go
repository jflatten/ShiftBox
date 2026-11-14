package guest_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/provider"
	_ "modernc.org/sqlite"
)

// TestEnrollmentGrantReplay verifies one-use issuance, lost-response recovery
// for the same key, and denial after expiry or registration revocation.
func TestEnrollmentGrantReplay(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "guest.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	const install = "00000000-0000-4000-8000-000000000001"
	_, err = db.Exec(`
	INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
	VALUES('` + install + `','active','ShiftBox','sqlite','https://management.test','ide.test','preview.test','release','release','now','now',1);
	INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
	VALUES('owner','a@example.test','member','active','local','setup','now','now',1);
	INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state)
	VALUES('artifact','release','amd64','test','test','1','[]','{}','{}','{}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','fixture.utm','owner','now','available');
	INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,generation,ownership_marker,provider_instance_id,created_at,updated_at,version)
	VALUES('work','` + install + `','owner','template',1,'artifact','https://github.com/example/test','devcontainer.json',2,1024,40,'{}','running','stopped',1,'marker','vm','now','now',1);
	INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,created_at,version)
	VALUES('op','work','provision',1,'owner','fixture-enrollment','running','now',1);
	INSERT INTO guest_registrations(workspace_id,generation,state) VALUES('work',1,'eligible');`)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, _, issuer := authorityFixture(t)
	now := time.Now().UTC()
	ca, err := guest.LoadCA(certPEM, keyPEM, now)
	if err != nil {
		t.Fatal(err)
	}
	enrollment := guest.NewEnrollment(db, ca)
	ref := provider.WorkspaceRef{InstallationID: install, WorkspaceID: "work", Generation: 1, OwnershipMarker: "marker", InstanceID: "vm"}
	inventory := provider.Inventory{Ref: ref, InstanceID: "vm", Power: provider.Stopped, ObservedAt: now}
	wrong := inventory
	wrong.Ref.OwnershipMarker = "foreign"
	if _, err := enrollment.Mint(ctx, "op", wrong, now); !errors.Is(err, guest.ErrEnrollmentDenied) {
		t.Errorf("mint for mismatched ownership = %v, want denial", err)
	}
	wrong = inventory
	wrong.Power = provider.Running
	if _, err := enrollment.Mint(ctx, "op", wrong, now); !errors.Is(err, guest.ErrEnrollmentDenied) {
		t.Errorf("mint for running VM = %v, want denial", err)
	}
	token, err := enrollment.Mint(ctx, "op", inventory, now)
	if err != nil {
		t.Fatal(err)
	}
	var digest string
	if err := db.QueryRow("SELECT id FROM enrollment_grants").Scan(&digest); err != nil || len(digest) != 64 || digest == token {
		t.Fatalf("stored grant ID length = %d, %v, want digest", len(digest), err)
	}
	csr := fixtureCSR(t)
	first, err := enrollment.Enroll(ctx, token, csr, now)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(first)
	if err != nil {
		t.Fatal(err)
	}
	if leaf.Subject.CommonName == "injected" || len(leaf.DNSNames) != 0 || len(leaf.URIs) != 1 ||
		!leaf.NotAfter.Equal(issuer.NotAfter) || leaf.CheckSignatureFrom(issuer) != nil ||
		len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Errorf("issued certificate did not enforce controller identity and issuer validity: %+v", leaf)
	}
	replay, err := enrollment.Enroll(ctx, token, csr, now.Add(time.Minute))
	if err != nil || !bytes.Equal(replay, first) {
		t.Errorf("same-key retry = %v, want exact issued certificate", err)
	}
	if _, err := enrollment.Enroll(ctx, token, fixtureCSR(t), now); !errors.Is(err, guest.ErrEnrollmentDenied) {
		t.Errorf("alternate key = %v, want denial", err)
	}
	if _, err := enrollment.Enroll(ctx, token, csr, now.Add(15*time.Minute)); !errors.Is(err, guest.ErrEnrollmentDenied) {
		t.Errorf("expired grant = %v, want denial", err)
	}
	if _, err := db.Exec("UPDATE guest_registrations SET state='revoked' WHERE workspace_id='work'"); err != nil {
		t.Fatal(err)
	}
	if _, err := enrollment.Enroll(ctx, token, csr, now); !errors.Is(err, guest.ErrEnrollmentDenied) {
		t.Errorf("revoked registration replay = %v, want denial", err)
	}
}

func fixtureCSR(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "injected"}, DNSNames: []string{"injected.test"},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	return csr
}
