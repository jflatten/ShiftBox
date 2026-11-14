package utm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/release"
)

const validatedVersion = "5.0.8"

// Provider exposes UTM's native VM operations to the workspace reconciler.
type Provider struct {
	controller *PowerController
	catalog    *release.Catalog
	staging    string
	storage    string
	minimum    provider.Resources
	maximum    provider.Resources
}

var _ provider.Provider = (*Provider)(nil)

// NewProvider binds the validated UTM build to managed storage and the
// published catalog. Resource bounds come from the installation's validated
// release and host configuration.
func NewProvider(catalog *release.Catalog, staging, storage string, minimum, maximum provider.Resources) (*Provider, error) {
	if catalog == nil || !filepath.IsAbs(staging) || !filepath.IsAbs(storage) ||
		minimum.CPU < 1 || minimum.MemoryBytes < 1 || minimum.DiskBytes < 1 ||
		maximum.CPU < minimum.CPU || maximum.MemoryBytes < minimum.MemoryBytes || maximum.DiskBytes < minimum.DiskBytes {
		return nil, provider.ErrInvalidState
	}
	return &Provider{controller: NewPowerController(validatedVersion), catalog: catalog,
		staging: staging, storage: storage, minimum: minimum, maximum: maximum}, nil
}

func (p *Provider) Capabilities(ctx context.Context) (provider.Capabilities, error) {
	// Confirm that this build can answer an authoritative inventory request.
	probe := provider.WorkspaceRef{InstallationID: uuid.NewString(), WorkspaceID: uuid.NewString(),
		Generation: 1, OwnershipMarker: uuid.NewString()}
	if _, err := p.controller.inspect(ctx, probe); !errors.Is(err, provider.ErrNotFound) {
		if err == nil {
			err = provider.ErrOutcomeUnknown
		}
		return provider.Capabilities{}, err
	}
	return provider.Capabilities{
		Name: "utm", Version: validatedVersion, Architecture: "amd64", ExecutionMode: "emulated",
		Evidence: "UTM 5.0.8 native QEMU scripting",
		Create:   true, Start: true, Stop: true, Delete: true, NetworkStoppedOnly: true,
		NetworkModes: []string{"shared"}, RecoveryMechanisms: []string{"media"},
		Minimum: p.minimum, Maximum: p.maximum,
	}, nil
}

func (p *Provider) Inspect(ctx context.Context, ref provider.WorkspaceRef) (provider.Inventory, error) {
	got, err := p.controller.inspect(ctx, ref)
	if errors.Is(err, provider.ErrNotFound) {
		return p.observeMissing(ref, err)
	}
	if err != nil || got.Power != provider.Stopped || got.DiskCapacityKnown {
		return got, err
	}
	return p.controller.InspectStoppedDisk(ctx, got.Ref, p.staging)
}

func (p *Provider) observeMissing(ref provider.WorkspaceRef, absent error) (provider.Inventory, error) {
	entries, err := os.ReadDir(p.storage)
	if err != nil {
		return provider.Inventory{}, provider.ErrOutcomeUnknown
	}
	// A bundle from a previous generation also prevents claiming complete
	// absence. Leave it visible for reconciliation instead of adopting it.
	var remaining []provider.OwnedResource
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, managedBundlePrefix(ref)) && filepath.Ext(name) == ".utm" {
			remaining = append(remaining, provider.OwnedResource{ID: name, Type: "bundle"})
		}
	}
	if len(remaining) > 0 {
		return provider.Inventory{Ref: ref, InstanceID: ref.InstanceID, Power: provider.Unknown, Resources: remaining}, provider.ErrPartial
	}
	return provider.Inventory{}, absent
}

func (p *Provider) Create(ctx context.Context, req provider.CreateRequest) (provider.Inventory, error) {
	if req.OperationID == "" || req.EnrollmentMedia != "" {
		return provider.Inventory{}, provider.ErrInvalidState
	}
	got, err := p.Inspect(ctx, req.Ref)
	switch {
	case err == nil:
		return got, verifyImportedVM(got, req, filepath.Join(p.storage, managedBundleName(req.Ref)))
	case !errors.Is(err, provider.ErrNotFound):
		return got, err
	case req.Ref.InstanceID != "":
		return got, provider.ErrOwnershipMismatch
	}
	stage, err := os.MkdirTemp(p.staging, "import-")
	if err != nil {
		return got, provider.ErrUnavailable
	}
	defer os.RemoveAll(stage)
	artifact, err := p.catalog.Stage(ctx, req.ArtifactID, stage, managedBundleName(req.Ref))
	if err != nil {
		return got, err
	}
	return p.controller.importStagedArtifact(ctx, req, artifact, p.staging, p.storage)
}

