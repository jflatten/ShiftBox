package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// EC2InstanceType is the reference instance shape for the first AWS provider.
const EC2InstanceType = "t3.medium"

const (
	// EC2CPUCount is the vCPU count of the reference EC2 workspace.
	EC2CPUCount = 2
	// EC2MemoryMiB is the memory of the reference EC2 workspace.
	EC2MemoryMiB = 4096
	// EC2DiskMiB is the root disk size of the reference EC2 workspace.
	EC2DiskMiB = 32768
)

// EC2ReferenceArchitecture identifies the validated reference canary architecture.
const EC2ReferenceArchitecture = "amd64"

// EC2PlatformArchitectures reports the architectures qualified by this release.
func EC2PlatformArchitectures() []string {
	return []string{EC2ReferenceArchitecture}
}

// SupportsEC2PlatformArchitecture reports whether this release admits AMIs for the architecture.
func SupportsEC2PlatformArchitecture(architecture string) bool {
	return slices.Contains(EC2PlatformArchitectures(), architecture)
}

const canonicalAMIOwner = "099720109477"

var (
	ec2AMIIDPattern    = regexp.MustCompile(`^ami-(?:[0-9a-f]{8}|[0-9a-f]{17})$`)
	ec2SnapshotPattern = regexp.MustCompile(`^snap-(?:[0-9a-f]{8}|[0-9a-f]{17})$`)
	ec2RegionPattern   = regexp.MustCompile(`^[a-z]{2}(-[a-z0-9]+)+-[0-9]+$`)
	ec2AccountPattern  = regexp.MustCompile(`^[0-9]{12}$`)
	ec2RevisionPattern = regexp.MustCompile(`^[0-9a-f]{40}(-dirty)?$`)
	ec2ReleasePattern  = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,96}$`)
	ec2FamilyPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	ec2RCPattern       = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-rc\.[1-9][0-9]*$`)
)

// ValidEC2FamilyName reports whether a name can identify an AMI family.
func ValidEC2FamilyName(name string) bool {
	return ec2FamilyPattern.MatchString(name)
}

// EC2Image binds a regional AMI to its installation and build provenance.
type EC2Image struct {
	Architecture           string `json:"-"`
	CatalogSHA256          string `json:"-"`
	AMIID                  string `json:"amiId"`
	Region                 string `json:"region"`
	AccountID              string `json:"accountId"`
	PublisherAccountID     string `json:"publisherAccountId,omitempty"`
	RootSnapshotID         string `json:"rootSnapshotId"`
	RootDeviceName         string `json:"rootDeviceName"`
	MinimumRootDiskGiB     int32  `json:"minimumRootDiskGiB"`
	ReleaseID              string `json:"releaseId"`
	SourceAMI              string `json:"sourceAmiId"`
	SourceRegion           string `json:"sourceRegion,omitempty"`
	SourceOwner            string `json:"sourceAmiOwnerId"`
	SourceRevision         string `json:"sourceRevision"`
	GuestUpdateKeySHA256   string `json:"guestUpdateKeySHA256"`
	ToolchainRecipeSHA256  string `json:"toolchainRecipeSHA256"`
	CodeServerRecipeSHA256 string `json:"codeServerRecipeSHA256"`
	Family                 string `json:"family,omitempty"`
	CreatedAtUnixMillis    int64  `json:"createdAtUnixMillis,omitempty"`
}

// UsesSingleBoot reports whether the catalog entry uses instance-identity enrollment.
func (i EC2Image) UsesSingleBoot() bool {
	return i.Family != "" || i.PublisherAccountID != ""
}

// OfficialEC2Image is the release descriptor installed by the customer stack.
type OfficialEC2Image struct {
	AMIID              string `json:"amiId"`
	PublisherAccountID string `json:"publisherAccountId"`
	RootDeviceName     string `json:"rootDeviceName"`
	RootSnapshotID     string `json:"rootSnapshotId"`
	SourceAMI          string `json:"sourceAmiId"`
	SourceRegion       string `json:"sourceRegion"`
	SourceCommit       string `json:"sourceCommit"`
	ReleaseID          string `json:"releaseId"`
	GuestRelease       string `json:"guestRelease"`
	Architecture       string `json:"architecture"`
	MinimumDiskGiB     int32  `json:"minimumDiskGiB"`
	CatalogSHA256      string `json:"catalogSha256"`
}

// Validate checks the descriptor against the regional customer deployment.
func (i OfficialEC2Image) Validate(region, accountID string) error {
	if !publicEC2Region(region) || !publicEC2Region(i.SourceRegion) || !ec2AccountPattern.MatchString(accountID) ||
		!validPinnedPublisher(region, accountID, i.PublisherAccountID, i.ReleaseID) ||
		!ec2AMIIDPattern.MatchString(i.AMIID) || !ec2AMIIDPattern.MatchString(i.SourceAMI) ||
		!ec2SnapshotPattern.MatchString(i.RootSnapshotID) || i.RootDeviceName != "/dev/sda1" ||
		len(i.SourceCommit) != 40 || !ec2RevisionPattern.MatchString(i.SourceCommit) ||
		!ec2ReleasePattern.MatchString(i.ReleaseID) || !ec2RCPattern.MatchString(i.ReleaseID) || i.GuestRelease != i.ReleaseID ||
		i.Architecture != EC2ReferenceArchitecture || i.MinimumDiskGiB != EC2DiskMiB/1024 || !validDigest(i.CatalogSHA256) {
		return ErrInvalidArtifact
	}
	return nil
}

