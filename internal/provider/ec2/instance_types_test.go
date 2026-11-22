package ec2

import (
	"context"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	api "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/jflatten/ShiftBox/internal/provider"
)

// TestLoadInstanceTypes joins paginated zone offerings to compatible compute
// facts while retaining regional facts for already-created instances, and excludes ARM64 shapes.
func TestLoadInstanceTypes(t *testing.T) {
	mock := &catalogAPI{t: t}
	p := &Provider{client: mock, zone: "us-east-2a"}
	if err := p.loadInstanceTypes(t.Context()); err != nil {
		t.Fatal(err)
	}
	caps, err := p.Capabilities(t.Context())
	want := []provider.InstanceType{
		{Name: "m6i.xlarge", Architecture: "amd64", CPUCount: 4, MemoryMiB: 16384},
		{Name: "t3.medium", Architecture: "amd64", CPUCount: 2, MemoryMiB: 4096},
	}
	if err != nil || !reflect.DeepEqual(caps.InstanceTypes, want) {
		t.Fatalf("instance offerings = %+v, %v; want %+v", caps.InstanceTypes, err, want)
	}
	multiArch := provider.Capabilities{Architectures: []string{"amd64", "arm64"}}
	both := offeredInstanceTypes(p.offeredTypes, multiArch)
	if len(both) != 3 || both[1].Name != "m7g.large" || both[1].Architecture != "arm64" {
		t.Fatalf("dual-architecture offerings = %+v; want ARM offered alongside x86", both)
	}
	if got, ok := p.instanceResources("r6i.large"); !ok || got.MemoryBytes != 16<<30 {
		t.Fatalf("regional inventory metadata = %+v, %t; want retained r6i.large shape", got, ok)
	}
}

type catalogAPI struct {
	client
	t *testing.T
}

func (m *catalogAPI) DescribeInstanceTypes(_ context.Context, req *api.DescribeInstanceTypesInput, _ ...func(*api.Options)) (*api.DescribeInstanceTypesOutput, error) {
	filters := make(map[string][]string)
	for _, filter := range req.Filters {
		filters[aws.ToString(filter.Name)] = filter.Values
	}
	want := map[string][]string{
		"processor-info.supported-architecture": {"x86_64", "arm64"},
		"supported-root-device-type":            {"ebs"},
		"supported-virtualization-type":         {"hvm"},
		"supported-usage-class":                 {"on-demand"},
		"hypervisor":                            {"nitro"},
		"bare-metal":                            {"false"},
	}
	if !reflect.DeepEqual(filters, want) {
		m.t.Errorf("DescribeInstanceTypes filters = %v, want %v", filters, want)
	}
	compute := func(name types.InstanceType, cpu int32, mib int64, architecture types.ArchitectureType) types.InstanceTypeInfo {
		return types.InstanceTypeInfo{InstanceType: name, VCpuInfo: &types.VCpuInfo{DefaultVCpus: aws.Int32(cpu)},
			MemoryInfo:    &types.MemoryInfo{SizeInMiB: aws.Int64(mib)},
			ProcessorInfo: &types.ProcessorInfo{SupportedArchitectures: []types.ArchitectureType{architecture}}}
	}
	return &api.DescribeInstanceTypesOutput{InstanceTypes: []types.InstanceTypeInfo{
		compute("t3.medium", 2, 4096, types.ArchitectureTypeX8664),
		compute("m6i.xlarge", 4, 16384, types.ArchitectureTypeX8664),
		compute("r6i.large", 2, 16384, types.ArchitectureTypeX8664),
		compute("m7g.large", 2, 8192, types.ArchitectureTypeArm64),
	}}, nil
}

func (m *catalogAPI) DescribeInstanceTypeOfferings(_ context.Context, req *api.DescribeInstanceTypeOfferingsInput, _ ...func(*api.Options)) (*api.DescribeInstanceTypeOfferingsOutput, error) {
	if req.LocationType != types.LocationTypeAvailabilityZone || len(req.Filters) != 1 ||
		aws.ToString(req.Filters[0].Name) != "location" || !reflect.DeepEqual(req.Filters[0].Values, []string{"us-east-2a"}) {
		m.t.Errorf("DescribeInstanceTypeOfferings location = %s/%+v; want us-east-2a", req.LocationType, req.Filters)
	}
	offer := func(name types.InstanceType, zone string) types.InstanceTypeOffering {
		return types.InstanceTypeOffering{InstanceType: name, Location: aws.String(zone)}
	}
	if aws.ToString(req.NextToken) == "" {
		return &api.DescribeInstanceTypeOfferingsOutput{NextToken: aws.String("next"), InstanceTypeOfferings: []types.InstanceTypeOffering{
			offer("t3.medium", "us-east-2a"), offer("m7g.large", "us-east-2a"), offer("r6i.large", "us-east-2b"),
		}}, nil
	}
	return &api.DescribeInstanceTypeOfferingsOutput{InstanceTypeOfferings: []types.InstanceTypeOffering{
		offer("m6i.xlarge", "us-east-2a"), offer("t3.medium", "us-east-2a"),
	}}, nil
}
