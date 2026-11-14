package utm

import (
	"context"
	"errors"
	"sync"

	"github.com/jflatten/ShiftBox/internal/provider"
)

// PowerController serializes VM power and recovery-media changes.
type PowerController struct {
	mu      sync.Mutex
	version string
	inspect func(context.Context, provider.WorkspaceRef) (provider.Inventory, error)
	apply   func(context.Context, provider.WorkspaceRef, string) error
	replace func(context.Context, provider.EnrollmentRequest, string) error
}

func NewPowerController(version string) *PowerController {
	return &PowerController{version: version, inspect: (Inspector{Version: version}).Inspect,
		apply: func(ctx context.Context, ref provider.WorkspaceRef, action string) error {
			return applyPower(ctx, version, ref, action)
		},
		replace: func(ctx context.Context, req provider.EnrollmentRequest, driveID string) error {
			return replaceMedia(ctx, version, req, driveID)
		}}
}

func (p *PowerController) Start(ctx context.Context, req provider.StartRequest) (provider.Inventory, error) {
	return p.change(ctx, req.Ref, req.OperationID, "start", provider.Stopped, provider.Running, func(inventory provider.Inventory) error {
		if req.NetworkMode != "" && req.NetworkMode != inventory.NetworkMode {
			return provider.ErrInvalidState
		}
		if !req.RequiresEnrollment || inventory.Power == provider.Running {
			return nil
		}
		if inventory.Power != provider.Stopped || inventory.Ref != req.Ref || inventory.InstanceID != req.Ref.InstanceID {
			return provider.ErrInvalidState
		}
		mediaID, err := enrollmentDrive(inventory, req.Ref)
		if err != nil {
			return err
		}
		// UTM's inventory exposes drive size but omits its source path. Apply
		// the journaled attachment again while power changes are locked.
		_, err = p.replaceRecoveryMediaLocked(ctx, provider.EnrollmentRequest{
			Ref: req.Ref, OperationID: req.OperationID, MediaPath: req.EnrollmentMedia, Eligible: true,
		}, mediaID)
		return err
	})
}

func (p *PowerController) Stop(ctx context.Context, req provider.StopRequest) (provider.Inventory, error) {
	action := "request"
	if req.ForceRetentionDelete {
		action = "force"
	}
	return p.change(ctx, req.Ref, req.OperationID, action, provider.Running, provider.Stopped, nil)
}

func (p *PowerController) change(ctx context.Context, ref provider.WorkspaceRef, operationID, action string, from, desired provider.PowerState, prepare func(provider.Inventory) error) (provider.Inventory, error) {
	if ref.InstanceID == "" || operationID == "" {
		return provider.Inventory{}, provider.ErrInvalidState
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	before, err := p.inspect(ctx, ref)
	if err != nil {
		return before, err
	}
	if prepare != nil {
		if err := prepare(before); err != nil {
			return before, err
		}
	}
	if before.Power == desired {
		return before, nil
	}
	if before.Power != from {
		return before, provider.ErrInvalidState
	}
	if err := ctx.Err(); err != nil {
		return before, err
	}
	err = p.apply(ctx, ref, action)
	after, inspectErr := p.inspect(ctx, ref)
	if inspectErr == nil && after.Power == desired {
		return after, nil
	}
	if errors.Is(err, provider.ErrPermission) || errors.Is(err, provider.ErrOwnershipMismatch) ||
		errors.Is(err, provider.ErrCapabilityUnsupported) || errors.Is(err, provider.ErrInvalidState) {
		return after, err
	}
	return after, errors.Join(provider.ErrOutcomeUnknown, inspectErr)
}