func validPinnedPublisher(region, account, publisher, version string) bool {
	if account != publisher {
		return ec2AccountPattern.MatchString(publisher)
	}
	return account == "888293065132" && region == "us-east-2" && strings.HasPrefix(version, "v0.0.0-rc.")
}

// MatchesManifest checks both the unmodified release bytes and their decoded provenance.
func (i OfficialEC2Image) MatchesManifest(artifact Artifact) bool {
	image := artifact.EC2
	return image != nil && i.CatalogSHA256 == artifact.ManifestSHA256 &&
		i.matchesProvenance(*image) && i.Architecture == artifact.Manifest.Architecture && i.GuestRelease == artifact.Manifest.Guest.Version
}

// MatchesCatalog checks the recorded release identity and the observed public AMI binding.
func (i OfficialEC2Image) MatchesCatalog(image EC2Image, catalogSHA256 string) bool {
	return image.Family == "" && i.PublisherAccountID == image.PublisherAccountID && i.CatalogSHA256 == catalogSHA256 &&
		i.matchesProvenance(image) && i.RootDeviceName == image.RootDeviceName && i.RootSnapshotID == image.RootSnapshotID
}

func (i OfficialEC2Image) matchesProvenance(image EC2Image) bool {
	return i.AMIID == image.AMIID && i.SourceAMI == image.SourceAMI && i.SourceRegion == image.SourceRegion &&
		i.SourceCommit == image.SourceRevision && i.ReleaseID == image.ReleaseID && i.MinimumDiskGiB == image.MinimumRootDiskGiB
}

func publicEC2Region(region string) bool {
	switch region {
	case "us-east-1", "us-east-2", "us-west-1", "us-west-2":
		return true
	default:
		return false
	}
}

type packerAMIManifest struct {
	Builds []struct {
		BuilderType string            `json:"builder_type"`
		ArtifactID  string            `json:"artifact_id"`
		RunUUID     string            `json:"packer_run_uuid"`
		CustomData  map[string]string `json:"custom_data"`
	} `json:"builds"`
	LastRunUUID string `json:"last_run_uuid"`
}

// ReadEC2Manifest reads the pinned release emitted by Packer. The caller must
// fill in the account, root snapshot, and root device from the observed AMI.
func ReadEC2Manifest(input io.Reader) (Artifact, error) {
	raw, err := io.ReadAll(io.LimitReader(input, maxManifestBytes+1))
	if err != nil || len(raw) > maxManifestBytes {
		return Artifact{}, fmt.Errorf("%w: cannot read Packer manifest", ErrInvalidArtifact)
	}
	var manifest packerAMIManifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&manifest); err != nil || decoder.Decode(new(any)) != io.EOF || len(manifest.Builds) != 1 {
		return Artifact{}, fmt.Errorf("%w: expected one Packer AMI build", ErrInvalidArtifact)
	}
	build := manifest.Builds[0]
	region, ami, paired := strings.Cut(build.ArtifactID, ":")
	data := build.CustomData
	if _, err := uuid.Parse(build.RunUUID); err != nil || build.RunUUID != manifest.LastRunUUID ||
		build.BuilderType != "amazon-ebs" || !paired || data["provider"] != "ec2" || data["region"] != region ||
		!SupportsEC2PlatformArchitecture(data["architecture"]) || data["platformVersion"] != "24.04" ||
		data["minimumRootDiskGiB"] != "32" || data["protocolVersion"] != "1" ||
		!ec2ReleasePattern.MatchString(data["guestAgentRelease"]) || !validDigest(data["guestAgentSHA256"]) {
		return Artifact{}, fmt.Errorf("%w: unsupported Packer release metadata", ErrInvalidArtifact)
	}
	image := &EC2Image{
		AMIID: ami, Region: region, MinimumRootDiskGiB: EC2DiskMiB / 1024,
		ReleaseID: data["releaseId"], SourceAMI: data["sourceAmiId"], SourceRegion: data["sourceRegion"], SourceOwner: data["sourceAmiOwnerId"],
		SourceRevision: data["sourceRevision"], GuestUpdateKeySHA256: data["guestUpdateKeySHA256"],
		ToolchainRecipeSHA256: data["toolchainRecipeSHA256"], CodeServerRecipeSHA256: data["codeServerRecipeSHA256"],
	}
	if !image.validProvenance() {
		return Artifact{}, fmt.Errorf("%w: incomplete AMI provenance", ErrInvalidArtifact)
	}
	sum := sha256.Sum256(raw)
	return Artifact{
		EC2: image, Bundle: ami, ManifestSHA256: hex.EncodeToString(sum[:]),
		Manifest: ArtifactManifest{
			SchemaVersion: 1, ReleaseID: image.ReleaseID, Architecture: data["architecture"],
			PlatformVersion: data["platformVersion"], ProtocolVersion: data["protocolVersion"],
			Guest:      ArtifactComponent{Version: data["guestAgentRelease"], SHA256: data["guestAgentSHA256"]},
			Provenance: ArtifactProvenance{Source: image.SourceAMI, Revision: image.SourceRevision},
		},
	}, nil
}

