package app

import (
	"runtime"
	"strings"
	"testing"
)

// TestBackupHostIDOnMac checks the actual registry format on supported hosts.
func TestBackupHostIDOnMac(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires the Mac controller host")
	}
	got, err := BackupHostID(t.Context())
	if err != nil || len(got) != 64 {
		t.Fatalf("BackupHostID returned %d characters, %v; want a host digest", len(got), err)
	}
}

// TestBackupHostIDFromIORegistry verifies the hardware binding uses the
// platform UUID and rejects invalid or ambiguous identity data.
func TestBackupHostIDFromIORegistry(t *testing.T) {
	const uuid = "AABBCCDD-EEFF-1122-3344-556677889900"
	line := "      \"IOPlatformUUID\" = \"" + uuid + "\""
	id, err := backupHostIDFromIORegistry([]byte(line))
	if err != nil || len(id) != 64 || strings.Contains(id, uuid) {
		t.Fatalf("platform UUID digest length = %d, %v; want a digest", len(id), err)
	}
	if folded, err := backupHostIDFromIORegistry([]byte(strings.Replace(line, uuid, strings.ToLower(uuid), 1))); err != nil || folded != id {
		t.Fatal("the same platform UUID used a different digest after case normalization")
	}
	for _, invalid := range []string{"", "    \"IOPlatformUUID\" = \"invalid\"", line + "\n" + line} {
		if _, err := backupHostIDFromIORegistry([]byte(invalid)); err == nil {
			t.Error("accepted invalid or ambiguous platform UUID")
		}
	}
}
