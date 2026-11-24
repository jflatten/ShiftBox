package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/ec2"
	"github.com/jflatten/ShiftBox/internal/release"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

const emptyStarterCatalog = `SELECT NOT EXISTS(SELECT 1 FROM platform_artifacts)
	AND NOT EXISTS(SELECT 1 FROM resource_profiles)`

// seedStarterProfiles uses the opt-in deployment catalog once. The durable
// catalog record keeps restarts from recreating edited or deleted profiles.
func (p *cloudEC2) seedStarterProfiles(ctx context.Context, db *sql.DB, active app.ActiveInstallation, caps provider.Capabilities,
	now time.Time, inspect func(context.Context) (ec2.Image, error)) error {
	if len(p.settings.StarterCatalog) == 0 {
		return nil
	}
	var empty bool
	if err := db.QueryRowContext(ctx, emptyStarterCatalog).Scan(&empty); err != nil || !empty {
		return err
	}
	artifact, err := p.readCandidate(string(p.settings.StarterCatalog), active.ActiveRelease, active.MinimumGuestRelease,
		app.PlatformImportOptions{Official: true})
	if err != nil {
		return err
	}
	pin := *p.settings.OfficialImage
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	observed, err := inspect(checkCtx)
	cancel()
	if err != nil {
		return err
	}
	if observed.ID != pin.AMIID || observed.Architecture != pin.Architecture {
		return release.ErrInvalidArtifact
	}
	image := artifact.EC2
	image.AccountID, image.RootDeviceName, image.RootSnapshotID = p.settings.AccountID, observed.RootDeviceName, observed.SnapshotID

	return database.Transaction(ctx, db, func(tx *sql.Tx) error {
		// Serialize initialization for this installation and recheck after AWS inspection.
		result, err := tx.ExecContext(ctx, "UPDATE installations SET version=version WHERE id=$1 AND state='active'", active.InstallationID)
		if err != nil {
			return err
		}
		if count, err := result.RowsAffected(); err != nil || count != 1 {
			return app.ErrActiveDatabase
		}
		if err := tx.QueryRowContext(ctx, emptyStarterCatalog).Scan(&empty); err != nil || !empty {
			return err
		}
		var actor string
		if err := tx.QueryRowContext(ctx, `SELECT a.activated_by FROM activated_configurations a
			JOIN users u ON u.id=a.activated_by
			WHERE a.installation_id=$1 AND a.generation=$2 AND a.retired_at IS NULL`,
			active.InstallationID, active.Generation).Scan(&actor); err != nil {
			return err
		}
		artifactID := uuid.NewString()
		if err := release.InsertOfficialStarterInTx(ctx, tx, artifactID, pin, artifact, actor, now); err != nil {
			return err
		}
		for _, size := range []struct {
			name          string
			diskGiB       int64
			instanceTypes []string
		}{
			{"Small", 32, []string{"t3.medium", "t3a.medium"}},
			{"Medium", 64, []string{"m6a.large", "m6i.large", "t3.large"}},
			{"Large", 96, []string{"m6a.xlarge", "m6i.xlarge", "t3.xlarge"}},
		} {
			var instance provider.InstanceType
			for _, name := range size.instanceTypes {
				if offered, ok := caps.LookupInstanceType(name); ok && offered.Architecture == pin.Architecture {
					instance = offered
					break
				}
			}
			if instance.Name == "" {
				return fmt.Errorf("starter profile %s: %w", size.name, provider.ErrCapabilityUnsupported)
			}
			profile := workspace.ResourceProfile{ID: uuid.NewString(), Name: size.name, Enabled: true, PlatformArtifactID: artifactID,
				Allocation: workspace.Allocation{CPU: instance.CPUCount, MemoryMiB: instance.MemoryMiB,
					DiskMiB: max(size.diskGiB, int64(pin.MinimumDiskGiB)) * 1024, InstanceType: instance.Name}}
			if err := workspace.CreateResourceProfileInTx(ctx, tx, profile, caps); err != nil {
				return err
			}
		}
		return nil
	})
}
