package ec2

import (
	"context"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	api "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/jflatten/ShiftBox/internal/provider"
)

// Start powers on exactly the retained private workspace instance.
func (p *Provider) Start(ctx context.Context, req provider.StartRequest) (provider.Inventory, error) {
	if req.Ref.InstanceID == "" || req.OperationID == "" || req.NetworkMode != NetworkMode {
		return provider.Inventory{}, provider.ErrInvalidState
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	instance, err := p.findInstance(ctx, req.Ref)
	if err != nil {
		return provider.Inventory{}, err
	}
	switch powerOf(instance) {
	case provider.Stopped:
		if _, err := p.inventory(ctx, req.Ref, instance); err != nil {
			return provider.Inventory{}, err
		}
		if req.RequiresEnrollment {
			receipt, err := p.enrollmentReceipt(ctx, req.Ref.InstanceID)
			if err != nil {
				return provider.Inventory{}, err
			}
			if receipt != req.EnrollmentMedia {
				return provider.Inventory{}, provider.ErrOutcomeUnknown
			}
		}
		if _, err := p.client.StartInstances(ctx, &api.StartInstancesInput{InstanceIds: []string{req.Ref.InstanceID}}); err != nil {
			return provider.Inventory{}, apiError(err)
		}
	case provider.Running:
		return p.inventory(ctx, req.Ref, instance)
	case provider.Starting:
	default:
		return provider.Inventory{}, provider.ErrInvalidState
	}
	instance, err = p.waitInstance(ctx, req.Ref, provider.Running)
	if err != nil {
		return provider.Inventory{}, err
	}
	return p.inventory(ctx, req.Ref, instance)
}

// Stop waits for AWS to confirm the workspace has shut down with its retained disk.
func (p *Provider) Stop(ctx context.Context, req provider.StopRequest) (provider.Inventory, error) {
	if req.Ref.InstanceID == "" || req.OperationID == "" {
		return provider.Inventory{}, provider.ErrInvalidState
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	instance, err := p.waitInstance(ctx, req.Ref, provider.Running, provider.Stopping, provider.Stopped)
	if err != nil {
		return provider.Inventory{}, err
	}
	if powerOf(instance) == provider.Running {
		if _, err := p.client.StopInstances(ctx, &api.StopInstancesInput{
			InstanceIds: []string{req.Ref.InstanceID}, Force: aws.Bool(req.ForceRetentionDelete),
		}); err != nil {
			return provider.Inventory{}, apiError(err)
		}
	}
	instance, err = p.waitInstance(ctx, req.Ref, provider.Stopped)
	if err != nil {
		return provider.Inventory{}, err
	}
	return p.inventory(ctx, req.Ref, instance)
}

// ApplyNetwork confirms the Terraform-managed network for a stopped workspace.
func (p *Provider) ApplyNetwork(ctx context.Context, req provider.NetworkRequest) (provider.Inventory, error) {
	if req.Ref.InstanceID == "" || req.Mode != NetworkMode || req.OperationID == "" {
		return provider.Inventory{}, provider.ErrCapabilityUnsupported
	}
	if err := p.verifyNetwork(ctx); err != nil {
		return provider.Inventory{}, err
	}
	observed, err := p.Inspect(ctx, req.Ref)
	if err != nil {
		return provider.Inventory{}, err
	}
	if observed.Power != provider.Stopped {
		return provider.Inventory{}, provider.ErrInvalidState
	}
	return observed, nil
}

// Delete terminates an owned stopped instance and waits for attached resources to be removed by AWS.
func (p *Provider) Delete(ctx context.Context, req provider.DeleteRequest) (provider.Inventory, error) {
	if req.Ref.InstanceID == "" || req.OperationID == "" {
		return provider.Inventory{}, provider.ErrInvalidState
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	instance, err := p.findInstance(ctx, req.Ref)
	if err != nil && !errors.Is(err, provider.ErrNotFound) {
		return provider.Inventory{}, err
	}
	var disk, nic string
	if err == nil && powerOf(instance) == provider.Stopped {
		if _, err := p.inventory(ctx, req.Ref, instance); err != nil {
			return provider.Inventory{}, err
		}
		disk, nic, err = instanceAttachments(instance)
		if err != nil {
			return provider.Inventory{}, err
		}
		if _, err := p.client.TerminateInstances(ctx, &api.TerminateInstancesInput{InstanceIds: []string{req.Ref.InstanceID}}); err != nil {
			return provider.Inventory{}, apiError(err)
		}
	} else if err == nil && !terminating(instance) {
		return provider.Inventory{}, provider.ErrInvalidState
	}

	ticker := time.NewTicker(p.poll)
	defer ticker.Stop()
	for {
		retained, err := p.findInstance(ctx, req.Ref)
		switch {
		case errors.Is(err, provider.ErrNotFound) || (err == nil && powerOf(retained) == provider.Deleted):
			gone, err := p.attachmentIDsRemoved(ctx, disk, nic)
			if err != nil && !errors.Is(err, provider.ErrOutcomeUnknown) {
				return provider.Inventory{}, err
			}
			if gone {
				evidence, err := p.confirmRemoved(ctx, req.Ref)
				if err == nil || (!errors.Is(err, provider.ErrPartial) && !errors.Is(err, provider.ErrOutcomeUnknown)) {
					return evidence, err
				}
			}
		case err != nil && !errors.Is(err, provider.ErrOutcomeUnknown):
			return provider.Inventory{}, err
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return provider.Inventory{}, provider.ErrOutcomeUnknown
		}
	}
}

func (p *Provider) attachmentIDsRemoved(ctx context.Context, disk, nic string) (bool, error) {
	if disk != "" {
		result, err := p.client.DescribeVolumes(ctx, &api.DescribeVolumesInput{VolumeIds: []string{disk}})
		if err != nil && !errors.Is(apiError(err), provider.ErrNotFound) {
			return false, apiError(err)
		}
		if err == nil && len(result.Volumes) != 0 {
			return false, nil
		}
	}
	if nic != "" {
		result, err := p.client.DescribeNetworkInterfaces(ctx, &api.DescribeNetworkInterfacesInput{
			NetworkInterfaceIds: []string{nic},
		})
		if err != nil && !errors.Is(apiError(err), provider.ErrNotFound) {
			return false, apiError(err)
		}
		if err == nil && len(result.NetworkInterfaces) != 0 {
			return false, nil
		}
	}
	return true, nil
}

func (p *Provider) confirmRemoved(ctx context.Context, ref provider.WorkspaceRef) (provider.Inventory, error) {
	filters := p.ownershipFilters(ref)
	disks, err := p.client.DescribeVolumes(ctx, &api.DescribeVolumesInput{Filters: filters})
	if err != nil {
		return provider.Inventory{}, apiError(err)
	}
	if disks.NextToken != nil || len(disks.Volumes) != 0 {
		return provider.Inventory{}, provider.ErrPartial
	}
	interfaces, err := p.client.DescribeNetworkInterfaces(ctx, &api.DescribeNetworkInterfacesInput{Filters: filters})
	if err != nil {
		return provider.Inventory{}, apiError(err)
	}
	if interfaces.NextToken != nil || len(interfaces.NetworkInterfaces) != 0 {
		return provider.Inventory{}, provider.ErrPartial
	}
	return provider.Inventory{Ref: ref, InstanceID: ref.InstanceID, Power: provider.Deleted, ObservedAt: p.now()}, nil
}
