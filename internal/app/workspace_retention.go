package app

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

const retentionScanInterval = time.Minute

type workspaceRetentionSchedule struct {
	checkpoint *backup.WriteFence
	maximum    *int64
	grace      time.Duration
	now        func() time.Time
	probe      func(context.Context) (provider.Capabilities, error)
	process    func(context.Context, time.Time, time.Time) error
	publish    func(retentionSnapshot)

	lastProbe    time.Time
	reconciledAt time.Time
}

type retentionSnapshot struct {
	reconciledAt time.Time
	lastProbe    time.Time
}

func (s *ControllerState) workspaceRetentionStatus(now time.Time) web.WorkspaceRetentionStatus {
	if s.retentionMaximum == nil {
		return web.WorkspaceRetentionStatus{State: "disabled"}
	}
	snapshot := s.retentionStatus.Load()
	if snapshot == nil || snapshot.reconciledAt.IsZero() || now.Before(snapshot.lastProbe) ||
		now.Sub(snapshot.lastProbe) > 2*retentionScanInterval {
		return web.WorkspaceRetentionStatus{State: "paused"}
	}
	until := snapshot.reconciledAt.Add(s.retentionGrace)
	if now.Before(until) {
		return web.WorkspaceRetentionStatus{State: "grace", GraceUntil: &until}
	}
	return web.WorkspaceRetentionStatus{State: "ready"}
}

func (s *workspaceRetentionSchedule) tick(ctx context.Context) error {
	now := s.now()
	if !s.lastProbe.IsZero() && (!now.After(s.lastProbe) || now.Sub(s.lastProbe) > 2*retentionScanInterval) {
		s.reconciledAt = time.Time{}
		s.publishStatus()
	}
	s.lastProbe = now
	capabilities, err := s.probe(ctx)
	if err != nil || !capabilities.Stop || (s.maximum != nil && !capabilities.Delete) {
		s.reconciledAt = time.Time{}
		s.publishStatus()
		return ctx.Err()
	}
	if s.reconciledAt.IsZero() {
		s.reconciledAt = s.now()
	}
	s.publishStatus()
	if now.Before(s.reconciledAt.Add(s.grace)) {
		return nil
	}
	return s.process(ctx, now, s.reconciledAt)
}

func (s *workspaceRetentionSchedule) publishStatus() {
	if s.publish != nil {
		s.publish(retentionSnapshot{reconciledAt: s.reconciledAt, lastProbe: s.lastProbe})
	}
}

func (s *workspaceRetentionSchedule) run(ctx context.Context) error {
	ticker := time.NewTicker(retentionScanInterval)
	defer ticker.Stop()
	defer func() {
		s.reconciledAt = time.Time{}
		s.publishStatus()
	}()
	for ctx.Err() == nil {
		pass, cancel := context.WithTimeout(ctx, retentionScanInterval)
		err := withCheckpointActivity(pass, s.checkpoint, func() error { return s.tick(pass) })
		cancel()
		if err != nil && ctx.Err() == nil {
			s.reconciledAt = time.Time{}
			s.publishStatus()
			slog.WarnContext(ctx, "Workspace inactivity check failed; automatic shutdown and cleanup are paused through recovery grace")
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
	return ctx.Err()
}

func newWorkspaceRetention(state *ControllerState, store *workspace.Store, machines provider.Provider,
	journal *operation.Journal, registry *proxy.Registry, now func() time.Time,
) *workspaceRetentionSchedule {
	maximum, grace := state.retentionMaximum, state.retentionGrace
	return &workspaceRetentionSchedule{
		maximum: maximum, grace: grace, now: now, probe: machines.Capabilities,
		publish: func(snapshot retentionSnapshot) { state.retentionStatus.Store(&snapshot) },
		process: func(ctx context.Context, stamp, reconciled time.Time) error {
			inactive, err := store.InactivityCandidates(ctx, state.Installation.InstallationID, stamp)
			if err != nil {
				return err
			}
			for _, id := range inactive {
				_, err := store.CommitInactivityStop(ctx, journal, id, now())
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
			}
			if maximum == nil {
				return nil
			}
			policy := workspace.RetentionEligibility{MaximumSeconds: maximum, ReconciledAt: reconciled, OutageGrace: grace}
			list, err := store.RetentionCandidates(ctx, state.Installation.InstallationID, stamp, policy, machines.Inspect)
			if err != nil {
				return err
			}
			for _, candidate := range list {
				_, err := store.CommitRetentionDeletion(ctx, journal, candidate, now(), policy)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
			}
			return nil
		},
	}
}
