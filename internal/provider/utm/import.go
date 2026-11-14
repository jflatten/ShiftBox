package utm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/release"
)

// ImportCatalogArtifact stages verified catalog content in a temporary
// directory and passes the workspace copy to the native UTM import path.
func (p *PowerController) ImportCatalogArtifact(ctx context.Context, req provider.CreateRequest, directory, manifestPath, stagingRoot, storage string) (provider.Inventory, error) {
	manifest, err := os.Open(manifestPath)
	if err != nil {
		return provider.Inventory{}, fmt.Errorf("%w: cannot open catalog manifest", release.ErrInvalidArtifact)
	}
	defer manifest.Close()
	stage, err := os.MkdirTemp(stagingRoot, "import-")
	if err != nil {
		return provider.Inventory{}, fmt.Errorf("create VM import staging directory: %w", err)
	}
	defer os.RemoveAll(stage)
	artifact, err := release.StageArtifact(ctx, directory, manifest, stage, managedBundleName(req.Ref))
	if err != nil {
		return provider.Inventory{}, err
	}
	if artifact.Manifest.ReleaseID != req.Release {
		return provider.Inventory{}, fmt.Errorf("%w: catalog release does not match workspace", release.ErrInvalidArtifact)
	}
	if err := verifyStagedDisk(ctx, artifact, req.Resources.DiskBytes); err != nil {
		return provider.Inventory{}, err
	}
	inventory, err := p.ImportManagedVM(ctx, req, artifact.Bundle, storage)
	if err != nil {
		return inventory, err
	}
	if !inventory.DiskCapacityKnown {
		inventory, err = p.InspectStoppedDisk(ctx, inventory.Ref, stagingRoot)
		if err != nil {
			return inventory, err
		}
	}
	if inventory.Ref.InstallationID != req.Ref.InstallationID || inventory.Ref.WorkspaceID != req.Ref.WorkspaceID ||
		inventory.Ref.Generation != req.Ref.Generation || inventory.Ref.OwnershipMarker != req.Ref.OwnershipMarker ||
		!inventory.DiskCapacityKnown {
		return inventory, provider.ErrOutcomeUnknown
	}
	return inventory, verifyImportedVM(inventory, req, filepath.Join(storage, managedBundleName(req.Ref)))
}

