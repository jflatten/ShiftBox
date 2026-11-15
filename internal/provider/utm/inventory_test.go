package utm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/provider"
)

// TestNormalizeInventory checks ownership, drive completeness, and size conversion.
func TestNormalizeInventory(t *testing.T) {
	ref := provider.WorkspaceRef{InstallationID: "installation", WorkspaceID: "workspace", Generation: 3, OwnershipMarker: "marker"}
	record := vmRecord{ID: "vm-id", Backend: "qemu", Status: "started", Architecture: "x86_64",
		Notes: "shiftbox:{\"installationId\":\"installation\",\"workspaceId\":\"workspace\",\"generation\":3,\"marker\":\"marker\",\"artifactId\":\"artifact\",\"release\":\"release\"}",
		CPU:   2, MemoryMiB: 1024, Drives: []driveRecord{{ID: "disk", GuestMiB: int64Ptr(4096)}, {ID: "enrollment", Removable: true}}}
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

func int64Ptr(value int64) *int64 { return &value }

// TestUnknownGuestDiskCapacity keeps host allocation separate from guest capacity.
func TestUnknownGuestDiskCapacity(t *testing.T) {
	vm := vmRecord{ID: "vm", Backend: "qemu", Architecture: "x86_64", Status: "stopped", CPU: 1, MemoryMiB: 512,
		Drives: []driveRecord{{ID: "disk", HostMiB: int64Ptr(12)}}}
	inventory, err := inventoryFromRecord(provider.WorkspaceRef{}, vm, ownership{ArtifactID: "artifact", Release: "release"})
	if err != nil || inventory.DiskCapacityKnown || inventory.Effective.DiskBytes != 0 ||
		len(inventory.Resources) != 2 || inventory.Resources[1].HostBytes == nil ||
		*inventory.Resources[1].HostBytes != 12<<20 {
		t.Fatalf("inventory = %+v, error = %v", inventory, err)
	}
}

// TestLiveUTMTaggedDrive creates and deletes a stopped, uniquely owned test VM.
// Mutation is opt-in independently of read-only inventory checks.
func TestLiveUTMTaggedDrive(t *testing.T) {
	version := os.Getenv("SHIFTBOX_TEST_UTM_MUTATIONS")
	if version == "" {
		t.Skip("set SHIFTBOX_TEST_UTM_MUTATIONS to the exact fixture version")
	}
	ref := provider.WorkspaceRef{InstallationID: uuid.NewString(), WorkspaceID: uuid.NewString(), Generation: 1, OwnershipMarker: uuid.NewString()}
	marker, err := json.Marshal(ownership{InstallationID: ref.InstallationID, WorkspaceID: ref.WorkspaceID, Generation: 1, Marker: ref.OwnershipMarker, ArtifactID: "fixture", Release: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	notes := ownershipPrefix + string(marker)
	const fixture = `
on run argv
    if item 2 of argv is "export" then set exportPath to POSIX file (item 5 of argv)
    tell application "/Applications/UTM.app"
        if UTM version is not item 1 of argv then error "unexpected UTM version"
        if item 2 of argv is "create" then
            set vm to make new virtual machine with properties {backend:qemu, configuration:{name:item 3 of argv, notes:item 4 of argv, architecture:"x86_64", cpu cores:2, memory:512, hypervisor:false, drives:{{guest size:64}}}}
            return id of vm
        end if
        if item 2 of argv is "source" then
            set vm to make new virtual machine with properties {backend:qemu, configuration:{name:item 3 of argv, notes:"ShiftBox disposable import source", architecture:"x86_64", cpu cores:2, memory:512, hypervisor:false, drives:{{guest size:64}, {removable:true}}}}
            return id of vm
        end if
        if item 2 of argv is "media" then
            set vm to make new virtual machine with properties {backend:qemu, configuration:{name:item 3 of argv, notes:item 4 of argv, architecture:"x86_64", cpu cores:2, memory:512, hypervisor:false, drives:{{guest size:64}, {removable:true}}}}
            return id of vm
        end if
        set vm to virtual machine id (item 3 of argv)
        if item 2 of argv is "export" then
            set vmConfig to configuration of vm
            if notes of vmConfig is not "ShiftBox disposable import source" or status of vm is not stopped then error "fixture does not match"
            export vm to exportPath
            return id of vm
        end if
        set vmConfig to configuration of vm
        if item 2 of argv is "delete source" then
            if notes of vmConfig is not "ShiftBox disposable import source" or status of vm is not stopped then error "fixture does not match"
            delete vm
            return
        end if
        if notes of vmConfig is not item 4 of argv or status of vm is not stopped then error "fixture does not match"
        delete vm
    end tell
end run
`
	run := func(action, value string, extra ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		args := append([]string{"-e", fixture, "--", version, action, value, notes}, extra...)
		out, err := exec.CommandContext(ctx, "/usr/bin/osascript", args...).Output()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Logf("fixture %s: %s", action, exit.Stderr)
		}
		return strings.TrimSpace(string(out)), err
	}
	action := "create"
	powerTest := os.Getenv("SHIFTBOX_TEST_UTM_POWER") == version
	mediaTest := os.Getenv("SHIFTBOX_TEST_UTM_MEDIA") == version
	expectedResources := 2
	if mediaTest {
		action = "media"
		expectedResources++
	}
	fixtureName := "ShiftBox-Inventory-" + ref.WorkspaceID
	ref.InstanceID, err = run(action, fixtureName)
	if err != nil || ref.InstanceID == "" {
		t.Fatalf("could not create fixture: %v", err)
	}
	controller := NewPowerController(version)
	t.Cleanup(func() {
		if _, err := (Inspector{Version: version}).Inspect(context.Background(), ref); errors.Is(err, provider.ErrNotFound) {
			return
		}
		if powerTest {
			_, _ = controller.Stop(context.Background(), provider.StopRequest{Ref: ref, OperationID: "fixture-cleanup", ForceRetentionDelete: true})
		}
		if _, err := run("delete", ref.InstanceID); err != nil {
			t.Errorf("could not delete disposable fixture %s: %v", ref.InstanceID, err)
			return
		}
		if _, err := (Inspector{Version: version}).Inspect(context.Background(), ref); !errors.Is(err, provider.ErrNotFound) {
			t.Errorf("disposable VM remained registered after cleanup: %v", err)
		}
	})
	inventory, err := (Inspector{Version: version}).Inspect(context.Background(), ref)
	if err != nil || inventory.Power != provider.Stopped || len(inventory.Resources) != expectedResources ||
		inventory.Resources[1].Type != "disk" || inventory.Resources[1].HostBytes == nil {
		t.Fatalf("native tagged inventory = %+v, error = %v", inventory, err)
	}
	if mediaTest {
		checkLiveMediaReplacement(t, controller, inventory)
	}
	if powerTest {
		checkLivePower(t, controller, ref)
	}
	if root := os.Getenv("SHIFTBOX_TEST_UTM_STORAGE"); root != "" {
		if os.Getenv("SHIFTBOX_TEST_UTM_IMPORT") == version {
			checkLiveImport(t, controller, root, run)
		}
		checkLiveDeletion(t, controller, ref, filepath.Join(root, fixtureName+".utm"))
	}
}

// checkLivePower starts and stops the blank fixture using real Apple events.
func checkLivePower(t *testing.T, controller *PowerController, ref provider.WorkspaceRef) {
	t.Helper()
	// Blank fixture exercises hypervisor power only. Force is confined to this
	// disposable fixture, which has no OS to acknowledge a shutdown request.
	start := provider.StartRequest{Ref: ref, OperationID: "fixture-start", NetworkMode: "shared"}
	for range 2 {
		running, err := controller.Start(context.Background(), start)
		if err != nil || running.Power != provider.Running {
			t.Fatalf("native start: %s, %v", running.Power, err)
		}
	}
	stopped, err := controller.Stop(context.Background(), provider.StopRequest{Ref: ref, OperationID: "fixture-stop", ForceRetentionDelete: true})
	if err != nil || stopped.Power != provider.Stopped {
		t.Fatalf("native stop: %s, %v", stopped.Power, err)
	}
}

// checkLiveDeletion verifies bundle removal and idempotent repeat deletion
// through the adapter on a disposable fixture with known storage.
func checkLiveDeletion(t *testing.T, controller *PowerController, ref provider.WorkspaceRef, bundlePath string) {
	t.Helper()
	if info, err := os.Lstat(bundlePath); err != nil || !info.IsDir() {
		t.Fatalf("locate disposable fixture bundle: %v", err)
	}
	req := provider.DeleteRequest{Ref: ref, OperationID: "fixture-delete"}
	for range 2 {
		after, err := controller.DeleteManagedVM(context.Background(), req, bundlePath)
		if err != nil || after.Power != provider.Deleted || len(after.Resources) != 0 {
			t.Fatalf("DeleteManagedVM(%s) = %s, %v, want confirmed deletion", ref.InstanceID, after.Power, err)
		}
	}
}

// checkLiveMediaReplacement observes two distinguishable media sizes through
// the native scripting API and verifies the persistent disk survives both updates.
func checkLiveMediaReplacement(t *testing.T, controller *PowerController, before provider.Inventory) {
	t.Helper()
	target := before.Resources[2]
	if target.Type != "media" {
		t.Fatalf("expected removable recovery drive, got %s", target.Type)
	}
	req := provider.EnrollmentRequest{Ref: before.Ref, OperationID: "fixture-recovery", Eligible: true}
	for _, size := range []int{2, 5} {
		payload := t.TempDir()
		if err := os.WriteFile(filepath.Join(payload, "fixture.txt"), make([]byte, size<<20), 0600); err != nil {
			t.Fatal(err)
		}
		req.MediaPath = filepath.Join(t.TempDir(), "recovery.iso")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		out, err := exec.CommandContext(ctx, "/usr/bin/hdiutil", "makehybrid", "-iso", "-o", req.MediaPath, payload).CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("make recovery image: %v, %s", err, out)
		}
		info, err := os.Stat(req.MediaPath)
		if err != nil {
			t.Fatal(err)
		}
		after, err := controller.ReplaceRecoveryMedia(context.Background(), req, target.ID)
		if err != nil {
			t.Fatalf("replace recovery media: %v", err)
		}
		got := after.Resources[2].HostBytes
		want := info.Size() / (1 << 20) * (1 << 20)
		if got == nil || *got != want {
			t.Fatalf("expected recovery image with %d host bytes, got %v", want, after.Resources[2])
		}
	}
}

// TestNativeARM64Inventory checks that a hardware-virtualized ARM workspace remains manageable.
func TestNativeARM64Inventory(t *testing.T) {
	vm := vmRecord{ID: "arm-vm", Backend: "qemu", Architecture: "aarch64", Status: "stopped",
		CPU: 2, MemoryMiB: 4096, Drives: []driveRecord{}}
	owner := ownership{ArtifactID: "arm-image", Release: "arm-release"}
	got, err := inventoryFromRecord(provider.WorkspaceRef{}, vm, owner)
	if err != nil || got.InstanceID != vm.ID {
		t.Fatalf("inventoryFromRecord(aarch64) = %q, %v, want %q and no error", got.InstanceID, err, vm.ID)
	}
}
