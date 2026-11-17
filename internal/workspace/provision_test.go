package workspace

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/fake"
)

// TestProvisionRecoversLostCreateReply verifies that a provider-created VM is
// discovered on retry and its identity and complete inventory are persisted.
func TestProvisionRecoversLostCreateReply(t *testing.T) {
	testProvisionRecoversLostCreateReply(t, false)
}

// TestProvisionReconcilesInertEC2Boot verifies that an observed initial EC2
// boot reenters Create to confirm the launch and stop the instance.
func TestProvisionReconcilesInertEC2Boot(t *testing.T) {
	testProvisionRecoversLostCreateReply(t, true)
}

func testProvisionRecoversLostCreateReply(t *testing.T, ec2 bool) {
	t.Helper()
	ctx := context.Background()
	db := openWorkspaceDatabase(t, "sqlite")
	seedReservationWorkspaces(t, db)
	t.Cleanup(func() {
		if _, err := db.Exec("DELETE FROM guest_registrations; DELETE FROM provider_resources; DELETE FROM reservations; DELETE FROM workspaces"); err != nil {
			t.Errorf("clean up provision fixture: %v", err)
		}
	})
	now := time.Now()
	allocation := Allocation{CPU: 2, MemoryMiB: 1024, DiskMiB: 40}
	if err := NewStore(db).SetCapacityBudget(ctx, testInstallation, allocation, 0, "reservation-owner", now); err != nil {
		t.Fatal(err)
	}
	journal := operation.NewJournal(db, time.Now, uuid.NewString)
	id := "new-workspace"
	var op operation.Operation
	err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		var err error
		op, err = AdmitLaunch(ctx, tx, journal, LaunchIntent{
			InstallationID: testInstallation, WorkspaceID: id, OwnerID: "reservation-owner",
			TemplateID: "template", TemplateRevision: 1, ArtifactID: "reservation-artifact",
			RequestKey: "test-provision-key", NetworkPolicyJSON: `{"mode":"nat"}`,
			RepositoryURL: "https://github.com/example/repo", DevcontainerPath: ".devcontainer/devcontainer.json",
			Allocation: allocation, OwnershipMarker: "owned",
		}, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	machines := &countedProvisioner{Provider: fake.New(), ec2: ec2}
	machines.LoseReply("create")
	worker := operation.Worker{Journal: journal, Name: "test", LeaseDuration: time.Minute,
		Executor: NewProvisionExecutor(NewStore(db), machines)}
	if err := worker.RunOnce(ctx, op.ID); !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Fatalf("first worker run = %v, want unknown provider outcome", err)
	}
	var instance, state string
	if err := db.QueryRow("SELECT COALESCE(provider_instance_id,''),observed_state FROM workspaces WHERE id=$1", id).Scan(&instance, &state); err != nil || instance != "" || state != "unknown" {
		t.Fatalf("state after lost reply = %q/%q, %v, want unknown", instance, state, err)
	}

	if err := worker.RunOnce(ctx, op.ID); err != nil {
		t.Fatalf("retry worker run: %v", err)
	}
	if err := db.QueryRow("SELECT provider_instance_id,observed_state FROM workspaces WHERE id=$1", id).Scan(&instance, &state); err != nil || instance != "fake-vm-1" || state != "stopped" {
		t.Fatalf("observed VM = %q/%q, %v, want fake-vm-1/stopped", instance, state, err)
	}
	wantCreates := 1
	if ec2 {
		wantCreates++
	}
	var assets int
	if err := db.QueryRow("SELECT COUNT(*) FROM provider_resources WHERE workspace_id=$1 AND generation=1", id).Scan(&assets); err != nil || assets != 3 || machines.creates != wantCreates {
		t.Fatalf("persisted resources/create calls = %d/%d, %v, want 3/%d", assets, machines.creates, err, wantCreates)
	}
	var registration string
	if err := db.QueryRow("SELECT state FROM guest_registrations WHERE workspace_id=$1 AND generation=1", id).Scan(&registration); err != nil || registration != "eligible" {
		t.Fatalf("initial guest registration = %q, %v, want eligible", registration, err)
	}
	op, err = journal.Get(ctx, op.ID)
	if err != nil || op.Status != "waiting" || op.Steps[0].Status != "succeeded" || op.Steps[1].Status != "pending" {
		t.Fatalf("operation after provisioning = %+v, %v, want pending enrollment after verified provision", op, err)
	}
	ref := provider.WorkspaceRef{InstallationID: testInstallation, WorkspaceID: id, Generation: 1,
		InstanceID: instance, OwnershipMarker: "owned"}
	got, err := machines.Inspect(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	got.Resources = got.Resources[:2]
	req := provider.CreateRequest{Ref: ref, ArtifactID: got.ArtifactID, Release: got.Release,
		NetworkMode: got.NetworkMode, Resources: got.Effective}
	if _, err := NewProvisionExecutor(NewStore(db), machines).record(ctx, req, got); !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Errorf("reconcile with omitted resource = %v, want uncertain outcome", err)
	}
}

type countedProvisioner struct {
	*fake.Provider
	creates int
	ec2     bool
}

func (p *countedProvisioner) Create(ctx context.Context, req provider.CreateRequest) (provider.Inventory, error) {
	p.creates++
	return p.Provider.Create(ctx, req)
}

func (p *countedProvisioner) Inspect(ctx context.Context, ref provider.WorkspaceRef) (provider.Inventory, error) {
	got, err := p.Provider.Inspect(ctx, ref)
	if err == nil && p.ec2 && p.creates == 1 {
		got.Power, got.EC2Enrollment = provider.Running, true
	}
	return got, err
}

// TestVerifyProvisionInventory rejects empty disk evidence and resources
// claimed for a different generation.
func TestVerifyProvisionInventory(t *testing.T) {
	ref := provider.WorkspaceRef{InstallationID: "installation", WorkspaceID: "workspace", Generation: 1, InstanceID: "vm", OwnershipMarker: "marker"}
	req := provider.CreateRequest{Ref: ref, ArtifactID: "artifact", Release: "release", NetworkMode: "nat",
		Resources: provider.Resources{CPU: 2, MemoryBytes: 1024 << 20, DiskBytes: 40 << 20}}
	got := provider.Inventory{Ref: ref, InstanceID: ref.InstanceID, ObservedAt: time.Now(), Power: provider.Stopped,
		ArtifactID: req.ArtifactID, Release: req.Release, NetworkMode: req.NetworkMode, Effective: req.Resources,
		DiskCapacityKnown: true, Resources: []provider.OwnedResource{
			{ID: "vm", Type: "vm", Generation: 1, OwnershipMarker: "marker"},
			{ID: "disk", Type: "disk", Generation: 1, OwnershipMarker: "marker"},
		}}
	if err := verifyProvisionInventory(req, got); err != nil {
		t.Fatalf("complete observation rejected: %v", err)
	}
	got.Resources[1].Generation = 2
	if err := verifyProvisionInventory(req, got); !errors.Is(err, provider.ErrOwnershipMismatch) {
		t.Errorf("foreign disk = %v, want ownership mismatch", err)
	}
	got.Resources[1].Generation = 1
	got.DiskCapacityKnown = false
	if err := verifyProvisionInventory(req, got); !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Errorf("unknown disk capacity = %v, want uncertain outcome", err)
	}
}
