package utm

import (
	"context"
	"errors"
	"sync"

	"github.com/jflatten/ShiftBox/internal/provider"
)

// PowerController serializes VM power changes and verifies their effects.
type PowerController struct {
	mu      sync.Mutex
	inspect func(context.Context, provider.WorkspaceRef) (provider.Inventory, error)
	apply   func(context.Context, provider.WorkspaceRef, string) error
}

func NewPowerController(version string) *PowerController {
	return &PowerController{inspect: (Inspector{Version: version}).Inspect,
		apply: func(ctx context.Context, ref provider.WorkspaceRef, action string) error {
			return applyPower(ctx, version, ref, action)
		}}
}

func (p *PowerController) Start(ctx context.Context, req provider.StartRequest) (provider.Inventory, error) {
	return p.change(ctx, req.Ref, req.OperationID, "start", provider.Stopped, provider.Running, func(inventory provider.Inventory) bool {
		return (req.NetworkMode == "" || req.NetworkMode == inventory.NetworkMode) &&
			(!req.RequiresEnrollment || inventory.Power == provider.Running)
	})
}

func (p *PowerController) Stop(ctx context.Context, req provider.StopRequest) (provider.Inventory, error) {
	action := "request"
	if req.ForceRetentionDelete {
		action = "force"
	}
	return p.change(ctx, req.Ref, req.OperationID, action, provider.Running, provider.Stopped, nil)
}

func (p *PowerController) change(ctx context.Context, ref provider.WorkspaceRef, operationID, action string, from, desired provider.PowerState, allowed func(provider.Inventory) bool) (provider.Inventory, error) {
	if ref.InstanceID == "" || operationID == "" {
		return provider.Inventory{}, provider.ErrInvalidState
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	before, err := p.inspect(ctx, ref)
	if err != nil {
		return before, err
	}
	if allowed != nil && !allowed(before) {
		return before, provider.ErrInvalidState
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
