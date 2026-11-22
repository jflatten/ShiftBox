package workspace

import (
	"context"
	"log/slog"
	"time"

	"github.com/jflatten/ShiftBox/internal/launchlog"
	"github.com/jflatten/ShiftBox/internal/provider"
)

// Provider effects remain authoritative when the optional diagnostic write fails.
func (p *ProvisionExecutor) observeLaunchTiming(ctx context.Context, operationID string) context.Context {
	return provider.WithLaunchTiming(ctx, func(phase string, started, finished time.Time, effectErr error) {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		if err := launchlog.RecordProvider(writeCtx, p.store.db, operationID, phase, started, finished, effectErr); err != nil {
			slog.WarnContext(ctx, "Could not record provider launch timing", "operation_id", operationID, "phase", phase)
		}
	})
}

func (p *ProvisionExecutor) markLaunchVMRunning(ctx context.Context, operationID string, inventory provider.Inventory) {
	if inventory.Power != provider.Running {
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if err := launchlog.MarkVMRunning(writeCtx, p.store.db, operationID, inventory.ObservedAt); err != nil {
		slog.WarnContext(ctx, "Could not record provider running milestone", "operation_id", operationID)
	}
}
