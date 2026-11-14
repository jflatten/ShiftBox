package release

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestMigrationPreflightPrerequisiteDetails verifies that each independent
// failure blocks migration and tells the operator what needs attention.
func TestMigrationPreflightPrerequisiteDetails(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	baseline := MigrationEvidence{
		VerifiedBackup: true, ProviderCompatible: true, ToolsCompatible: true,
		StorageWritable: true, DatabaseOwned: true, KeyAccessible: true,
		BrowserValid: true, GuestCAUntil: now.Add(8 * time.Hour),
	}
	for _, test := range []struct {
		code, guidance string
		alter          func(*MigrationEvidence)
	}{
		{"migration.backup_required", "verify a management backup", func(e *MigrationEvidence) { e.VerifiedBackup = false }},
		{"migration.database_unverified", "schema and service ownership", func(e *MigrationEvidence) { e.DatabaseOwned = false }},
		{"migration.guests_not_current", "every retained workspace", func(e *MigrationEvidence) {
			e.Guests = []GuestUpgradeBlocker{{WorkspaceID: "retained", ObservedState: "stopped", GuestRelease: "previous", ProtocolVersion: "1"}}
		}},
		{"migration.provider_incompatible", "required UTM version", func(e *MigrationEvidence) { e.ProviderCompatible = false }},
		{"migration.tools_incompatible", "pinned Dex and the backup tools", func(e *MigrationEvidence) { e.ToolsCompatible = false }},
		{"migration.storage_unavailable", "persistent storage permissions", func(e *MigrationEvidence) { e.StorageWritable = false }},
		{"migration.key_unavailable", "retained installation encryption key", func(e *MigrationEvidence) { e.KeyAccessible = false }},
		{"migration.browser_certificate_invalid", "all browser origins", func(e *MigrationEvidence) { e.BrowserValid = false }},
		{"migration.guest_ca_expiring", "eight-hour guest certificate", func(e *MigrationEvidence) {
			e.GuestCAUntil = now.Add(8*time.Hour - time.Second)
		}},
	} {
		t.Run(test.code, func(t *testing.T) {
			evidence := baseline
			test.alter(&evidence)
			report := AssessMigration("candidate", evidence, now)
			if report.ReleaseID != "candidate" || report.Ready || len(report.Blockers) != 1 ||
				report.Blockers[0].Code != test.code || !strings.Contains(report.Blockers[0].Detail, test.guidance) {
				t.Fatalf("AssessMigration(%s) = %+v, want one blocker advising %q", test.code, report, test.guidance)
			}
			if len(evidence.Guests) == 1 && (len(report.Guests) != 1 || report.Guests[0] != evidence.Guests[0]) {
				t.Errorf("retained guest report = %+v, want %+v", report.Guests, evidence.Guests)
			}
		})
	}
}

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
