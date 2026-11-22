package imagecache

import (
	"context"
	"database/sql"
)

const defaultSharedBudget = 14 << 30

// BuiltPolicy controls whether this installation can publish and reuse built
// images. Generation fences grants independently of public cache settings.
type BuiltPolicy struct {
	Enabled     bool
	Version     int64
	Generation  int64
	PurgeCutoff sql.NullInt64
}

type builtPolicyQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// ReadBuiltPolicy reads durable built-image policy from the active database or
// the caller's existing authority transaction.
func ReadBuiltPolicy(ctx context.Context, db builtPolicyQueryer) (BuiltPolicy, error) {
	var policy BuiltPolicy
	var enabled int
	err := db.QueryRowContext(ctx, `SELECT built_image_enabled,built_image_version,built_image_generation,built_image_purge_cutoff
		FROM installations WHERE singleton=1`).Scan(&enabled, &policy.Version, &policy.Generation, &policy.PurgeCutoff)
	if err != nil {
		return BuiltPolicy{}, err
	}
	policy.Enabled = enabled == 1
	return policy, nil
}

// SharedSettings enables the storage coordinator when either cache requires
// retention and a configured budget can support it.
func (p BuiltPolicy) SharedSettings(settings Settings) Settings {
	settings.Enabled = settings.Enabled || p.Enabled && settings.Budget() > 0
	return settings
}
