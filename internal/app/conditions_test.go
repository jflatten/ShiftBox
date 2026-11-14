package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/provider"
)

// TestOperationalConditionsPersistUntilProbeRecovers verifies that failed
// observations survive database reopen and clear only after a healthy probe.
func TestOperationalConditionsPersistUntilProbeRecovers(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "conditions.db")
	cfg := database.Config{Profile: "sqlite", SQLitePath: path}
	db, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	healthy := false
	now := time.Now()
	probes := operationalProbes{
		provider: func(context.Context) (provider.Capabilities, error) {
			if !healthy {
				return provider.Capabilities{}, errors.New("private provider diagnostics")
			}
			return provider.Capabilities{Create: true, Start: true, Stop: true, Delete: true, Architecture: "amd64"}, nil
		},
		key: func() bool { return healthy },
		space: func() (uint64, error) {
			if !healthy {
				return 16, nil
			}
			return 1024, nil
		},
		minimumDiskBytes: 512,
		startedAt:        now,
	}
	if err := RecordControllerFailure(ctx, db, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := sampleOperationalConditions(ctx, db, probes, now); err != nil {
		t.Fatal(err)
	}
	reopened, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var count int
	if err := reopened.QueryRowContext(ctx, `SELECT count(*) FROM operational_conditions WHERE condition_key IN
		('provider.unavailable','storage.low_space','encryption_key.unavailable','controller.unavailable') AND detail NOT LIKE '%private%'`).Scan(&count); err != nil || count != 4 {
		t.Fatalf("persisted operational conditions = %d (%v), want 4 with safe details", count, err)
	}
	healthy = true
	if err := sampleOperationalConditions(ctx, reopened, probes, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM operational_conditions").Scan(&count); err != nil || count != 0 {
		t.Fatalf("operational conditions after recovery = %d (%v), want zero", count, err)
	}
}
