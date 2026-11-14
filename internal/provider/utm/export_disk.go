package utm

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/provider"
)

// InspectStoppedDisk measures the attached persistent disk through a temporary
// UTM export when the native inventory lacks virtual capacity. The caller
// supplies a controller-owned absolute staging directory.
func (p *PowerController) InspectStoppedDisk(ctx context.Context, ref provider.WorkspaceRef, stagingRoot string) (provider.Inventory, error) {
	if ref.InstanceID == "" || !filepath.IsAbs(stagingRoot) {
		return provider.Inventory{}, provider.ErrInvalidState
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	before, err := p.inspect(ctx, ref)
	if err != nil {
		return before, err
	}
	if before.Power != provider.Stopped {
		return before, provider.ErrInvalidState
	}
	if before.DiskCapacityKnown {
		return before, nil
	}
	pending, err := os.MkdirTemp(stagingRoot, "disk-inspect-")
	if err != nil {
		return before, provider.ErrOutcomeUnknown
	}
	defer os.RemoveAll(pending)
	destination := filepath.Join(pending, "inspection.utm")
	if err := exportStoppedVM(ctx, p.version, ref, destination); err != nil {
		return before, err
	}
	after, err := p.inspect(ctx, ref)
	if err != nil {
		return after, err
	}
	before.ObservedAt = after.ObservedAt
	if !reflect.DeepEqual(before, after) {
		return after, provider.ErrOutcomeUnknown
	}
	return observeExportedDisk(ctx, after, destination)
}

const exportDiskScript = `
use framework "Foundation"

on run argv
    set vmID to item 2 of argv
    set exportPath to POSIX file (item 4 of argv)
    tell application "/Applications/UTM.app"
        if UTM version is not item 1 of argv then return "version"
        set vm to virtual machine id vmID
        if not (exists vm) then return "ownership"
        if backend of vm is not qemu then return "ownership"
        set cfg to configuration of vm
        if not my matchesOwner(notes of cfg, argv) then return "ownership"
        if status of vm is not stopped then return "state"
        export vm to exportPath
    end tell
    return "issued"
end run
` + matchOwnerScript

func exportStoppedVM(ctx context.Context, version string, ref provider.WorkspaceRef, destination string) error {
	if version == "" {
		return provider.ErrCapabilityUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", exportDiskScript, "--",
		version, ref.InstanceID, "", destination, ref.InstallationID, ref.WorkspaceID,
		strconv.FormatInt(ref.Generation, 10), ref.OwnershipMarker)
	return effectResult(cmd.Output())
}

func observeExportedDisk(ctx context.Context, inventory provider.Inventory, directory string) (provider.Inventory, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return inventory, provider.ErrOutcomeUnknown
	}
	defer root.Close()
	info, err := root.Lstat("config.plist")
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return inventory, provider.ErrOutcomeUnknown
	}
	config, err := exec.CommandContext(ctx, "/usr/bin/plutil", "-convert", "json", "-o", "-", filepath.Join(directory, "config.plist")).Output()
	if err != nil {
		return inventory, provider.ErrOutcomeUnknown
	}
	return exportedDiskInventory(inventory, root, config)
}

func exportedDiskInventory(inventory provider.Inventory, root *os.Root, config []byte) (provider.Inventory, error) {
	var exported struct {
		Information struct{ UUID, Notes string }
		Drive       []struct{ Identifier, ImageName, ImageType string }
	}
	if json.Unmarshal(config, &exported) != nil || !strings.EqualFold(exported.Information.UUID, inventory.InstanceID) {
		return inventory, provider.ErrOwnershipMismatch
	}
	notes, tagged := strings.CutPrefix(exported.Information.Notes, ownershipPrefix)
	var owner ownership
	ref := inventory.Ref
	if !tagged || json.Unmarshal([]byte(notes), &owner) != nil ||
		owner.InstallationID != ref.InstallationID || owner.WorkspaceID != ref.WorkspaceID ||
		owner.Generation != ref.Generation || owner.Marker != ref.OwnershipMarker ||
		owner.ArtifactID != inventory.ArtifactID || owner.Release != inventory.Release {
		return inventory, provider.ErrOwnershipMismatch
	}
	if len(exported.Drive)+1 != len(inventory.Resources) {
		return inventory, provider.ErrOutcomeUnknown
	}
	found := -1
	var capacity int64
	for i, drive := range exported.Drive {
		resource := inventory.Resources[i+1]
		if resource.ID != inventory.InstanceID+"/"+drive.Identifier {
			return inventory, provider.ErrOutcomeUnknown
		}
		if resource.Type == "media" && drive.ImageName == "" {
			continue
		}
		if found >= 0 || resource.Type != "disk" || drive.ImageType != "Disk" ||
			filepath.Base(drive.ImageName) != drive.ImageName || filepath.Ext(drive.ImageName) != ".qcow2" {
			return inventory, provider.ErrCapabilityUnsupported
		}
		size, err := qcowCapacity(root, filepath.Join("Data", drive.ImageName))
		if err != nil {
			return inventory, err
		}
		found, capacity = i+1, size
	}
	if found < 0 {
		return inventory, provider.ErrOutcomeUnknown
	}
	inventory.Resources = append([]provider.OwnedResource(nil), inventory.Resources...)
	inventory.Resources[found].GuestBytes = &capacity
	inventory.Effective.DiskBytes = capacity
	inventory.DiskCapacityKnown = true
	return inventory, nil
}
