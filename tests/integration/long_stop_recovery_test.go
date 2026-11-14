package integration_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/fake"
	"github.com/jflatten/ShiftBox/internal/workspace"
	_ "modernc.org/sqlite"
)

// TestLongStopRecoveryBeforeBoot verifies that an authorized start delivers
// fresh recovery media to the retained VM, then restarts before and after
// power-on without minting another grant or attaching another image.
func TestLongStopRecoveryBeforeBoot(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("media generation requires macOS hdiutil")
	}
	f, enrollment, authority, now := newLongStopFixture(t)
	ctx := t.Context()
	_, leaf := seedPreviousGuest(t, f, enrollment, now)
	if _, err := enrollment.Authenticate(ctx, leaf, now); !errors.Is(err, guest.ErrGuestUnauthorized) {
		t.Fatalf("Authenticate(expired guest) = %v, want %v", err, guest.ErrGuestUnauthorized)
	}
	journal := operation.NewJournal(f.db, func() time.Time { return now }, uuid.NewString)
	op := admitLongStopStart(t, f, journal, now)
	machines := &recoveryProvider{Provider: f.machines, now: now}
	machines.authenticate = func(ctx context.Context, image string) error {
		der, err := enrollment.Enroll(ctx, recoveryMediaGrant(t, image), recoveryCSR(t), now)
		if err != nil {
			return err
		}
		leaf, err := x509.ParseCertificate(der)
		if err != nil {
			return err
		}
		_, err = enrollment.Authenticate(ctx, leaf, now)
		return err
	}
	clock := func() time.Time { return now }
	mediaRoot := t.TempDir()
	newWorker := func() *operation.Worker {
		store := workspace.NewStore(f.db)
		launch := workspace.NewLaunchExecutor(store, machines, enrollment, mediaRoot,
			"https://controller.test/internal/v1/enroll", "https://controller.test:8443/internal/v1/guest/poll", clock, nil)
		return workspace.NewWorker(journal, launch, workspace.NewPowerExecutor(store, machines, clock, nil))
	}
	restart := func() {
		t.Helper()
		f.reopen(t)
		journal = operation.NewJournal(f.db, clock, uuid.NewString)
		enrollment = guest.NewEnrollment(f.db, authority)
	}
	worker := newWorker()
	interrupted, cancel := context.WithCancel(ctx)
	defer cancel()
	machines.interruptBeforeStart = cancel
	if err := worker.RunOnce(interrupted, op.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunOnce(interrupted after media attachment) = %v, want cancellation", err)
	}
	restart()
	var grantID string
	if err := f.db.QueryRowContext(ctx, `SELECT id FROM enrollment_grants
		WHERE operation_id=$1 AND state='issued' AND media_attached_at IS NOT NULL`, op.ID).Scan(&grantID); err != nil ||
		machines.starts != 0 || len(machines.deliveries) != 1 {
		t.Fatalf("attachment after restart = %v, %d deliveries and %d starts, want a durable attachment and no start",
			err, len(machines.deliveries), machines.starts)
	}
	worker = newWorker()
	if err := worker.RunOnce(ctx, op.ID); !errors.Is(err, operation.ErrConflict) {
		t.Fatalf("RunOnce(before old lease expires) = %v, want conflict", err)
	}
	now = now.Add(worker.LeaseDuration + time.Second)
	machines.now = now
	machines.loseStartReply = true
	if err := worker.RunOnce(ctx, op.ID); !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Fatalf("RunOnce(start with lost provider reply) = %v, want an uncertain outcome", err)
	}
	assertRecoveryDeliveryBeforeStart(t, f.ref, op.ID, machines)
	restart()
	if err := newWorker().RunOnce(ctx, op.ID); err != nil {
		t.Fatalf("RunOnce(start after controller restart) = %v, want a verified start", err)
	}
	if len(machines.deliveries) != 1 || machines.starts != 1 {
		t.Errorf("retry issued %d attachments and %d starts, want one of each", len(machines.deliveries), machines.starts)
	}
	var state string
	if err := f.db.QueryRowContext(ctx, "SELECT state FROM enrollment_grants WHERE id=$1", grantID).Scan(&state); err != nil || state != "consumed" {
		t.Errorf("original recovery grant = %s (%v), want consumed", state, err)
	}
	before := f.ref
	after, err := machines.Inspect(ctx, before)
	if err != nil || after.Ref != before || after.Power != provider.Running {
		t.Errorf("Inspect(after recovered start) = %v/%s, %v, want %v/running", after.Ref, after.Power, err, before)
	}
	media, err := os.ReadFile(machines.deliveries[0].MediaPath)
	if err != nil || bytes.Contains(media, []byte(`{"grant":"`)) {
		t.Errorf("authenticated recovery did not clear the seed while preserving the removable image: %v", err)
	}
}

