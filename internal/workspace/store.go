package workspace

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/provider"
)

// ErrCapacityConflict indicates a duplicate profile or an outdated budget revision.
var ErrCapacityConflict = errors.New("workspace capacity configuration conflict")

// ResourceProfile describes an allocation that can be selected for new workspaces.
type ResourceProfile struct {
	ID, Name string
	Allocation
	Enabled bool
	Version int64
}

// Store persists workspace state in the installation database.
type Store struct {
	db *sql.DB
}

// NewStore binds workspace persistence to the selected database.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// CreateResourceProfile persists an allocation after validating provider bounds.
func (s *Store) CreateResourceProfile(ctx context.Context, profile ResourceProfile, caps provider.Capabilities) error {
	return database.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		return CreateResourceProfileInTx(ctx, tx, profile, caps)
	})
}

// CreateResourceProfileInTx validates and inserts a profile with its management authority.
func CreateResourceProfileInTx(ctx context.Context, tx *sql.Tx, profile ResourceProfile, caps provider.Capabilities) error {
	if profile.ID == "" || strings.TrimSpace(profile.Name) == "" {
		return errors.New("resource profile requires an ID and name")
	}
	if _, err := ProfileResources(profile.Allocation, caps); err != nil {
		return err
	}
	enabled := 0
	if profile.Enabled {
		enabled = 1
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO resource_profiles(id,name,cpu_count,memory_mib,disk_mib,enabled,version)
		VALUES($1,$2,$3,$4,$5,$6,1) ON CONFLICT DO NOTHING`, profile.ID, profile.Name,
		profile.CPU, profile.MemoryMiB, profile.DiskMiB, enabled)
	return capacityWrite(result, err)
}

// ResourceProfile reads an allocation and its current selection status.
func (s *Store) ResourceProfile(ctx context.Context, id string) (ResourceProfile, error) {
	var profile ResourceProfile
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT id,name,cpu_count,memory_mib,disk_mib,enabled,version
		FROM resource_profiles WHERE id=$1`, id).Scan(&profile.ID, &profile.Name, &profile.CPU,
		&profile.MemoryMiB, &profile.DiskMiB, &enabled, &profile.Version)
	profile.Enabled = enabled == 1
	return profile, err
}

// SetCapacityBudget creates the first budget at version zero or updates a
// specific existing revision. Budget changes affect future admissions.
func (s *Store) SetCapacityBudget(ctx context.Context, installation string, limit Allocation, expected int64, actor string, now time.Time) error {
	return database.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		return SetCapacityBudgetInTx(ctx, tx, installation, limit, expected, actor, now)
	})
}

// SetCapacityBudgetInTx applies a versioned budget in the caller's management transaction.
func SetCapacityBudgetInTx(ctx context.Context, tx *sql.Tx, installation string, limit Allocation, expected int64, actor string, now time.Time) error {
	if installation == "" || !limit.positive() || expected < 0 || actor == "" || now.IsZero() {
		return errors.New("invalid capacity budget")
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	if expected == 0 {
		result, err := tx.ExecContext(ctx, `INSERT INTO capacity_budgets
			(installation_id,cpu_limit,memory_mib_limit,disk_mib_limit,updated_by,updated_at,version)
			VALUES($1,$2,$3,$4,$5,$6,1) ON CONFLICT DO NOTHING`, installation,
			limit.CPU, limit.MemoryMiB, limit.DiskMiB, actor, stamp)
		return capacityWrite(result, err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE capacity_budgets SET cpu_limit=$2,memory_mib_limit=$3,
		disk_mib_limit=$4,updated_by=$5,updated_at=$6,version=version+1
		WHERE installation_id=$1 AND version=$7`, installation, limit.CPU, limit.MemoryMiB,
		limit.DiskMiB, actor, stamp, expected)
	return capacityWrite(result, err)
}

// CapacityBudget reads the current configured limit and revision.
func (s *Store) CapacityBudget(ctx context.Context, installation string) (Allocation, int64, error) {
	var limit Allocation
	var version int64
	err := s.db.QueryRowContext(ctx, `SELECT cpu_limit,memory_mib_limit,disk_mib_limit,version
		FROM capacity_budgets WHERE installation_id=$1`, installation).
		Scan(&limit.CPU, &limit.MemoryMiB, &limit.DiskMiB, &version)
	return limit, version, err
}

func capacityWrite(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrCapacityConflict
	}
	return nil
}
