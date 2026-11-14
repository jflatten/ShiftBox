package workspace

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
)

var errGuestContactPending = errors.New("waiting for authenticated guest contact")

var errGuestConfigurationPending = errors.New("waiting for guest configuration")

// EnrollmentDelivery handles the private recovery image for one operation.
type EnrollmentDelivery interface {
	Deliver(context.Context, string, provider.Inventory, time.Time, string, string, string,
		func(context.Context, provider.EnrollmentRequest) (provider.DeliveryResult, error)) (string, error)
	CleanupDelivery(context.Context, string, provider.WorkspaceRef, string) error
}

// LaunchExecutor reconciles VM provisioning and authenticated initial enrollment.
type LaunchExecutor struct {
	provision                         *ProvisionExecutor
	enrollment                        EnrollmentDelivery
	mediaRoot, endpoint, pollEndpoint string
	now                               func() time.Time
}

// NewLaunchExecutor binds launch workflow steps to the provider and enrollment service.
func NewLaunchExecutor(store *Store, machines provider.Provider, enrollment EnrollmentDelivery,
	mediaRoot, endpoint, pollEndpoint string, now func() time.Time) *LaunchExecutor {
	return &LaunchExecutor{provision: NewProvisionExecutor(store, machines), enrollment: enrollment,
		mediaRoot: mediaRoot, endpoint: endpoint, pollEndpoint: pollEndpoint, now: now}
}

func (e *LaunchExecutor) Observe(ctx context.Context, op operation.Operation, step operation.Step) (string, bool, error) {
	if step.Key == "configure" {
		evidence, err := e.configuration(ctx, op)
		return evidence, err == nil, err
	}
	if step.Key != "enroll" {
		return e.provision.Observe(ctx, op, step)
	}
	req, got, err := e.inspectEnrollment(ctx, op, step)
	if err != nil {
		return "", false, err
	}
	switch got.Power {
	case provider.Stopped:
		return "", false, nil
	case provider.Running:
		evidence, err := e.confirmContact(ctx, req)
		return evidence, err == nil, err
	default:
		return "", false, provider.ErrOutcomeUnknown
	}
}

func (e *LaunchExecutor) Apply(ctx context.Context, op operation.Operation, step operation.Step) (string, error) {
	if step.Key == "configure" {
		return e.configuration(ctx, op)
	}
	if step.Key != "enroll" {
		return e.provision.Apply(ctx, op, step)
	}
	req, got, err := e.inspectEnrollment(ctx, op, step)
	if err != nil {
		return "", err
	}
	switch got.Power {
	case provider.Running:
		return e.confirmContact(ctx, req)
	case provider.Stopped:
	default:
		return "", provider.ErrOutcomeUnknown
	}
	if _, err := e.enrollment.Deliver(ctx, op.ID, got, e.now(), e.mediaRoot, e.endpoint, e.pollEndpoint, e.provision.provider.DeliverEnrollment); err != nil {
		return "", err
	}
	// Recheck the workspace after the external attachment. Only this path,
	// following confirmed grant delivery, can start an unenrolled VM.
	current, err := e.provision.request(ctx, op, step)
	if err != nil {
		return "", err
	}
	if current != req {
		return "", provider.ErrOwnershipMismatch
	}
	started, err := e.provision.provider.Start(ctx, provider.StartRequest{Ref: req.Ref, OperationID: op.ID, NetworkMode: req.NetworkMode})
	if err != nil {
		return "", err
	}
	if err := verifyWorkspaceVM(req, started); err != nil {
		return "", err
	}
	if started.Power != provider.Running {
		return "", provider.ErrOutcomeUnknown
	}
	return e.confirmContact(ctx, req)
}

