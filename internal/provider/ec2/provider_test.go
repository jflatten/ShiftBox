package ec2

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	api "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/release"
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
			return Image{ID: imageID, RootDeviceName: "/dev/sda1", Architecture: "amd64"}, nil
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

// TestCreateSelectedShape proves the pinned instance type and EBS size reach AWS
// and that reconciliation reports the observed disk size.
func TestCreateSelectedShape(t *testing.T) {
	resources := provider.Resources{InstanceType: "m6i.xlarge", CPU: 4, MemoryBytes: 16 << 30, DiskBytes: 64 << 30}
	req := provider.CreateRequest{
		Ref:         provider.WorkspaceRef{InstallationID: "install", WorkspaceID: "workspace", Generation: 1, OwnershipMarker: "marker"},
		OperationID: "operation", ArtifactID: "artifact", Release: "release", NetworkMode: NetworkMode, Resources: resources,
	}
	cfg := Config{AccountID: "123456789012", SubnetID: "subnet-test", SecurityGroupID: "sg-test", Deployment: "demo"}
	mock := &lostLaunchAPI{cfg: cfg}
	p := &Provider{cfg: cfg, client: mock, poll: time.Millisecond, now: time.Now,
		instanceTypes: map[string]provider.InstanceType{"m6i.xlarge": {Name: "m6i.xlarge", Architecture: "amd64", CPUCount: 4, MemoryMiB: 16384}}}
	image := Image{ID: "ami-11112222333344445", RootDeviceName: "/dev/sda1", Architecture: "amd64"}
	wrong := image
	wrong.Architecture = "arm64"
	if _, err := p.CreateCandidate(t.Context(), req, wrong); !errors.Is(err, provider.ErrCapabilityUnsupported) || mock.launches != 0 {
		t.Fatalf("CreateCandidate with mismatched AMI = %v and %d launches; want refusal before AWS mutation", err, mock.launches)
	}
	if _, err := p.CreateCandidate(t.Context(), req, image); !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Fatalf("CreateCandidate after lost response = %v, want uncertain result", err)
	}
	got, err := p.CreateCandidate(t.Context(), req, image)
	if err != nil || got.Effective != resources || mock.launches != 1 || mock.instance.InstanceType != types.InstanceType("m6i.xlarge") || aws.ToInt32(mock.volume.Size) != 64 {
		t.Fatalf("CreateCandidate effective = %+v, %v; type/disk = %s/%d; want %+v and one launch", got.Effective, err, mock.instance.InstanceType, aws.ToInt32(mock.volume.Size), resources)
	}
	mock.volume.Size = aws.Int32(32)
	if _, err := p.CreateCandidate(t.Context(), req, image); !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Fatalf("CreateCandidate after disk capacity drift = %v, want uncertain result", err)
	}
}

