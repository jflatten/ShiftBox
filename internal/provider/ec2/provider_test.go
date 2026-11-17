package ec2

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	api "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/jflatten/ShiftBox/internal/provider"
)

// TestCreateReconcilesLostResponse verifies that a timed-out launch is found,
// checked for ownership, and stopped without sending a second RunInstances.
func TestCreateReconcilesLostResponse(t *testing.T) {
	const imageID = "ami-11112222333344445"
	ref := provider.WorkspaceRef{InstallationID: "install", WorkspaceID: "workspace", Generation: 1, OwnershipMarker: "marker"}
	request := provider.CreateRequest{Ref: ref, OperationID: "operation", ArtifactID: "artifact", Release: "release",
		NetworkMode: NetworkMode, Resources: ReferenceResources()}
	cfg := Config{Region: "us-east-2", AccountID: "123456789012", SubnetID: "subnet-test", SecurityGroupID: "sg-test", Deployment: "demo"}
	mock := &lostLaunchAPI{cfg: cfg}
	p := &Provider{cfg: cfg, client: mock, poll: time.Millisecond, now: time.Now,
		resolve: func(context.Context, string, string) (Image, error) {
			return Image{ID: imageID, RootDeviceName: "/dev/sda1"}, nil
		}}
	if _, err := p.Create(t.Context(), request); !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Fatalf("Create after lost response = %v, want uncertain result", err)
	}
	got, err := p.Create(t.Context(), request)
	if err != nil || got.Power != provider.Stopped || !got.DiskCapacityKnown || got.Effective != ReferenceResources() {
		t.Fatalf("reconciled Create = %+v, %v, want stopped reference instance", got, err)
	}
	if mock.launches != 1 || mock.stops != 1 || mock.token != launchToken(request) {
		t.Fatalf("AWS mutations = %d launches and %d stops, want one each with a stable token", mock.launches, mock.stops)
	}
}

type lostLaunchAPI struct {
	image types.Image
	client
	cfg                     Config
	instance                *types.Instance
	volume                  types.Volume
	network                 types.NetworkInterface
	launches, starts, stops int
	token, userData         string
	userDataTarget          types.InstanceAttributeName
}

func (m *lostLaunchAPI) DescribeInstances(context.Context, *api.DescribeInstancesInput, ...func(*api.Options)) (*api.DescribeInstancesOutput, error) {
	if m.instance == nil {
		return &api.DescribeInstancesOutput{}, nil
	}
	return &api.DescribeInstancesOutput{Reservations: []types.Reservation{{OwnerId: aws.String(m.cfg.AccountID), Instances: []types.Instance{*m.instance}}}}, nil
}

func (m *lostLaunchAPI) RunInstances(_ context.Context, input *api.RunInstancesInput, _ ...func(*api.Options)) (*api.RunInstancesOutput, error) {
	m.launches++
	m.token = aws.ToString(input.ClientToken)
	tags := input.TagSpecifications[0].Tags
	m.instance = &types.Instance{
		InstanceId: aws.String("i-test"), State: &types.InstanceState{Name: types.InstanceStateNameRunning},
		InstanceType: types.InstanceTypeT3Medium, ImageId: input.ImageId, Tags: tags, RootDeviceName: aws.String("/dev/sda1"),
		MetadataOptions: &types.InstanceMetadataOptionsResponse{HttpTokens: types.HttpTokensStateRequired,
			HttpEndpoint: types.InstanceMetadataEndpointStateEnabled, HttpPutResponseHopLimit: aws.Int32(1)},
		SubnetId: aws.String(m.cfg.SubnetID), SecurityGroups: []types.GroupIdentifier{{GroupId: aws.String(m.cfg.SecurityGroupID)}},
		NetworkInterfaces: []types.InstanceNetworkInterface{{NetworkInterfaceId: aws.String("eni-test"),
			Attachment: &types.InstanceNetworkInterfaceAttachment{DeviceIndex: aws.Int32(0), DeleteOnTermination: aws.Bool(true)}}},
		BlockDeviceMappings: []types.InstanceBlockDeviceMapping{{DeviceName: aws.String("/dev/sda1"),
			Ebs: &types.EbsInstanceBlockDevice{VolumeId: aws.String("vol-test"), DeleteOnTermination: aws.Bool(true)}}},
	}
	m.volume = types.Volume{VolumeId: aws.String("vol-test"), Size: aws.Int32(32), Encrypted: aws.Bool(true), Tags: tags,
		Attachments: []types.VolumeAttachment{{InstanceId: aws.String("i-test")}}}
	m.network = types.NetworkInterface{NetworkInterfaceId: aws.String("eni-test"), TagSet: tags, SubnetId: aws.String(m.cfg.SubnetID),
		Groups:     []types.GroupIdentifier{{GroupId: aws.String(m.cfg.SecurityGroupID)}},
		Attachment: &types.NetworkInterfaceAttachment{InstanceId: aws.String("i-test"), DeviceIndex: aws.Int32(0)}}
	return nil, context.DeadlineExceeded
}

