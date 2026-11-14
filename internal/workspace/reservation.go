package workspace

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/provider"
)

// ReserveStart claims capacity for a pinned workspace in the same transaction
// that accepts its start. Admissions lock the workspace and then the budget
// before counting usage.
func ReserveStart(ctx context.Context, tx *sql.Tx, installation, workspace string, generation int64, now time.Time) error {
	if now.IsZero() {
		return provider.ErrInvalidState
	}
	if err := lockWorkspace(ctx, tx, installation, workspace, generation); err != nil {
		return err
	}
	// A no-op update locks the PostgreSQL budget row and obtains the SQLite
	// write lock. It also serializes admissions against budget changes.
	result, err := tx.ExecContext(ctx, `UPDATE capacity_budgets SET version=version WHERE installation_id=$1`, installation)
	if err := capacityWrite(result, err); err != nil {
		return err
	}
	var pinned Allocation
	var desired, compute, disk string
	err = tx.QueryRowContext(ctx, `SELECT w.cpu_count,w.memory_mib,w.disk_mib,w.desired_state,
		COALESCE(r.cpu_memory_state,''),COALESCE(r.disk_state,'')
		FROM workspaces w LEFT JOIN reservations r ON r.workspace_id=w.id WHERE w.id=$1`, workspace).
		Scan(&pinned.CPU, &pinned.MemoryMiB, &pinned.DiskMiB, &desired, &compute, &disk)
	if err != nil {
		return err
	}
	if desired != "running" || disk == "released" {
		return provider.ErrInvalidState
	}
	if compute == "reserved" {
		return nil
	}

	var limit, used Allocation
	err = tx.QueryRowContext(ctx, `SELECT cpu_limit,memory_mib_limit,disk_mib_limit
		FROM capacity_budgets WHERE installation_id=$1`, installation).
		Scan(&limit.CPU, &limit.MemoryMiB, &limit.DiskMiB)
	if err != nil {
		return err
	}
	err = tx.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN r.cpu_memory_state='reserved' THEN r.cpu_count ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN r.cpu_memory_state='reserved' THEN r.memory_mib ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN r.disk_state='reserved' THEN r.disk_mib ELSE 0 END),0)
		FROM reservations r JOIN workspaces w ON w.id=r.workspace_id WHERE w.installation_id=$1`, installation).
		Scan(&used.CPU, &used.MemoryMiB, &used.DiskMiB)
	if err != nil {
		return err
	}
	if err := CheckCapacity(limit, used, pinned, disk == "reserved"); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `INSERT INTO reservations
		(workspace_id,cpu_count,memory_mib,disk_mib,cpu_memory_state,disk_state,reserved_at)
		VALUES($1,$2,$3,$4,'reserved','reserved',$5)
		ON CONFLICT(workspace_id) DO UPDATE SET cpu_memory_state='reserved',reserved_at=excluded.reserved_at,
		cpu_memory_released_at=NULL WHERE reservations.cpu_memory_state='released' AND reservations.disk_state='reserved'`,
		workspace, pinned.CPU, pinned.MemoryMiB, pinned.DiskMiB, now.UTC().Format(time.RFC3339Nano))
	return capacityWrite(result, err)
}

// ReleaseReservation applies provider-confirmed stop or complete deletion for
// the exact workspace generation and instance. Callers supply an observation
// from the provider reconciliation, together with any tracked orphan resources.
func ReleaseReservation(ctx context.Context, tx *sql.Tx, observed provider.Inventory, now time.Time) error {
	ref := observed.Ref
	if now.IsZero() || observed.ObservedAt.IsZero() || observed.ObservedAt.After(now) ||
		ref.InstanceID == "" || observed.InstanceID != ref.InstanceID ||
		(observed.Power != provider.Stopped && observed.Power != provider.Deleted) {
		return provider.ErrInvalidState
	}
	if observed.Power == provider.Deleted {
		for _, resource := range observed.Resources {
			if !resource.Shared && !resource.Immutable {
				return provider.ErrPartial
			}
		}
	}
	if err := lockWorkspace(ctx, tx, ref.InstallationID, ref.WorkspaceID, ref.Generation); err != nil {
		return err
	}
	var instance, marker, desired string
	var reservedAt sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(w.provider_instance_id,''),w.ownership_marker,w.desired_state,r.reserved_at
		FROM workspaces w LEFT JOIN reservations r ON r.workspace_id=w.id WHERE w.id=$1`, ref.WorkspaceID).
		Scan(&instance, &marker, &desired, &reservedAt)
	if err != nil {
		return err
	}
	if instance != ref.InstanceID || marker != ref.OwnershipMarker {
		return provider.ErrOwnershipMismatch
	}
	if desired == "running" || (observed.Power == provider.Deleted && desired != "deleted") {
		return provider.ErrInvalidState
	}
	if !reservedAt.Valid {
		return nil
	}
	start, err := time.Parse(time.RFC3339Nano, reservedAt.String)
	if err != nil {
		return err
	}
	if observed.ObservedAt.Before(start) {
		return provider.ErrInvalidState
	}
	stamp := observed.ObservedAt.UTC().Format(time.RFC3339Nano)
	if observed.Power == provider.Deleted {
		_, err = tx.ExecContext(ctx, `UPDATE reservations SET cpu_memory_state='released',disk_state='released',
			cpu_memory_released_at=COALESCE(cpu_memory_released_at,$2),disk_released_at=COALESCE(disk_released_at,$2)
			WHERE workspace_id=$1`, ref.WorkspaceID, stamp)
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE reservations SET cpu_memory_state='released',cpu_memory_released_at=$2
		WHERE workspace_id=$1 AND cpu_memory_state='reserved'`, ref.WorkspaceID, stamp)
	return err
}

func lockWorkspace(ctx context.Context, tx *sql.Tx, installation, workspace string, generation int64) error {
	result, err := tx.ExecContext(ctx, `UPDATE workspaces SET version=version
		WHERE id=$1 AND installation_id=$2 AND generation=$3`, workspace, installation, generation)
	err = capacityWrite(result, err)
	if errors.Is(err, ErrCapacityConflict) {
		return provider.ErrOwnershipMismatch
	}
	return err
}