type lostLaunchAPI struct {
	image types.Image
	client
	cfg                     Config
	lastImageQuery          *api.DescribeImagesInput
	lastLaunch              *api.RunInstancesInput
	imageAbsent             bool
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
	m.lastLaunch = input
	m.token = aws.ToString(input.ClientToken)
	m.userData = aws.ToString(input.UserData)
	tags := input.TagSpecifications[0].Tags
	m.instance = &types.Instance{
		InstanceId: aws.String("i-test"), ClientToken: input.ClientToken, State: &types.InstanceState{Name: types.InstanceStateNameRunning},
		InstanceType: input.InstanceType, Architecture: types.ArchitectureValuesX8664, ImageId: input.ImageId, Tags: tags, RootDeviceName: aws.String("/dev/sda1"),
		MetadataOptions: &types.InstanceMetadataOptionsResponse{HttpTokens: types.HttpTokensStateRequired,
			HttpEndpoint: types.InstanceMetadataEndpointStateEnabled, HttpPutResponseHopLimit: aws.Int32(1)},
		SubnetId: aws.String(m.cfg.SubnetID), SecurityGroups: []types.GroupIdentifier{{GroupId: aws.String(m.cfg.SecurityGroupID)}},
		NetworkInterfaces: []types.InstanceNetworkInterface{{NetworkInterfaceId: aws.String("eni-test"),
			Attachment: &types.InstanceNetworkInterfaceAttachment{DeviceIndex: aws.Int32(0), DeleteOnTermination: aws.Bool(true)}}},
		BlockDeviceMappings: []types.InstanceBlockDeviceMapping{{DeviceName: aws.String("/dev/sda1"),
			Ebs: &types.EbsInstanceBlockDevice{VolumeId: aws.String("vol-test"), DeleteOnTermination: aws.Bool(true)}}},
	}
	m.volume = types.Volume{VolumeId: aws.String("vol-test"), Size: input.BlockDeviceMappings[0].Ebs.VolumeSize, Encrypted: aws.Bool(true), Tags: tags,
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
	image := Image{ID: "ami-11112222333344445", RootDeviceName: "/dev/sda1", Architecture: "amd64"}
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

func (m *lostLaunchAPI) DescribeImages(_ context.Context, input *api.DescribeImagesInput, _ ...func(*api.Options)) (*api.DescribeImagesOutput, error) {
	m.lastImageQuery = input
	if m.imageAbsent {
		return &api.DescribeImagesOutput{}, nil
	}
	return &api.DescribeImagesOutput{Images: []types.Image{m.image}}, nil
}

// TestWorkspaceRootInitialization checks the requested hydration rate when a
// fresh workspace boots from its platform AMI snapshot.
func TestWorkspaceRootInitialization(t *testing.T) {
	p := &Provider{}
	input := p.launchInput(provider.CreateRequest{}, Image{RootDeviceName: "/dev/sda1"})
	if got := aws.ToInt32(input.BlockDeviceMappings[0].Ebs.VolumeInitializationRate); got != 300 {
		t.Errorf("launchInput(root).VolumeInitializationRate = %d, want 300 MiB/s", got)
	}
}

func officialProviderFixture() (Config, *lostLaunchAPI) {
	pin := &release.OfficialEC2Image{
		AMIID: "ami-11112222333344445", PublisherAccountID: "222233334444",
		RootDeviceName: "/dev/sda1", RootSnapshotID: "snap-12345678901234567",
		SourceAMI: "ami-11112222333344446", SourceRegion: "us-east-2",
		SourceCommit: "aabbccddeeff00112233445566778899aabbccdd",
		ReleaseID:    "v1.4.2-rc.1", GuestRelease: "v1.4.2-rc.1", Architecture: "amd64",
		MinimumDiskGiB: 32, CatalogSHA256: "123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0",
	}
	cfg := Config{Region: "us-west-2", AccountID: "123456789012", SubnetID: "subnet-test",
		SecurityGroupID: "sg-test", Deployment: "demo", OfficialImage: pin}
	return cfg, &lostLaunchAPI{cfg: cfg, image: types.Image{
		ImageId: aws.String(pin.AMIID), OwnerId: aws.String(pin.PublisherAccountID),
		Public: aws.Bool(true), Architecture: types.ArchitectureValuesX8664,
		RootDeviceType: types.DeviceTypeEbs, RootDeviceName: aws.String(pin.RootDeviceName),
		VirtualizationType: types.VirtualizationTypeHvm, State: types.ImageStateAvailable,
		BlockDeviceMappings: []types.BlockDeviceMapping{
			{DeviceName: aws.String(pin.RootDeviceName), Ebs: &types.EbsBlockDevice{
				SnapshotId: aws.String(pin.RootSnapshotID), VolumeSize: aws.Int32(12), Encrypted: aws.Bool(false),
			}},
			{DeviceName: aws.String("/dev/sdb"), VirtualName: aws.String("ephemeral0")},
		},
	}}
}

// TestInspectOfficialImage verifies observable publisher identity without relying on cross-account AMI tags.
func TestInspectOfficialImage(t *testing.T) {
	cfg, mock := officialProviderFixture()
	p := &Provider{cfg: cfg, client: mock, now: time.Now}
	got, err := p.InspectOfficialImage(t.Context())
	pin := cfg.OfficialImage
	if err != nil || got.ID != pin.AMIID || got.SnapshotID != pin.RootSnapshotID ||
		got.Architecture != pin.Architecture || !got.SingleBoot {
		t.Fatalf("InspectOfficialImage(%s) = %+v, %v, want the pinned image and single-boot enrollment", pin.AMIID, got, err)
	}
	query := mock.lastImageQuery
	if query == nil || len(query.ImageIds) != 1 || query.ImageIds[0] != pin.AMIID ||
		len(query.Owners) != 1 || query.Owners[0] != pin.PublisherAccountID {
		t.Fatalf("DescribeImages request = %+v, want exact image and publisher filters", query)
	}
	if _, err := p.InspectImage(t.Context(), pin.AMIID); !errors.Is(err, provider.ErrOwnershipMismatch) {
		t.Fatalf("private InspectImage(%s) = %v, want rejection of cross-account public image", pin.AMIID, err)
	}
}

// TestInspectOfficialImageRejectsObservedDrift verifies stale and incompatible AMIs fail closed.
func TestInspectOfficialImageRejectsObservedDrift(t *testing.T) {
	now := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		change func(*lostLaunchAPI)
		want   error
	}{
		{name: "missing", change: func(m *lostLaunchAPI) { m.imageAbsent = true }, want: provider.ErrNotFound},
		{name: "wrong ID", change: func(m *lostLaunchAPI) { m.image.ImageId = aws.String("ami-11112222333344446") }, want: provider.ErrOwnershipMismatch},
		{name: "wrong owner", change: func(m *lostLaunchAPI) { m.image.OwnerId = aws.String(m.cfg.AccountID) }, want: provider.ErrOwnershipMismatch},
		{name: "no longer public", change: func(m *lostLaunchAPI) { m.image.Public = aws.Bool(false) }, want: provider.ErrOwnershipMismatch},
		{name: "architecture drift", change: func(m *lostLaunchAPI) { m.image.Architecture = types.ArchitectureValuesArm64 }, want: provider.ErrOwnershipMismatch},
		{name: "not available", change: func(m *lostLaunchAPI) { m.image.State = types.ImageStatePending }, want: provider.ErrOwnershipMismatch},
		{name: "expired", change: func(m *lostLaunchAPI) { m.image.DeprecationTime = aws.String(now.Format(time.RFC3339)) }, want: provider.ErrNotFound},
		{name: "different root", change: func(m *lostLaunchAPI) { m.image.RootDeviceName = aws.String("/dev/sdb") }, want: provider.ErrOwnershipMismatch},
		{name: "different snapshot", change: func(m *lostLaunchAPI) {
			m.image.BlockDeviceMappings[0].Ebs.SnapshotId = aws.String("snap-01234567890123456")
		}, want: provider.ErrOwnershipMismatch},
		{name: "encrypted public snapshot", change: func(m *lostLaunchAPI) {
			m.image.BlockDeviceMappings[0].Ebs.Encrypted = aws.Bool(true)
		}, want: provider.ErrInvalidState},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, mock := officialProviderFixture()
			tc.change(mock)
			p := &Provider{cfg: cfg, client: mock, now: func() time.Time { return now }}
			if _, err := p.InspectOfficialImage(t.Context()); !errors.Is(err, tc.want) {
				t.Fatalf("InspectOfficialImage(%s) = %v, want %v", tc.name, err, tc.want)
			}
		})
	}
}

