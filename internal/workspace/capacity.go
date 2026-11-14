package workspace

import (
	"errors"
	"fmt"
	"math"

	"github.com/jflatten/ShiftBox/internal/provider"
)

// Allocation is the CPU, memory and disk allocation persisted for a workspace.
type Allocation struct {
	CPU                int
	MemoryMiB, DiskMiB int64
}

func (a Allocation) positive() bool {
	return a.CPU > 0 && a.MemoryMiB > 0 && a.DiskMiB > 0
}

// CapacityError reports the first resource that prevents admission.
type CapacityError struct {
	Resource               string
	Limit, Used, Requested int64
}

// Error returns the limiting resource for a refused reservation.
func (err *CapacityError) Error() string {
	return fmt.Sprintf("insufficient %s capacity (limit %d, used %d, requested %d)", err.Resource, err.Limit, err.Used, err.Requested)
}

// CheckCapacity evaluates an accepted start against existing reservations. A
// restart with retained disk adds only CPU and memory to the current usage.
func CheckCapacity(limit, used, pinned Allocation, diskReserved bool) error {
	if !limit.positive() || !pinned.positive() || used.CPU < 0 || used.MemoryMiB < 0 || used.DiskMiB < 0 {
		return errors.New("invalid capacity inputs")
	}
	additionalDisk := pinned.DiskMiB
	if diskReserved {
		additionalDisk = 0
	}
	for _, dimension := range []CapacityError{
		{Resource: "cpu", Limit: int64(limit.CPU), Used: int64(used.CPU), Requested: int64(pinned.CPU)},
		{Resource: "memory", Limit: limit.MemoryMiB, Used: used.MemoryMiB, Requested: pinned.MemoryMiB},
		{Resource: "disk", Limit: limit.DiskMiB, Used: used.DiskMiB, Requested: additionalDisk},
	} {
		if dimension.Requested > 0 && dimension.Requested > dimension.Limit-dimension.Used {
			return &dimension
		}
	}
	return nil
}

// ProfileResources converts a resource profile to provider units and verifies
// every configured value against the provider's validated bounds.
func ProfileResources(profile Allocation, caps provider.Capabilities) (provider.Resources, error) {
	const mib = 1 << 20
	if !profile.positive() || profile.MemoryMiB > math.MaxInt64/mib || profile.DiskMiB > math.MaxInt64/mib ||
		caps.Minimum.CPU < 1 || caps.Minimum.MemoryBytes < 1 || caps.Minimum.DiskBytes < 1 {
		return provider.Resources{}, provider.ErrCapabilityUnsupported
	}
	request := provider.Resources{CPU: profile.CPU, MemoryBytes: profile.MemoryMiB * mib, DiskBytes: profile.DiskMiB * mib}
	if request.CPU < caps.Minimum.CPU || request.CPU > caps.Maximum.CPU ||
		request.MemoryBytes < caps.Minimum.MemoryBytes || request.MemoryBytes > caps.Maximum.MemoryBytes ||
		request.DiskBytes < caps.Minimum.DiskBytes || request.DiskBytes > caps.Maximum.DiskBytes {
		return provider.Resources{}, provider.ErrCapabilityUnsupported
	}
	return request, nil
}
