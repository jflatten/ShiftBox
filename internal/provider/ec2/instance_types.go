package ec2

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	api "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/release"
)

// loadInstanceTypes snapshots the workspace zone's offerings at controller startup.
// Keep regional compute facts for inventory of owned instances if offerings change.
func (p *Provider) loadInstanceTypes(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	specs := make(map[string]provider.InstanceType)
	pages := api.NewDescribeInstanceTypesPaginator(p.client, &api.DescribeInstanceTypesInput{
		Filters: []types.Filter{
			{Name: aws.String("processor-info.supported-architecture"), Values: []string{"x86_64", "arm64"}},
			{Name: aws.String("supported-root-device-type"), Values: []string{"ebs"}},
			{Name: aws.String("supported-virtualization-type"), Values: []string{"hvm"}},
			{Name: aws.String("supported-usage-class"), Values: []string{"on-demand"}},
			{Name: aws.String("hypervisor"), Values: []string{"nitro"}},
			{Name: aws.String("bare-metal"), Values: []string{"false"}},
		},
	})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return apiError(err)
		}
		for _, info := range page.InstanceTypes {
			if info.InstanceType == "" || info.VCpuInfo == nil || info.MemoryInfo == nil || info.ProcessorInfo == nil {
				continue
			}
			architecture := ""
			for _, reported := range info.ProcessorInfo.SupportedArchitectures {
				if candidate := platformArchitecture(string(reported)); candidate != "" {
					architecture = candidate
					break
				}
			}
			cpu := int(aws.ToInt32(info.VCpuInfo.DefaultVCpus))
			memory := aws.ToInt64(info.MemoryInfo.SizeInMiB)
			if architecture != "" && cpu > 0 && memory > 0 {
				name := string(info.InstanceType)
				specs[name] = provider.InstanceType{Name: name, Architecture: architecture, CPUCount: cpu, MemoryMiB: memory}
			}
		}
	}

	available := make(map[string]provider.InstanceType)
	offerings := api.NewDescribeInstanceTypeOfferingsPaginator(p.client, &api.DescribeInstanceTypeOfferingsInput{
		LocationType: types.LocationTypeAvailabilityZone,
		Filters:      []types.Filter{{Name: aws.String("location"), Values: []string{p.zone}}},
	})
	for offerings.HasMorePages() {
		page, err := offerings.NextPage(ctx)
		if err != nil {
			return apiError(err)
		}
		for _, item := range page.InstanceTypeOfferings {
			name := string(item.InstanceType)
			if spec, ok := specs[name]; ok && aws.ToString(item.Location) == p.zone {
				available[name] = spec
			}
		}
	}

	offered := make([]provider.InstanceType, 0, len(available))
	for _, spec := range available {
		offered = append(offered, spec)
	}
	slices.SortFunc(offered, func(a, b provider.InstanceType) int { return strings.Compare(a.Name, b.Name) })
	p.instanceTypes, p.offeredTypes = specs, offered
	return nil
}

func offeredInstanceTypes(offered []provider.InstanceType, caps provider.Capabilities) []provider.InstanceType {
	compatible := make([]provider.InstanceType, 0, len(offered))
	for _, instance := range offered {
		if caps.SupportsArchitecture(instance.Architecture) {
			compatible = append(compatible, instance)
		}
	}
	return compatible
}

func platformArchitecture(awsArchitecture string) string {
	switch awsArchitecture {
	case "x86_64":
		return "amd64"
	case "arm64":
		return "arm64"
	default:
		return ""
	}
}

func (p *Provider) instanceArchitecture(name string) string {
	if spec, exists := p.instanceTypes[name]; exists {
		return spec.Architecture
	}
	if name == string(instanceType) {
		return release.EC2ReferenceArchitecture
	}
	return ""
}

func (p *Provider) instanceResources(name string) (provider.Resources, bool) {
	if name == string(instanceType) {
		res := ReferenceResources()
		res.DiskBytes = 0
		return res, true
	}
	spec, ok := p.instanceTypes[name]
	if !ok {
		return provider.Resources{}, false
	}
	return provider.Resources{InstanceType: name, CPU: spec.CPUCount, MemoryBytes: spec.MemoryMiB << 20}, true
}