// TestOfficialBootChecksLiveImageBeforeLaunch verifies both fresh launches retain exact identity and storage encryption.
func TestOfficialBootChecksLiveImageBeforeLaunch(t *testing.T) {
	for _, kind := range []string{"catalog canary", "workspace"} {
		t.Run(kind, func(t *testing.T) {
			cfg, mock := officialProviderFixture()
			pin := cfg.OfficialImage
			image := Image{ID: pin.AMIID, Architecture: pin.Architecture, RootDeviceName: pin.RootDeviceName,
				SnapshotID: pin.RootSnapshotID, SingleBoot: true}
			p := &Provider{cfg: cfg, client: mock, poll: time.Millisecond, now: time.Now,
				resolve: func(context.Context, string, string) (Image, error) { return image, nil }}
			req := provider.CreateRequest{
				Ref:         provider.WorkspaceRef{InstallationID: "install", WorkspaceID: "workspace", Generation: 1, OwnershipMarker: "marker"},
				OperationID: "operation", ArtifactID: "artifact", Release: pin.ReleaseID,
				NetworkMode: NetworkMode, Resources: ReferenceResources(), BootstrapData: "fixture",
			}
			create := p.Create
			if kind == "catalog canary" {
				create = func(ctx context.Context, req provider.CreateRequest) (provider.Inventory, error) {
					return p.CreateCandidate(ctx, req, image)
				}
			}
			wrongMode := req
			wrongMode.BootstrapData = ""
			if _, err := create(t.Context(), wrongMode); !errors.Is(err, provider.ErrCapabilityUnsupported) || mock.launches != 0 {
				t.Fatalf("%s without single-boot enrollment = %v, AWS launches = %d, want capability refusal and no launch", kind, err, mock.launches)
			}
			mock.imageAbsent = true
			if _, err := create(t.Context(), req); !errors.Is(err, provider.ErrNotFound) || mock.launches != 0 {
				t.Fatalf("%s after AMI withdrawal = %v, AWS launches = %d, want not found and no launch", kind, err, mock.launches)
			}
			mock.imageAbsent = false
			if _, err := create(t.Context(), req); !errors.Is(err, provider.ErrOutcomeUnknown) || mock.launches != 1 {
				t.Fatalf("%s with valid AMI = %v, AWS launches = %d, want one simulated lost response", kind, err, mock.launches)
			}
			input := mock.lastLaunch
			if aws.ToString(input.ImageId) != pin.AMIID || !aws.ToBool(input.BlockDeviceMappings[0].Ebs.Encrypted) ||
				aws.ToBool(input.NetworkInterfaces[0].AssociatePublicIpAddress) {
				t.Fatalf("%s RunInstances = %+v, want exact public AMI with private network and encrypted root", kind, input)
			}
			mock.imageAbsent = true
			got, err := create(t.Context(), req)
			if err != nil || got.Power != provider.Running || mock.launches != 1 {
				t.Fatalf("%s retry = %+v, %v, AWS launches = %d, want existing single-boot instance", kind, got, err, mock.launches)
			}
		})
	}
}