// ImportCandidate imports the disposable copy supplied by catalog validation.
func (p *Provider) ImportCandidate(ctx context.Context, req provider.CreateRequest, artifact release.Artifact) (provider.Inventory, error) {
	disk, err := stagedDiskCapacity(ctx, artifact)
	if err != nil {
		return provider.Inventory{}, err
	}
	req.Resources.DiskBytes = disk
	if req.Resources.CPU < p.minimum.CPU || req.Resources.CPU > p.maximum.CPU ||
		req.Resources.MemoryBytes < p.minimum.MemoryBytes || req.Resources.MemoryBytes > p.maximum.MemoryBytes ||
		req.Resources.DiskBytes < p.minimum.DiskBytes || req.Resources.DiskBytes > p.maximum.DiskBytes {
		return provider.Inventory{}, provider.ErrCapabilityUnsupported
	}
	artifactDir := filepath.Dir(artifact.Bundle)
	bundle := filepath.Join(artifactDir, managedBundleName(req.Ref))
	if err := os.Rename(artifact.Bundle, bundle); err != nil {
		return provider.Inventory{}, err
	}
	artifact.Bundle = bundle
	return p.controller.importStagedArtifact(ctx, req, artifact, p.staging, p.storage)
}

func (p *Provider) Start(ctx context.Context, req provider.StartRequest) (provider.Inventory, error) {
	return p.controller.Start(ctx, req)
}

func (p *Provider) Stop(ctx context.Context, req provider.StopRequest) (provider.Inventory, error) {
	return p.controller.Stop(ctx, req)
}

func (p *Provider) Delete(ctx context.Context, req provider.DeleteRequest) (provider.Inventory, error) {
	got, err := p.controller.DeleteManagedVM(ctx, req, filepath.Join(p.storage, managedBundleName(req.Ref)))
	if err != nil {
		return got, err
	}
	remaining, err := p.observeMissing(req.Ref, nil)
	if err != nil {
		return remaining, err
	}
	return got, nil
}

func (p *Provider) ApplyNetwork(ctx context.Context, req provider.NetworkRequest) (provider.Inventory, error) {
	if req.Mode != "shared" {
		return provider.Inventory{}, provider.ErrCapabilityUnsupported
	}
	if req.Ref.InstanceID == "" || req.OperationID == "" {
		return provider.Inventory{}, provider.ErrInvalidState
	}
	got, err := p.controller.inspect(ctx, req.Ref)
	if err != nil {
		return got, err
	}
	if got.Power != provider.Stopped {
		return got, provider.ErrInvalidState
	}
	if got.NetworkMode != req.Mode {
		return got, provider.ErrCapabilityUnsupported
	}
	return got, nil
}

func (p *Provider) DeliverEnrollment(ctx context.Context, req provider.EnrollmentRequest) (provider.DeliveryResult, error) {
	if !req.Eligible || req.Ref.InstanceID == "" || req.OperationID == "" {
		return provider.DeliveryResult{}, provider.ErrInvalidState
	}
	got, err := p.controller.inspect(ctx, req.Ref)
	if err != nil {
		return provider.DeliveryResult{Inventory: got}, err
	}
	mediaID, err := enrollmentDrive(got, req.Ref)
	if err != nil {
		return provider.DeliveryResult{Inventory: got}, err
	}
	got, err = p.controller.ReplaceRecoveryMedia(ctx, req, mediaID)
	return provider.DeliveryResult{Inventory: got, MediaID: mediaID}, err
}

func enrollmentDrive(got provider.Inventory, ref provider.WorkspaceRef) (string, error) {
	var mediaID string
	for _, resource := range got.Resources {
		if resource.Type == "media" && !resource.Shared && !resource.Immutable &&
			resource.Generation == ref.Generation && resource.OwnershipMarker == ref.OwnershipMarker {
			if mediaID != "" {
				return "", provider.ErrInvalidState
			}
			mediaID = resource.ID
		}
	}
	if mediaID == "" {
		return "", provider.ErrInvalidState
	}
	return mediaID, nil
}