// TestLongStopRevokedRegistrationStaysStopped checks the real start workflow
// before it can deliver media or power on a VM with revoked guest authority.
func TestLongStopRevokedRegistrationStaysStopped(t *testing.T) {
	f, enrollment, _, now := newLongStopFixture(t)
	seedPreviousGuest(t, f, enrollment, now)
	capacityExec(t, f.db, "UPDATE guest_registrations SET state='revoked' WHERE workspace_id=$1", f.ref.WorkspaceID)
	clock := func() time.Time { return now }
	journal := operation.NewJournal(f.db, clock, uuid.NewString)
	op := admitLongStopStart(t, f, journal, now)
	store := workspace.NewStore(f.db)
	machines := &recoveryProvider{Provider: f.machines, now: now}
	worker := workspace.NewWorker(journal, nil, workspace.NewPowerExecutor(store, machines, clock, nil))
	if err := worker.RunOnce(t.Context(), op.ID); err != nil {
		t.Fatalf("RunOnce(start with revoked registration) = %v, want recorded failure", err)
	}
	status, err := journal.Get(t.Context(), op.ID)
	if err != nil || status.Status != "failed" || len(machines.deliveries) != 0 || machines.starts != 0 {
		t.Errorf("revoked guest start = %s, %v, %d deliveries, %d power requests, want failed with no delivery or power request",
			status.Status, err, len(machines.deliveries), machines.starts)
	}
}