type suppliedNetworkAPI struct {
	client
	subnet types.Subnet
	group  types.SecurityGroup
}

func (f suppliedNetworkAPI) DescribeSubnets(context.Context, *api.DescribeSubnetsInput, ...func(*api.Options)) (*api.DescribeSubnetsOutput, error) {
	return &api.DescribeSubnetsOutput{Subnets: []types.Subnet{f.subnet}}, nil
}

func (f suppliedNetworkAPI) DescribeSecurityGroups(context.Context, *api.DescribeSecurityGroupsInput, ...func(*api.Options)) (*api.DescribeSecurityGroupsOutput, error) {
	return &api.DescribeSecurityGroupsOutput{SecurityGroups: []types.SecurityGroup{f.group}}, nil
}

// TestVerifyNetworkAcceptsDefaultSubnet verifies that subnet auto-assignment is
// independent of the explicit private primary interface in RunInstances.
func TestVerifyNetworkAcceptsDefaultSubnet(t *testing.T) {
	cfg := Config{AccountID: "123456789012", SubnetID: "subnet-default", SecurityGroupID: "sg-workspace"}
	fake := suppliedNetworkAPI{
		subnet: types.Subnet{SubnetId: aws.String(cfg.SubnetID), OwnerId: aws.String(cfg.AccountID), VpcId: aws.String("vpc-default"),
			State: types.SubnetStateAvailable, AvailabilityZone: aws.String("us-east-2a"), MapPublicIpOnLaunch: aws.Bool(true)},
		group: types.SecurityGroup{GroupId: aws.String(cfg.SecurityGroupID), OwnerId: aws.String(cfg.AccountID), VpcId: aws.String("vpc-default")},
	}
	p := &Provider{cfg: cfg, client: fake}
	if err := p.verifyNetwork(t.Context()); err != nil {
		t.Fatalf("verifyNetwork(default subnet) = %v, want acceptance", err)
	}
	fake.group.IpPermissions = []types.IpPermission{{IpProtocol: aws.String("tcp")}}
	p.client = fake
	if err := p.verifyNetwork(t.Context()); !errors.Is(err, provider.ErrOwnershipMismatch) {
		t.Fatalf("verifyNetwork(workspace ingress) = %v, want rejection", err)
	}
}

