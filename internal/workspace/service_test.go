package workspace

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/fake"
)

// TestWorkspaceLaunchKeepsPinnedOwnerAndSettings verifies that later
// configuration changes preserve the original launch, its capacity claim,
// and the owner context used to authorize workspace entry.
func TestWorkspaceLaunchKeepsPinnedOwnerAndSettings(t *testing.T) {
	ctx := t.Context()
	db := openWorkspaceDatabase(t, "sqlite")
	seedReservationWorkspaces(t, db)
	t.Cleanup(func() {
		for _, query := range []string{
			"DELETE FROM reservations WHERE workspace_id='pinned-workspace'",
			"DELETE FROM workspaces WHERE id='pinned-workspace'",
		} {
			if _, err := db.Exec(query); err != nil {
				t.Errorf("clean up workspace snapshot: %v", err)
			}
		}
	})
	now := time.Unix(10000, 0).UTC()
	allocation := Allocation{CPU: 2, MemoryMiB: 1024, DiskMiB: 40}
	store := NewStore(db)
	const owner = "reservation-owner"
	if err := store.SetCapacityBudget(ctx, testInstallation, allocation, 0, owner, now); err != nil {
		t.Fatal(err)
	}
	ref := "refs/heads/main"
	launch := LaunchIntent{
		InstallationID: testInstallation, WorkspaceID: "pinned-workspace", OwnerID: owner,
		RequestKey: "pinned-workspace-request", TemplateID: "original-template", TemplateRevision: 3,
		ArtifactID: "reservation-artifact", RepositoryURL: "https://github.com/example/original",
		RepositoryRef: &ref, DevcontainerPath: ".devcontainer/custom.json", Allocation: allocation,
		NetworkPolicyJSON: `{"mode":"shared"}`, SecretNames: []string{"API_KEY"}, OwnershipMarker: "pinned-owner",
	}
	journal := operation.NewJournal(db, func() time.Time { return now }, uuid.NewString)
	var first operation.Operation
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		var err error
		first, err = AdmitLaunch(ctx, tx, journal, launch, now)
		return err
	}); err != nil {
		t.Fatalf("AdmitLaunch(original configuration): %v", err)
	}
	checkReservation(t, db, launch.WorkspaceID, "reserved", "reserved")
	if err := store.SetCapacityBudget(ctx, testInstallation, Allocation{CPU: 1, MemoryMiB: 512, DiskMiB: 20}, 1, owner, now); err != nil {
		t.Fatal(err)
	}
	var replay operation.Operation
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		changed := launch
		changed.RepositoryURL = "https://github.com/example/replacement"
		changed.CPU = 1
		var err error
		replay, err = AdmitLaunch(ctx, tx, journal, changed, now)
		return err
	}); err != nil || replay.ID != first.ID || first.Status != "pending" {
		t.Fatalf("AdmitLaunch(retry after configuration change) = %s, %v, want %s with capacity already reserved", replay.ID, err, first.ID)
	}
	var actual LaunchIntent
	var repositoryRef, names, desired, observed string
	err := db.QueryRowContext(ctx, `SELECT owner_user_id,template_id,template_revision,platform_artifact_id,
		repository_url,repository_ref,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,secret_names_json,
		desired_state,observed_state FROM workspaces WHERE id=$1`, launch.WorkspaceID).
		Scan(&actual.OwnerID, &actual.TemplateID, &actual.TemplateRevision, &actual.ArtifactID, &actual.RepositoryURL,
			&repositoryRef, &actual.DevcontainerPath, &actual.CPU, &actual.MemoryMiB, &actual.DiskMiB,
			&actual.NetworkPolicyJSON, &names, &desired, &observed)
	if err != nil {
		t.Fatal(err)
	}
	if actual.OwnerID != owner || actual.TemplateID != launch.TemplateID || actual.TemplateRevision != launch.TemplateRevision ||
		actual.ArtifactID != launch.ArtifactID || actual.RepositoryURL != launch.RepositoryURL || repositoryRef != ref ||
		actual.DevcontainerPath != launch.DevcontainerPath || actual.Allocation != allocation ||
		actual.NetworkPolicyJSON != launch.NetworkPolicyJSON || names != `["API_KEY"]` {
		t.Errorf("persisted snapshot = %+v, ref %q, names %q, want original launch %+v", actual, repositoryRef, names, launch)
	}
	if desired != "running" || observed != "unknown" {
		t.Errorf("pending launch state = %s/%s, want running/unknown", desired, observed)
	}
	resource := auth.Resource{Type: "workspace", ID: launch.WorkspaceID, OwnerID: actual.OwnerID, Eligible: true}
	for _, actor := range []struct {
		id, role, action string
		grant            bool
		allowed, audited bool
	}{
		{id: owner, role: "member", action: "workspace.connect_owner", allowed: true},
		{id: "another-member", role: "member", action: "workspace.connect_owner"},
		{id: "administrator", role: "administrator", action: "workspace.connect_admin"},
		{id: "administrator", role: "administrator", action: "workspace.connect_admin", grant: true, allowed: true, audited: true},
	} {
		principal := auth.Principal{
			User:              auth.User{ID: actor.id, Role: actor.role, MembershipState: "active", CredentialMode: "local"},
			Session:           auth.Session{UserID: actor.id, Mode: "normal", AuthenticatedAt: now, ExpiresAt: now.Add(time.Hour)},
			InstallationState: "active",
		}
		var grants []auth.ResourceGrant
		if actor.grant {
			grants = []auth.ResourceGrant{{SubjectUserID: actor.id, ResourceType: resource.Type, ResourceID: resource.ID, Action: actor.action}}
		}
		got := auth.Authorize(principal, actor.action, resource, grants, now)
		if got.Allowed != actor.allowed || got.AuditRequired != actor.audited || (got.Allowed && got.OwnerContext != owner) {
			t.Errorf("Authorize(%s, %s, grant=%t) = %+v, want allowed %t, audited %t, pinned owner on success",
				actor.id, actor.action, actor.grant, got, actor.allowed, actor.audited)
		}
	}
}

