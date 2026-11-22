package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
)

func activateRestore(ctx context.Context, stage, destination string, metadata backup.ArchiveMetadata,
	validationID string, target *postgresReplacement) (string, error) {
	now := time.Now().UTC()
	const actor = "local-operator"
	if target == nil {
		return backup.ActivateSQLiteRestore(ctx, stage, destination, metadata.InstallationID, validationID, actor, now)
	}
	key, err := cryptostore.LoadKey(filepath.Join(stage, "installation.key"))
	if err != nil {
		return "", err
	}
	defer clear(key)
	cipher, err := cryptostore.New(key)
	if err != nil {
		return "", err
	}
	if err := backup.RestorePostgresSchemas(ctx, stage, target.database, target.caPath); err != nil {
		return "", err
	}
	db, err := database.OpenPostgresSetupApplication(ctx, target.database)
	if err != nil {
		return "", err
	}
	defer db.Close()
	if err := database.CheckSchema(ctx, db, "postgres"); err != nil {
		return "", err
	}
	journal := operation.NewJournal(db, time.Now, uuid.NewString)
	review, err := backup.EnterRecoveryWithConfiguration(ctx, db, journal, metadata.InstallationID,
		validationID, actor, now, func(ctx context.Context, tx *sql.Tx) error {
			return app.ReplaceRecoveredDexStorage(ctx, tx, metadata.InstallationID, app.PostgresSetupCredentials{
				ApplicationURI: target.database.ApplicationURI, DexURI: target.database.DexURI, CAFile: target.caPath,
			}, cipher)
		})
	if err != nil {
		return "", err
	}
	for _, name := range []string{"application.dump", "dex.dump"} {
		if err := os.Remove(filepath.Join(stage, name)); err != nil {
			return "", err
		}
	}
	if err := backup.PublishRecoveryDirectory(ctx, stage, destination); err != nil {
		return "", err
	}
	return review, nil
}
