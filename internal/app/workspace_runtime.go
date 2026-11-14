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
	listeners *GuestListeners
	worker    *operation.Worker
}

// ListenWorkspaceRuntime binds guest endpoints before constructing the worker
// so enrollment media advertises the actual listener addresses.
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
	worker := workspace.NewWorker(journal,
		workspace.NewLaunchExecutor(store, machines, enrollment, mediaRoot, endpoints.EnrollURL, endpoints.PollURL, now, probe),
		workspace.NewPowerExecutor(store, machines, now, probe))
	return &WorkspaceRuntime{listeners: endpoints, worker: worker}, nil
}

// Serve runs workspace reconciliation and guest communication together. A
// listener or worker exit cancels the other and waits for it to finish.
func (r *WorkspaceRuntime) Serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 2)
	go func() { done <- r.listeners.Serve(ctx) }()
	go func() { done <- r.worker.Run(ctx, time.Second) }()
	err := <-done
	cancel()
	<-done
	return err
}

// Close releases the listener binds, including before Serve.
func (r *WorkspaceRuntime) Close() { r.listeners.Close() }
