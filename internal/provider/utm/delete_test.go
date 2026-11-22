package utm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jflatten/ShiftBox/internal/provider"
)

func ownedDeleteRequest() provider.DeleteRequest {
	return provider.DeleteRequest{Ref: provider.WorkspaceRef{
		InstallationID: "install", WorkspaceID: "work", InstanceID: "vm", Generation: 1, OwnershipMarker: "marker",
	}, OperationID: "delete"}
}

// TestDeleteReconcilesLostReply checks that a vanished VM and bundle are
// reconciled without issuing a second destructive command.
func TestDeleteReconcilesLostReply(t *testing.T) {
	req := ownedDeleteRequest()
	bundle := filepath.Join(t.TempDir(), "workspace.utm")
	if err := os.Mkdir(bundle, 0700); err != nil {
		t.Fatal(err)
	}
	calls := 0
	p := PowerController{
		inspect: func(context.Context, provider.WorkspaceRef) (provider.Inventory, error) {
			if calls > 0 {
				return provider.Inventory{}, provider.ErrNotFound
			}
			return provider.Inventory{Ref: req.Ref, Power: provider.Stopped}, nil
		},
		apply: func(_ context.Context, ref provider.WorkspaceRef, action string) error {
			if ref != req.Ref || action != "delete" {
				t.Fatalf("native mutation = %q for %v, want delete for %v", action, ref, req.Ref)
			}
			calls++
			if err := os.Remove(bundle); err != nil {
				t.Fatal(err)
			}
			return provider.ErrOutcomeUnknown
		},
	}
	for range 2 {
		got, err := p.DeleteManagedVM(context.Background(), req, bundle)
		if err != nil || got.Power != provider.Deleted || got.InstanceID != "vm" || len(got.Resources) != 0 {
			t.Fatalf("DeleteManagedVM(%s) = %+v, %v, want absent registry and bundle", req.Ref.InstanceID, got, err)
		}
	}
	if calls != 1 {
		t.Errorf("native delete calls = %d, want 1", calls)
	}
}

// TestDeleteKeepsOrphanedBundle detects deregistration without removal and
// leaves the remaining disk visible to the controller.
func TestDeleteKeepsOrphanedBundle(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "workspace.utm")
	if err := os.Mkdir(bundle, 0700); err != nil {
		t.Fatal(err)
	}
	req := ownedDeleteRequest()
	p := PowerController{
		inspect: func(context.Context, provider.WorkspaceRef) (provider.Inventory, error) {
			return provider.Inventory{}, provider.ErrNotFound
		},
		apply: func(context.Context, provider.WorkspaceRef, string) error {
			t.Fatal("deregistered VM reached native deletion")
			return nil
		},
	}
	got, err := p.DeleteManagedVM(context.Background(), req, bundle)
	if !errors.Is(err, provider.ErrPartial) || got.Power == provider.Deleted || len(got.Resources) != 1 || got.Resources[0].Type != "bundle" {
		t.Errorf("DeleteManagedVM(%s) = %+v, %v, want partial outcome with remaining bundle", req.Ref.InstanceID, got, err)
	}
}

// TestDeleteRequiresKnownStorage keeps disk accounting when the parent
// directory is unavailable and UTM no longer lists the VM.
func TestDeleteRequiresKnownStorage(t *testing.T) {
	req := ownedDeleteRequest()
	p := PowerController{
		inspect: func(context.Context, provider.WorkspaceRef) (provider.Inventory, error) {
			return provider.Inventory{}, provider.ErrNotFound
		},
	}
	path := filepath.Join(t.TempDir(), "missing", "workspace.utm")
	got, err := p.DeleteManagedVM(context.Background(), req, path)
	if !errors.Is(err, provider.ErrOutcomeUnknown) || got.Power == provider.Deleted {
		t.Errorf("DeleteManagedVM(%s) = %s, %v, want uncertain storage", req.Ref.InstanceID, got.Power, err)
	}
}

// TestDeleteRequiresCompleteReference prevents an unscoped absence result.
func TestDeleteRequiresCompleteReference(t *testing.T) {
	req := ownedDeleteRequest()
	req.Ref.OwnershipMarker = ""
	p := PowerController{
		inspect: func(context.Context, provider.WorkspaceRef) (provider.Inventory, error) {
			return provider.Inventory{}, provider.ErrNotFound
		},
	}
	got, err := p.DeleteManagedVM(context.Background(), req, filepath.Join(t.TempDir(), "workspace.utm"))
	if !errors.Is(err, provider.ErrInvalidState) || got.Power == provider.Deleted {
		t.Errorf("DeleteManagedVM(%s) = %s, %v, want invalid ownership reference", req.Ref.InstanceID, got.Power, err)
	}
}

// TestDeleteRequiresOwnedStoppedVM checks that a stale or running VM cannot
// reach the destructive Apple event.
func TestDeleteRequiresOwnedStoppedVM(t *testing.T) {
	for _, tc := range []struct {
		name       string
		power      provider.PowerState
		inspectErr error
		want       error
	}{
		{name: "running", power: provider.Running, want: provider.ErrInvalidState},
		{name: "stale", inspectErr: provider.ErrOwnershipMismatch, want: provider.ErrOwnershipMismatch},
		{name: "unavailable", inspectErr: provider.ErrUnavailable, want: provider.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := ownedDeleteRequest()
			p := PowerController{
				inspect: func(context.Context, provider.WorkspaceRef) (provider.Inventory, error) {
					return provider.Inventory{Ref: req.Ref, Power: tc.power}, tc.inspectErr
				},
				apply: func(context.Context, provider.WorkspaceRef, string) error {
					t.Fatal("unsafe VM reached native deletion")
					return nil
				},
			}
			got, err := p.DeleteManagedVM(context.Background(), req, filepath.Join(t.TempDir(), "workspace.utm"))
			if !errors.Is(err, tc.want) || got.Power == provider.Deleted {
				t.Errorf("DeleteManagedVM(%s) = %s, %v, want %v", tc.name, got.Power, err, tc.want)
			}
		})
	}
}
