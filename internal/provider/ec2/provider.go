// Package ec2 provides the private AWS workspace provider.
package ec2

import (
	"context"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials/endpointcreds"
	api "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/release"
)

// NetworkMode is the provider-managed private subnet with shared outbound access.
const NetworkMode = "shared"

const instanceType = types.InstanceTypeT3Medium

// Keep the boot volume below the size that requires validating GPT and GRUB2.
const maximumRootDiskGiB = 2047
const minimumRootDiskGiB = 32

var (
	taskPathPattern = regexp.MustCompile(`^/v2/credentials/[A-Za-z0-9-]+$`)
	accountPattern  = regexp.MustCompile(`^[0-9]{12}$`)
	regionPattern   = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[1-9][0-9]*$`)
	subnetPattern   = regexp.MustCompile(`^subnet-[0-9a-f]{8}([0-9a-f]{9})?$`)
	groupPattern    = regexp.MustCompile(`^sg-[0-9a-f]{8}([0-9a-f]{9})?$`)
)

// Config fixes the AWS identity, network placement, and deployment tag for the provider.
type Config struct {
	Region, AccountID, SubnetID, SecurityGroupID, Deployment, Environment string
	NamespacedTags                                                        bool
	OfficialImage                                                         *release.OfficialEC2Image
}

// Image identifies the immutable AMI and its verified root disk mapping.
type Image struct {
	ID, RootDeviceName, SnapshotID, Architecture   string
	Artifact, ReleaseID, SourceAMI, SourceRevision string
	CreationDate                                   string
	SingleBoot                                     bool
}

// ImageResolver resolves the validated AMI bound to a catalog artifact and release.
type ImageResolver func(ctx context.Context, artifactID, releaseID string) (Image, error)

type client interface {
	api.DescribeInstanceTypesAPIClient
	api.DescribeInstanceTypeOfferingsAPIClient
	RunInstances(context.Context, *api.RunInstancesInput, ...func(*api.Options)) (*api.RunInstancesOutput, error)
	DescribeInstances(context.Context, *api.DescribeInstancesInput, ...func(*api.Options)) (*api.DescribeInstancesOutput, error)
	DescribeVolumes(context.Context, *api.DescribeVolumesInput, ...func(*api.Options)) (*api.DescribeVolumesOutput, error)
	DescribeNetworkInterfaces(context.Context, *api.DescribeNetworkInterfacesInput, ...func(*api.Options)) (*api.DescribeNetworkInterfacesOutput, error)
	DescribeImages(context.Context, *api.DescribeImagesInput, ...func(*api.Options)) (*api.DescribeImagesOutput, error)
	DescribeSubnets(context.Context, *api.DescribeSubnetsInput, ...func(*api.Options)) (*api.DescribeSubnetsOutput, error)
	DescribeSecurityGroups(context.Context, *api.DescribeSecurityGroupsInput, ...func(*api.Options)) (*api.DescribeSecurityGroupsOutput, error)
	DescribeInstanceAttribute(context.Context, *api.DescribeInstanceAttributeInput, ...func(*api.Options)) (*api.DescribeInstanceAttributeOutput, error)
	ModifyInstanceAttribute(context.Context, *api.ModifyInstanceAttributeInput, ...func(*api.Options)) (*api.ModifyInstanceAttributeOutput, error)
	StartInstances(context.Context, *api.StartInstancesInput, ...func(*api.Options)) (*api.StartInstancesOutput, error)
	StopInstances(context.Context, *api.StopInstancesInput, ...func(*api.Options)) (*api.StopInstancesOutput, error)
	TerminateInstances(context.Context, *api.TerminateInstancesInput, ...func(*api.Options)) (*api.TerminateInstancesOutput, error)
}

// Provider owns EC2 side effects for one validated deployment.
type Provider struct {
	cfg           Config
	client        client
	resolve       ImageResolver
	poll          time.Duration
	now           func() time.Time
	zone          string
	instanceTypes map[string]provider.InstanceType
	offeredTypes  []provider.InstanceType
}

// New uses explicitly scoped ECS task credentials and verifies the AWS account and private network.
func New(ctx context.Context, cfg Config, resolve ImageResolver) (*Provider, error) {
	if !regionPattern.MatchString(cfg.Region) || !accountPattern.MatchString(cfg.AccountID) ||
		!subnetPattern.MatchString(cfg.SubnetID) || !groupPattern.MatchString(cfg.SecurityGroupID) ||
		cfg.Deployment == "" || len(cfg.Deployment) > 256 || len(cfg.Environment) > 256 || resolve == nil {
		return nil, provider.ErrInvalidState
	}
	if cfg.OfficialImage != nil {
		pin := *cfg.OfficialImage
		if err := pin.Validate(cfg.Region, cfg.AccountID); err != nil {
			return nil, provider.ErrInvalidState
		}
		cfg.OfficialImage = &pin
	}
	path := os.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI")
	if !taskPathPattern.MatchString(path) {
		return nil, provider.ErrPermission
	}
	credentialHTTP := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	creds := endpointcreds.New("http://169.254.170.2"+path, func(options *endpointcreds.Options) {
		options.HTTPClient = credentialHTTP
	})
	sdk := aws.Config{Region: cfg.Region, Credentials: aws.NewCredentialsCache(creds)}
	identity, err := sts.NewFromConfig(sdk).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, apiError(err)
	}
	if aws.ToString(identity.Account) != cfg.AccountID {
		return nil, provider.ErrOwnershipMismatch
	}
	if cfg.Environment == "" {
		cfg.Environment = "private"
	}
	p := &Provider{cfg: cfg, client: api.NewFromConfig(sdk), resolve: resolve, poll: 2 * time.Second, now: time.Now}
	if err := p.verifyNetwork(ctx); err != nil {
		return nil, err
	}
	if err := p.loadInstanceTypes(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *Provider) verifyNetwork(ctx context.Context) error {
	subnet, err := p.client.DescribeSubnets(ctx, &api.DescribeSubnetsInput{SubnetIds: []string{p.cfg.SubnetID}})
	if err != nil {
		return apiError(err)
	}
	if len(subnet.Subnets) != 1 {
		return provider.ErrInvalidState
	}
	s := subnet.Subnets[0]
	// RunInstances explicitly disables public IPv4 on the primary interface.
	// The subnet's default only applies to launches that omit that setting.
	if aws.ToString(s.SubnetId) != p.cfg.SubnetID || aws.ToString(s.OwnerId) != p.cfg.AccountID ||
		s.State != types.SubnetStateAvailable || aws.ToString(s.AvailabilityZone) == "" {
		return provider.ErrOwnershipMismatch
	}
	groups, err := p.client.DescribeSecurityGroups(ctx, &api.DescribeSecurityGroupsInput{GroupIds: []string{p.cfg.SecurityGroupID}})
	if err != nil {
		return apiError(err)
	}
	if len(groups.SecurityGroups) != 1 {
		return provider.ErrInvalidState
	}
	g := groups.SecurityGroups[0]
	if aws.ToString(g.GroupId) != p.cfg.SecurityGroupID || aws.ToString(g.OwnerId) != p.cfg.AccountID ||
		aws.ToString(g.VpcId) == "" || aws.ToString(g.VpcId) != aws.ToString(s.VpcId) || len(g.IpPermissions) != 0 {
		return provider.ErrOwnershipMismatch
	}
	p.zone = aws.ToString(s.AvailabilityZone)
	return nil
}

// ReferenceResources returns the pinned shape used for platform canaries and legacy workspaces.
func ReferenceResources() provider.Resources {
	return provider.Resources{InstanceType: string(instanceType), CPU: 2, MemoryBytes: 4 << 30, DiskBytes: minimumRootDiskGiB << 30}
}

func validRootDiskBytes(disk int64) bool {
	return disk >= minimumRootDiskGiB<<30 && disk <= maximumRootDiskGiB<<30 && disk%(1<<30) == 0
}

// Capabilities describes the supported EC2 operations. Catalog admission gates actual launches.
func (p *Provider) Capabilities(context.Context) (provider.Capabilities, error) {
	caps := provider.Capabilities{Name: "ec2", Version: "1", Architecture: release.EC2ReferenceArchitecture, Architectures: release.EC2PlatformArchitectures(), ExecutionMode: "aws",
		Evidence: "Verified AWS account and private workspace network",
		Minimum:  provider.Resources{DiskBytes: minimumRootDiskGiB << 30},
		Maximum:  provider.Resources{DiskBytes: maximumRootDiskGiB << 30},
		Create:   true, Start: true, Stop: true, Delete: true, NetworkModes: []string{NetworkMode},
		NetworkStoppedOnly: true, RecoveryMechanisms: []string{"ec2-user-data"}}
	caps.InstanceTypes = offeredInstanceTypes(p.offeredTypes, caps)
	if pin := p.cfg.OfficialImage; pin != nil {
		caps.OfficialImage = &provider.OfficialAMI{ReleaseID: pin.ReleaseID, Region: p.cfg.Region, AMIID: pin.AMIID}
	}
	return caps, nil
}

// InspectImage verifies an encrypted private AMI owned by the deployment account.
func (p *Provider) InspectImage(ctx context.Context, imageID string) (Image, error) {
	return p.inspectImage(ctx, imageID, p.cfg.AccountID, nil)
}

// InspectOfficialImage verifies the deployment-pinned AMI against EC2.
func (p *Provider) InspectOfficialImage(ctx context.Context) (Image, error) {
	pin := p.cfg.OfficialImage
	if pin == nil {
		return Image{}, provider.ErrCapabilityUnsupported
	}
	return p.inspectImage(ctx, pin.AMIID, pin.PublisherAccountID, pin)
}

func (p *Provider) inspectImage(ctx context.Context, imageID, owner string, official *release.OfficialEC2Image) (Image, error) {
	result, err := p.client.DescribeImages(ctx, &api.DescribeImagesInput{ImageIds: []string{imageID}, Owners: []string{owner}})
	if err != nil {
		return Image{}, apiError(err)
	}
	if len(result.Images) != 1 {
		return Image{}, provider.ErrNotFound
	}
	image := result.Images[0]
	architecture := platformArchitecture(string(image.Architecture))
	public := official != nil && owner != p.cfg.AccountID
	if aws.ToString(image.ImageId) != imageID || aws.ToString(image.OwnerId) != owner || aws.ToBool(image.Public) != public ||
		!release.SupportsEC2PlatformArchitecture(architecture) || image.RootDeviceType != types.DeviceTypeEbs ||
		image.VirtualizationType != types.VirtualizationTypeHvm || image.State != types.ImageStateAvailable {
		return Image{}, provider.ErrOwnershipMismatch
	}
	root := aws.ToString(image.RootDeviceName)
	if official != nil {
		if root != official.RootDeviceName || architecture != official.Architecture || len(image.ProductCodes) != 0 || image.Platform != "" {
			return Image{}, provider.ErrOwnershipMismatch
		}
		if at := aws.ToString(image.DeprecationTime); at != "" {
			expiry, err := time.Parse(time.RFC3339, at)
			if err != nil {
				return Image{}, provider.ErrInvalidState
			}
			if !expiry.After(p.now()) {
				return Image{}, provider.ErrNotFound
			}
		}
	}
	var rootDisk *types.EbsBlockDevice
	for _, device := range image.BlockDeviceMappings {
		if aws.ToString(device.DeviceName) == root && device.Ebs != nil && rootDisk == nil {
			rootDisk = device.Ebs
			continue
		}
		// Canonical AMIs retain virtual instance-store mappings. Only the
		// EBS root disk participates in durable workspace allocation.
		if device.Ebs != nil || aws.ToString(device.DeviceName) == root ||
			!strings.HasPrefix(aws.ToString(device.VirtualName), "ephemeral") {
			return Image{}, provider.ErrOwnershipMismatch
		}
	}
	// Private AMIs remain encrypted. A public AMI uses an unencrypted source snapshot;
	// RunInstances still requests an encrypted workspace root volume for either path.
	encryptedSnapshot := !public
	if root == "" || rootDisk == nil || aws.ToBool(rootDisk.Encrypted) != encryptedSnapshot ||
		aws.ToString(rootDisk.SnapshotId) == "" || aws.ToInt32(rootDisk.VolumeSize) < 1 || aws.ToInt32(rootDisk.VolumeSize) > minimumRootDiskGiB {
		return Image{}, provider.ErrInvalidState
	}
	observed := Image{ID: imageID, Architecture: architecture, RootDeviceName: root, SnapshotID: aws.ToString(rootDisk.SnapshotId),
		CreationDate: aws.ToString(image.CreationDate)}
	if official != nil {
		if observed.SnapshotID != official.RootSnapshotID || aws.ToInt32(rootDisk.VolumeSize) > official.MinimumDiskGiB {
			return Image{}, provider.ErrOwnershipMismatch
		}
		if !public {
			tags := tagValues(image.Tags)
			if tags[tagArtifact] != "workspace-ami" || tags[tagRelease] != official.ReleaseID ||
				tags["ShiftBoxSourceAMI"] != official.SourceAMI || tags["ShiftBoxSourceCommit"] != official.SourceCommit {
				return Image{}, provider.ErrOwnershipMismatch
			}
		}
		observed.SingleBoot = true
		return observed, nil
	}
	tags := tagValues(image.Tags)
	observed.Artifact, observed.ReleaseID = tags[tagArtifact], tags[tagRelease]
	observed.SourceAMI, observed.SourceRevision = tags["ShiftBoxSourceAMI"], tags["ShiftBoxSourceCommit"]
	return observed, nil
}

func apiError(err error) error {
	var fault smithy.APIError
	if errors.As(err, &fault) {
		switch fault.ErrorCode() {
		case "UnauthorizedOperation", "AuthFailure", "AccessDenied", "AccessDeniedException", "InvalidClientTokenId":
			return provider.ErrPermission
		case "InvalidInstanceID.NotFound", "InvalidAMIID.NotFound", "InvalidVolume.NotFound", "InvalidNetworkInterfaceID.NotFound":
			return provider.ErrNotFound
		case "IdempotentParameterMismatch":
			return provider.ErrOwnershipMismatch
		}
	}
	// Avoid exposing an AWS payload, such as user data, in errors or logs.
	return provider.ErrOutcomeUnknown
}
