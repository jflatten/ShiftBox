package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/ec2"
	"github.com/jflatten/ShiftBox/internal/release"
)

type cloudEC2 struct {
	*ec2.Provider
	settings config.EC2Runtime
}

func newCloudEC2(ctx context.Context, settings config.EC2Runtime, store *release.Store) (*cloudEC2, error) {
	machines, err := ec2.New(ctx, ec2.Config{Region: settings.Region, AccountID: settings.AccountID, SubnetID: settings.SubnetID,
		SecurityGroupID: settings.SecurityGroupID, Deployment: settings.Deployment, Environment: settings.Environment,
		NamespacedTags: settings.NamespacedTags, OfficialImage: settings.OfficialImage},
		func(ctx context.Context, id, releaseID string) (ec2.Image, error) {
			image, err := store.EC2Image(ctx, id, settings.Region, settings.AccountID)
			if err != nil {
				return ec2.Image{}, err
			}
			if image.ReleaseID != releaseID || !verifiedCloudCatalog(settings, image, image.CatalogSHA256) {
				return ec2.Image{}, provider.ErrCapabilityUnsupported
			}
			resolved := launchImage(image)
			resolved.SingleBoot = image.UsesSingleBoot() || image.AMIID == settings.VerifiedSingleBootAMI
			return resolved, nil
		})
	if err != nil {
		return nil, err
	}
	return &cloudEC2{Provider: machines, settings: settings}, nil
}

func verifiedCloudAMI(settings config.EC2Runtime, image string) bool {
	return image != "" && (image == settings.VerifiedEnrollmentAMI || image == settings.VerifiedSingleBootAMI)
}

func verifiedCloudCatalog(settings config.EC2Runtime, image release.EC2Image, checksum string) bool {
	if pin := settings.OfficialImage; pin != nil && pin.AMIID == image.AMIID {
		return pin.MatchesCatalog(image, checksum)
	}
	return image.PublisherAccountID == "" && (image.Family != "" || verifiedCloudAMI(settings, image.AMIID))
}

func launchImage(image release.EC2Image) ec2.Image {
	return ec2.Image{ID: image.AMIID, RootDeviceName: image.RootDeviceName, SnapshotID: image.RootSnapshotID, Architecture: image.Architecture}
}

func (p *cloudEC2) ImportCandidate(ctx context.Context, req provider.CreateRequest, artifact release.Artifact) (provider.Inventory, error) {
	if artifact.EC2 == nil || artifact.EC2.Region != p.settings.Region || artifact.EC2.AccountID != p.settings.AccountID ||
		!verifiedCloudCatalog(p.settings, *artifact.EC2, artifact.ManifestSHA256) {
		return provider.Inventory{}, provider.ErrCapabilityUnsupported
	}
	image := launchImage(*artifact.EC2)
	image.SingleBoot = artifact.EC2.UsesSingleBoot()
	image.Architecture = artifact.Manifest.Architecture
	return p.CreateCandidate(ctx, req, image)
}

func (p *cloudEC2) readCandidate(raw, current, previous string, options app.PlatformImportOptions) (release.Artifact, error) {
	artifact, err := release.ReadEC2Manifest(strings.NewReader(raw))
	if err != nil {
		return release.Artifact{}, err
	}
	image, manifest := artifact.EC2, artifact.Manifest
	if image.Region != p.settings.Region ||
		manifest.ProtocolVersion != guestProtocol || (manifest.Guest.Version != current && manifest.Guest.Version != previous) {
		return release.Artifact{}, release.ErrCatalogConflict
	}
	pin := p.settings.OfficialImage
	if options.Official {
		if pin == nil || options.AMIFamily != "" {
			return release.Artifact{}, release.ErrCatalogConflict
		}
		if !pin.MatchesManifest(artifact) {
			return release.Artifact{}, release.ErrInvalidArtifact
		}
		image.PublisherAccountID = pin.PublisherAccountID
	} else if (pin != nil && image.AMIID == pin.AMIID) || (options.AMIFamily == "" && !verifiedCloudAMI(p.settings, image.AMIID)) {
		return release.Artifact{}, release.ErrCatalogConflict
	}
	return artifact, nil
}

