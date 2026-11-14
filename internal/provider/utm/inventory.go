package utm

import (
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/provider"
)

const ownershipPrefix = "shiftbox:"

type ownership struct {
	InstallationID string `json:"installationId"`
	WorkspaceID    string `json:"workspaceId"`
	Generation     int64  `json:"generation"`
	Marker         string `json:"marker"`
	ArtifactID     string `json:"artifactId"`
	Release        string `json:"release"`
}

type vmRecord struct {
	ID, Backend, Status, Architecture, Notes string
	CPU                                      int
	MemoryMiB                                int64
	Drives                                   []driveRecord
	NetworkModes                             []string
}

type driveRecord struct {
	ID        string
	GuestMiB  int64
	Removable bool
}

func normalizeInventory(ref provider.WorkspaceRef, records []vmRecord) (provider.Inventory, error) {
	var match *vmRecord
	var owner ownership
	for i := range records {
		vm := &records[i]
		known := ref.InstanceID != "" && vm.ID == ref.InstanceID
		tag, tagged := strings.CutPrefix(vm.Notes, ownershipPrefix)
		var candidate ownership
		if !tagged || json.Unmarshal([]byte(tag), &candidate) != nil {
			// A damaged ShiftBox marker cannot establish absence for an unknown ID.
			if known || tagged {
				return provider.Inventory{}, provider.ErrOwnershipMismatch
			}
			continue
		}
		sameWorkspace := candidate.InstallationID == ref.InstallationID && candidate.WorkspaceID == ref.WorkspaceID
		if !sameWorkspace && !known {
			continue
		}
		if match != nil || !sameWorkspace || ref.InstallationID == "" || ref.WorkspaceID == "" ||
			ref.OwnershipMarker == "" || ref.Generation < 1 || candidate.Generation != ref.Generation ||
			candidate.Marker != ref.OwnershipMarker || (ref.InstanceID != "" && !known) {
			return provider.Inventory{}, provider.ErrOwnershipMismatch
		}
		match, owner = vm, candidate
	}
	if match == nil {
		return provider.Inventory{}, provider.ErrNotFound
	}
	return inventoryFromRecord(ref, *match, owner)
}

func inventoryFromRecord(ref provider.WorkspaceRef, vm vmRecord, owner ownership) (provider.Inventory, error) {
	if vm.Backend != "qemu" || vm.Architecture != "x86_64" {
		return provider.Inventory{}, provider.ErrCapabilityUnsupported
	}
	const mib = 1 << 20
	if vm.ID == "" || vm.CPU < 1 || vm.MemoryMiB < 1 || vm.MemoryMiB > math.MaxInt64/mib || vm.Drives == nil || owner.ArtifactID == "" || owner.Release == "" {
		return provider.Inventory{}, provider.ErrOutcomeUnknown
	}
	ref.InstanceID = vm.ID
	result := provider.Inventory{
		Ref: ref, InstanceID: vm.ID, Power: powerState(vm.Status),
		ArtifactID: owner.ArtifactID, Release: owner.Release, ObservedAt: time.Now().UTC(),
		Effective: provider.Resources{CPU: vm.CPU, MemoryBytes: vm.MemoryMiB * mib},
		Resources: []provider.OwnedResource{{ID: vm.ID, Type: "vm", OwnershipMarker: owner.Marker, Generation: owner.Generation}},
	}
	if len(vm.NetworkModes) == 1 {
		result.NetworkMode = vm.NetworkModes[0]
	}
	ids := make(map[string]bool, len(vm.Drives))
	for _, drive := range vm.Drives {
		if drive.ID == "" || ids[drive.ID] || drive.GuestMiB < 0 || drive.GuestMiB > (math.MaxInt64-result.Effective.DiskBytes)/mib {
			return provider.Inventory{}, provider.ErrOutcomeUnknown
		}
		ids[drive.ID] = true
		kind := "disk"
		if drive.Removable {
			kind = "media"
		} else {
			result.Effective.DiskBytes += drive.GuestMiB * mib
		}
		result.Resources = append(result.Resources, provider.OwnedResource{
			ID: vm.ID + "/" + drive.ID, Type: kind, OwnershipMarker: owner.Marker, Generation: owner.Generation,
		})
	}
	return result, nil
}

func powerState(status string) provider.PowerState {
	switch status {
	case "stopped":
		return provider.Stopped
	case "starting", "resuming":
		return provider.Starting
	case "started":
		return provider.Running
	case "stopping":
		return provider.Stopping
	default:
		return provider.Unknown
	}
}
