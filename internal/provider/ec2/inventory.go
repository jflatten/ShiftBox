package ec2

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	api "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/jflatten/ShiftBox/internal/provider"
)

const (
	tagDeployment   = "ShiftBoxWorkspaceDeployment"
	tagInstallation = "ShiftBoxInstallation"
	tagWorkspace    = "ShiftBoxWorkspace"
	tagGeneration   = "ShiftBoxGeneration"
	tagOwner        = "ShiftBoxOwnership"
	tagProvision    = "ShiftBoxProvisionOperation"
	tagArtifact     = "ShiftBoxArtifact"
	tagRelease      = "ShiftBoxRelease"
)

func validRef(ref provider.WorkspaceRef) bool {
	return ref.InstallationID != "" && ref.WorkspaceID != "" && ref.Generation > 0 && ref.OwnershipMarker != ""
}

func (p *Provider) ownershipTags(ref provider.WorkspaceRef) []types.Tag {
	return []types.Tag{
		{Key: aws.String(tagDeployment), Value: aws.String(p.cfg.Deployment)},
		{Key: aws.String(tagInstallation), Value: aws.String(ref.InstallationID)},
		{Key: aws.String(tagWorkspace), Value: aws.String(ref.WorkspaceID)},
		{Key: aws.String(tagGeneration), Value: aws.String(strconv.FormatInt(ref.Generation, 10))},
		{Key: aws.String(tagOwner), Value: aws.String(ref.OwnershipMarker)},
	}
}

func (p *Provider) ownershipFilters(ref provider.WorkspaceRef) []types.Filter {
	tags := p.ownershipTags(ref)
	filters := make([]types.Filter, 0, len(tags))
	for _, tag := range tags {
		filters = append(filters, types.Filter{Name: aws.String("tag:" + aws.ToString(tag.Key)), Values: []string{aws.ToString(tag.Value)}})
	}
	return filters
}

func tagValues(tags []types.Tag) map[string]string {
	values := make(map[string]string, len(tags))
	for _, tag := range tags {
		values[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return values
}

func (p *Provider) owns(ref provider.WorkspaceRef, tags []types.Tag) bool {
	values := tagValues(tags)
	for _, tag := range p.ownershipTags(ref) {
		if values[aws.ToString(tag.Key)] != aws.ToString(tag.Value) {
			return false
		}
	}
	return true
}

func (p *Provider) findInstance(ctx context.Context, ref provider.WorkspaceRef) (types.Instance, error) {
	if !validRef(ref) {
		return types.Instance{}, provider.ErrInvalidState
	}
	input := &api.DescribeInstancesInput{}
	if ref.InstanceID != "" {
		input.InstanceIds = []string{ref.InstanceID}
	} else {
		input.Filters = p.ownershipFilters(ref)
	}
	result, err := p.client.DescribeInstances(ctx, input)
	if err != nil {
		return types.Instance{}, apiError(err)
	}
	if aws.ToString(result.NextToken) != "" {
		return types.Instance{}, provider.ErrOutcomeUnknown
	}
	var found types.Instance
	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			id := aws.ToString(instance.InstanceId)
			if aws.ToString(reservation.OwnerId) != p.cfg.AccountID || id == "" ||
				(ref.InstanceID != "" && id != ref.InstanceID) ||
				found.InstanceId != nil || !p.owns(ref, instance.Tags) {
				return types.Instance{}, provider.ErrOwnershipMismatch
			}
			state := powerOf(instance)
			if (state == provider.Running || state == provider.Stopped) && !p.matchesPlacement(instance) {
				return types.Instance{}, provider.ErrOwnershipMismatch
			}
			found = instance
		}
	}
	if found.InstanceId == nil {
		return types.Instance{}, provider.ErrNotFound
	}
	return found, nil
}

