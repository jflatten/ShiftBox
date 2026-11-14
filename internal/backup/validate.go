package backup

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/jflatten/ShiftBox/internal/database"
)

var (
	// ErrRestoreCompatibility indicates that the archive does not match the
	// expected host, installation, release, or database schema.
	ErrRestoreCompatibility = errors.New("archive is incompatible with this host or installation")
	// ErrRestoreKey indicates that a required key is invalid or mismatched.
	ErrRestoreKey = errors.New("archive and installation key requirements do not match")
	// ErrRestoreTarget indicates that the replacement target could not be verified.
	ErrRestoreTarget = errors.New("replace only an empty, owned restore target")
)

// RestoreRequirements identifies the source installation and any retained key
// or recorded archive digest that restore must honor.
type RestoreRequirements struct {
	Metadata      ArchiveMetadata
	MountedKey    []byte
	ArchiveDigest string
}

// RestoreValidation contains the verified evidence and planned key disposition.
// The result describes validation only. Activation must recheck the target.
type RestoreValidation struct {
	Evidence       ArchiveEvidence
	KeyDisposition string
}

// ValidateRestore verifies the full archive before inspecting the replacement
// target. The target check must only inspect state, never prepare or mutate it.
func ValidateRestore(ctx context.Context, archive io.Reader, password string, expected RestoreRequirements,
	checkTarget func(context.Context) error) (RestoreValidation, error) {
	evidence, doc, err := verifyArchive(ctx, archive, password)
	if err != nil {
		return RestoreValidation{}, err
	}
	if expected.ArchiveDigest != "" && subtle.ConstantTimeCompare([]byte(expected.ArchiveDigest), []byte(evidence.ArchiveDigest)) != 1 {
		return RestoreValidation{}, errArchiveInvalid
	}
	if expected.Metadata.HostID == "" || evidence.Metadata != expected.Metadata {
		return RestoreValidation{}, ErrRestoreCompatibility
	}
	keyDisposition := "restore_archived_key"
	for _, entry := range doc.Files {
		if entry.Name != "installation.key" {
			continue
		}
		if entry.Size != 32 {
			return RestoreValidation{}, ErrRestoreKey
		}
		if expected.MountedKey != nil {
			if len(expected.MountedKey) != 32 || subtle.ConstantTimeCompare([]byte(entry.Digest), []byte(digestBytes(expected.MountedKey))) != 1 {
				return RestoreValidation{}, ErrRestoreKey
			}
			keyDisposition = "retain_matching_key"
		}
	}
	if err := ctx.Err(); err != nil {
		return RestoreValidation{}, err
	}
	if checkTarget == nil || checkTarget(ctx) != nil {
		return RestoreValidation{}, ErrRestoreTarget
	}
	return RestoreValidation{Evidence: evidence, KeyDisposition: keyDisposition}, nil
}

// ValidateSQLiteReplacement checks the private destination and absent target
// stores without creating or opening either database.
func ValidateSQLiteReplacement(directory, applicationPath, dexPath string) error {
	if !privateCheckpointDirectory(directory) || applicationPath == dexPath {
		return ErrRestoreTarget
	}
	for _, path := range []string{applicationPath, dexPath} {
		if filepath.Clean(path) != path || filepath.Dir(path) != directory {
			return ErrRestoreTarget
		}
		// Avoid attaching SQLite sidecars from an earlier installation.
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			if _, err := os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
				return ErrRestoreTarget
			}
		}
	}
	return nil
}

// ValidatePostgresReplacement checks verified TLS, scoped ownership, and empty
// application and Dex schemas using the setup role preflight.
func ValidatePostgresReplacement(ctx context.Context, target database.PostgresSetupTarget) error {
	if err := database.PreflightPostgresSetup(ctx, target); err != nil {
		return ErrRestoreTarget
	}
	return nil
}