// TestLongStopRecoveryGrant verifies delivery to a stopped retained instance,
// unchanged drives, and one issuance with a same-key retry after a lost reply.
func TestLongStopRecoveryGrant(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("media generation requires macOS hdiutil")
	}
	f, enrollment, _, now := newLongStopFixture(t)
	ctx := t.Context()
	previousGrant, oldLeaf := seedPreviousGuest(t, f, enrollment, now)
	journal := operation.NewJournal(f.db, func() time.Time { return now }, uuid.NewString)
	op := admitLongStopStart(t, f, journal, now)
	// Model an authorized recovery operation until the start reconciler owns
	// the transition from an expired leaf to recovery eligibility.
	capacityExec(t, f.db, "UPDATE operations SET status='running' WHERE id=$1", op.ID)
	capacityExec(t, f.db, "UPDATE guest_registrations SET state='expired' WHERE workspace_id=$1", f.ref.WorkspaceID)
	inventory := stoppedRecoveryInventory(t, f, now)
	machines := &recoveryProvider{Provider: f.machines, now: now}
	mediaRoot := t.TempDir()
	deliver := func() (string, error) {
		return enrollment.Deliver(ctx, op.ID, inventory, now, mediaRoot,
			"https://controller.test/internal/v1/enroll", "https://controller.test:8443/internal/v1/guest/poll", machines.DeliverEnrollment)
	}
	firstImage, err := deliver()
	if err != nil {
		t.Fatalf("Deliver(stopped retained VM) = %v, want attachment", err)
	}
	retryImage, err := deliver()
	if err != nil || retryImage != firstImage || len(machines.deliveries) != 1 {
		t.Fatalf("Deliver(retry) = same image %t, %d attachments, %v, want original image and one attachment",
			retryImage == firstImage, len(machines.deliveries), err)
	}
	if got := machines.deliveries[0]; got.Ref != f.ref || got.OperationID != op.ID || !got.Eligible {
		t.Errorf("DeliverEnrollment recovery scope = %v/%s eligible %t, want %v/%s eligible", got.Ref, got.OperationID, got.Eligible, f.ref, op.ID)
	}
	after, err := machines.Inspect(ctx, f.ref)
	if err != nil {
		t.Fatalf("Inspect(after recovery media) = %v, want retained stopped VM", err)
	}
	want := append([]provider.OwnedResource(nil), inventory.Resources...)
	// The fake provider reports the replacement ISO path as the media resource ID.
	want[len(want)-1].ID = firstImage
	if diff := cmp.Diff(want, after.Resources); diff != "" {
		t.Errorf("DeliverEnrollment(stopped retained VM) changed drive inventory (-want +got):\n%s", diff)
	}
	if after.Ref != inventory.Ref || after.Power != provider.Stopped {
		t.Errorf("Inspect(after recovery media) = %v/%s, want %v/stopped", after.Ref, after.Power, inventory.Ref)
	}
	token := recoveryMediaGrant(t, firstImage)
	if token == previousGrant {
		t.Error("Deliver(after long stop) reused the previous grant, want a fresh grant")
	}
	csr := recoveryCSR(t)
	der, err := enrollment.Enroll(ctx, token, csr, now)
	if err != nil {
		t.Fatalf("Enroll(fresh recovery grant) = %v, want certificate", err)
	}
	recovered, err := enrollment.Enroll(ctx, token, csr, now)
	if err != nil || !bytes.Equal(recovered, der) {
		t.Errorf("Enroll(same key retry) = same certificate %t, %v, want original certificate", bytes.Equal(recovered, der), err)
	}
	if _, err := enrollment.Enroll(ctx, token, recoveryCSR(t), now); !errors.Is(err, guest.ErrEnrollmentDenied) {
		t.Errorf("Enroll(consumed grant with another test key) = %v, want %v", err, guest.ErrEnrollmentDenied)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate(recovery leaf) = %v, want valid DER", err)
	}
	contact, err := enrollment.Authenticate(ctx, leaf, now)
	if err != nil || contact.WorkspaceID != f.ref.WorkspaceID || contact.Generation != f.ref.Generation || leaf.SerialNumber.Cmp(oldLeaf.SerialNumber) == 0 {
		t.Errorf("Authenticate(recovery leaf) = %s generation %d new serial %t, %v, want %s generation %d with a new serial",
			contact.WorkspaceID, contact.Generation, leaf.SerialNumber.Cmp(oldLeaf.SerialNumber) != 0, err, f.ref.WorkspaceID, f.ref.Generation)
	}
}

