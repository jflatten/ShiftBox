package workspace

import (
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

// TestNetworkPolicyAdmission checks that unrestricted UTM policies stay
// exception-free and that invalid or unsupported restrictions cannot be saved.
func TestNetworkPolicyAdmission(t *testing.T) {
	ctx := t.Context()
	db := openWorkspaceDatabase(t, "sqlite")
	caps := provider.Capabilities{Name: "utm", Version: "5.0.8", NetworkModes: []string{"shared"}, NetworkStoppedOnly: true}
	create := func(policy NetworkPolicy, caps provider.Capabilities) error {
		return database.Transaction(ctx, db, func(tx *sql.Tx) error {
			return CreateNetworkPolicyInTx(ctx, tx, "outbound-policy", policy, caps)
		})
	}
	allow := NetworkRule{Destination: "192.0.2.0/24", Transport: "tcp", Port: 443}
	for _, tc := range []struct {
		name string
		mode string
		rule NetworkRule
		want error
	}{
		{"unverified restriction", "allowlist", allow, provider.ErrCapabilityUnsupported},
		{"bad CIDR", "allowlist", NetworkRule{Destination: "192.0.2.2", Transport: "tcp", Port: 443}, ErrNetworkPolicyInvalid},
		{"bad transport", "allowlist", NetworkRule{Destination: allow.Destination, Transport: "sctp", Port: 443}, ErrNetworkPolicyInvalid},
		{"bad port", "allowlist", NetworkRule{Destination: allow.Destination, Transport: "udp", Port: 65536}, ErrNetworkPolicyInvalid},
		{"rules for unrestricted mode", "all", allow, ErrNetworkPolicyInvalid},
		{"rules for no-network mode", "no_network", allow, ErrNetworkPolicyInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := NetworkPolicy{Name: tc.name, Mode: tc.mode, Rules: []NetworkRule{tc.rule}}
			if err := create(policy, caps); !errors.Is(err, tc.want) {
				t.Errorf("create(%+v) = %v, want %v", policy, err, tc.want)
			}
		})
	}
	if err := create(NetworkPolicy{Name: "No network", Mode: "no_network"}, caps); !errors.Is(err, provider.ErrCapabilityUnsupported) {
		t.Errorf("create(no_network) = %v, want unsupported without verified exceptions", err)
	}
	unrestricted := NetworkPolicy{Name: "Outbound", Mode: "all"}
	withoutShared := caps
	withoutShared.NetworkModes = nil
	if err := create(unrestricted, withoutShared); !errors.Is(err, provider.ErrCapabilityUnsupported) {
		t.Errorf("create(all without shared) = %v, want unsupported", err)
	}
	if err := create(unrestricted, caps); err != nil {
		t.Fatalf("create(all with shared) = %v, want success", err)
	}
	var rules, exceptions string
	if err := db.QueryRowContext(ctx, "SELECT rules_json,required_exceptions_json FROM network_policies WHERE id='outbound-policy'").Scan(&rules, &exceptions); err != nil ||
		rules != "[]" || exceptions != "[]" {
		t.Fatalf("unrestricted rules and exceptions = %q and %q (%v), want empty arrays", rules, exceptions, err)
	}
}