func (p *Provider) matchesPlacement(instance types.Instance) bool {
	groups := instance.SecurityGroups
	options := instance.MetadataOptions
	architecture := p.instanceArchitecture(string(instance.InstanceType))
	return architecture != "" && architecture == platformArchitecture(string(instance.Architecture)) && aws.ToString(instance.SubnetId) == p.cfg.SubnetID && len(groups) == 1 &&
		aws.ToString(groups[0].GroupId) == p.cfg.SecurityGroupID &&
		aws.ToString(instance.PublicIpAddress) == "" && instance.IamInstanceProfile == nil &&
		options != nil && options.HttpTokens == types.HttpTokensStateRequired &&
		options.HttpEndpoint == types.InstanceMetadataEndpointStateEnabled && aws.ToInt32(options.HttpPutResponseHopLimit) == 1
}

func powerOf(instance types.Instance) provider.PowerState {
	if instance.State == nil {
		return provider.Unknown
	}
	switch instance.State.Name {
	case types.InstanceStateNamePending:
		return provider.Starting
	case types.InstanceStateNameRunning:
		return provider.Running
	case types.InstanceStateNameStopping, types.InstanceStateNameShuttingDown:
		return provider.Stopping
	case types.InstanceStateNameStopped:
		return provider.Stopped
	case types.InstanceStateNameTerminated:
		return provider.Deleted
	default:
		return provider.Unknown
	}
}

func (p *Provider) waitInstance(ctx context.Context, ref provider.WorkspaceRef, want ...provider.PowerState) (types.Instance, error) {
	ticker := time.NewTicker(p.poll)
	defer ticker.Stop()
	for {
		instance, err := p.findInstance(ctx, ref)
		if err == nil {
			for _, state := range want {
				if powerOf(instance) == state {
					return instance, nil
				}
			}
			if powerOf(instance) == provider.Deleted {
				return types.Instance{}, provider.ErrOutcomeUnknown
			}
		} else if !errors.Is(err, provider.ErrNotFound) && !errors.Is(err, provider.ErrOutcomeUnknown) {
			return types.Instance{}, err
		}
		select {
		case <-ctx.Done():
			return types.Instance{}, provider.ErrOutcomeUnknown
		case <-ticker.C:
		}
	}
}

// Inspect reads the exact installation, workspace, and owned EC2 resources.
func (p *Provider) Inspect(ctx context.Context, ref provider.WorkspaceRef) (provider.Inventory, error) {
	instance, err := p.findInstance(ctx, ref)
	if err != nil {
		return provider.Inventory{}, err
	}
	if powerOf(instance) == provider.Deleted {
		ref.InstanceID = aws.ToString(instance.InstanceId)
		return p.confirmRemoved(ctx, ref)
	}
	return p.inventory(ctx, ref, instance)
}

func (p *Provider) inventory(ctx context.Context, ref provider.WorkspaceRef, instance types.Instance) (provider.Inventory, error) {
	ref.InstanceID = aws.ToString(instance.InstanceId)
	tags := tagValues(instance.Tags)
	if tags[tagArtifact] == "" || tags[tagRelease] == "" {
		return provider.Inventory{}, provider.ErrOwnershipMismatch
	}
	result := provider.Inventory{Ref: ref, InstanceID: ref.InstanceID, Power: powerOf(instance),
		ArtifactID: tags[tagArtifact], Release: tags[tagRelease], NetworkMode: NetworkMode, ObservedAt: p.now(), EC2Enrollment: true}
	if result.Power == provider.Starting || result.Power == provider.Stopping {
		// EC2 can omit attachment fields during transitional states.
		return result, nil
	}
	diskID, nicID, err := instanceAttachments(instance)
	if err != nil {
		return provider.Inventory{}, err
	}
	disk, err := p.verifyDisk(ctx, ref, diskID)
	if err != nil {
		return provider.Inventory{}, err
	}
	if err := p.verifyInterface(ctx, ref, nicID); err != nil {
		return provider.Inventory{}, err
	}
	resources, supported := p.instanceResources(string(instance.InstanceType))
	if !supported {
		return provider.Inventory{}, provider.ErrOwnershipMismatch
	}
	resources.DiskBytes = int64(disk) << 30
	result.Effective = resources
	result.DiskCapacityKnown = true
	result.Resources = []provider.OwnedResource{
		{ID: ref.InstanceID, Type: "vm", OwnershipMarker: ref.OwnershipMarker, Generation: ref.Generation},
		{ID: diskID, Type: "disk", OwnershipMarker: ref.OwnershipMarker, Generation: ref.Generation},
		{ID: nicID, Type: "network", OwnershipMarker: ref.OwnershipMarker, Generation: ref.Generation},
	}
	return result, nil
}