// TestLongStopRecoveryDeniesIneligibleTargets checks that stale provider
// evidence and identities without current authority cannot receive a grant.
func TestLongStopRecoveryDeniesIneligibleTargets(t *testing.T) {
	f, enrollment, _, now := newLongStopFixture(t)
	journal := operation.NewJournal(f.db, func() time.Time { return now }, uuid.NewString)
	op := admitLongStopStart(t, f, journal, now)
	capacityExec(t, f.db, "UPDATE operations SET status='running' WHERE id=$1", op.ID)
	capacityExec(t, f.db, "INSERT INTO guest_registrations(workspace_id,generation,state) VALUES($1,$2,'expired')", f.ref.WorkspaceID, f.ref.Generation)
	inventory := stoppedRecoveryInventory(t, f, now)
	for _, tc := range []struct {
		name   string
		change func(*provider.Inventory)
	}{
		{"unknown workspace", func(got *provider.Inventory) { got.Ref.WorkspaceID = "unknown-workspace" }},
		{"foreign installation", func(got *provider.Inventory) { got.Ref.InstallationID = "foreign-installation" }},
		{"cloned instance", func(got *provider.Inventory) {
			got.Ref.InstanceID = "fixture-clone"
			got.InstanceID = "fixture-clone"
		}},
		{"mismatched instance", func(got *provider.Inventory) { got.InstanceID = "mismatched-instance" }},
		{"mismatched ownership", func(got *provider.Inventory) { got.Ref.OwnershipMarker = "foreign-owner" }},
		{"mismatched generation", func(got *provider.Inventory) { got.Ref.Generation++ }},
		{"stale inspection", func(got *provider.Inventory) { got.ObservedAt = now.Add(-2 * time.Minute) }},
		{"running instance", func(got *provider.Inventory) { got.Power = provider.Running }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := inventory
			tc.change(&candidate)
			if _, err := enrollment.Mint(t.Context(), op.ID, candidate, now); !errors.Is(err, guest.ErrEnrollmentDenied) {
				t.Errorf("Mint(%s) = %v, want %v", tc.name, err, guest.ErrEnrollmentDenied)
			}
		})
	}
	for _, state := range []string{"revoked", "deleted"} {
		t.Run(state, func(t *testing.T) {
			capacityExec(t, f.db, "UPDATE guest_registrations SET state='expired' WHERE workspace_id=$1", f.ref.WorkspaceID)
			token, err := enrollment.Mint(t.Context(), op.ID, inventory, now)
			if err != nil {
				t.Fatalf("Mint(eligible recovery) = %v, want an issued grant", err)
			}
			capacityExec(t, f.db, "UPDATE guest_registrations SET state=$2 WHERE workspace_id=$1", f.ref.WorkspaceID, state)
			if _, err := enrollment.Mint(t.Context(), op.ID, inventory, now); !errors.Is(err, guest.ErrEnrollmentDenied) {
				t.Errorf("Mint(%s) = %v, want %v", state, err, guest.ErrEnrollmentDenied)
			}
			if _, err := enrollment.Enroll(t.Context(), token, recoveryCSR(t), now); !errors.Is(err, guest.ErrEnrollmentDenied) {
				t.Errorf("Enroll(grant issued before registration became %s) = %v, want %v", state, err, guest.ErrEnrollmentDenied)
			}
		})
	}
	capacityExec(t, f.db, "UPDATE guest_registrations SET state='expired' WHERE workspace_id=$1", f.ref.WorkspaceID)
	token, err := enrollment.Mint(t.Context(), op.ID, inventory, now)
	if err != nil {
		t.Fatalf("Mint(eligible recovery) = %v, want an issued grant", err)
	}
	capacityExec(t, f.db, "UPDATE workspaces SET provider_instance_id='fixture-replacement' WHERE id=$1", f.ref.WorkspaceID)
	if _, err := enrollment.Enroll(t.Context(), token, recoveryCSR(t), now); !errors.Is(err, guest.ErrEnrollmentDenied) {
		t.Errorf("Enroll(grant issued for the previous provider instance) = %v, want %v", err, guest.ErrEnrollmentDenied)
	}
	capacityExec(t, f.db, "UPDATE workspaces SET provider_instance_id=$2,desired_state='deleted' WHERE id=$1",
		f.ref.WorkspaceID, f.ref.InstanceID)
	if _, err := enrollment.Enroll(t.Context(), token, recoveryCSR(t), now); !errors.Is(err, guest.ErrEnrollmentDenied) {
		t.Errorf("Enroll(grant issued before workspace deletion) = %v, want %v", err, guest.ErrEnrollmentDenied)
	}
}

