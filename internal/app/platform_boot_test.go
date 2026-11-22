package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/fake"
	"github.com/jflatten/ShiftBox/internal/release"
)

type bootTestProvider struct{ *fake.Provider }

func (p bootTestProvider) ImportCandidate(ctx context.Context, req provider.CreateRequest, _ release.Artifact) (provider.Inventory, error) {
	return p.Create(ctx, req)
}

// TestSeedEC2FamilyCanary proves that a running import canary can start the
// single-boot challenge against the real isolated database schema.
func TestSeedEC2FamilyCanary(t *testing.T) {
	now := time.Now()
	ref := provider.WorkspaceRef{InstallationID: uuid.NewString(), WorkspaceID: uuid.NewString(), Generation: 1, OwnershipMarker: uuid.NewString()}
	db, _, err := database.OpenBootstrap(t.Context(), filepath.Join(t.TempDir(), "boot.db"), database.BootstrapSeed{
		ID: ref.InstallationID, Organization: "Validation", Origin: "https://validation.invalid",
		IDEPattern: "{workspace}.ide.invalid", PreviewPattern: "{service}--{workspace}.preview.invalid",
		Release: "development", MinGuestRelease: "development", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	digest := strings.Repeat("a", 64)
	source := release.ArtifactProvenance{Source: "ami-0123456789abcdef1", Revision: strings.Repeat("e", 40)}
	artifact := release.Artifact{
		Bundle: "ami-0123456789abcdef0", ManifestSHA256: digest,
		Manifest: release.ArtifactManifest{SchemaVersion: 1, ReleaseID: "fixture", Architecture: "amd64",
			PlatformVersion: "24.04", ProtocolVersion: "1", Guest: release.ArtifactComponent{Version: "development", SHA256: digest}, Provenance: source},
		EC2: &release.EC2Image{AMIID: "ami-0123456789abcdef0", Region: "us-east-2", AccountID: "524941137496",
			RootSnapshotID: "snap-0123456789abcdef0", RootDeviceName: "/dev/sda1", MinimumRootDiskGiB: 32,
			ReleaseID: "fixture", SourceAMI: source.Source, SourceOwner: "099720109477", SourceRevision: source.Revision,
			GuestUpdateKeySHA256: digest, ToolchainRecipeSHA256: digest, CodeServerRecipeSHA256: digest,
			Family: "fixture-family", CreatedAtUnixMillis: now.UnixMilli()},
	}
	req := provider.CreateRequest{Ref: ref, OperationID: uuid.NewString(), ArtifactID: uuid.NewString(), BootstrapData: "{}"}
	ref.InstanceID = "i-0123456789abcdef0"
	vm := provider.Inventory{Ref: ref, InstanceID: ref.InstanceID, Power: provider.Running,
		Effective: provider.Resources{CPU: 2, MemoryBytes: 4 << 30, DiskBytes: 32 << 30}}
	reference := strings.Repeat("b", 64)
	if err := seedPlatformBoot(t.Context(), db, req, vm, artifact, reference); err != nil {
		t.Fatalf("seedPlatformBoot(single boot) = %v, want a launch enrollment record", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := guest.NewEC2Bootstrap(guest.NewEnrollment(db, nil), artifact.EC2.Region, artifact.EC2.AccountID, time.Now, nil)
	challenge, err := bootstrap.Challenge(t.Context(), reference, csr)
	if err != nil || challenge.Nonce == "" {
		t.Errorf("Challenge(canary launch) = %+v, %v; want a nonce", challenge, err)
	}
}

// TestPlatformBootCleansUpLostImport checks that an uncertain import never
// publishes the candidate and that its created clone is removed by ownership.
func TestPlatformBootCleansUpLostImport(t *testing.T) {
	machines := bootTestProvider{fake.New()}
	machines.LoseReply("create")
	ref := provider.WorkspaceRef{InstallationID: uuid.NewString(), WorkspaceID: uuid.NewString(), Generation: 1, OwnershipMarker: uuid.NewString()}
	req := provider.CreateRequest{Ref: ref, OperationID: uuid.NewString(), ArtifactID: uuid.NewString(), Release: "test", NetworkMode: "nat",
		Resources: provider.Resources{CPU: 2, MemoryBytes: 1024 << 20, DiskBytes: 32768 << 20}}
	artifact := release.Artifact{Manifest: release.ArtifactManifest{Guest: release.ArtifactComponent{Version: "test"}, ProtocolVersion: "1"}}
	err := ValidatePlatformBoot(t.Context(), t.TempDir(), machines, req, artifact, GuestListenerConfig{
		PublicHost: "127.0.0.1", EnrollmentAddress: "127.0.0.1:0", PollAddress: "127.0.0.1:0",
	})
	if !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Fatalf("validation after lost create reply = %v, want uncertain outcome", err)
	}
	vm, err := machines.Inspect(t.Context(), ref)
	if err != nil || vm.Power != provider.Deleted || len(vm.Resources) != 1 || !vm.Resources[0].Shared {
		t.Fatalf("inspection after failed validation = %+v, %v, want only the shared catalog resource", vm, err)
	}
}
