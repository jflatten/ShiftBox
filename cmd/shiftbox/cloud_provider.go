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
		SecurityGroupID: settings.SecurityGroupID, Deployment: settings.Deployment, Environment: settings.Environment},
		func(ctx context.Context, id, releaseID string) (ec2.Image, error) {
			image, err := store.EC2Image(ctx, id, settings.Region, settings.AccountID)
			if err != nil {
				return ec2.Image{}, err
			}
			if image.ReleaseID != releaseID || image.AMIID != settings.VerifiedEnrollmentAMI {
				return ec2.Image{}, provider.ErrCapabilityUnsupported
			}
			return launchImage(image), nil
		})
	if err != nil {
		return nil, err
	}
	return &cloudEC2{Provider: machines, settings: settings}, nil
}

func launchImage(image release.EC2Image) ec2.Image {
	return ec2.Image{ID: image.AMIID, RootDeviceName: image.RootDeviceName, SnapshotID: image.RootSnapshotID}
}

func (p *cloudEC2) Capabilities(ctx context.Context) (provider.Capabilities, error) {
	caps, err := p.Provider.Capabilities(ctx)
	if p.settings.VerifiedEnrollmentAMI == "" {
		caps.Create = false
	}
	return caps, err
}

func (p *cloudEC2) ImportCandidate(ctx context.Context, req provider.CreateRequest, artifact release.Artifact) (provider.Inventory, error) {
	if artifact.EC2 == nil || artifact.EC2.AMIID != p.settings.VerifiedEnrollmentAMI {
		return provider.Inventory{}, provider.ErrCapabilityUnsupported
	}
	return p.CreateCandidate(ctx, req, launchImage(*artifact.EC2))
}

func (p *cloudEC2) importPlatform(ctx context.Context, dir, guestHost string, store *release.Store, catalog *release.Catalog,
	ref provider.WorkspaceRef, actor, raw, current, previous string, makeDefault bool) error {
	artifact, err := release.ReadEC2Manifest(strings.NewReader(raw))
	if err != nil {
		return err
	}
	image, manifest := artifact.EC2, artifact.Manifest
	if image.Region != p.settings.Region || image.AMIID != p.settings.VerifiedEnrollmentAMI ||
		manifest.ProtocolVersion != guestProtocol || (manifest.Guest.Version != current && manifest.Guest.Version != previous) {
		return release.ErrCatalogConflict
	}
	observed, err := p.InspectImage(ctx, image.AMIID)
	if errors.Is(err, provider.ErrNotFound) || errors.Is(err, provider.ErrOwnershipMismatch) {
		return release.ErrInvalidArtifact
	}
	if err != nil {
		return err
	}
	if observed.Artifact != "workspace-ami" || observed.ReleaseID != image.ReleaseID ||
		observed.SourceAMI != image.SourceAMI || observed.SourceRevision != image.SourceRevision {
		return release.ErrInvalidArtifact
	}
	image.AccountID, image.RootDeviceName, image.RootSnapshotID = p.settings.AccountID, observed.RootDeviceName, observed.SnapshotID
	entry, err := store.ImportEC2(ctx, artifact, actor, time.Now())
	if err != nil {
		return err
	}
	req := provider.CreateRequest{Ref: ref, OperationID: ref.WorkspaceID, ArtifactID: entry.ID,
		Release: manifest.ReleaseID, Resources: ec2.ReferenceResources(), NetworkMode: ec2.NetworkMode}
	listeners := app.GuestListenerConfig{PublicHost: guestHost, EnrollmentAddress: ":19446", PollAddress: ":19447"}
	if err := catalog.ValidateImport(ctx, entry.ID, func(ctx context.Context, artifact release.Artifact) error {
		return app.ValidatePlatformBoot(ctx, dir, p, req, artifact, listeners)
	}); err != nil {
		return err
	}
	if makeDefault {
		return store.SelectDefault(ctx, entry.ID, []string{guestProtocol})
	}
	return nil
}

// pendingEC2 lets the cloud controller serve management before EC2 admission
// is wired. It cannot authorize launches or perform any infrastructure effects.
type pendingEC2 struct{}

func (pendingEC2) Capabilities(context.Context) (provider.Capabilities, error) {
	return provider.Capabilities{Name: "ec2", Architecture: "amd64"}, nil
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