func newLongStopFixture(t *testing.T) (*capacityFixture, *guest.Enrollment, *guest.Authority, time.Time) {
	t.Helper()
	f := newCapacityFixture(t, workspace.Allocation{CPU: 2, MemoryMiB: 1024, DiskMiB: 40})
	now := time.Now().UTC().Truncate(time.Second)
	f.machines = fake.New()
	ref := f.ref
	ref.InstanceID = ""
	created, err := f.machines.Create(t.Context(), provider.CreateRequest{
		Ref: ref, OperationID: "retained-fixture", NetworkMode: "nat", ArtifactID: "artifact", Release: "release",
		Resources:       provider.Resources{CPU: 2, MemoryBytes: 1024 << 20, DiskBytes: 40 << 20},
		EnrollmentMedia: "prior-boot.iso",
	})
	if err != nil {
		t.Fatalf("Create(retained fixture) = %v, want stopped VM", err)
	}
	f.ref = created.Ref
	capacityExec(t, f.db, "UPDATE workspaces SET desired_state='stopped',observed_state='stopped',provider_instance_id=$2 WHERE id=$1",
		f.ref.WorkspaceID, f.ref.InstanceID)
	capacityExec(t, f.db, "UPDATE reservations SET cpu_memory_state='released',cpu_memory_released_at=$2 WHERE workspace_id=$1",
		f.ref.WorkspaceID, now.Format(time.RFC3339Nano))
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	authority, err := app.EnsureInstallationCA(t.TempDir(), f.ref.InstallationID, cipher, now.Add(-48*time.Hour))
	if err != nil {
		t.Fatalf("EnsureInstallationCA(fixture) = %v, want test authority", err)
	}
	return f, guest.NewEnrollment(f.db, authority), authority, now
}

