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
)

// ValidEC2FamilyName reports whether a name can identify an AMI family.
func ValidEC2FamilyName(name string) bool {
	return ec2FamilyPattern.MatchString(name)
}

// EC2Image pins one account-owned regional AMI and its build provenance.
type EC2Image struct {
	Architecture           string `json:"-"`
	AMIID                  string `json:"amiId"`
	Region                 string `json:"region"`
	AccountID              string `json:"accountId"`
	RootSnapshotID         string `json:"rootSnapshotId"`
	RootDeviceName         string `json:"rootDeviceName"`
	MinimumRootDiskGiB     int32  `json:"minimumRootDiskGiB"`
	ReleaseID              string `json:"releaseId"`
	SourceAMI              string `json:"sourceAmiId"`
	SourceOwner            string `json:"sourceAmiOwnerId"`
	SourceRevision         string `json:"sourceRevision"`
	GuestUpdateKeySHA256   string `json:"guestUpdateKeySHA256"`
	ToolchainRecipeSHA256  string `json:"toolchainRecipeSHA256"`
	CodeServerRecipeSHA256 string `json:"codeServerRecipeSHA256"`
	Family                 string `json:"family,omitempty"`
	CreatedAtUnixMillis    int64  `json:"createdAtUnixMillis,omitempty"`
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
		ReleaseID: data["releaseId"], SourceAMI: data["sourceAmiId"], SourceOwner: data["sourceAmiOwnerId"],
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
		(i.Family == "" && i.CreatedAtUnixMillis == 0 || ValidEC2FamilyName(i.Family) && i.CreatedAtUnixMillis > 0) &&
		ec2SnapshotPattern.MatchString(i.RootSnapshotID) && i.RootDeviceName == "/dev/sda1" &&
		i.AMIID == artifact.Bundle && validDigest(artifact.ManifestSHA256) &&
		m.SchemaVersion == 1 && m.ReleaseID == i.ReleaseID && SupportsEC2PlatformArchitecture(m.Architecture) &&
		m.PlatformVersion == "24.04" && m.ProtocolVersion == "1" && len(m.Resources) == 0 &&
		m.Provenance.Source == i.SourceAMI && m.Provenance.Revision == i.SourceRevision &&
		ec2ReleasePattern.MatchString(m.Guest.Version) && validDigest(m.Guest.SHA256)
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
	return resolved, nil
}
