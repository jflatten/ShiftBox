package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

var migrationReleaseID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// ValidMigrationReleaseID reports whether an ID names one local release folder.
func ValidMigrationReleaseID(id string) bool { return migrationReleaseID.MatchString(id) }

// ReadLocalMigrationRelease reads a pinned release from a private local catalog.
func ReadLocalMigrationRelease(root, id string) (MigrationRelease, error) {
	unavailable := errors.New("local pinned release manifest is unavailable")
	if !filepath.IsAbs(root) || !ValidMigrationReleaseID(id) {
		return MigrationRelease{}, unavailable
	}
	folder := filepath.Join(root, id)
	for _, path := range []string{root, folder} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return MigrationRelease{}, unavailable
		}
	}
	file, err := openPrivateReleaseFile(filepath.Join(folder, "release.json"))
	if err != nil {
		return MigrationRelease{}, unavailable
	}
	defer file.Close()
	manifest, err := ReadMigrationRelease(file)
	if err != nil || manifest.ReleaseID != id {
		return MigrationRelease{}, unavailable
	}
	return manifest, nil
}

func openPrivateReleaseFile(path string) (*os.File, error) {
	unavailable := errors.New("private release file is unavailable")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, unavailable
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, unavailable
	}
	opened, err := file.Stat()
	if err != nil || opened.Mode().Perm()&0077 != 0 || !os.SameFile(info, opened) {
		file.Close()
		return nil, unavailable
	}
	return file, nil
}

// MigrationRelease identifies the pinned inputs for a proposed controller release.
type MigrationRelease struct {
	SchemaVersion   int               `json:"schemaVersion"`
	ReleaseID       string            `json:"releaseId"`
	ProtocolVersion string            `json:"protocolVersion"`
	Controller      ArtifactComponent `json:"controller"`
	Dex             ArtifactComponent `json:"dex"`
	UTM             ArtifactComponent `json:"utm"`
	PackerFork      ArtifactComponent `json:"packerFork"`
	Ubuntu          ArtifactComponent `json:"ubuntu"`
	Guest           ArtifactComponent `json:"guest"`
	IDE             ArtifactComponent `json:"ide"`
}

// ReadMigrationRelease validates bounded release metadata before checking the host.
func ReadMigrationRelease(input io.Reader) (MigrationRelease, error) {
	const limit = 64 << 10
	raw, err := io.ReadAll(io.LimitReader(input, limit+1))
	if err != nil || len(raw) > limit {
		return MigrationRelease{}, errors.New("cannot read release manifest")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var manifest MigrationRelease
	if err := decoder.Decode(&manifest); err != nil || decoder.Decode(new(any)) != io.EOF ||
		manifest.SchemaVersion != 1 || !hasMetadata(manifest.ReleaseID, manifest.ProtocolVersion) {
		return MigrationRelease{}, errors.New("invalid release manifest")
	}
	for _, component := range []ArtifactComponent{manifest.Controller, manifest.Dex, manifest.UTM, manifest.PackerFork, manifest.Ubuntu, manifest.Guest, manifest.IDE} {
		if !hasMetadata(component.Version) || !validDigest(component.SHA256) {
			return MigrationRelease{}, errors.New("release manifest requires pinned component versions and digests")
		}
	}
	return manifest, nil
}

// MigrationEvidence holds observations from trusted local checks.
type MigrationEvidence struct {
	VerifiedBackup, ProviderCompatible, ToolsCompatible bool
	StorageWritable, DatabaseOwned, KeyAccessible       bool
	BrowserValid                                        bool
	GuestCAUntil                                        time.Time
	Guests                                              []GuestUpgradeBlocker
}

// MigrationBlocker describes one prerequisite that still needs attention.
type MigrationBlocker struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// MigrationReport lists the observations that prevent an irreversible change.
type MigrationReport struct {
	ReleaseID string                `json:"releaseId"`
	Ready     bool                  `json:"ready"`
	Blockers  []MigrationBlocker    `json:"blockers"`
	Guests    []GuestUpgradeBlocker `json:"guestBlockers,omitempty"`
}

// AssessMigration evaluates evidence without changing the installation.
func AssessMigration(releaseID string, evidence MigrationEvidence, now time.Time) MigrationReport {
	report := MigrationReport{ReleaseID: releaseID, Blockers: []MigrationBlocker{}, Guests: evidence.Guests}
	for _, check := range []struct {
		ok     bool
		code   string
		detail string
	}{
		{evidence.VerifiedBackup, "migration.backup_required", "Complete and verify a management backup for the deployed release."},
		{evidence.DatabaseOwned, "migration.database_unverified", "Verify the active database schema and service ownership."},
		{len(evidence.Guests) == 0, "migration.guests_not_current", "Bring every retained workspace to the deployed guest release and resolve any unfinished maintenance."},
		{evidence.ProviderCompatible, "migration.provider_incompatible", "Verify the required UTM version and workspace lifecycle capabilities."},
		{evidence.ToolsCompatible, "migration.tools_incompatible", "Verify pinned Dex and the backup tools required by the data profile."},
		{evidence.StorageWritable, "migration.storage_unavailable", "Check protected persistent storage permissions and availability."},
		{evidence.KeyAccessible, "migration.key_unavailable", "Restore access to the retained installation encryption key."},
		{evidence.BrowserValid, "migration.browser_certificate_invalid", "Activate a valid certificate for all browser origins."},
		{!now.IsZero() && !evidence.GuestCAUntil.Before(now.Add(8*time.Hour)), "migration.guest_ca_expiring", "Review the installation CA before issuing the next eight-hour guest certificate."},
	} {
		if !check.ok {
			report.Blockers = append(report.Blockers, MigrationBlocker{Code: check.code, Detail: check.detail})
		}
	}
	report.Ready = len(report.Blockers) == 0
	return report
}
