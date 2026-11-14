package backup

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jflatten/ShiftBox/internal/database"
)

// RestorePostgresSchemas loads a verified stage into the operator's dedicated,
// empty replacement database. Each owner loads only its own schema. The caller
// must retain exclusive use of the target and keep it out of controller
// configuration until both schemas and recovery review have been prepared.
func RestorePostgresSchemas(ctx context.Context, stage string, replacement database.PostgresSetupTarget, caPath string) error {
	if !privateCheckpointDirectory(stage) || (caPath != "" && !filepath.IsAbs(caPath)) {
		return ErrRestoreTarget
	}
	if err := ValidatePostgresReplacement(ctx, replacement); err != nil {
		return err
	}
	dex, err := postgresRestoreTarget(replacement.DexURI, caPath, "dex", "dex.dump")
	if err != nil {
		return err
	}
	app, err := postgresRestoreTarget(replacement.ApplicationURI, caPath, "shiftbox", "application.dump")
	if err != nil {
		return err
	}
	binary, err := exec.LookPath("pg_restore")
	if err != nil {
		return errors.New("PostgreSQL restore requires pg_restore")
	}
	conn, err := connectDumpOwner(ctx, app)
	if err != nil {
		return ErrRestoreTarget
	}
	defer conn.Close(context.Background())
	if err := checkPostgresToolVersion(ctx, binary, "pg_restore", conn); err != nil {
		return err
	}

	// Read both catalogs before either load. The destination schemas already
	// exist and belong to the replacement logins, so omit the schema entries.
	targets := []dumpTarget{dex, app}
	for _, target := range targets {
		if err := preparePostgresRestore(ctx, binary, stage, target); err != nil {
			return err
		}
		defer os.Remove(filepath.Join(stage, target.filename+".list"))
	}
	if err := ValidatePostgresReplacement(ctx, replacement); err != nil {
		return err
	}
	for _, target := range targets {
		path := filepath.Join(stage, target.filename)
		command := exec.CommandContext(ctx, binary, "--dbname="+target.uri.String(), "--no-password",
			"--single-transaction", "--schema="+target.schema, "--strict-names", "--no-owner", "--no-privileges",
			"--use-list="+path+".list", path)
		if err := runPostgresTool(command, stage, target.password); err != nil {
			return errors.New("could not load the scoped PostgreSQL dumps; keep the replacement offline and review the target before retrying")
		}
	}
	return nil
}

func postgresRestoreTarget(raw, caPath, schema, filename string) (dumpTarget, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return dumpTarget{}, ErrRestoreTarget
	}
	q := u.Query()
	if caPath != "" {
		q.Set("sslrootcert", caPath)
	}
	u.RawQuery = q.Encode()
	target, err := parseDumpTarget(u.String(), schema, filename)
	if err != nil {
		return dumpTarget{}, ErrRestoreTarget
	}
	return target, nil
}

func preparePostgresRestore(ctx context.Context, binary, stage string, target dumpTarget) error {
	path := filepath.Join(stage, target.filename)
	file, err := openCheckpoint(path)
	if err != nil {
		return errArchiveInvalid
	}
	file.Close()
	command := exec.CommandContext(ctx, binary, "--list", "--schema="+target.schema, "--strict-names", path)
	list, err := command.Output()
	if err != nil {
		return errArchiveInvalid
	}
	var include strings.Builder
	hasEntries := false
	for line := range strings.Lines(string(list)) {
		_, rest, found := strings.Cut(line, "; ")
		fields := strings.Fields(rest)
		if !found || len(fields) < 4 {
			continue
		}
		// pg_restore's TOC prefix contains the catalog OID, object OID,
		// and object kind. Keep the original line and the dependency order.
		if fields[2] == "SCHEMA" {
			continue
		}
		hasEntries = true
		include.WriteString(line)
	}
	if !hasEntries {
		return errArchiveInvalid
	}
	// Render the archive to discard before loading either schema. Listing
	// alone can succeed when a custom-format data block is truncated.
	verify := exec.CommandContext(ctx, binary, "--file=-", "--schema="+target.schema,
		"--strict-names", "--no-owner", "--no-privileges", path)
	if err := verify.Run(); err != nil {
		return errArchiveInvalid
	}
	listFile, err := os.OpenFile(path+".list", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ErrRestoreTarget
	}
	_, writeErr := listFile.WriteString(include.String())
	if err := errors.Join(writeErr, listFile.Close()); err != nil {
		_ = os.Remove(path + ".list")
		return ErrRestoreTarget
	}
	return nil
}
