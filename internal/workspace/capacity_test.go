package workspace

import (
	"errors"
	"math"
	"testing"

	"github.com/jflatten/ShiftBox/internal/provider"
)

// TestCapacityAdmission distinguishes a fresh launch from a restart whose
// disk is already reserved, and includes outstanding usage in each limit.
func TestCapacityAdmission(t *testing.T) {
	limit := Allocation{CPU: 8, MemoryMiB: 8192, DiskMiB: 100}
	used := Allocation{CPU: 4, MemoryMiB: 4096, DiskMiB: 100}
	pinned := Allocation{CPU: 4, MemoryMiB: 4096, DiskMiB: 40}
	if err := CheckCapacity(limit, used, pinned, true); err != nil {
		t.Errorf("restart with retained disk = %v, want accepted", err)
	}
	var refused *CapacityError
	if err := CheckCapacity(limit, used, pinned, false); !errors.As(err, &refused) || refused.Resource != "disk" {
		t.Errorf("launch with full disk budget = %v, want disk refusal", err)
	}
	used.CPU++
	if err := CheckCapacity(limit, used, pinned, true); !errors.As(err, &refused) || refused.Resource != "cpu" {
		t.Errorf("restart beyond CPU budget = %v, want CPU refusal", err)
	}
	used.CPU--
	used.MemoryMiB++
	if err := CheckCapacity(limit, used, pinned, true); !errors.As(err, &refused) || refused.Resource != "memory" {
		t.Errorf("restart beyond memory budget = %v, want memory refusal", err)
	}
}

// TestCapacityAfterBudgetReduction allows use of retained disk while
// refusing work that would add allocation to a dimension over its limit.
func TestCapacityAfterBudgetReduction(t *testing.T) {
	limit := Allocation{CPU: 8, MemoryMiB: 8192, DiskMiB: 60}
	used := Allocation{CPU: 6, MemoryMiB: 4096, DiskMiB: 100}
	pinned := Allocation{CPU: 2, MemoryMiB: 2048, DiskMiB: 40}
	if err := CheckCapacity(limit, used, pinned, true); err != nil {
		t.Errorf("restart with existing disk after lowering disk budget = %v, want accepted", err)
	}
	if err := CheckCapacity(limit, used, pinned, false); err == nil {
		t.Error("launch added disk to an exceeded budget")
	}
	limit.DiskMiB = math.MaxInt64
	used.DiskMiB = math.MaxInt64 - 1
	if err := CheckCapacity(limit, used, pinned, false); err == nil {
		t.Error("launch overflowed disk usage instead of refusing allocation")
	}
}

// TestProfileCapabilityBounds verifies exact MiB conversion and rejects
// unsupported allocations and missing provider limits.
func TestProfileCapabilityBounds(t *testing.T) {
	const mib = 1 << 20
	caps := provider.Capabilities{
		Minimum: provider.Resources{CPU: 1, MemoryBytes: 512 * mib, DiskBytes: 10 * mib},
		Maximum: provider.Resources{CPU: 8, MemoryBytes: 8192 * mib, DiskBytes: 100 * mib},
	}
	pinned := Allocation{CPU: 4, MemoryMiB: 4096, DiskMiB: 50}
	got, err := ProfileResources(pinned, caps)
	want := provider.Resources{CPU: 4, MemoryBytes: 4096 * mib, DiskBytes: 50 * mib}
	if err != nil || got != want {
		t.Errorf("ProfileResources(%+v) = %+v, %v, want %+v", pinned, got, err, want)
	}
	for _, candidate := range []Allocation{
		{CPU: 9, MemoryMiB: 4096, DiskMiB: 50},
		{CPU: 4, MemoryMiB: 511, DiskMiB: 50},
		{CPU: 4, MemoryMiB: 4096, DiskMiB: math.MaxInt64},
	} {
		if _, err := ProfileResources(candidate, caps); !errors.Is(err, provider.ErrCapabilityUnsupported) {
			t.Errorf("ProfileResources(%+v) = %v, want unsupported", candidate, err)
		}
	}
	if _, err := ProfileResources(pinned, provider.Capabilities{}); !errors.Is(err, provider.ErrCapabilityUnsupported) {
		t.Errorf("ProfileResources without evidence = %v, want unsupported", err)
	}
}
