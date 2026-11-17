package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
)

// ProvisionExecutor reconciles the VM creation step of a workspace launch.
type ProvisionExecutor struct {
	store         *Store
	provider      provider.Provider
	bootstrapData func(string) (string, error)
}

// NewProvisionExecutor binds workspace records and provider effects to the worker.
func NewProvisionExecutor(store *Store, machines provider.Provider) *ProvisionExecutor {
	return &ProvisionExecutor{store: store, provider: machines}
}

// Observe checks the provider before creating or retrying the workspace VM.
func (p *ProvisionExecutor) Observe(ctx context.Context, op operation.Operation, step operation.Step) (string, bool, error) {
	if step.Key != "provision" {
		return "", false, provider.ErrInvalidState
	}
	req, err := p.request(ctx, op, step)
	if err != nil {
		return "", false, err
	}
	got, err := p.provider.Inspect(ctx, req.Ref)
	if errors.Is(err, provider.ErrNotFound) && req.Ref.InstanceID == "" {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if req.Ref.InstanceID == "" && got.EC2Enrollment {
		// Reenter Create to verify the original launch request. Legacy instances
		// finish their unseeded stop, while single-boot instances stay running.
		return "", false, nil
	}
	evidence, err := p.record(ctx, req, got)
	return evidence, err == nil, err
}

// Apply creates the workspace VM using the pinned launch inputs and enrollment mode.
func (p *ProvisionExecutor) Apply(ctx context.Context, op operation.Operation, step operation.Step) (string, error) {
	if step.Key != "provision" {
		return "", provider.ErrInvalidState
	}
	req, err := p.request(ctx, op, step)
	if err != nil {
		return "", err
	}
	if req.Ref.InstanceID != "" {
		return "", provider.ErrInvalidState
	}
	if err := p.prepareBootstrap(ctx, &req); err != nil {
		return "", err
	}
	got, err := p.provider.Create(ctx, req)
	if err != nil {
		return "", err
	}
	return p.record(ctx, req, got)
}

func (p *ProvisionExecutor) request(ctx context.Context, op operation.Operation, step operation.Step) (provider.CreateRequest, error) {
	if op.Type != "provision" || op.OriginatingAction != "workspace.launch" || op.WorkspaceID == nil ||
		op.Status != "running" || step.Status != "active" {
		return provider.CreateRequest{}, provider.ErrInvalidState
	}

	req := provider.CreateRequest{OperationID: op.ID, Ref: provider.WorkspaceRef{WorkspaceID: *op.WorkspaceID, Generation: op.Generation}}
	var pinned Allocation
	var policy, kind string
	err := p.store.db.QueryRowContext(ctx, `SELECT w.installation_id,w.ownership_marker,COALESCE(w.provider_instance_id,''),
		w.platform_artifact_id,a.release_id,w.cpu_count,w.memory_mib,w.disk_mib,w.network_policy_snapshot_json,a.provider_kind,w.instance_type,COALESCE(b.public_data,'')
		FROM workspaces w JOIN platform_artifacts a ON a.id=w.platform_artifact_id JOIN reservations r ON r.workspace_id=w.id
		JOIN users u ON u.id=w.owner_user_id JOIN installations i ON i.id=w.installation_id
		LEFT JOIN ec2_launch_enrollment b ON b.operation_id=$4
		WHERE w.id=$1 AND w.generation=$2 AND w.owner_user_id=$3 AND w.desired_state='running'
		  AND i.state='active' AND u.membership_state='active'
		  AND a.state='available' AND r.cpu_memory_state='reserved' AND r.disk_state='reserved'`,
		req.Ref.WorkspaceID, op.Generation, op.Actor, op.ID).
		Scan(&req.Ref.InstallationID, &req.Ref.OwnershipMarker, &req.Ref.InstanceID, &req.ArtifactID, &req.Release,
			&pinned.CPU, &pinned.MemoryMiB, &pinned.DiskMiB, &policy, &kind, &pinned.InstanceType, &req.BootstrapData)
	if err != nil {
		return provider.CreateRequest{}, err
	}
	var network struct {
		Mode string `json:"mode"`
	}
	const mib = 1 << 20
	if json.Unmarshal([]byte(policy), &network) != nil || network.Mode == "" ||
		!pinned.positive() || pinned.MemoryMiB > math.MaxInt64/mib || pinned.DiskMiB > math.MaxInt64/mib {
		return provider.CreateRequest{}, provider.ErrInvalidState
	}
	if kind == "ec2" {
		pinned.InstanceType = pinned.EC2Type()
		if pinned.InstanceType == "" {
			return provider.CreateRequest{}, provider.ErrInvalidState
		}
	}
	req.NetworkMode = network.Mode
	req.Resources = provider.Resources{InstanceType: pinned.InstanceType, CPU: pinned.CPU, MemoryBytes: pinned.MemoryMiB * mib, DiskBytes: pinned.DiskMiB * mib}
	return req, nil
}

func verifyProvisionInventory(req provider.CreateRequest, got provider.Inventory) error {
	want := req.Ref
	if want.InstanceID == "" {
		want.InstanceID = got.InstanceID
	}
	if want != got.Ref || want.InstanceID == "" || got.InstanceID != want.InstanceID {
		return provider.ErrOwnershipMismatch
	}
	wantPower := provider.Stopped
	if req.BootstrapData != "" {
		wantPower = provider.Running
	}
	if got.Power != wantPower || got.ObservedAt.IsZero() ||
		got.ArtifactID != req.ArtifactID || got.Release != req.Release || got.NetworkMode != req.NetworkMode ||
		!got.DiskCapacityKnown || got.Effective != req.Resources {
		return provider.ErrOutcomeUnknown
	}
	ids := make(map[string]bool, len(got.Resources))
	hasVM, hasDisk := false, false
	for _, asset := range got.Resources {
		if asset.ID == "" || asset.Type == "" || ids[asset.ID] {
			return provider.ErrOutcomeUnknown
		}
		ids[asset.ID] = true
		if asset.Shared || asset.Immutable {
			continue
		}
		if asset.Generation != want.Generation || asset.OwnershipMarker != want.OwnershipMarker {
			return provider.ErrOwnershipMismatch
		}
		hasVM = hasVM || (asset.Type == "vm" && asset.ID == got.InstanceID)
		hasDisk = hasDisk || asset.Type == "disk"
	}
	if !hasVM || !hasDisk {
		return provider.ErrOutcomeUnknown
	}
	return nil
}

func (p *ProvisionExecutor) record(ctx context.Context, req provider.CreateRequest, got provider.Inventory) (string, error) {
	if err := verifyProvisionInventory(req, got); err != nil {
		return "", err
	}
	err := database.Transaction(ctx, p.store.db, func(tx *sql.Tx) error {
		ref := req.Ref
		stamp := got.ObservedAt.UTC().Format(time.RFC3339Nano)
		observed := "stopped"
		if req.BootstrapData != "" {
			observed = "starting"
		}
		result, err := tx.ExecContext(ctx, `UPDATE workspaces SET provider_instance_id=$1, observed_state=$7, updated_at=$2, version=version+1
			WHERE id=$3 AND installation_id=$4 AND generation=$5 AND ownership_marker=$6 AND desired_state='running'
			  AND (provider_instance_id IS NULL OR provider_instance_id=$1)
			  AND observed_state IN ('unknown','provisioning','stopped','starting')`,
			got.InstanceID, stamp, ref.WorkspaceID, ref.InstallationID, ref.Generation, ref.OwnershipMarker, observed)
		if err := capacityWrite(result, err); err != nil {
			return err
		}
		for _, asset := range got.Resources {
			facts, err := json.Marshal(asset)
			if err != nil {
				return err
			}
			result, err := tx.ExecContext(ctx, `INSERT INTO provider_resources
				(workspace_id,provider_resource_id,resource_type,generation,observed_json,ownership_proof,last_observed_at)
				VALUES($1,$2,$3,$4,$5,$6,$7)
				ON CONFLICT(workspace_id,provider_resource_id) DO UPDATE SET observed_json=excluded.observed_json,
				  last_observed_at=excluded.last_observed_at
				WHERE provider_resources.resource_type=excluded.resource_type AND provider_resources.generation=excluded.generation
				  AND provider_resources.ownership_proof=excluded.ownership_proof AND provider_resources.deleted_at IS NULL`,
				ref.WorkspaceID, asset.ID, asset.Type, ref.Generation, string(facts), asset.OwnershipMarker, stamp)
			if err := capacityWrite(result, err); err != nil {
				return err
			}
		}
		// An omitted resource remains unresolved until a later lifecycle
		// operation reconciles it. A retry cannot silently discard it.
		var tracked int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_resources
			WHERE workspace_id=$1 AND generation=$2 AND deleted_at IS NULL`, ref.WorkspaceID, ref.Generation).Scan(&tracked); err != nil {
			return err
		}
		if tracked != len(got.Resources) {
			return provider.ErrOutcomeUnknown
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO guest_registrations(workspace_id,generation,state)
			VALUES($1,$2,'eligible') ON CONFLICT(workspace_id,generation) DO NOTHING`, ref.WorkspaceID, ref.Generation)
		return err
	})
	if err != nil {
		return "", err
	}
	if req.BootstrapData != "" {
		return "Provider confirmed the running workspace VM and its resources.", nil
	}
	return "Provider confirmed the stopped workspace VM and its resources.", nil
}

// Freeze the public user data before the external launch. Retries reuse the
// exact payload even if listener configuration changed after a lost response.
func (p *ProvisionExecutor) prepareBootstrap(ctx context.Context, req *provider.CreateRequest) error {
	if req.BootstrapData != "" {
		return nil
	}
	var reference string
	err := p.store.db.QueryRowContext(ctx, `SELECT launch_reference FROM ec2_launch_enrollment WHERE operation_id=$1`, req.OperationID).Scan(&reference)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if p.bootstrapData == nil {
		return provider.ErrCapabilityUnsupported
	}
	data, err := p.bootstrapData(reference)
	if err != nil {
		return err
	}
	return p.store.db.QueryRowContext(ctx, `UPDATE ec2_launch_enrollment SET public_data=COALESCE(public_data,$2)
		WHERE operation_id=$1 RETURNING public_data`, req.OperationID, data).Scan(&req.BootstrapData)
}
