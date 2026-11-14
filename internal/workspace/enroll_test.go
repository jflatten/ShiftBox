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

// TestLaunchWorkerWaitsForGuestContact verifies that a lost boot reply cannot
// trigger another media delivery or let a VM skip authenticated enrollment.
// It also checks that IDE readiness and later setup hooks are tracked separately.
func TestLaunchWorkerWaitsForGuestContact(t *testing.T) {
	ctx := context.Background()
	db := openWorkspaceDatabase(t, "sqlite")
	seedReservationWorkspaces(t, db)
	t.Cleanup(func() {
		if _, err := db.Exec("DELETE FROM enrollment_grants; DELETE FROM guest_registrations; DELETE FROM provider_resources; DELETE FROM reservations; DELETE FROM workspaces"); err != nil {
			t.Errorf("clean up enrollment fixture: %v", err)
		}
	})
	store := NewStore(db)
	allocation := Allocation{CPU: 2, MemoryMiB: 1024, DiskMiB: 40}
	if err := store.SetCapacityBudget(ctx, testInstallation, allocation, 0, "reservation-owner", time.Now()); err != nil {
		t.Fatal(err)
	}
	journal := operation.NewJournal(db, time.Now, uuid.NewString)
	var op operation.Operation
	err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		var err error
		op, err = AdmitLaunch(ctx, tx, journal, LaunchIntent{
			InstallationID: testInstallation, WorkspaceID: "enroll-workspace", OwnerID: "reservation-owner",
			TemplateID: "template", TemplateRevision: 1, ArtifactID: "reservation-artifact",
			RequestKey: "test-enrollment-key", NetworkPolicyJSON: `{"mode":"nat"}`,
			RepositoryURL: "https://github.com/example/repo", DevcontainerPath: ".devcontainer/devcontainer.json",
			Allocation: allocation, OwnershipMarker: "owned",
		}, time.Now())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	machines := &enrollmentFixtureProvider{Provider: fake.New()}
	enrollment := &stubEnrollment{}
	mediaRoot := t.TempDir()
	var probeCalls int
	probe := func(_ context.Context, workspace string, generation int64) error {
		probeCalls++
		if workspace != "enroll-workspace" || generation != 1 {
			t.Errorf("IDE probe got workspace %q generation %d, want enrolled workspace generation 1", workspace, generation)
		}
		if probeCalls == 1 {
			return errors.New("IDE still starting")
		}
		return nil
	}
	newWorker := func() *operation.Worker {
		return &operation.Worker{Journal: journal, Name: "test", LeaseDuration: time.Minute,
			Executor: NewLaunchExecutor(store, machines, enrollment, mediaRoot, "https://internal.test/internal/v1/enroll", "https://internal.test:8443/internal/v1/guest/poll", time.Now, probe)}
	}
	worker := newWorker()
	if err := worker.RunOnce(ctx, op.ID); err != nil {
		t.Fatalf("provision workspace: %v", err)
	}
	machines.LoseReply("start")
	if err := worker.RunOnce(ctx, op.ID); !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Fatalf("start with lost reply = %v, want unknown", err)
	}
	worker = newWorker()
	if err := worker.RunOnce(ctx, op.ID); !errors.Is(err, errGuestContactPending) {
		t.Fatalf("running without authenticated contact = %v, want pending", err)
	}
	if enrollment.deliveries != 1 || enrollment.cleanups != 0 {
		t.Fatalf("delivery and cleanup calls = %d/%d, want 1/0", enrollment.deliveries, enrollment.cleanups)
	}
	var state string
	if err := db.QueryRow("SELECT observed_state FROM workspaces WHERE id=$1", *op.WorkspaceID).Scan(&state); err != nil || state != "running" {
		t.Fatalf("provider observation = %q, %v, want running", state, err)
	}
	// Simulate the durable evidence written by successful issuance and the
	// authenticated guest poll, which are exercised separately over mTLS.
	expiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	err = database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE guest_registrations SET state='active',public_key_fingerprint='key',
			certificate_not_after=$1,last_authenticated_at=$2 WHERE workspace_id=$3`, expiry, testStamp, *op.WorkspaceID); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO enrollment_grants(id,workspace_id,generation,operation_id,provider_instance_id,expires_at,state,key_proof,certificate_der)
			VALUES($1,$2,1,$3,'fake-vm-1',$4,'consumed','key','public')`,
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", *op.WorkspaceID, op.ID, expiry)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(ctx, op.ID); err != nil {
		t.Fatalf("reconcile authenticated enrollment: %v", err)
	}
	op, err = journal.Get(ctx, op.ID)
	if err != nil || op.Steps[1].Status != "succeeded" || op.Steps[2].Status != "pending" || enrollment.cleanups != 1 {
		t.Fatalf("enrollment completion = %+v, %v, cleanup %d, want verified enrollment and pending configure", op, err, enrollment.cleanups)
	}
	if err := worker.RunOnce(ctx, op.ID); !errors.Is(err, errGuestConfigurationPending) {
		t.Fatalf("configure without guest report = %v, want pending", err)
	}
	const container = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := db.Exec(`INSERT INTO guest_dispatches(operation_id,dispatch_version,status,container_id) VALUES($1,1,'running',$2)`, op.ID, container); err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(ctx, op.ID); err != nil {
		t.Fatalf("configure at waitFor = %v, want success while later hooks run", err)
	}
	op, err = journal.Get(ctx, op.ID)
	if err != nil || op.Steps[2].Status != "succeeded" || op.Steps[3].Status != "pending" {
		t.Fatalf("configuration completion = %+v, %v, want configured with browser IDE verification pending", op, err)
	}
	if probeCalls != 0 {
		t.Fatal("IDE was probed before the verification step")
	}
	if err := worker.RunOnce(ctx, op.ID); !errors.Is(err, errIDEProbePending) {
		t.Fatalf("IDE startup while service unavailable = %v, want retryable", err)
	}
	if err := worker.RunOnce(ctx, op.ID); err != nil {
		t.Fatalf("verify active IDE = %v, want success", err)
	}
	op, err = journal.Get(ctx, op.ID)
	if err != nil || op.Status != "waiting" || op.Steps[3].Status != "succeeded" || probeCalls != 2 {
		t.Fatalf("IDE verification = %+v, %v, probe calls %d, want IDE ready with hooks pending", op, err, probeCalls)
	}
	if err := worker.RunOnce(ctx, op.ID); !errors.Is(err, errGuestConfigurationPending) {
		t.Fatalf("finish hooks with only waitFor evidence = %v, want pending", err)
	}
	if _, err := db.Exec(`UPDATE guest_dispatches SET status='succeeded' WHERE operation_id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(ctx, op.ID); err != nil {
		t.Fatalf("observe completed hooks = %v, want success", err)
	}
	op, err = journal.Get(ctx, op.ID)
	if err != nil || op.Status != "succeeded" {
		t.Fatalf("operation after remaining hooks = %+v, %v, want succeeded", op, err)
	}
}

// TestLaunchWorkerRecordsGuestFailure verifies a terminal guest setup report
// becomes a visible operation failure and leaves IDE verification pending.
func TestLaunchWorkerRecordsGuestFailure(t *testing.T) {
	ctx := t.Context()
	db := openWorkspaceDatabase(t, "sqlite")
	seedReservationWorkspaces(t, db)
	journal := operation.NewJournal(db, time.Now, uuid.NewString)
	workspaceID := "configuration-failure"
	op, err := journal.Create(ctx, operation.Request{
		Actor: "reservation-owner", Key: "guest-configuration-failure", Type: "provision",
		OriginatingAction: "workspace.launch", WorkspaceID: &workspaceID, Generation: 1,
		Steps: []operation.Step{{Key: "configure", Label: "Configure"}, {Key: "verify_ide", Label: "Verify IDE"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO guest_dispatches(operation_id,dispatch_version,status,problem)
		VALUES($1,1,'failed','guest.required_setup_failed')`, op.ID); err != nil {
		t.Fatal(err)
	}
	executor := NewLaunchExecutor(NewStore(db), fake.New(), nil, "", "", "", time.Now, nil)
	worker := &operation.Worker{Journal: journal, Name: "test", LeaseDuration: time.Minute, Executor: executor}
	if err := worker.RunOnce(ctx, op.ID); err != nil {
		t.Fatalf("run with terminal guest result = %v, want a recorded failure", err)
	}
	op, err = journal.Get(ctx, op.ID)
	if err != nil || op.Status != "failed" || op.Steps[0].Status != "failed" ||
		op.Steps[0].Detail == "" || op.Steps[1].Status != "pending" || op.FinishedAt == nil {
		t.Errorf("operation after guest failure = %+v, %v, want a failed configuration step with IDE pending", op, err)
	}
}

type stubEnrollment struct {
	deliveries, cleanups int
}

// The platform fixture supplies the empty removable drive present in a UTM artifact.
type enrollmentFixtureProvider struct{ *fake.Provider }

func (p *enrollmentFixtureProvider) Create(ctx context.Context, req provider.CreateRequest) (provider.Inventory, error) {
	req.EnrollmentMedia = "/empty-fixture.iso"
	return p.Provider.Create(ctx, req)
}

func (s *stubEnrollment) Deliver(ctx context.Context, operationID string, inv provider.Inventory, _ time.Time,
	_, _, _ string, attach func(context.Context, provider.EnrollmentRequest) (provider.DeliveryResult, error),
) (string, error) {
	s.deliveries++
	_, err := attach(ctx, provider.EnrollmentRequest{Ref: inv.Ref, OperationID: operationID, MediaPath: "/fixture.iso", Eligible: true})
	return "/fixture.iso", err
}

func (s *stubEnrollment) CleanupDelivery(context.Context, string, provider.WorkspaceRef, string) error {
	s.cleanups++
	return nil
}
