package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/database"
)

// BackupMetadata identifies the live installation, current schema and recovery binding.
func (s *ControllerState) BackupMetadata(ctx context.Context) (backup.ArchiveMetadata, error) {
	active := s.Installation
	profile := active.Settings.DataProfile
	schema, err := database.Version(ctx, s.DB, profile)
	if err != nil {
		return backup.ArchiveMetadata{}, err
	}
	host, err := BackupHostIDForRuntime(ctx, s.runtime)
	if err != nil {
		return backup.ArchiveMetadata{}, err
	}
	return backup.ArchiveMetadata{InstallationID: active.InstallationID, Release: active.ActiveRelease,
		Profile: profile, Schema: schema, HostID: host}, nil
}

var backupVolumeID = regexp.MustCompile(`^vol-([a-f0-9]{8}|[a-f0-9]{17})$`)

// BackupHostIDForRuntime binds an AWS deployment to its retained data volume, which survives an ECS host replacement.
func BackupHostIDForRuntime(ctx context.Context, runtime *config.ControllerRuntime) (string, error) {
	if runtime == nil || runtime.Provider != "ec2" {
		return BackupHostID(ctx)
	}
	target := runtime.EC2
	if target == nil || target.AccountID == "" || target.Region == "" || !backupVolumeID.MatchString(target.DataVolumeID) {
		return "", errors.New("cannot determine the AWS data volume identity for recovery")
	}
	binding := strings.Join([]string{"shiftbox-aws-data-volume", target.AccountID, target.Region, target.DataVolumeID}, "\x00")
	digest := sha256.Sum256([]byte(binding))
	return hex.EncodeToString(digest[:]), nil
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