// TestLaunchNetworkRechecksPolicy verifies that an old or altered policy
// cannot become launch authority after the provider's capabilities change.
func TestLaunchNetworkRechecksPolicy(t *testing.T) {
	ctx := t.Context()
	db := openWorkspaceDatabase(t, "sqlite")
	caps := provider.Capabilities{Name: "utm", Version: "5.0.8", NetworkModes: []string{"shared"}, NetworkStoppedOnly: true}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		return CreateNetworkPolicyInTx(ctx, tx, "outbound-policy", NetworkPolicy{Name: "Outbound", Mode: "all"}, caps)
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := func(id string, caps provider.Capabilities) (string, error) {
		var result string
		err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			var err error
			result, err = LaunchNetworkSnapshot(ctx, tx, id, caps)
			return err
		})
		return result, err
	}
	if got, err := snapshot("outbound-policy", caps); err != nil || got != `{"mode":"shared","policyId":"outbound-policy"}` {
		t.Fatalf("snapshot(outbound-policy) = %q, %v, want pinned shared policy", got, err)
	}
	replacement := caps
	replacement.Version = "fixture-next"
	if got, err := snapshot("outbound-policy", replacement); got != "" || !errors.Is(err, provider.ErrCapabilityUnsupported) {
		t.Errorf("snapshot(outbound-policy after provider change) = %q, %v, want unsupported", got, err)
	}
	// A restricted policy recorded by some other provider cannot be applied
	// to UTM merely because it includes the required destination exceptions.
	if _, err := db.ExecContext(ctx, `INSERT INTO network_policies(id,name,mode,rules_json,required_exceptions_json,provider_capability_version)
		VALUES('restricted','Restricted','no_network','[]',$1,$2)`,
		`[{"destination":"192.0.2.2/32","transport":"tcp","port":9443},{"destination":"192.0.2.53/32","transport":"udp","port":53},{"destination":"192.0.2.80/32","transport":"tcp","port":443}]`,
		caps.Version); err != nil {
		t.Fatal(err)
	}
	if got, err := snapshot("restricted", caps); got != "" || !errors.Is(err, provider.ErrCapabilityUnsupported) {
		t.Errorf("snapshot(restricted with exceptions) = %q, %v, want unsupported", got, err)
	}
}

// TestNetworkVerificationLeavesVMStopped exercises the worker when its
// provider reports a different mode from the stopped workspace's policy.
func TestNetworkVerificationLeavesVMStopped(t *testing.T) {
	ctx := t.Context()
	db := openWorkspaceDatabase(t, "sqlite")
	seedReservationWorkspaces(t, db, "policy-vm")
	machines := &powerFixtureProvider{Provider: fake.New()}
	ref := provider.WorkspaceRef{InstallationID: testInstallation, WorkspaceID: "policy-vm", OwnershipMarker: "owned", Generation: 1}
	vm, err := machines.Create(ctx, provider.CreateRequest{Ref: ref, OperationID: "fixture-create",
		ArtifactID: "reservation-artifact", Release: "reservation-release", NetworkMode: "nat",
		Resources: provider.Resources{CPU: 2, MemoryBytes: 1024 << 20, DiskBytes: 40 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE workspaces SET desired_state='stopped',observed_state='stopped',
		provider_instance_id=$1,network_policy_override_json='{"mode":"shared","policyId":"outbound-policy"}' WHERE id=$2`,
		vm.InstanceID, ref.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	if err := store.SetCapacityBudget(ctx, testInstallation, Allocation{CPU: 2, MemoryMiB: 1024, DiskMiB: 40}, 0, "reservation-owner", time.Now()); err != nil {
		t.Fatal(err)
	}
	journal := operation.NewJournal(db, time.Now, uuid.NewString)
	var start operation.Operation
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		var err error
		start, err = AdmitPower(ctx, tx, journal, PowerIntent{
			WorkspaceID: ref.WorkspaceID, ActorID: "reservation-owner", Action: "start", RequestKey: uuid.NewString(), Version: 1,
		}, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(journal, nil, NewPowerExecutor(store, machines, time.Now, nil))
	if err := worker.RunOnce(ctx, start.ID); !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Errorf("RunOnce(start with mismatched network) = %v, want unverified observation", err)
	}
	if machines.starts != 0 {
		t.Errorf("provider start calls = %d, want 0", machines.starts)
	}
	inventory, err := machines.Inspect(ctx, vm.Ref)
	if err != nil || inventory.Power != provider.Stopped {
		t.Errorf("Inspect(unverified VM) = %s, %v, want stopped", inventory.Power, err)
	}
	var observed string
	if err := db.QueryRowContext(ctx, "SELECT observed_state FROM workspaces WHERE id=$1", ref.WorkspaceID).Scan(&observed); err != nil || observed != "stopped" {
		t.Errorf("stored observed power = %q, %v, want stopped", observed, err)
	}
	op, err := journal.Get(ctx, start.ID)
	if err != nil || op.Status != "waiting" {
		t.Errorf("Get(unverified start) = %s, %v, want waiting", op.Status, err)
	}
}