func (m *lostLaunchAPI) StopInstances(context.Context, *api.StopInstancesInput, ...func(*api.Options)) (*api.StopInstancesOutput, error) {
	m.stops++
	m.instance.State.Name = types.InstanceStateNameStopped
	return &api.StopInstancesOutput{}, nil
}

func (m *lostLaunchAPI) DescribeVolumes(context.Context, *api.DescribeVolumesInput, ...func(*api.Options)) (*api.DescribeVolumesOutput, error) {
	if m.volume.VolumeId == nil {
		return &api.DescribeVolumesOutput{}, nil
	}
	return &api.DescribeVolumesOutput{Volumes: []types.Volume{m.volume}}, nil
}

func (m *lostLaunchAPI) DescribeNetworkInterfaces(context.Context, *api.DescribeNetworkInterfacesInput, ...func(*api.Options)) (*api.DescribeNetworkInterfacesOutput, error) {
	if m.network.NetworkInterfaceId == nil {
		return &api.DescribeNetworkInterfacesOutput{}, nil
	}
	return &api.DescribeNetworkInterfacesOutput{NetworkInterfaces: []types.NetworkInterface{m.network}}, nil
}

func (m *lostLaunchAPI) StartInstances(context.Context, *api.StartInstancesInput, ...func(*api.Options)) (*api.StartInstancesOutput, error) {
	m.starts++
	m.instance.State.Name = types.InstanceStateNameRunning
	return &api.StartInstancesOutput{}, nil
}

// TestDeliverEnrollmentConfirmsLostResponse verifies that a lost write reply
// counts as delivered only after AWS reads back the exact seed.
func TestDeliverEnrollmentConfirmsLostResponse(t *testing.T) {
	ref := provider.WorkspaceRef{InstallationID: "install", WorkspaceID: "workspace", Generation: 1, OwnershipMarker: "marker"}
	cfg := Config{Region: "us-east-2", AccountID: "123456789012", SubnetID: "subnet-test", SecurityGroupID: "sg-test", Deployment: "demo"}
	mock := &lostLaunchAPI{cfg: cfg}
	p := &Provider{cfg: cfg, client: mock, poll: time.Millisecond, now: time.Now}
	req := provider.CreateRequest{Ref: ref, OperationID: "operation", ArtifactID: "artifact", Release: "release",
		NetworkMode: NetworkMode, Resources: ReferenceResources()}
	image := Image{ID: "ami-11112222333344445", RootDeviceName: "/dev/sda1"}
	if _, err := p.CreateCandidate(t.Context(), req, image); !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Fatalf("CreateCandidate after lost response = %v, want uncertain result", err)
	}
	stopped, err := p.CreateCandidate(t.Context(), req, image)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.DeliverEnrollment(t.Context(), provider.EnrollmentRequest{
		Ref: stopped.Ref, OperationID: "enroll", Seed: []byte("opaque-seed"), Eligible: true,
	})
	if err != nil || got.MediaID == "" || got.Inventory.Ref != stopped.Ref || got.Inventory.Power != provider.Stopped {
		t.Fatalf("DeliverEnrollment after lost response = %+v, %v, want confirmed stopped delivery", got, err)
	}
	if mock.userDataTarget != types.InstanceAttributeNameUserData {
		t.Fatalf("modify attribute = %q; want the IAM-scoped userData attribute", mock.userDataTarget)
	}

	// A stopped recovery must still see the seed it verified before power-on.
	start := provider.StartRequest{Ref: stopped.Ref, OperationID: "enroll", NetworkMode: NetworkMode,
		RequiresEnrollment: true, EnrollmentMedia: got.MediaID}
	confirmed := mock.userData
	mock.userData = ""
	if _, err := p.Start(t.Context(), start); !errors.Is(err, provider.ErrOutcomeUnknown) || mock.starts != 0 {
		t.Fatalf("Start without the delivered seed = %v (%d AWS starts), want uncertain result and zero starts", err, mock.starts)
	}
	mock.userData = confirmed
	if running, err := p.Start(t.Context(), start); err != nil || running.Ref != stopped.Ref || running.Power != provider.Running || mock.starts != 1 {
		t.Fatalf("Start with the confirmed seed = %v, %s (%d AWS starts), want the same running workspace and one start", err, running.Power, mock.starts)
	}
}

