package workspace

import (
	"context"
	"time"

	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
)

// NewWorker reconciles the workspace actions implemented by the controller.
func NewWorker(journal *operation.Journal, launch *LaunchExecutor, power *PowerExecutor) *operation.Worker {
	return &operation.Worker{
		Journal: journal, Name: "workspace-controller", LeaseDuration: 4 * time.Minute,
		Executor: workspaceExecutor{launch: launch, power: power},
		Actions:  []string{"workspace.launch", "workspace.start", "workspace.stop"},
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
	case "workspace.start", "workspace.stop":
		if op.OriginatingAction == "workspace."+op.Type && e.power != nil {
			return e.power
		}
	}
	return nil
}

func (e workspaceExecutor) Observe(ctx context.Context, op operation.Operation, step operation.Step) (string, bool, error) {
	executor := e.executor(op)
	if executor == nil {
		return "", false, provider.ErrInvalidState
	}
	return executor.Observe(ctx, op, step)
}

func (e workspaceExecutor) Apply(ctx context.Context, op operation.Operation, step operation.Step) (string, error) {
	executor := e.executor(op)
	if executor == nil {
		return "", provider.ErrInvalidState
	}
	return executor.Apply(ctx, op, step)
}