func instanceAttachments(instance types.Instance) (diskID, nicID string, err error) {
	if len(instance.BlockDeviceMappings) != 1 || len(instance.NetworkInterfaces) != 1 {
		return "", "", provider.ErrOutcomeUnknown
	}
	disk := instance.BlockDeviceMappings[0]
	nic := instance.NetworkInterfaces[0]
	if aws.ToString(disk.DeviceName) != aws.ToString(instance.RootDeviceName) || disk.Ebs == nil ||
		!aws.ToBool(disk.Ebs.DeleteOnTermination) || nic.Attachment == nil ||
		aws.ToInt32(nic.Attachment.DeviceIndex) != 0 || !aws.ToBool(nic.Attachment.DeleteOnTermination) {
		return "", "", provider.ErrOwnershipMismatch
	}
	diskID, nicID = aws.ToString(disk.Ebs.VolumeId), aws.ToString(nic.NetworkInterfaceId)
	if diskID == "" || nicID == "" {
		return "", "", provider.ErrOutcomeUnknown
	}
	return diskID, nicID, nil
}

func (p *Provider) verifyDisk(ctx context.Context, ref provider.WorkspaceRef, id string) (int32, error) {
	result, err := p.client.DescribeVolumes(ctx, &api.DescribeVolumesInput{VolumeIds: []string{id}})
	if err != nil {
		return 0, apiError(err)
	}
	if len(result.Volumes) != 1 {
		return 0, provider.ErrOutcomeUnknown
	}
	disk := result.Volumes[0]
	if aws.ToString(disk.VolumeId) != id || !validRootDiskBytes(int64(aws.ToInt32(disk.Size))<<30) || !aws.ToBool(disk.Encrypted) ||
		!p.owns(ref, disk.Tags) || len(disk.Attachments) != 1 || aws.ToString(disk.Attachments[0].InstanceId) != ref.InstanceID {
		return 0, provider.ErrOwnershipMismatch
	}
	return aws.ToInt32(disk.Size), nil
}

func (p *Provider) verifyInterface(ctx context.Context, ref provider.WorkspaceRef, id string) error {
	result, err := p.client.DescribeNetworkInterfaces(ctx, &api.DescribeNetworkInterfacesInput{NetworkInterfaceIds: []string{id}})
	if err != nil {
		return apiError(err)
	}
	if len(result.NetworkInterfaces) != 1 {
		return provider.ErrOutcomeUnknown
	}
	nic := result.NetworkInterfaces[0]
	if aws.ToString(nic.NetworkInterfaceId) != id || aws.ToString(nic.SubnetId) != p.cfg.SubnetID ||
		!p.owns(ref, nic.TagSet) || len(nic.Groups) != 1 || aws.ToString(nic.Groups[0].GroupId) != p.cfg.SecurityGroupID ||
		nic.Association != nil || nic.Attachment == nil || aws.ToString(nic.Attachment.InstanceId) != ref.InstanceID ||
		aws.ToInt32(nic.Attachment.DeviceIndex) != 0 {
		return provider.ErrOwnershipMismatch
	}
	return nil
}

func terminating(instance types.Instance) bool {
	return instance.State != nil &&
		(instance.State.Name == types.InstanceStateNameShuttingDown || instance.State.Name == types.InstanceStateNameTerminated)
}
