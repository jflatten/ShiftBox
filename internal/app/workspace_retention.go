package app

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

const retentionScanInterval = time.Minute

type workspaceRetentionSchedule struct {
	maximum *int64
	grace   time.Duration
	now     func() time.Time
	probe   func(context.Context) (provider.Capabilities, error)
	process func(context.Context, time.Time, time.Time) error

	lastProbe    time.Time
	reconciledAt time.Time
}

func (s *workspaceRetentionSchedule) tick(ctx context.Context) error {
	if s.maximum == nil {
		return nil
	}
	now := s.now()
	if !s.lastProbe.IsZero() && (!now.After(s.lastProbe) || now.Sub(s.lastProbe) > 2*retentionScanInterval) {
		s.reconciledAt = time.Time{}
	}
	s.lastProbe = now
	capabilities, err := s.probe(ctx)
	if err != nil || !capabilities.Stop || !capabilities.Delete {
		s.reconciledAt = time.Time{}
		return ctx.Err()
	}
	if s.reconciledAt.IsZero() {
		s.reconciledAt = s.now()
	}
	if now.Before(s.reconciledAt.Add(s.grace)) {
		return nil
	}
	return s.process(ctx, now, s.reconciledAt)
}

func (s *workspaceRetentionSchedule) run(ctx context.Context) error {
	ticker := time.NewTicker(retentionScanInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		pass, cancel := context.WithTimeout(ctx, retentionScanInterval)
		err := s.tick(pass)
		cancel()
		if err != nil && ctx.Err() == nil {
			s.reconciledAt = time.Time{}
			slog.WarnContext(ctx, "Workspace retention check failed; automatic cleanup is paused through recovery grace")
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
	if maximum == nil {
		return nil
	}
	return &workspaceRetentionSchedule{
		maximum: maximum, grace: grace, now: now, probe: machines.Capabilities,
		process: func(ctx context.Context, stamp, reconciled time.Time) error {
			policy := workspace.RetentionEligibility{MaximumSeconds: maximum, ReconciledAt: reconciled, OutageGrace: grace}
			list, err := store.RetentionCandidates(ctx, state.Installation.InstallationID, stamp, policy, machines.Inspect)
			if err != nil {
				return err
			}
			for _, candidate := range list {
				err := registry.IfIDEDisconnected(candidate.Ref.WorkspaceID, func() error {
					_, err := store.CommitRetentionDeletion(ctx, journal, candidate, now(), policy)
					return err
				})
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
			}
			return nil
		},
	}
}
