package workspace

import (
	"context"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
)

// NewWorker reconciles the workspace actions implemented by the controller.
func NewWorker(journal *operation.Journal, launch *LaunchExecutor, power *PowerExecutor) *operation.Worker {
	if power != nil {
		configured := *power
		configured.recovery = launch
		power = &configured
	}
	return &operation.Worker{
		Journal: journal, Name: "workspace-controller", LeaseDuration: 4 * time.Minute,
		Executor: workspaceExecutor{launch: launch, power: power},
		Actions:  []string{"workspace.launch", "workspace.start", "workspace.stop", "workspace.rebuild", "workspace.repair", "workspace.delete", "user.disable", retentionDeleteAction},
	}
}

type workspaceExecutor struct {
	launch *LaunchExecutor
	power  *PowerExecutor
}

func (e workspaceExecutor) executor(op operation.Operation) operation.Executor {
	switch op.OriginatingAction {
	case "workspace.launch":
		if op.Type == "provision" && e.launch != nil {
			return e.launch
		}
	case "workspace.start", "workspace.stop", "workspace.rebuild", "workspace.repair":
		if (op.OriginatingAction == "workspace."+op.Type || isRepairRetry(op)) && e.power != nil {
			return e.power
		}
	case "user.disable":
		if op.Type == "stop" && e.power != nil {
			return e.power
		}
	case "workspace.delete", retentionDeleteAction:
		if op.Type == "delete" && e.power != nil {
			return deletionExecutor{store: e.power.store, machines: e.power.provider, now: e.power.now}
		}
	}
	return nil
}

func (e workspaceExecutor) Observe(ctx context.Context, op operation.Operation, step operation.Step) (string, bool, error) {
	executor := e.executor(op)
	if executor == nil {
		return "", false, provider.ErrInvalidState
	}
	evidence, done, err := executor.Observe(ctx, op, step)
	return evidence, done, workspaceEffectError(op, err)
}

func (e workspaceExecutor) Apply(ctx context.Context, op operation.Operation, step operation.Step) (string, error) {
	executor := e.executor(op)
	if executor == nil {
		return "", provider.ErrInvalidState
	}
	evidence, err := executor.Apply(ctx, op, step)
	return evidence, workspaceEffectError(op, err)
}

func workspaceEffectError(op operation.Operation, err error) error {
	var alreadyWaiting *operation.StepWaiting
	if err == nil || errors.As(err, &alreadyWaiting) {
		return err
	}
	var detail string
	switch {
	case errors.Is(err, provider.ErrNotFound):
		detail = "UTM could not find the workspace VM. Check the provider inventory. ShiftBox is retaining its reservations."
	case errors.Is(err, provider.ErrOwnershipMismatch):
		detail = "The VM no longer matches the workspace's ownership record. Check the provider inventory. ShiftBox is retaining its reservations."
	case errors.Is(err, provider.ErrPartial):
		detail = "The provider still reports workspace resources after cleanup. Check the VM, disk, and removable media in UTM. ShiftBox is retaining its reservations."
	case errors.Is(err, provider.ErrPermission):
		detail = "ShiftBox could not confirm the requested action through UTM. Check the controller's UTM automation permissions."
	default:
		if op.OriginatingAction != "user.disable" {
			return err
		}
		detail = "The provider has not confirmed the workspace stopped. Check that UTM is running and the VM is reachable."
	}
	return &operation.StepWaiting{Detail: detail, Cause: err}
}
