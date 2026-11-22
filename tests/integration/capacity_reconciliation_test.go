package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/fake"
	"github.com/jflatten/ShiftBox/internal/testsupport"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// TestCapacityRefusalLeavesRunningWorkspaceAlone verifies that each limiting
// resource refuses admission without creating queued work or displacing the VM.
func TestCapacityRefusalLeavesRunningWorkspaceAlone(t *testing.T) {
	for _, tc := range []struct {
		resource            string
		limit               workspace.Allocation
		wantUsed, wantLimit int64
	}{
		{"cpu", workspace.Allocation{CPU: 3, MemoryMiB: 2048, DiskMiB: 80}, 2, 3},
		{"memory", workspace.Allocation{CPU: 4, MemoryMiB: 1536, DiskMiB: 80}, 1024, 1536},
		{"disk", workspace.Allocation{CPU: 4, MemoryMiB: 2048, DiskMiB: 60}, 40, 60},
	} {
		// Each budget has space in the other two dimensions.
		t.Run(tc.resource, func(t *testing.T) {
			f := newCapacityFixture(t, tc.limit)
			ctx := t.Context()
			journal := operation.NewJournal(f.db, time.Now, uuid.NewString)
			active := f.snapshot(t)
			before, err := f.machines.Inspect(ctx, f.ref)
			if err != nil {
				t.Fatal(err)
			}
			intent := workspace.LaunchIntent{
				InstallationID: f.ref.InstallationID, OwnerID: f.owner, WorkspaceID: testsupport.ID(503),
				RequestKey: "capacity-refusal-launch", OwnershipMarker: "request-owner",
				TemplateID: "template", TemplateRevision: 1, ArtifactID: "artifact",
				RepositoryURL: "https://github.com/example/workspace", DevcontainerPath: ".devcontainer/devcontainer.json",
				NetworkPolicyJSON: `{"mode":"nat"}`, Allocation: active.allocation,
			}
			for range 2 {
				err := database.Transaction(ctx, f.db, func(tx *sql.Tx) error {
					_, err := workspace.AdmitLaunch(ctx, tx, journal, intent, time.Now())
					return err
				})
				want := workspace.CapacityError{Resource: tc.resource, Limit: tc.wantLimit, Used: tc.wantUsed, Requested: tc.wantUsed}
				var capacity *workspace.CapacityError
				if !errors.As(err, &capacity) || *capacity != want {
					t.Fatalf("AdmitLaunch with %s pressure = %v, want %+v", tc.resource, err, want)
				}
			}
			if got := f.snapshot(t); got != active {
				t.Errorf("running workspace changed on refusal (-want +got): %s", cmp.Diff(active, got, cmp.AllowUnexported(capacitySnapshot{})))
			}
			var workspaces, reservations, operations int
			if err := f.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM workspaces),
				(SELECT COUNT(*) FROM reservations), (SELECT COUNT(*) FROM operations)`).
				Scan(&workspaces, &reservations, &operations); err != nil || workspaces != 1 || reservations != 1 || operations != 0 {
				t.Errorf("state after refused launches = %d workspaces, %d reservations, %d operations (%v), want 1/1/0", workspaces, reservations, operations, err)
			}
			after, err := f.machines.Inspect(ctx, f.ref)
			if err != nil || after.Power != provider.Running || before.Effective != after.Effective || before.InstanceID != after.InstanceID {
				t.Errorf("running provider VM after refusal = %s/%s/%+v (%v), want unchanged %s/%s/%+v", after.InstanceID, after.Power, after.Effective, err, before.InstanceID, before.Power, before.Effective)
			}
		})
	}
}

// TestCapacityReleaseReconcilesAfterControllerRestart exercises a user delete
// across conflicting inventory, lost replies, and partial resource deletion.
func TestCapacityReleaseReconcilesAfterControllerRestart(t *testing.T) {
	f := newCapacityFixture(t, workspace.Allocation{CPU: 2, MemoryMiB: 1024, DiskMiB: 40})
	ctx := t.Context()
	machines := &capacityProvider{Provider: f.machines}
	journal := operation.NewJournal(f.db, time.Now, uuid.NewString)
	var op operation.Operation
	err := database.Transaction(ctx, f.db, func(tx *sql.Tx) error {
		var err error
		op, err = workspace.AdmitPower(ctx, tx, journal, workspace.PowerIntent{
			WorkspaceID: f.ref.WorkspaceID, ActorID: f.owner, Action: "delete", RequestKey: "capacity-delete-request", Version: 1,
		}, time.Now())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	worker := func() *operation.Worker {
		return workspace.NewWorker(operation.NewJournal(f.db, time.Now, uuid.NewString), nil,
			workspace.NewPowerExecutor(workspace.NewStore(f.db), machines, time.Now, nil))
	}
	w := worker()
	run := func(want error) {
		t.Helper()
		if err := w.RunOnce(ctx, op.ID); !errors.Is(err, want) {
			t.Fatalf("RunOnce(delete) = %v, want %v", err, want)
		}
	}
	check := func(observed, compute, disk string) {
		t.Helper()
		got := f.snapshot(t)
		if got.desired != "deleted" || got.observed != observed || got.compute != compute || got.disk != disk {
			t.Errorf("delete power/reservations = %s/%s/%s/%s, want deleted/%s/%s/%s", got.desired, got.observed, got.compute, got.disk, observed, compute, disk)
		}
	}
	launch := workspace.LaunchIntent{
		InstallationID: f.ref.InstallationID, OwnerID: f.owner, WorkspaceID: testsupport.ID(503),
		RequestKey: "capacity-reuse-after-delete", OwnershipMarker: "replacement-owner",
		TemplateID: "template", TemplateRevision: 1, ArtifactID: "artifact",
		RepositoryURL: "https://github.com/example/workspace", DevcontainerPath: ".devcontainer/devcontainer.json",
		NetworkPolicyJSON: `{"mode":"nat"}`, Allocation: f.snapshot(t).allocation,
	}
	admitReplacement := func(wantResource string) {
		t.Helper()
		err := database.Transaction(ctx, f.db, func(tx *sql.Tx) error {
			_, err := workspace.AdmitLaunch(ctx, tx, w.Journal, launch, time.Now())
			return err
		})
		if wantResource == "" {
			if err != nil {
				t.Errorf("replacement after verified deletion = %v, want admitted", err)
			}
			return
		}
		var capacity *workspace.CapacityError
		if !errors.As(err, &capacity) || capacity.Resource != wantResource {
			t.Errorf("replacement while resources are held = %v, want %s capacity refusal", err, wantResource)
		}
	}
	// The controller received a graceful shutdown report from the workload.
	capacityExec(t, f.db, "INSERT INTO guest_dispatches(operation_id,dispatch_version,status) VALUES($1,1,'succeeded')", op.ID)
	run(nil)
	machines.conflict = true
	run(provider.ErrOwnershipMismatch)
	check("running", "reserved", "reserved")
	if machines.stops != 0 || machines.deletes != 0 {
		t.Fatalf("provider effects after conflicting inventory = %d stops, %d deletes, want zero", machines.stops, machines.deletes)
	}
	machines.conflict = false
	machines.LoseReply("stop")
	run(provider.ErrOutcomeUnknown)
	check("running", "reserved", "reserved")
	admitReplacement("cpu")

	f.reopen(t)
	w = worker()
	run(nil)
	check("stopped", "released", "reserved")
	admitReplacement("disk")
	if machines.stops != 1 {
		t.Errorf("provider stop calls after restart = %d, want one", machines.stops)
	}

	machines.FailDelete()
	run(provider.ErrPartial)
	check("stopped", "released", "reserved")
	machines.LoseReply("delete")
	run(provider.ErrOutcomeUnknown)
	check("stopped", "released", "reserved")
	admitReplacement("disk")

	f.reopen(t)
	w = worker()
	run(nil)
	check("deleted", "released", "released")
	admitReplacement("")
	if machines.deletes != 2 {
		t.Errorf("provider delete calls after reconciliation = %d, want two", machines.deletes)
	}
	got, err := w.Journal.Get(ctx, op.ID)
	if err != nil || got.Status != "succeeded" {
		t.Errorf("delete operation after confirmed removal = %s (%v), want succeeded", got.Status, err)
	}
}

type capacityFixture struct {
	db       *sql.DB
	path     string
	ref      provider.WorkspaceRef
	owner    string
	machines *fake.Provider
}

type capacitySnapshot struct {
	desired, observed, compute, disk string
	version                          int64
	allocation                       workspace.Allocation
}

func newCapacityFixture(t *testing.T, budget workspace.Allocation) *capacityFixture {
	t.Helper()
	f := &capacityFixture{path: filepath.Join(t.TempDir(), "capacity.db"), owner: "capacity-owner", machines: fake.New()}
	f.reopen(t)
	t.Cleanup(func() { f.db.Close() })
	ctx := t.Context()
	if err := database.Migrate(ctx, f.db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	ref := provider.WorkspaceRef{InstallationID: testsupport.ID(501), WorkspaceID: testsupport.ID(502), Generation: 1, OwnershipMarker: "incumbent"}
	got, err := f.machines.Create(ctx, provider.CreateRequest{Ref: ref, OperationID: "fixture-create", NetworkMode: "nat",
		ArtifactID: "artifact", Release: "release", Resources: provider.Resources{CPU: 2, MemoryBytes: 1024 << 20, DiskBytes: 40 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	f.ref = got.Ref
	if _, err := f.machines.Start(ctx, provider.StartRequest{Ref: f.ref, OperationID: "fixture-start"}); err != nil {
		t.Fatal(err)
	}
	capacityExec(t, f.db, `INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,preview_host_pattern,
		active_release,min_guest_release,created_at,updated_at,version)
		VALUES($1,'active','Fixture','sqlite','https://management.test','{workspace}.ide.test','{service}--{workspace}.preview.test','release','release',$2,$2,1)`, ref.InstallationID, stamp)
	capacityExec(t, f.db, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES($1,'owner@example.test','member','active','local','setup',$2,$2,1)`, f.owner, stamp)
	capacityExec(t, f.db, `INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,owned_resources_json,
		digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state)
		VALUES('artifact','release','amd64','fixture','release','1','[]','{}','{}','{}',
		'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','fixture.utm',$1,$2,'available')`, f.owner, stamp)
	capacityExec(t, f.db, `INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,repository_url,
		devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,generation,ownership_marker,provider_instance_id,created_at,updated_at,version)
		VALUES($1,$2,$3,'template',1,'artifact','https://github.com/example/workspace','.devcontainer/devcontainer.json',2,1024,40,'{"mode":"nat"}',
		'running','running',1,$4,$5,$6,$6,1)`, ref.WorkspaceID, ref.InstallationID, f.owner, ref.OwnershipMarker, f.ref.InstanceID, stamp)
	if err := workspace.NewStore(f.db).SetCapacityBudget(ctx, ref.InstallationID, budget, 0, f.owner, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, f.db, func(tx *sql.Tx) error {
		return workspace.ReserveStart(ctx, tx, ref.InstallationID, ref.WorkspaceID, ref.Generation, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *capacityFixture) reopen(t *testing.T) {
	t.Helper()
	if f.db != nil {
		if err := f.db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	db, err := database.Open(t.Context(), database.Config{Profile: "sqlite", SQLitePath: f.path})
	if err != nil {
		t.Fatal(err)
	}
	f.db = db
}

func (f *capacityFixture) snapshot(t *testing.T) capacitySnapshot {
	t.Helper()
	var s capacitySnapshot
	err := f.db.QueryRowContext(t.Context(), `SELECT w.desired_state,w.observed_state,w.version,w.cpu_count,w.memory_mib,w.disk_mib,r.cpu_memory_state,r.disk_state
		FROM workspaces w JOIN reservations r ON r.workspace_id=w.id WHERE w.id=$1`, f.ref.WorkspaceID).
		Scan(&s.desired, &s.observed, &s.version, &s.allocation.CPU, &s.allocation.MemoryMiB, &s.allocation.DiskMiB, &s.compute, &s.disk)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func capacityExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

type capacityProvider struct {
	*fake.Provider
	conflict       bool
	stops, deletes int
}

func (p *capacityProvider) Inspect(ctx context.Context, ref provider.WorkspaceRef) (provider.Inventory, error) {
	got, err := p.Provider.Inspect(ctx, ref)
	if err == nil && p.conflict {
		got.Ref.OwnershipMarker = "foreign-owner"
	}
	return got, err
}

func (p *capacityProvider) Stop(ctx context.Context, req provider.StopRequest) (provider.Inventory, error) {
	p.stops++
	return p.Provider.Stop(ctx, req)
}

func (p *capacityProvider) Delete(ctx context.Context, req provider.DeleteRequest) (provider.Inventory, error) {
	p.deletes++
	return p.Provider.Delete(ctx, req)
}
