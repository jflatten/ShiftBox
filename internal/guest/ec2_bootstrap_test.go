package guest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/provider"
	_ "modernc.org/sqlite"
)

// TestEC2BootstrapReplay ensures wrong AWS identity, a consumed nonce and another guest key cannot authorize enrollment.
func TestEC2BootstrapReplay(t *testing.T) {
	ctx := t.Context()
	cfg := database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "test.db")}
	if uri := os.Getenv("SHIFTBOX_TEST_EC2_BOOTSTRAP_POSTGRES_URI"); uri != "" {
		cfg = database.Config{Profile: "postgres", PostgresURI: uri, Schema: "shiftbox_bootstrap_test"}
	}
	db, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, cfg.Profile); err != nil {
		t.Fatal(err)
	}
	const reference = "1234567890123456789012345678901234567890123456789012345678901234"
	const account = "524941137496"
	const instance = "i-0123456789abcdef0"
	now := time.Now().UTC()
	_, err = db.ExecContext(ctx, `
	INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
	VALUES('00000000-0000-4000-8000-000000000001','active','ShiftBox','`+cfg.Profile+`','https://management.test','ide.test','preview.test','release','release','now','now',1);
	INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
	VALUES('owner','a@example.test','member','active','local','setup','now','now',1);
	INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state,provider_kind,ec2_image_json)
	VALUES('artifact','release','amd64','test','test','1','[]','{}','{}','{}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','fixture.ami','owner','now','available','ec2','{}');
	INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,generation,ownership_marker,provider_instance_id,created_at,updated_at,version)
	VALUES('work','00000000-0000-4000-8000-000000000001','owner','template',1,'artifact','https://example.test/test','devcontainer.json',2,1024,40,'{}','running','running',1,'marker','`+instance+`','now','now',1);
	INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,current_step,originating_action,created_at,version)
	VALUES('op','work','provision',1,'owner','fixture-enrollment','waiting','enroll','workspace.launch','now',1);
	INSERT INTO operation_steps(operation_id,step_index,step_key,label,status) VALUES('op',1,'enroll','Enroll','active');
	INSERT INTO guest_registrations(workspace_id,generation,state) VALUES('work',1,'eligible');
	INSERT INTO ec2_launch_enrollment(operation_id,launch_reference,public_data) VALUES('op','`+reference+`','{}');`)
	if err != nil {
		t.Fatal(err)
	}
	b := NewEC2Bootstrap(NewEnrollment(db, nil), "us-east-2", account, func() time.Time { return now },
		func(_ context.Context, ref provider.WorkspaceRef) (provider.Inventory, error) {
			return provider.Inventory{Ref: ref, InstanceID: ref.InstanceID, ArtifactID: "artifact", Power: provider.Running, EC2Enrollment: true}, nil
		})
	caller := "i-foreign"
	b.client.Transport = proofRoundTripper(func(*http.Request) (*http.Response, error) {
		result := "<GetCallerIdentityResponse><GetCallerIdentityResult><Account>" + account + "</Account><Arn>arn:aws:sts::" + account + ":assumed-role/aws:ec2-instance/" + caller + "</Arn></GetCallerIdentityResult></GetCallerIdentityResponse>"
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(result))}, nil
	})
	key, csr := bootstrapTestKey(t)
	ch, err := b.Challenge(ctx, reference, csr)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(ch.ProofContext))
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	proof, err := signEC2IdentityProof(ctx, ch.Region, ch.ProofContext, aws.Credentials{
		AccessKeyID: "ASIAEXAMPLE000000000", SecretAccessKey: "synthetic secret", SessionToken: "synthetic session",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Grant(ctx, reference, ch.Nonce, csr, sig, proof); !errors.Is(err, ErrEnrollmentDenied) {
		t.Fatalf("Grant from another instance = %v, want denial", err)
	}
	caller = instance
	type attempt struct {
		result EC2EnrollmentGrant
		err    error
	}
	results := make(chan attempt, 2)
	for range 2 {
		go func() {
			result, err := b.Grant(ctx, reference, ch.Nonce, csr, sig, proof)
			results <- attempt{result, err}
		}()
	}
	var grants, denied int
	for range 2 {
		result := <-results
		if result.err == nil && len(result.result.Grant) == 43 {
			grants++
		} else if errors.Is(result.err, ErrEnrollmentDenied) {
			denied++
		} else {
			t.Errorf("concurrent Grant = %v, want a grant or denial", result.err)
		}
	}
	if grants != 1 || denied != 1 {
		t.Errorf("concurrent Grant issued %d grants and returned %d denials, want one each", grants, denied)
	}
	if _, err := b.Grant(ctx, reference, ch.Nonce, csr, sig, proof); !errors.Is(err, ErrEnrollmentDenied) {
		t.Errorf("Grant with a consumed challenge = %v, want denial", err)
	}
	_, replacementCSR := bootstrapTestKey(t)
	if _, err := b.Challenge(ctx, reference, replacementCSR); !errors.Is(err, ErrEnrollmentDenied) {
		t.Errorf("Challenge with a replacement key = %v, want denial", err)
	}
	fresh, err := b.Challenge(ctx, reference, csr)
	if err != nil || fresh.Nonce == ch.Nonce {
		t.Fatalf("same-key Challenge after lost grant response = %v, want a fresh nonce", err)
	}
	digest = sha256.Sum256([]byte(fresh.ProofContext))
	sig, err = ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	proof, err = signEC2IdentityProof(ctx, fresh.Region, fresh.ProofContext, aws.Credentials{
		AccessKeyID: "ASIAEXAMPLE000000000", SecretAccessKey: "synthetic secret", SessionToken: "synthetic session",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Grant(ctx, reference, fresh.Nonce, csr, sig, proof); err != nil {
		t.Errorf("same-key Grant after lost response = %v, want a replacement", err)
	}
	expiring, err := b.Challenge(ctx, reference, csr)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(ec2ChallengeTTL)
	if _, err := b.Grant(ctx, reference, expiring.Nonce, csr, sig, proof); !errors.Is(err, ErrEnrollmentDenied) {
		t.Errorf("expired Grant = %v, want denial", err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE operations SET status='cancelled' WHERE id='op'"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Challenge(ctx, reference, csr); !errors.Is(err, ErrEnrollmentDenied) {
		t.Errorf("Challenge for cancelled operation = %v, want denial", err)
	}
	var issued int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM enrollment_grants WHERE state='issued'").Scan(&issued); err != nil || issued != 1 {
		t.Errorf("issued grants = %d, %v, want one", issued, err)
	}
}

func bootstrapTestKey(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	return key, csr
}