// TestNamespacedWorkspaceTags checks that CloudFormation workspaces use only
// ShiftBox-owned keys and that those same keys fence inventory and cleanup.
func TestNamespacedWorkspaceTags(t *testing.T) {
	ref := provider.WorkspaceRef{InstallationID: "install", WorkspaceID: "workspace", Generation: 1, OwnershipMarker: "marker"}
	request := provider.CreateRequest{Ref: ref, OperationID: "operation", ArtifactID: "artifact", Release: "release",
		NetworkMode: NetworkMode, Resources: ReferenceResources()}
	p := &Provider{cfg: Config{Deployment: "fixture", Environment: "testing", NamespacedTags: true}}
	input := p.launchInput(request, Image{ID: "ami-11112222333344445", RootDeviceName: "/dev/sda1"})
	for _, spec := range input.TagSpecifications {
		tags := tagValues(spec.Tags)
		if tags["shiftbox-workspace-deployment"] != "fixture" || tags["shiftbox-project"] != "shiftbox" ||
			tags["shiftbox-cluster-name"] != "fixture" {
			t.Fatalf("launchInput(%s) tags = %v, want namespaced deployment ownership", spec.ResourceType, tags)
		}
		for key := range tags {
			if !strings.HasPrefix(key, "shiftbox-") {
				t.Errorf("launchInput(%s) tag = %q, want shiftbox- prefix", spec.ResourceType, key)
			}
		}
		if !p.owns(ref, spec.Tags) {
			t.Errorf("owns(%s) = false, want ownership under the same tag convention", spec.ResourceType)
		}
	}
	legacy := &Provider{cfg: Config{Deployment: "fixture", Environment: "testing"}}
	if !legacy.owns(ref, legacy.ownershipTags(ref)) || p.owns(ref, legacy.ownershipTags(ref)) {
		t.Fatal("ownership checks did not retain the legacy deployment's tag contract and isolation")
	}
	p.cfg.AccountID = "123456789012"
	p.cfg.SubnetID = "subnet-test"
	p.cfg.SecurityGroupID = "sg-test"
	p.client = &lostLaunchAPI{cfg: p.cfg}
	p.poll, p.now = time.Millisecond, time.Now
	p.resolve = func(context.Context, string, string) (Image, error) {
		return Image{ID: "ami-11112222333344445", RootDeviceName: "/dev/sda1", Architecture: "amd64"}, nil
	}
	if _, err := p.Create(t.Context(), request); !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Fatalf("Create after lost response = %v, want uncertain result", err)
	}
	got, err := p.Create(t.Context(), request)
	if err != nil || got.Power != provider.Stopped || got.ArtifactID != "artifact" || got.Release != "release" {
		t.Fatalf("Create after namespaced retry = %+v, %v, want original stopped workspace", got, err)
	}
}
