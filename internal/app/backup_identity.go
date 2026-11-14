package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/database"
)

// BackupMetadata identifies the live installation, current schema and Mac.
func (s *ControllerState) BackupMetadata(ctx context.Context) (backup.ArchiveMetadata, error) {
	active := s.Installation
	profile := active.Settings.DataProfile
	schema, err := database.Version(ctx, s.DB, profile)
	if err != nil {
		return backup.ArchiveMetadata{}, err
	}
	host, err := BackupHostID(ctx)
	if err != nil {
		return backup.ArchiveMetadata{}, err
	}
	return backup.ArchiveMetadata{InstallationID: active.InstallationID, Release: active.ActiveRelease,
		Profile: profile, Schema: schema, HostID: host}, nil
}

// BackupHostID returns a stable, opaque binding to the current Mac.
func BackupHostID(ctx context.Context) (string, error) {
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(probe, "/usr/sbin/ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return "", errors.New("cannot determine the Mac hardware identity for recovery")
	}
	return backupHostIDFromIORegistry(raw)
}

func backupHostIDFromIORegistry(output []byte) (string, error) {
	invalid := errors.New("cannot determine the Mac hardware identity for recovery")
	const prefix = `"IOPlatformUUID" = "`
	var platform uuid.UUID
	for line := range strings.Lines(string(output)) {
		value, found := strings.CutPrefix(strings.TrimSpace(line), prefix)
		if !found {
			continue
		}
		raw, quoted := strings.CutSuffix(value, `"`)
		id, err := uuid.Parse(raw)
		if !quoted || len(raw) != 36 || err != nil || id == uuid.Nil || platform != uuid.Nil {
			return "", invalid
		}
		platform = id
	}
	if platform == uuid.Nil {
		return "", invalid
	}
	digest := sha256.Sum256(platform[:])
	return hex.EncodeToString(digest[:]), nil
}
