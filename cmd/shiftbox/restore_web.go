package main

import (
	"context"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"

	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/web"
)

func validateWebRestore(ctx context.Context, cfg config.Config, metadata backup.ArchiveMetadata, job web.RestoreValidationJob) (string, error) {
	destination := job.Destination
	if destination == "" {
		destination = filepath.Join(cfg.InstallationDir, "replacement-"+job.OperationID)
	}
	if !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return "", backup.ErrRestoreTarget
	}
	checkLocal := func() error { return checkRestoreDirectory(destination) }
	var target restoreReplacement
	switch {
	case cfg.DataProfile == "sqlite" && job.Postgres == nil:
		var err error
		target, err = restoreTarget(cfg, destination, "")
		if err != nil {
			return "", err
		}
	case cfg.DataProfile == "postgres" && job.Postgres != nil:
		roots, err := x509.SystemCertPool()
		if err != nil {
			return "", backup.ErrRestoreTarget
		}
		target, err = postgresRestoreReplacement(destination, job.Postgres.ConnectionURI,
			&app.PostgresProfileSetup{DexURI: job.Postgres.DexConnectionURI, Roots: roots}, checkLocal)
		if err != nil {
			return "", err
		}
	default:
		return "", backup.ErrRestoreTarget
	}

	key, err := cryptostore.LoadKey(filepath.Join(cfg.InstallationDir, "installation.key"))
	if err != nil {
		return "", backup.ErrRestoreKey
	}
	defer clear(key)
	archive, err := openRestoreInput(job.ArchivePath)
	if err != nil {
		return "", err
	}
	defer archive.Close()
	expected := backup.RestoreRequirements{Metadata: metadata, MountedKey: key}
	validation, err := backup.ValidateRestore(ctx, archive, job.Password, expected, target.check)
	if err != nil {
		return "", err
	}
	plans := filepath.Join(cfg.InstallationDir, "restore-validations")
	if err := os.Mkdir(plans, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	plan, err := backup.RecordRestorePlan(ctx, plans, target.digest, validation)
	return plan.ID, err
}
