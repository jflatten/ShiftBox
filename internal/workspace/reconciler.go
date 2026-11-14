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
	return &operation.Worker{
		Journal: journal, Name: "workspace-controller", LeaseDuration: 4 * time.Minute,
		Executor: workspaceExecutor{launch: launch, power: power},
		Actions:  []string{"workspace.launch", "workspace.start", "workspace.stop", "workspace.rebuild", "user.disable", retentionDeleteAction},
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
	case "workspace.start", "workspace.stop", "workspace.rebuild":
		if op.OriginatingAction == "workspace."+op.Type && e.power != nil {
			return e.power
		}
	case "user.disable":
		if op.Type == "stop" && e.power != nil {
			return e.power
		}
	case retentionDeleteAction:
		if op.Type == "delete" && e.power != nil {
			return retentionExecutor{store: e.power.store, machines: e.power.provider, now: e.power.now}
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
	if err == nil || op.OriginatingAction != "user.disable" {
		return err
	}
	detail := "The provider has not confirmed the workspace stopped. Check that UTM is running and the VM is reachable."
	switch {
	case errors.Is(err, provider.ErrNotFound):
		detail = "UTM could not find the workspace VM. Check the provider inventory. ShiftBox is retaining its reservations."
	case errors.Is(err, provider.ErrOwnershipMismatch):
		detail = "The VM no longer matches the workspace's ownership record. Check the provider inventory before attempting a stop."
	case errors.Is(err, provider.ErrPermission):
		detail = "ShiftBox could not stop the workspace through UTM. Check the controller's UTM automation permissions."
	}
	return &operation.StepWaiting{Detail: detail, Cause: err}
}