// ImportManagedVM imports a verified, staged artifact under its reserved
// filename. Storage is UTM's configured Documents directory. Enrollment is
// delivered separately while the workspace is stopped.
func (p *PowerController) ImportManagedVM(ctx context.Context, req provider.CreateRequest, stagedBundle, storage string) (provider.Inventory, error) {
	const mib = 1 << 20
	ref := req.Ref
	if ref.InstallationID == "" || ref.WorkspaceID == "" || ref.Generation < 1 || ref.OwnershipMarker == "" ||
		req.OperationID == "" || req.ArtifactID == "" || req.Release == "" ||
		req.Resources.CPU < 1 || req.Resources.MemoryBytes < mib || req.Resources.MemoryBytes%mib != 0 ||
		req.Resources.DiskBytes < 1 {
		return provider.Inventory{}, provider.ErrInvalidState
	}
	if req.NetworkMode != "shared" {
		return provider.Inventory{}, provider.ErrCapabilityUnsupported
	}
	name := managedBundleName(ref)
	destination := filepath.Join(storage, name)
	if !filepath.IsAbs(storage) || !filepath.IsAbs(stagedBundle) ||
		filepath.Base(stagedBundle) != name || filepath.Clean(stagedBundle) == destination {
		return provider.Inventory{}, provider.ErrInvalidState
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	before, err := p.inspect(ctx, ref)
	if err == nil {
		return before, verifyImportedVM(before, req, destination)
	}
	if !errors.Is(err, provider.ErrNotFound) {
		return before, err
	}
	if ref.InstanceID != "" {
		return before, provider.ErrOutcomeUnknown
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return before, provider.ErrOutcomeUnknown
	}
	if info, err := os.Stat(storage); err != nil || !info.IsDir() {
		return before, provider.ErrOutcomeUnknown
	}
	if info, err := os.Lstat(stagedBundle); err != nil || !info.IsDir() {
		return before, provider.ErrInvalidState
	}
	if err := ctx.Err(); err != nil {
		return before, err
	}

	effectErr := importBundle(ctx, p.version, req, stagedBundle, destination)
	after, inspectErr := p.inspect(ctx, ref)
	if inspectErr == nil {
		return after, verifyImportedVM(after, req, destination)
	}
	if errors.Is(effectErr, provider.ErrPermission) || errors.Is(effectErr, provider.ErrCapabilityUnsupported) {
		return after, effectErr
	}
	return after, errors.Join(provider.ErrOutcomeUnknown, inspectErr)
}

func managedBundleName(ref provider.WorkspaceRef) string {
	identity, _ := json.Marshal([2]string{ref.InstallationID, ref.WorkspaceID})
	digest := sha256.Sum256(identity)
	return fmt.Sprintf("ShiftBox-%x-%d.utm", digest[:16], ref.Generation)
}

func verifyImportedVM(vm provider.Inventory, req provider.CreateRequest, destination string) error {
	info, err := os.Lstat(destination)
	if err != nil || !info.IsDir() || vm.InstanceID == "" || vm.Power != provider.Stopped ||
		vm.ArtifactID != req.ArtifactID || vm.Release != req.Release || vm.NetworkMode != req.NetworkMode ||
		vm.Effective.CPU != req.Resources.CPU || vm.Effective.MemoryBytes != req.Resources.MemoryBytes ||
		(vm.DiskCapacityKnown && vm.Effective.DiskBytes != req.Resources.DiskBytes) {
		return provider.ErrOutcomeUnknown
	}
	for _, kind := range []string{"disk", "media"} {
		if !slices.ContainsFunc(vm.Resources, func(resource provider.OwnedResource) bool {
			return resource.Type == kind && !resource.Shared && !resource.Immutable
		}) {
			return provider.ErrOutcomeUnknown
		}
	}
	return nil
}

const importBundleScript = `
use framework "Foundation"

on run argv
    set sourceFile to POSIX file (item 2 of argv) as alias
    set targetPath to item 3 of argv
    set ownerNotes to item 4 of argv
    set cores to (item 5 of argv) as integer
    set memoryMiB to (item 6 of argv) as integer
    tell application "/Applications/UTM.app"
        if UTM version is not item 1 of argv then return "version"
        if (current application's NSFileManager's defaultManager()'s fileExistsAtPath:targetPath) as boolean then return "state"
        set vm to import new virtual machine from sourceFile
        if backend of vm is not qemu or status of vm is not stopped then return "state"
        set cfg to configuration of vm
        if architecture of cfg is not "x86_64" then return "state"
        set interfaces to network interfaces of cfg
        if (count of interfaces) is not 1 then return "state"
        if mode of item 1 of interfaces is not shared then return "state"
        if (notes of cfg) starts with "shiftbox:" then return "ownership"
        update configuration vm with {notes:ownerNotes, cpu cores:cores, memory:memoryMiB}
    end tell
    return "issued"
end run
`

func importBundle(ctx context.Context, version string, req provider.CreateRequest, source, destination string) error {
	if version == "" {
		return provider.ErrCapabilityUnsupported
	}
	ref := req.Ref
	notes, err := json.Marshal(ownership{InstallationID: ref.InstallationID, WorkspaceID: ref.WorkspaceID,
		Generation: ref.Generation, Marker: ref.OwnershipMarker, ArtifactID: req.ArtifactID, Release: req.Release})
	if err != nil {
		return provider.ErrInvalidState
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", importBundleScript, "--", version,
		source, destination, ownershipPrefix+string(notes), strconv.Itoa(req.Resources.CPU),
		strconv.FormatInt(req.Resources.MemoryBytes>>20, 10))
	return effectResult(cmd.Output())
}