// TestDeleteReconcilesPartialCleanup verifies that neither a terminated instance
// nor a missing instance proves deletion while its disk or interface remains.
func TestDeleteReconcilesPartialCleanup(t *testing.T) {
	ref := provider.WorkspaceRef{InstallationID: "install", WorkspaceID: "workspace", Generation: 1, OwnershipMarker: "marker", InstanceID: "i-test"}
	cfg := Config{AccountID: "123456789012", Deployment: "demo"}
	mock := &lostLaunchAPI{cfg: cfg}
	p := &Provider{cfg: cfg, client: mock, poll: time.Millisecond, now: time.Now}
	mock.instance = &types.Instance{InstanceId: aws.String(ref.InstanceID), Tags: p.ownershipTags(ref),
		State: &types.InstanceState{Name: types.InstanceStateNameTerminated}}
	mock.volume.VolumeId = aws.String("vol-test")
	mock.network.NetworkInterfaceId = aws.String("eni-test")
	if _, err := p.Inspect(t.Context(), ref); !errors.Is(err, provider.ErrPartial) {
		t.Fatalf("Inspect terminated instance with remaining resources = %v, want partial deletion", err)
	}
	mock.instance = nil
	mock.volume.VolumeId = nil
	request := provider.DeleteRequest{Ref: ref, OperationID: "delete"}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := p.Delete(ctx, request); !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Fatalf("Delete with only the interface remaining = %v, want uncertain deletion", err)
	}
	mock.network.NetworkInterfaceId = nil
	got, err := p.Delete(t.Context(), request)
	if err != nil || got.Power != provider.Deleted || got.Ref != ref || len(got.Resources) != 0 {
		t.Fatalf("Delete after resource removal = %+v, %v, want deletion proven for the same workspace", got, err)
	}
}

func (m *lostLaunchAPI) ModifyInstanceAttribute(_ context.Context, input *api.ModifyInstanceAttributeInput, _ ...func(*api.Options)) (*api.ModifyInstanceAttributeOutput, error) {
	m.userDataTarget = input.Attribute
	m.userData = aws.ToString(input.Value)
	return nil, context.DeadlineExceeded
}

func (m *lostLaunchAPI) DescribeInstanceAttribute(context.Context, *api.DescribeInstanceAttributeInput, ...func(*api.Options)) (*api.DescribeInstanceAttributeOutput, error) {
	return &api.DescribeInstanceAttributeOutput{UserData: &types.AttributeValue{Value: aws.String(m.userData)}}, nil
}

// TestInspectImageAllowsCanonicalVirtualMappings checks the captured Ubuntu AMI shape.
func TestInspectImageAllowsCanonicalVirtualMappings(t *testing.T) {
	const account = "123456789012"
	const imageID = "ami-11112222333344445"
	mock := &lostLaunchAPI{image: types.Image{
		ImageId: aws.String(imageID), OwnerId: aws.String(account), Architecture: types.ArchitectureValuesX8664,
		RootDeviceType: types.DeviceTypeEbs, RootDeviceName: aws.String("/dev/sda1"),
		VirtualizationType: types.VirtualizationTypeHvm, State: types.ImageStateAvailable,
		BlockDeviceMappings: []types.BlockDeviceMapping{
			{DeviceName: aws.String("/dev/sda1"), Ebs: &types.EbsBlockDevice{SnapshotId: aws.String("snap-12345678901234567"),
				VolumeSize: aws.Int32(32), Encrypted: aws.Bool(true)}},
			{DeviceName: aws.String("/dev/sdb"), VirtualName: aws.String("ephemeral0")},
			{DeviceName: aws.String("/dev/sdc"), VirtualName: aws.String("ephemeral1")},
		},
	}}
	p := &Provider{cfg: Config{AccountID: account}, client: mock}
	got, err := p.InspectImage(t.Context(), imageID)
	if err != nil || got.SnapshotID != "snap-12345678901234567" {
		t.Fatalf("InspectImage = %+v, %v; want the verified encrypted root", got, err)
	}
	mock.image.BlockDeviceMappings[1].Ebs = &types.EbsBlockDevice{VolumeSize: aws.Int32(32)}
	if _, err := p.InspectImage(t.Context(), imageID); !errors.Is(err, provider.ErrOwnershipMismatch) {
		t.Fatalf("InspectImage with extra EBS volume = %v; want ownership mismatch", err)
	}
}

func (m *lostLaunchAPI) DescribeImages(context.Context, *api.DescribeImagesInput, ...func(*api.Options)) (*api.DescribeImagesOutput, error) {
	return &api.DescribeImagesOutput{Images: []types.Image{m.image}}, nil
}
