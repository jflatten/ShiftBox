package utm

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/provider"
)

// TestNormalizeInventory checks ownership, drive completeness, and size conversion.
func TestNormalizeInventory(t *testing.T) {
	ref := provider.WorkspaceRef{InstallationID: "installation", WorkspaceID: "workspace", Generation: 3, OwnershipMarker: "marker"}
	record := vmRecord{ID: "vm-id", Backend: "qemu", Status: "started", Architecture: "x86_64",
		Notes: "shiftbox:{\"installationId\":\"installation\",\"workspaceId\":\"workspace\",\"generation\":3,\"marker\":\"marker\",\"artifactId\":\"artifact\",\"release\":\"release\"}",
		CPU:   2, MemoryMiB: 1024, Drives: []driveRecord{{ID: "disk", GuestMiB: 4096}, {ID: "enrollment", Removable: true}}}
	got, err := normalizeInventory(ref, []vmRecord{{ID: "unrelated"}, record})
	if err != nil || got.Power != provider.Running || len(got.Resources) != 3 ||
		got.Effective.DiskBytes != 4096<<20 || got.Effective.MemoryBytes != 1024<<20 ||
		got.Resources[2].Type != "media" || got.Ref.InstanceID != "vm-id" {
		t.Fatalf("inventory = %+v, error = %v", got, err)
	}
	ref.Generation++
	if _, err := normalizeInventory(ref, []vmRecord{record}); !errors.Is(err, provider.ErrOwnershipMismatch) {
		t.Fatalf("stale generation: %v", err)
	}
	ref.Generation--
	if _, err := normalizeInventory(ref, []vmRecord{record, record}); !errors.Is(err, provider.ErrOwnershipMismatch) {
		t.Fatalf("ambiguous ownership: %v", err)
	}
	record.Drives[1].ID = "disk"
	if _, err := normalizeInventory(ref, []vmRecord{record}); !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Fatalf("ambiguous drives: %v", err)
	}
}

// TestKnownInstanceMismatch rejects a VM found by ID with missing ownership evidence.
func TestKnownInstanceMismatch(t *testing.T) {
	ref := provider.WorkspaceRef{InstanceID: "known"}
	if _, err := normalizeInventory(ref, []vmRecord{{ID: "known"}}); !errors.Is(err, provider.ErrOwnershipMismatch) {
		t.Fatalf("untagged known instance: %v", err)
	}
}

// TestLiveUTMInventory checks that the exact local build can enumerate VMs and
// prove absence for a synthetic installation without touching registered VMs.
func TestLiveUTMInventory(t *testing.T) {
	version := os.Getenv("SHIFTBOX_TEST_UTM_VERSION")
	if version == "" {
		t.Skip("set SHIFTBOX_TEST_UTM_VERSION for native UTM inspection")
	}
	ref := provider.WorkspaceRef{InstallationID: uuid.NewString(), WorkspaceID: uuid.NewString(), Generation: 1, OwnershipMarker: uuid.NewString()}
	_, err := (Inspector{Version: version}).Inspect(context.Background(), ref)
	if !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("native inventory did not confirm absence: %v", err)
	}
}
