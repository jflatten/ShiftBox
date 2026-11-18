package ec2

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	api "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/jflatten/ShiftBox/internal/provider"
)

// Create starts the pinned AMI and reconciles its initial enrollment power state.
func (p *Provider) Create(ctx context.Context, req provider.CreateRequest) (provider.Inventory, error) {
	image, err := p.resolve(ctx, req.ArtifactID, req.Release)
	if err != nil {
		return provider.Inventory{}, err
	}
	return p.create(ctx, req, image)
}

// CreateCandidate boots a disposable validation instance from a previously inspected AMI.
func (p *Provider) CreateCandidate(ctx context.Context, req provider.CreateRequest, image Image) (provider.Inventory, error) {
	if (req.BootstrapData != "") != image.SingleBoot {
		return provider.Inventory{}, provider.ErrCapabilityUnsupported
	}
	return p.create(ctx, req, image)
}

func (p *Provider) create(ctx context.Context, req provider.CreateRequest, image Image) (provider.Inventory, error) {
	bootOnce := req.BootstrapData != ""
	if bootOnce && (!image.SingleBoot || len(req.BootstrapData) > enrollmentSeedLimit) {
		return provider.Inventory{}, provider.ErrCapabilityUnsupported
	}
	compute, supported := p.instanceResources(req.Resources.InstanceType)
	disk := req.Resources.DiskBytes
	compute.DiskBytes = disk
	architecture := p.instanceArchitecture(req.Resources.InstanceType)
	if !supported || architecture == "" || architecture != image.Architecture ||
		req.Resources != compute || !validRootDiskBytes(disk) {
		return provider.Inventory{}, provider.ErrCapabilityUnsupported
	}
	if !validRef(req.Ref) || req.OperationID == "" || req.ArtifactID == "" || req.Release == "" ||
		req.NetworkMode != NetworkMode || req.EnrollmentMedia != "" ||
		!strings.HasPrefix(image.ID, "ami-") || !strings.HasPrefix(image.RootDeviceName, "/dev/") ||
		path.Clean(image.RootDeviceName) != image.RootDeviceName {
		return provider.Inventory{}, provider.ErrInvalidState
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	instance, err := p.findInstance(ctx, req.Ref)
	switch {
	case errors.Is(err, provider.ErrNotFound) && req.Ref.InstanceID == "":
		result, err := p.client.RunInstances(ctx, p.launchInput(req, image))
		if err != nil {
			return provider.Inventory{}, apiError(err)
		}
		if len(result.Instances) != 1 || aws.ToString(result.Instances[0].InstanceId) == "" {
			return provider.Inventory{}, provider.ErrOutcomeUnknown
		}
		req.Ref.InstanceID = aws.ToString(result.Instances[0].InstanceId)
	case err != nil:
		return provider.Inventory{}, err
	default:
		req.Ref.InstanceID = aws.ToString(instance.InstanceId)
	}
	instance, err = p.waitInstance(ctx, req.Ref, provider.Running, provider.Stopping, provider.Stopped)
	if err != nil {
		return provider.Inventory{}, err
	}
	tags := tagValues(instance.Tags)
	if tags[tagProvision] != req.OperationID || tags[tagArtifact] != req.ArtifactID || tags[tagRelease] != req.Release ||
		string(instance.InstanceType) != req.Resources.InstanceType || aws.ToString(instance.ImageId) != image.ID || aws.ToString(instance.RootDeviceName) != image.RootDeviceName {
		return provider.Inventory{}, provider.ErrOwnershipMismatch
	}
	if bootOnce {
		if powerOf(instance) != provider.Running || aws.ToString(instance.ClientToken) != launchToken(req) {
			return provider.Inventory{}, provider.ErrOwnershipMismatch
		}
		return p.launchInventory(ctx, req, instance)
	}
	if powerOf(instance) == provider.Running {
		if _, err := p.client.StopInstances(ctx, &api.StopInstancesInput{InstanceIds: []string{req.Ref.InstanceID}}); err != nil {
			return provider.Inventory{}, apiError(err)
		}
	}
	instance, err = p.waitInstance(ctx, req.Ref, provider.Stopped)
	if err != nil {
		return provider.Inventory{}, err
	}
	return p.launchInventory(ctx, req, instance)
}

func (p *Provider) launchInventory(ctx context.Context, req provider.CreateRequest, instance types.Instance) (provider.Inventory, error) {
	got, err := p.inventory(ctx, req.Ref, instance)
	if err == nil && got.Effective != req.Resources {
		return provider.Inventory{}, provider.ErrOutcomeUnknown
	}
	return got, err
}

func launchToken(req provider.CreateRequest) string {
	// Hash the verified operation scope, then reuse the token on timeout and restart.
	sum := sha256.Sum256([]byte(req.Ref.InstallationID + "\x00" + req.Ref.WorkspaceID + "\x00" +
		strconv.FormatInt(req.Ref.Generation, 10) + "\x00" + req.OperationID))
	return hex.EncodeToString(sum[:])
}

func (p *Provider) launchInput(req provider.CreateRequest, image Image) *api.RunInstancesInput {
	tagSet := append(p.ownershipTags(req.Ref),
		types.Tag{Key: aws.String(tagProvision), Value: aws.String(req.OperationID)},
		types.Tag{Key: aws.String(tagArtifact), Value: aws.String(req.ArtifactID)},
		types.Tag{Key: aws.String(tagRelease), Value: aws.String(req.Release)},
		types.Tag{Key: aws.String("project"), Value: aws.String("shiftbox")},
		types.Tag{Key: aws.String("environment"), Value: aws.String(p.cfg.Environment)},
		types.Tag{Key: aws.String("cluster_name"), Value: aws.String(p.cfg.Deployment)},
	)
	input := &api.RunInstancesInput{
		ClientToken: aws.String(launchToken(req)), ImageId: aws.String(image.ID), InstanceType: types.InstanceType(req.Resources.InstanceType),
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1),
		MetadataOptions: &types.InstanceMetadataOptionsRequest{HttpTokens: types.HttpTokensStateRequired,
			HttpEndpoint: types.InstanceMetadataEndpointStateEnabled, HttpPutResponseHopLimit: aws.Int32(1)},
		BlockDeviceMappings: []types.BlockDeviceMapping{{DeviceName: aws.String(image.RootDeviceName),
			Ebs: &types.EbsBlockDevice{DeleteOnTermination: aws.Bool(true), Encrypted: aws.Bool(true),
				VolumeInitializationRate: aws.Int32(300), VolumeType: types.VolumeTypeGp3, VolumeSize: aws.Int32(int32(req.Resources.DiskBytes >> 30))}}},
		NetworkInterfaces: []types.InstanceNetworkInterfaceSpecification{{AssociatePublicIpAddress: aws.Bool(false),
			DeleteOnTermination: aws.Bool(true), DeviceIndex: aws.Int32(0), SubnetId: aws.String(p.cfg.SubnetID),
			Groups: []string{p.cfg.SecurityGroupID}}},
		TagSpecifications: []types.TagSpecification{
			{ResourceType: types.ResourceTypeInstance, Tags: tagSet},
			{ResourceType: types.ResourceTypeVolume, Tags: tagSet},
			{ResourceType: types.ResourceTypeNetworkInterface, Tags: tagSet},
		},
	}
	if req.BootstrapData != "" {
		input.UserData = aws.String(base64.StdEncoding.EncodeToString([]byte(req.BootstrapData)))
	}
	return input
}