func (i EC2Image) validProvenance() bool {
	return ec2AMIIDPattern.MatchString(i.AMIID) && ec2AMIIDPattern.MatchString(i.SourceAMI) &&
		ec2RegionPattern.MatchString(i.Region) && ec2ReleasePattern.MatchString(i.ReleaseID) &&
		i.SourceOwner == canonicalAMIOwner && ec2RevisionPattern.MatchString(i.SourceRevision) &&
		i.MinimumRootDiskGiB == EC2DiskMiB/1024 && validDigest(i.GuestUpdateKeySHA256) &&
		validDigest(i.ToolchainRecipeSHA256) && validDigest(i.CodeServerRecipeSHA256)
}

func validEC2Artifact(artifact Artifact) bool {
	i, m := artifact.EC2, artifact.Manifest
	return i != nil && i.validProvenance() && ec2AccountPattern.MatchString(i.AccountID) &&
		(i.PublisherAccountID == "" || i.validOfficialCatalog(m)) &&
		(i.Family == "" && i.CreatedAtUnixMillis == 0 || ValidEC2FamilyName(i.Family) && i.CreatedAtUnixMillis > 0) &&
		ec2SnapshotPattern.MatchString(i.RootSnapshotID) && i.RootDeviceName == "/dev/sda1" &&
		i.AMIID == artifact.Bundle && validDigest(artifact.ManifestSHA256) &&
		m.SchemaVersion == 1 && m.ReleaseID == i.ReleaseID && SupportsEC2PlatformArchitecture(m.Architecture) &&
		m.PlatformVersion == "24.04" && m.ProtocolVersion == "1" && len(m.Resources) == 0 &&
		m.Provenance.Source == i.SourceAMI && m.Provenance.Revision == i.SourceRevision &&
		ec2ReleasePattern.MatchString(m.Guest.Version) && validDigest(m.Guest.SHA256)
}

func (i EC2Image) validOfficialCatalog(manifest ArtifactManifest) bool {
	return validPinnedPublisher(i.Region, i.AccountID, i.PublisherAccountID, i.ReleaseID) &&
		i.Family == "" && publicEC2Region(i.Region) && publicEC2Region(i.SourceRegion) && len(i.SourceRevision) == 40 &&
		ec2RCPattern.MatchString(i.ReleaseID) && manifest.Guest.Version == i.ReleaseID
}

// ImportEC2 records an observed image pending canary validation. Retries
// converge on the same manifest and observed identity.
func (s *Store) ImportEC2(ctx context.Context, artifact Artifact, actor string, now time.Time) (CatalogEntry, error) {
	if !validEC2Artifact(artifact) {
		return CatalogEntry{}, ErrInvalidArtifact
	}
	image := artifact.EC2
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte("shiftbox:ec2:"+image.Region+":"+image.AccountID+":"+image.ReleaseID)).String()
	if err := s.BeginImport(ctx, id, artifact, actor, now); err != nil && !errors.Is(err, ErrCatalogConflict) {
		return CatalogEntry{}, err
	}
	entry, err := s.getByRelease(ctx, image.ReleaseID)
	if errors.Is(err, sql.ErrNoRows) {
		return CatalogEntry{}, ErrCatalogConflict
	}
	if err != nil {
		return CatalogEntry{}, err
	}
	if entry.State == "rejected" || entry.EC2Retired || entry.ImportedBy != actor || entry.Artifact.EC2 == nil ||
		entry.Artifact.ManifestSHA256 != artifact.ManifestSHA256 || *entry.Artifact.EC2 != *image {
		return CatalogEntry{}, ErrCatalogConflict
	}
	return entry, nil
}

// EC2Image resolves only an available image in the requested account and region.
func (s *Store) EC2Image(ctx context.Context, id, region, accountID string) (EC2Image, error) {
	entry, err := s.Get(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return EC2Image{}, ErrCatalogConflict
	}
	if err != nil {
		return EC2Image{}, err
	}
	image := entry.Artifact.EC2
	if entry.State != "available" || entry.EC2Retired || image == nil || image.Region != region || image.AccountID != accountID {
		return EC2Image{}, ErrCatalogConflict
	}
	resolved := *image
	resolved.Architecture = entry.Artifact.Manifest.Architecture
	if resolved.PublisherAccountID != "" {
		resolved.CatalogSHA256 = entry.Artifact.ManifestSHA256
	}
	return resolved, nil
}
