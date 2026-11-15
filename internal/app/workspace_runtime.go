package app

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/web"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// WorkspaceRuntime owns the guest listeners and durable workspace worker.
type WorkspaceRuntime struct {
	listeners   *GuestListeners
	worker      *operation.Worker
	retention   *workspaceRetentionSchedule
	maintenance []*operation.Worker
}

// ListenWorkspaceRuntime prepares guest endpoints before constructing the worker
// so enrollment media advertises the same addresses the listeners will use.
func ListenWorkspaceRuntime(state *ControllerState, listeners GuestListenerConfig, compatibility web.GuestCompatibility,
	journal *operation.Journal, machines provider.Provider, mediaRoot string, now func() time.Time,
) (*WorkspaceRuntime, error) {
	if state == nil || state.DB == nil || state.GuestCA == nil || journal == nil || machines == nil ||
		listeners.Tunnels == nil || !filepath.IsAbs(mediaRoot) || now == nil {
		return nil, errors.New("workspace runtime is incompletely configured")
	}
	settings := state.Installation.Settings
	hosts, err := config.NewServiceHosts(settings.ManagementOrigin, settings.IDEHostPattern, settings.PreviewHostPattern)
	if err != nil {
		return nil, err
	}
	enrollment := guest.NewEnrollment(state.DB, state.GuestCA)
	listeners.Secrets = state.Secrets
	endpoints, err := ListenGuests(listeners, enrollment, state.GuestCA, compatibility, now)
	if err != nil {
		return nil, err
	}
	probe := func(ctx context.Context, id string, generation int64) error {
		host, err := hosts.ServiceAuthority(id, "ide")
		if err != nil {
			return err
		}
		return listeners.Tunnels.ProbeIDE(ctx, id, generation, host)
	}
	store := workspace.NewStore(state.DB)
	power := workspace.NewPowerExecutor(store, machines, now, probe)
	power.SetDeletionAccessCloser(listeners.Tunnels.Registry.CloseWorkspaceAccess)
	power.SetRepairProbe(func(ctx context.Context, id string, generation int64) error {
		host, err := hosts.ServiceAuthority(id, "repair")
		if err != nil {
			return err
		}
		return listeners.Tunnels.ProbeRepair(ctx, id, generation, host)
	})
	launch := workspace.NewLaunchExecutor(store, machines, enrollment, mediaRoot, endpoints.EnrollURL, endpoints.PollURL, now, probe)
	worker := workspace.NewWorker(journal, launch, power)
	var maintenance []*operation.Worker
	if listeners.GuestUpdates != nil {
		campaign, targets := store.MaintenanceWorkers(journal, state.Installation.InstallationID, compatibility.Protocol, listeners.GuestUpdates, launch)
		maintenance = []*operation.Worker{campaign, targets}
	}
	retention := newWorkspaceRetention(state, store, machines, journal, listeners.Tunnels.Registry, now)
	if listeners.Checkpoint != nil {
		worker.EnterActivity = listeners.Checkpoint.Enter
		for _, worker := range maintenance {
			worker.EnterActivity = listeners.Checkpoint.Enter
		}
		if retention != nil {
			retention.checkpoint = listeners.Checkpoint
		}
	}
	return &WorkspaceRuntime{listeners: endpoints, worker: worker, retention: retention, maintenance: maintenance}, nil
}

// Serve runs workspace reconciliation, guest communication and configured
// retention together. A service exit cancels its peers and waits for them.
func (r *WorkspaceRuntime) Serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 3+len(r.maintenance))
	count := 2 + len(r.maintenance)
	go func() { done <- r.listeners.Serve(ctx) }()
	go func() { done <- r.worker.Run(ctx, time.Second) }()
	for _, worker := range r.maintenance {
		go func() { done <- worker.Run(ctx, time.Second) }()
	}
	if r.retention != nil {
		count++
		go func() { done <- r.retention.run(ctx) }()
	}
	err := <-done
	cancel()
	for range count - 1 {
		<-done
	}
	return err
}

// Close releases the listener binds, including before Serve.
func (r *WorkspaceRuntime) Close() { r.listeners.Close() }