func (p *cloudEC2) importPlatform(ctx context.Context, dir, guestHost string, store *release.Store, catalog *release.Catalog,
	ref provider.WorkspaceRef, actor, raw, current, previous string, options app.PlatformImportOptions) error {
	artifact, err := p.readCandidate(raw, current, previous, options)
	if err != nil {
		return err
	}
	image, manifest := artifact.EC2, artifact.Manifest
	var observed ec2.Image
	if options.Official {
		observed, err = p.InspectOfficialImage(ctx)
	} else {
		observed, err = p.InspectImage(ctx, image.AMIID)
	}
	if errors.Is(err, provider.ErrNotFound) || errors.Is(err, provider.ErrOwnershipMismatch) ||
		(options.Official && errors.Is(err, provider.ErrInvalidState)) {
		return release.ErrInvalidArtifact
	}
	if err != nil {
		return err
	}
	if !options.Official && (observed.Architecture != manifest.Architecture || observed.Artifact != "workspace-ami" ||
		observed.ReleaseID != image.ReleaseID || observed.SourceAMI != image.SourceAMI || observed.SourceRevision != image.SourceRevision) {
		return release.ErrInvalidArtifact
	}
	image.AccountID, image.RootDeviceName, image.RootSnapshotID = p.settings.AccountID, observed.RootDeviceName, observed.SnapshotID
	if options.AMIFamily != "" {
		created, err := time.Parse(time.RFC3339Nano, observed.CreationDate)
		if err != nil {
			return release.ErrInvalidArtifact
		}
		image.Family, image.CreatedAtUnixMillis = options.AMIFamily, created.UnixMilli()
	}
	entry, err := store.ImportEC2(ctx, artifact, actor, time.Now())
	if err != nil {
		return err
	}
	req := provider.CreateRequest{Ref: ref, OperationID: ref.WorkspaceID, ArtifactID: entry.ID,
		Release: manifest.ReleaseID, Resources: ec2.ReferenceResources(), NetworkMode: ec2.NetworkMode}
	listeners := app.GuestListenerConfig{PublicHost: guestHost, EnrollmentAddress: ":19446", PollAddress: ":19447",
		EC2Region: p.settings.Region, EC2Account: p.settings.AccountID}
	if err := catalog.ValidateImport(ctx, entry.ID, func(ctx context.Context, artifact release.Artifact) error {
		return app.ValidatePlatformBoot(ctx, dir, p, req, artifact, listeners)
	}); err != nil {
		return err
	}
	if options.MakeDefault {
		return store.SelectDefault(ctx, entry.ID, []string{guestProtocol})
	}
	return nil
}

// pendingEC2 lets the cloud controller serve management before EC2 admission
// is wired. It cannot authorize launches or perform any infrastructure effects.
type pendingEC2 struct{}

func (pendingEC2) Capabilities(context.Context) (provider.Capabilities, error) {
	return provider.Capabilities{Name: "ec2", Architecture: release.EC2ReferenceArchitecture}, nil
}

func (pendingEC2) Inspect(context.Context, provider.WorkspaceRef) (provider.Inventory, error) {
	return provider.Inventory{}, provider.ErrUnavailable
}

func (pendingEC2) Create(context.Context, provider.CreateRequest) (provider.Inventory, error) {
	return provider.Inventory{}, provider.ErrCapabilityUnsupported
}

func (pendingEC2) Start(context.Context, provider.StartRequest) (provider.Inventory, error) {
	return provider.Inventory{}, provider.ErrCapabilityUnsupported
}

func (pendingEC2) Stop(context.Context, provider.StopRequest) (provider.Inventory, error) {
	return provider.Inventory{}, provider.ErrCapabilityUnsupported
}

func (pendingEC2) Delete(context.Context, provider.DeleteRequest) (provider.Inventory, error) {
	return provider.Inventory{}, provider.ErrCapabilityUnsupported
}

func (pendingEC2) ApplyNetwork(context.Context, provider.NetworkRequest) (provider.Inventory, error) {
	return provider.Inventory{}, provider.ErrCapabilityUnsupported
}

func (pendingEC2) DeliverEnrollment(context.Context, provider.EnrollmentRequest) (provider.DeliveryResult, error) {
	return provider.DeliveryResult{}, provider.ErrCapabilityUnsupported
}

func (pendingEC2) ImportCandidate(context.Context, provider.CreateRequest, release.Artifact) (provider.Inventory, error) {
	return provider.Inventory{}, provider.ErrCapabilityUnsupported
}
