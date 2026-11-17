package release

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/migrations"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// TestEC2CatalogPinsValidatedRegionalAMI checks that upgrading preserves the
// local catalog and only a successful canary exposes the pinned EC2 image.
func TestEC2CatalogPinsValidatedRegionalAMI(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "catalog.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	files, err := fs.Sub(migrations.Files, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, files, goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 29); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES('operator','operator@example.test','administrator','active','local','setup',$1,$1,1)`, stamp); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	if _, err := db.ExecContext(ctx, `INSERT INTO platform_artifacts
		(id,release_id,architecture,platform_version,guest_release,protocol_version,owned_resources_json,digests_json,
		provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state)
		VALUES('utm-fixture','local','amd64','ubuntu','local','1','[]','{}','{}','{}',$1,'local.utm','operator',$2,'available')`, digest, stamp); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	legacy, err := store.Get(ctx, "utm-fixture")
	if err != nil || legacy.Artifact.EC2 != nil || legacy.Artifact.Bundle != "local.utm" {
		t.Fatalf("legacy artifact = %+v, %v, want preserved UTM bundle", legacy.Artifact, err)
	}

	raw := strings.NewReplacer("DIGEST", digest).Replace(`{
	  "builds": [{
	    "name": "shiftbox", "builder_type": "amazon-ebs", "artifact_id": "us-east-2:ami-0123456789abcdef0",
	    "packer_run_uuid": "6e005968-cfd0-4f74-b290-361c25f5ac71",
	    "custom_data": {
	      "provider": "ec2", "region": "us-east-2", "sourceAmiId": "ami-0123456789abcdef1",
	      "sourceAmiOwnerId": "099720109477", "sourceRevision": "aabbccddeeff00112233445566778899aabbccdd",
	      "releaseId": "development", "architecture": "amd64", "platformVersion": "24.04",
	      "protocolVersion": "1", "minimumRootDiskGiB": "32", "guestAgentRelease": "development",
	      "guestAgentSHA256": "DIGEST", "guestUpdateKeySHA256": "DIGEST",
	      "toolchainRecipeSHA256": "DIGEST", "codeServerRecipeSHA256": "DIGEST"
	    }
	  }],
	  "last_run_uuid": "6e005968-cfd0-4f74-b290-361c25f5ac71"
	}`)
	artifact, err := ReadEC2Manifest(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("read Packer manifest: %v", err)
	}
	image := artifact.EC2
	if image == nil || image.AMIID != "ami-0123456789abcdef0" || image.AccountID != "" {
		t.Fatalf("read AMI = %+v, want a pinned ID awaiting account verification", image)
	}
	image.AccountID = "524941137496"
	image.RootSnapshotID = "snap-0123456789abcdef0"
	image.RootDeviceName = "/dev/sda1"
	entry, err := store.ImportEC2(ctx, artifact, "operator", now)
	if err != nil || entry.State != "validating" {
		t.Fatalf("import AMI = %s, %v, want validating", entry.State, err)
	}
	if _, err := store.EC2Image(ctx, entry.ID, image.Region, image.AccountID); !errors.Is(err, ErrCatalogConflict) {
		t.Fatalf("lookup before validating = %v, want catalog conflict", err)
	}

	catalog := NewCatalog(store, "")
	if err := catalog.ValidateImport(ctx, entry.ID, func(_ context.Context, candidate Artifact) error {
		if candidate.EC2 == nil || *candidate.EC2 != *image {
			t.Fatalf("canary candidate = %+v, want exact pinned image", candidate.EC2)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.EC2Image(ctx, entry.ID, image.Region, image.AccountID)
	want := *image
	want.Architecture = EC2ReferenceArchitecture
	if err != nil || got != want {
		t.Fatalf("lookup validated AMI = %+v, %v, want %+v", got, err, want)
	}
	if _, err := store.EC2Image(ctx, entry.ID, "us-east-1", image.AccountID); !errors.Is(err, ErrCatalogConflict) {
		t.Fatalf("lookup in different region = %v, want catalog conflict", err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE platform_artifacts SET ec2_image_json='{}' WHERE id=$1", entry.ID); err == nil {
		t.Fatal("changed the immutable AMI binding")
	}
}
