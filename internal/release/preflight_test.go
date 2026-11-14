package release

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestMigrationPreflightBlocksUnverifiedPrerequisites keeps a current guest
// from masking missing backup, provider, tool, storage, or trust evidence.
func TestMigrationPreflightBlocksUnverifiedPrerequisites(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	evidence := MigrationEvidence{
		VerifiedBackup: true, ProviderCompatible: true, ToolsCompatible: true,
		StorageWritable: true, DatabaseOwned: true, KeyAccessible: true,
		BrowserValid: true, GuestCAUntil: now.Add(24 * time.Hour),
	}
	if report := AssessMigration("candidate", evidence, now); !report.Ready || len(report.Blockers) != 0 {
		t.Fatalf("preflight with verified evidence = %+v; want ready", report)
	}
	evidence.VerifiedBackup = false
	evidence.GuestCAUntil = now.Add(time.Hour)
	evidence.Guests = []GuestUpgradeBlocker{{WorkspaceID: "stopped", ObservedState: "stopped"}}
	report := AssessMigration("candidate", evidence, now)
	if report.Ready || len(report.Blockers) != 3 ||
		report.Blockers[0].Code != "migration.backup_required" ||
		report.Blockers[1].Code != "migration.guests_not_current" ||
		report.Blockers[2].Code != "migration.guest_ca_expiring" {
		t.Fatalf("preflight with incomplete evidence = %+v; want backup, retained guest and CA blockers", report)
	}
}

// TestReadMigrationRelease ensures a manifest contains pinned component
// versions and digests and rejects unknown fields and trailing input.
func TestReadMigrationRelease(t *testing.T) {
	component := `{"version":"pinned","sha256":"` + strings.Repeat("a", 64) + `"}`
	doc := []byte(`{"schemaVersion":1,"releaseId":"candidate","protocolVersion":"1","controller":` + component +
		`,"dex":` + component + `,"utm":` + component + `,"packerFork":` + component + `,"ubuntu":` + component +
		`,"guest":` + component + `,"ide":` + component + `}`)
	m, err := ReadMigrationRelease(bytes.NewReader(doc))
	if err != nil || m.ReleaseID != "candidate" {
		t.Fatalf("manifest = %+v, %v; want candidate", m, err)
	}
	for _, invalid := range [][]byte{
		bytes.Replace(doc, []byte(strings.Repeat("a", 64)), []byte("unpinned"), 1),
		bytes.Replace(doc, []byte(`"guest":`), []byte(`"unknown":`), 1),
		append(bytes.Clone(doc), []byte("{}")...),
	} {
		if _, err := ReadMigrationRelease(bytes.NewReader(invalid)); err == nil {
			t.Error("preflight accepted invalid release metadata")
		}
	}
}
