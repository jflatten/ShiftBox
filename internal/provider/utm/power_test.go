package utm

import (
	"context"
	"errors"
	"testing"

	"github.com/jflatten/ShiftBox/internal/provider"
)

// TestPowerReconcilesLostReply ensures a lost scripting response cannot cause
// another start when native inspection already confirms the running VM.
func TestPowerReconcilesLostReply(t *testing.T) {
	ref := provider.WorkspaceRef{InstallationID: "install", WorkspaceID: "work", InstanceID: "vm", Generation: 1, OwnershipMarker: "marker"}
	state := provider.Stopped
	calls := 0
	p := PowerController{
		inspect: func(context.Context, provider.WorkspaceRef) (provider.Inventory, error) {
			return provider.Inventory{Ref: ref, Power: state, NetworkMode: "shared"}, nil
		},
		apply: func(context.Context, provider.WorkspaceRef, string) error {
			calls++
			state = provider.Running
			return provider.ErrUnavailable
		},
	}
	req := provider.StartRequest{Ref: ref, OperationID: "start", NetworkMode: "shared"}
	for range 2 {
		got, err := p.Start(context.Background(), req)
		if err != nil || got.Power != provider.Running {
			t.Fatalf("start observation: %s, %v", got.Power, err)
		}
	}
	if calls != 1 {
		t.Fatalf("native start calls = %d, want 1", calls)
	}
}

// TestPowerKeepsUnconfirmedStopUncertain checks that a successful Apple event
// alone cannot release the capacity reserved for a still-running VM.
func TestPowerKeepsUnconfirmedStopUncertain(t *testing.T) {
	ref := provider.WorkspaceRef{InstanceID: "vm"}
	p := PowerController{
		inspect: func(context.Context, provider.WorkspaceRef) (provider.Inventory, error) {
			return provider.Inventory{Power: provider.Running}, nil
		},
		apply: func(_ context.Context, _ provider.WorkspaceRef, action string) error {
			if action != "request" {
				t.Fatalf("ordinary stop used %q", action)
			}
			return nil
		},
	}
	got, err := p.Stop(context.Background(), provider.StopRequest{Ref: ref, OperationID: "stop"})
	if got.Power != provider.Running || !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Fatalf("stop observation: %s, %v", got.Power, err)
	}
}

// TestPowerRejectsUnverifiedStart prevents boot until the observed network and
// controller-side enrollment prerequisites are satisfied.
func TestPowerRejectsUnverifiedStart(t *testing.T) {
	p := PowerController{
		inspect: func(context.Context, provider.WorkspaceRef) (provider.Inventory, error) {
			return provider.Inventory{Power: provider.Stopped, NetworkMode: "shared"}, nil
		},
		apply: func(context.Context, provider.WorkspaceRef, string) error {
			t.Error("unsafe start reached AppleScript")
			return nil
		},
	}
	req := provider.StartRequest{Ref: provider.WorkspaceRef{InstanceID: "vm"}, OperationID: "start", NetworkMode: "isolated"}
	if _, err := p.Start(context.Background(), req); !errors.Is(err, provider.ErrInvalidState) {
		t.Fatalf("network mismatch: %v", err)
	}
	req.NetworkMode = "shared"
	req.RequiresEnrollment = true
	if _, err := p.Start(context.Background(), req); !errors.Is(err, provider.ErrInvalidState) {
		t.Fatalf("pending enrollment: %v", err)
	}
}