func (e *LaunchExecutor) configuration(ctx context.Context, op operation.Operation) (string, error) {
	var status, problem string
	err := e.provision.store.db.QueryRowContext(ctx, `SELECT status,problem FROM guest_configurations WHERE operation_id=$1`, op.ID).
		Scan(&status, &problem)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errGuestConfigurationPending
	}
	if err != nil {
		return "", err
	}
	switch status {
	case "succeeded":
		return "The guest reports that the Dev Container completed its setup hooks.", nil
	case "failed":
		switch problem {
		case "guest.repository_preparation_failed":
			return "", &operation.StepFailure{Detail: "The guest could not prepare the repository with the owner's Git authorization."}
		case "guest.required_setup_failed":
			return "", &operation.StepFailure{Detail: "Required Dev Container setup failed before the connection gate."}
		case "guest.later_setup_failed":
			return "", &operation.StepFailure{Detail: "A Dev Container hook failed after the connection gate."}
		case "guest.lifecycle_interrupted":
			return "", &operation.StepFailure{Detail: "The guest restarted during a Dev Container setup hook."}
		default:
			return "", errors.New("invalid persisted guest configuration failure")
		}
	default:
		return "", errGuestConfigurationPending
	}
}

func (e *LaunchExecutor) inspectEnrollment(ctx context.Context, op operation.Operation, step operation.Step) (provider.CreateRequest, provider.Inventory, error) {
	if e.enrollment == nil || e.now == nil {
		return provider.CreateRequest{}, provider.Inventory{}, provider.ErrInvalidState
	}
	req, err := e.provision.request(ctx, op, step)
	if err != nil {
		return req, provider.Inventory{}, err
	}
	if req.Ref.InstanceID == "" {
		return req, provider.Inventory{}, provider.ErrInvalidState
	}
	got, err := e.provision.provider.Inspect(ctx, req.Ref)
	if err != nil {
		return req, got, err
	}
	return req, got, verifyWorkspaceVM(req, got)
}

func verifyWorkspaceVM(req provider.CreateRequest, got provider.Inventory) error {
	if got.Ref != req.Ref || got.InstanceID != req.Ref.InstanceID {
		return provider.ErrOwnershipMismatch
	}
	if got.ArtifactID != req.ArtifactID || got.Release != req.Release || got.NetworkMode != req.NetworkMode ||
		got.ObservedAt.IsZero() {
		return provider.ErrOutcomeUnknown
	}
	return nil
}

func (e *LaunchExecutor) confirmContact(ctx context.Context, req provider.CreateRequest) (string, error) {
	ref := req.Ref
	now := e.now().UTC()
	db := e.provision.store.db
	if _, err := db.ExecContext(ctx, `UPDATE workspaces SET observed_state='running',updated_at=$1,version=version+1
		WHERE id=$2 AND installation_id=$3 AND generation=$4 AND ownership_marker=$5 AND provider_instance_id=$6
		  AND desired_state='running' AND observed_state IN ('stopped','starting')`,
		now.Format(time.RFC3339Nano), ref.WorkspaceID, ref.InstallationID, ref.Generation, ref.OwnershipMarker, ref.InstanceID); err != nil {
		return "", err
	}

	var expiry string
	err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(r.certificate_not_after),'') FROM guest_registrations r
		JOIN workspaces w ON w.id=r.workspace_id AND w.generation=r.generation
		JOIN enrollment_grants g ON g.workspace_id=w.id AND g.generation=w.generation AND g.key_proof=r.public_key_fingerprint
		JOIN users u ON u.id=w.owner_user_id JOIN installations i ON i.id=w.installation_id
		WHERE w.id=$1 AND w.installation_id=$2 AND w.generation=$3 AND w.ownership_marker=$4
		  AND w.provider_instance_id=$5 AND w.desired_state='running' AND w.observed_state='running'
		  AND g.operation_id=$6 AND g.provider_instance_id=$5 AND g.state='consumed'
		  AND i.state='active' AND u.membership_state='active' AND r.state='active' AND r.last_authenticated_at IS NOT NULL`,
		ref.WorkspaceID, ref.InstallationID, ref.Generation, ref.OwnershipMarker, ref.InstanceID, req.OperationID).Scan(&expiry)
	if err != nil {
		return "", err
	}
	if expiry == "" {
		return "", errGuestContactPending
	}
	deadline, err := time.Parse(time.RFC3339Nano, expiry)
	if err != nil {
		return "", err
	}
	if !now.Before(deadline) {
		return "", errGuestContactPending
	}
	if err := e.enrollment.CleanupDelivery(ctx, req.OperationID, ref, e.mediaRoot); err != nil {
		return "", err
	}
	return "The enrolled guest authenticated with its registered key.", nil
}