// TestWorkspaceRepairRetainsProviderDisk proves repair is separately gated
// after failed setup and runs only its trusted steps on the retained VM.
func TestWorkspaceRepairRetainsProviderDisk(t *testing.T) {
	ctx := t.Context()
	db := openWorkspaceDatabase(t, "sqlite")
	const id, owner = "repair-workspace", "reservation-owner"
	seedReservationWorkspaces(t, db, id)
	t.Cleanup(func() {
		if _, err := db.Exec("DELETE FROM guest_registrations WHERE workspace_id=$1", id); err != nil {
			t.Errorf("clean up guest fixture: %v", err)
		}
	})
	now := time.Unix(10000, 0).UTC()
	machines := &powerFixtureProvider{Provider: fake.New()}
	machine, err := machines.Create(ctx, provider.CreateRequest{
		Ref:         provider.WorkspaceRef{InstallationID: testInstallation, WorkspaceID: id, OwnershipMarker: "owned", Generation: 1},
		OperationID: "create-repair-fixture", ArtifactID: "reservation-artifact", Release: "reservation-release",
		NetworkMode: "nat", Resources: provider.Resources{CPU: 2, MemoryBytes: 1024 << 20, DiskBytes: 40 << 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := machines.Provider.Start(ctx, provider.StartRequest{Ref: machine.Ref, OperationID: "start-repair-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE workspaces SET observed_state='running',provider_instance_id=$1,network_policy_override_json='{"mode":"nat"}' WHERE id=$2`, machine.InstanceID, id)
	exec(`INSERT INTO guest_registrations(workspace_id,generation,state,certificate_not_after,last_authenticated_at)
		VALUES($1,1,'active',$2,$3)`, id, now.Add(time.Hour).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	store := NewStore(db)
	if err := store.SetCapacityBudget(ctx, testInstallation, Allocation{CPU: 2, MemoryMiB: 1024, DiskMiB: 40}, 0, owner, now); err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		return ReserveStart(ctx, tx, testInstallation, id, 1, now)
	}); err != nil {
		t.Fatal(err)
	}
	journal := operation.NewJournal(db, func() time.Time { return now }, uuid.NewString)
	admit := func(action string) (operation.Operation, error) {
		t.Helper()
		var op operation.Operation
		err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			var version int64
			if err := tx.QueryRowContext(ctx, "SELECT version FROM workspaces WHERE id=$1", id).Scan(&version); err != nil {
				return err
			}
			var err error
			op, err = AdmitPower(ctx, tx, journal, PowerIntent{
				WorkspaceID: id, ActorID: owner, RequestKey: uuid.NewString(), Action: action, Version: version,
			}, now)
			return err
		})
		return op, err
	}
	if _, err := admit("repair"); !errors.Is(err, provider.ErrInvalidState) {
		t.Fatalf("AdmitPower(repair before setup failure) = %v, want invalid state", err)
	}
	failed, err := admit("rebuild")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := journal.Claim(ctx, failed.ID, "repair-fixture", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.StartStep(ctx, claim, 0); err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		return journal.FailStepInTx(ctx, tx, claim, 0, "required project hook failed")
	}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := admit("repair"); !errors.Is(err, provider.ErrInvalidState) {
		t.Fatalf("AdmitPower(repair with stale guest) = %v, want invalid state", err)
	}
	exec("UPDATE guest_registrations SET last_authenticated_at=$1 WHERE workspace_id=$2", now.Format(time.RFC3339Nano), id)
	repair, err := admit("repair")
	if err != nil {
		t.Fatal(err)
	}
	var stepKeys []string
	for _, step := range repair.Steps {
		stepKeys = append(stepKeys, step.Key)
	}
	if diff := cmp.Diff([]string{"repair_enter", "verify_repair"}, stepKeys); diff != "" {
		t.Fatalf("repair workflow includes unexpected automation (-want +got):\n%s", diff)
	}
	if _, err := admit("retry"); !errors.Is(err, ErrOperationInProgress) {
		t.Errorf("AdmitPower(retry during repair) = %v, want operation in progress", err)
	}
	exec("INSERT INTO guest_dispatches(operation_id,dispatch_version,status,container_id) VALUES($1,1,'succeeded','repair-container')", repair.ID)
	power := NewPowerExecutor(store, machines, func() time.Time { return now }, nil)
	probes := 0
	power.SetRepairProbe(func(_ context.Context, workspaceID string, generation int64) error {
		probes++
		if workspaceID != id || generation != before.Ref.Generation {
			t.Errorf("repair probe scope = %s/%d, want %s/%d", workspaceID, generation, id, before.Ref.Generation)
		}
		return nil
	})
	worker := NewWorker(journal, nil, power)
	for range 2 {
		if err := worker.RunOnce(ctx, repair.ID); err != nil {
			t.Fatal(err)
		}
	}
	current, err := journal.Get(ctx, repair.ID)
	if err != nil || current.Status != "succeeded" || probes != 1 {
		t.Fatalf("repair status = %s, %v, probes = %d, want success after one repair IDE probe", current.Status, err, probes)
	}
	after, err := machines.Inspect(ctx, before.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(before.Resources, after.Resources); diff != "" {
		t.Errorf("repair changed owned provider resources (-want +got):\n%s", diff)
	}
	if after.Power != provider.Running || machines.starts != 0 || machines.stops != 0 {
		t.Errorf("repair provider effects = %v, %d starts, %d stops, want original running VM", after.Power, machines.starts, machines.stops)
	}
	checkReservation(t, db, id, "reserved", "reserved")
	if _, err := admit("rebuild"); !errors.Is(err, provider.ErrInvalidState) {
		t.Errorf("AdmitPower(rebuild during verified repair) = %v, want invalid state", err)
	}
	if retry, err := admit("retry"); err != nil || !isRepairRetry(retry) {
		t.Errorf("AdmitPower(explicit exit and retry) = %s/%s, %v, want repair retry", retry.Type, retry.OriginatingAction, err)
	}
}