func seedPreviousGuest(t *testing.T, f *capacityFixture, enrollment *guest.Enrollment, now time.Time) (string, *x509.Certificate) {
	t.Helper()
	const prior = "previous-start"
	then := now.Add(-24 * time.Hour)
	capacityExec(t, f.db, "UPDATE workspaces SET desired_state='running' WHERE id=$1", f.ref.WorkspaceID)
	capacityExec(t, f.db, `INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,created_at,version,originating_action)
		VALUES($1,$2,'start',$3,$4,$1,'running',$5,1,'workspace.start')`, prior, f.ref.WorkspaceID, f.ref.Generation, f.owner, then.Format(time.RFC3339Nano))
	capacityExec(t, f.db, "INSERT INTO guest_registrations(workspace_id,generation,state) VALUES($1,$2,'eligible')", f.ref.WorkspaceID, f.ref.Generation)
	token, err := enrollment.Mint(t.Context(), prior, stoppedRecoveryInventory(t, f, then), then)
	if err != nil {
		t.Fatalf("Mint(previous start) = %v, want initial grant", err)
	}
	der, err := enrollment.Enroll(t.Context(), token, recoveryCSR(t), then)
	if err != nil {
		t.Fatalf("Enroll(previous start) = %v, want initial certificate", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enrollment.Authenticate(t.Context(), leaf, then); err != nil {
		t.Fatalf("Authenticate(previous start) = %v, want initialized guest contact", err)
	}
	capacityExec(t, f.db, "UPDATE operations SET status='succeeded' WHERE id=$1", prior)
	capacityExec(t, f.db, "UPDATE workspaces SET desired_state='stopped' WHERE id=$1", f.ref.WorkspaceID)
	return token, leaf
}

func admitLongStopStart(t *testing.T, f *capacityFixture, journal *operation.Journal, now time.Time) operation.Operation {
	t.Helper()
	var op operation.Operation
	version := f.snapshot(t).version
	err := database.Transaction(t.Context(), f.db, func(tx *sql.Tx) error {
		var err error
		op, err = workspace.AdmitPower(t.Context(), tx, journal, workspace.PowerIntent{
			WorkspaceID: f.ref.WorkspaceID, ActorID: f.owner, Action: "start", RequestKey: "start-after-long-stop", Version: version,
		}, now)
		return err
	})
	if err != nil {
		t.Fatalf("AdmitPower(start after long stop) = %v, want approved start", err)
	}
	return op
}

func stoppedRecoveryInventory(t *testing.T, f *capacityFixture, now time.Time) provider.Inventory {
	t.Helper()
	inventory, err := f.machines.Inspect(t.Context(), f.ref)
	if err != nil || inventory.Power != provider.Stopped {
		t.Fatalf("Inspect(retained fixture) = %s, %v, want stopped VM", inventory.Power, err)
	}
	inventory.ObservedAt = now
	return inventory
}

func recoveryCSR(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func recoveryMediaGrant(t *testing.T, image string) string {
	t.Helper()
	data, err := os.ReadFile(image)
	if err != nil {
		t.Fatalf("ReadFile(recovery ISO) = %v, want attached media", err)
	}
	offset := bytes.Index(data, []byte(`{"grant":"`))
	if offset < 0 {
		t.Fatal("recovery ISO did not contain the enrollment seed")
	}
	var seed guest.EnrollmentSeed
	if err := json.NewDecoder(bytes.NewReader(data[offset:])).Decode(&seed); err != nil || seed.Grant == "" {
		t.Fatalf("Decode(recovery seed) = %v, want fresh grant", err)
	}
	return seed.Grant
}

type recoveryProvider struct {
	provider.Provider
	now                  time.Time
	deliveries           []provider.EnrollmentRequest
	start                *provider.StartRequest
	deliveredBeforeStart bool
	authenticate         func(context.Context, string) error
	loseStartReply       bool
	interruptBeforeStart context.CancelFunc
	starts               int
}

func (p *recoveryProvider) DeliverEnrollment(ctx context.Context, req provider.EnrollmentRequest) (provider.DeliveryResult, error) {
	p.deliveries = append(p.deliveries, req)
	return p.Provider.DeliverEnrollment(ctx, req)
}

func (p *recoveryProvider) Inspect(ctx context.Context, ref provider.WorkspaceRef) (provider.Inventory, error) {
	inventory, err := p.Provider.Inspect(ctx, ref)
	inventory.ObservedAt = p.now
	return inventory, err
}

func (p *recoveryProvider) Start(ctx context.Context, req provider.StartRequest) (provider.Inventory, error) {
	if p.interruptBeforeStart != nil {
		p.interruptBeforeStart()
		p.interruptBeforeStart = nil
		return provider.Inventory{}, context.Canceled
	}
	p.starts++
	p.start = &req
	p.deliveredBeforeStart = len(p.deliveries) == 1
	inventory, err := p.Provider.Start(ctx, req)
	inventory.ObservedAt = p.now
	if err == nil && p.deliveredBeforeStart && p.authenticate != nil {
		err = p.authenticate(ctx, p.deliveries[0].MediaPath)
	}
	if err == nil && p.loseStartReply {
		p.loseStartReply = false
		return provider.Inventory{}, provider.ErrOutcomeUnknown
	}
	return inventory, err
}

func assertRecoveryDeliveryBeforeStart(t *testing.T, ref provider.WorkspaceRef, op string, machines *recoveryProvider) {
	t.Helper()
	if machines.start == nil || !machines.deliveredBeforeStart {
		t.Fatalf("start reconciliation requested power-on %t after %d recovery deliveries, want one verified delivery before power-on",
			machines.start != nil, len(machines.deliveries))
	}
	if got := machines.deliveries[0]; got.Ref != ref || got.OperationID != op || !got.Eligible {
		t.Errorf("DeliverEnrollment(expired retained VM) = %v/%s eligible %t, want %v/%s eligible", got.Ref, got.OperationID, got.Eligible, ref, op)
	}
	if req := machines.start; req.Ref != ref || req.OperationID != op || !req.RequiresEnrollment {
		t.Errorf("Start(expired retained VM) = %v/%s requires enrollment %t, want %v/%s with required enrollment",
			req.Ref, req.OperationID, req.RequiresEnrollment, ref, op)
	}
}
