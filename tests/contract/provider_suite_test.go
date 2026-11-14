package contract_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/fake"
)

type providerFixture struct {
	Provider       provider.Provider
	LoseReply      func(string)
	FailDelete     func()
	SetUnavailable func(bool)
}

// runProviderSuite separates host fault injection from the provider being tested.
func runProviderSuite(t *testing.T, fixture func() providerFixture) {
	t.Helper()
	ctx := context.Background()
	ref := provider.WorkspaceRef{InstallationID: "install", WorkspaceID: "workspace", Generation: 1, OwnershipMarker: "owner-marker"}
	create := provider.CreateRequest{Ref: ref, OperationID: "create", ArtifactID: "catalog"}
	t.Run("lost replies reconcile against complete inventory", func(t *testing.T) {
		f := fixture()
		f.LoseReply("create")
		if _, err := f.Provider.Create(ctx, create); !errors.Is(err, provider.ErrOutcomeUnknown) {
			t.Fatalf("create error: %v", err)
		}
		got, err := f.Provider.Inspect(ctx, ref)
		if err != nil || got.InstanceID == "" || len(got.Resources) != 3 || got.Power != provider.Stopped {
			t.Fatalf("inventory: %+v, %v", got, err)
		}
		replay, err := f.Provider.Create(ctx, create)
		if err != nil || replay.InstanceID != got.InstanceID {
			t.Fatal("create retry duplicated instance")
		}
		refWithID := ref
		refWithID.InstanceID = got.InstanceID
		start := provider.StartRequest{Ref: refWithID, OperationID: "start"}
		f.LoseReply("start")
		if _, err := f.Provider.Start(ctx, start); !errors.Is(err, provider.ErrOutcomeUnknown) {
			t.Fatalf("start error: %v", err)
		}
		if got, err = f.Provider.Inspect(ctx, refWithID); err != nil || got.Power != provider.Running {
			t.Fatal("lost start reply hid running instance")
		}
		if _, err = f.Provider.Start(ctx, start); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("partial deletion preserves shared assets and fences stale requests", func(t *testing.T) {
		f := fixture()
		inventory, err := f.Provider.Create(ctx, create)
		if err != nil {
			t.Fatal(err)
		}
		known := ref
		known.InstanceID = inventory.InstanceID
		stale := known
		stale.Generation++
		if _, err := f.Provider.Delete(ctx, provider.DeleteRequest{Ref: stale, OperationID: "stale"}); !errors.Is(err, provider.ErrOwnershipMismatch) {
			t.Fatalf("stale deletion: %v", err)
		}
		f.FailDelete()
		req := provider.DeleteRequest{Ref: known, OperationID: "delete"}
		remaining, err := f.Provider.Delete(ctx, req)
		if !errors.Is(err, provider.ErrPartial) || len(remaining.Resources) != 2 {
			t.Fatalf("partial deletion: %+v, %v", remaining, err)
		}
		observed, err := f.Provider.Inspect(ctx, known)
		if err != nil || len(observed.Resources) != 2 {
			t.Fatal("inspection omitted remaining resources")
		}
		done, err := f.Provider.Delete(ctx, req)
		if err != nil || done.Power != provider.Deleted || len(done.Resources) != 1 || !done.Resources[0].Shared {
			t.Fatal("retry failed to confirm owned deletion and shared preservation")
		}
	})
	t.Run("recovery preserves the disk and running networks reject mutation", func(t *testing.T) {
		f := fixture()
		req := create
		req.EnrollmentMedia = "initial-media"
		inventory, err := f.Provider.Create(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		known := inventory.Ref
		start := provider.StartRequest{Ref: known, OperationID: "start", RequiresEnrollment: true}
		if _, err := f.Provider.Start(ctx, start); err != nil {
			t.Fatal(err)
		}
		network := provider.NetworkRequest{Ref: known, OperationID: "network", Mode: "nat"}
		if _, err := f.Provider.ApplyNetwork(ctx, network); !errors.Is(err, provider.ErrInvalidState) {
			t.Fatalf("running network mutation: %v", err)
		}
		stopped, err := f.Provider.Stop(ctx, provider.StopRequest{Ref: known, OperationID: "stop"})
		if err != nil || stopped.Power != provider.Stopped || len(stopped.Resources) != len(inventory.Resources) {
			t.Fatal("stop did not preserve inventory")
		}
		if _, err := f.Provider.Start(ctx, start); !errors.Is(err, provider.ErrInvalidState) {
			t.Fatalf("expired enrollment start: %v", err)
		}
		delivery, err := f.Provider.DeliverEnrollment(ctx, provider.EnrollmentRequest{Ref: known, OperationID: "recover", MediaPath: "replacement-media", Eligible: true})
		if err != nil || delivery.MediaID != "replacement-media" || delivery.Inventory.InstanceID != inventory.InstanceID ||
			len(delivery.Inventory.Resources) != len(inventory.Resources) {
			t.Fatal("recovery changed VM identity or drive count")
		}
		for i, before := range inventory.Resources {
			if before.Type != "media" && delivery.Inventory.Resources[i] != before {
				t.Fatal("recovery modified a non-media resource")
			}
		}
		if _, err := f.Provider.Start(ctx, start); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("clones have distinct owned resources", func(t *testing.T) {
		f := fixture()
		first, err := f.Provider.Create(ctx, create)
		if err != nil {
			t.Fatal(err)
		}
		secondRequest := create
		secondRequest.Ref.WorkspaceID = "second-workspace"
		second, err := f.Provider.Create(ctx, secondRequest)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range first.Resources {
			for _, b := range second.Resources {
				if !a.Shared && !b.Shared && a.ID == b.ID {
					t.Fatal("clones share owned resources")
				}
			}
		}
	})

	t.Run("unavailable host cannot prove absence", func(t *testing.T) {
		f := fixture()
		f.SetUnavailable(true)
		if _, err := f.Provider.Inspect(ctx, ref); !errors.Is(err, provider.ErrUnavailable) {
			t.Fatalf("offline inspection: %v", err)
		}
		f.SetUnavailable(false)
		if _, err := f.Provider.Inspect(ctx, ref); !errors.Is(err, provider.ErrNotFound) {
			t.Fatalf("empty inventory: %v", err)
		}
	})
}

// TestFakeProviderContract applies controller retry and observation rules to the fake.
func TestFakeProviderContract(t *testing.T) {
	runProviderSuite(t, func() providerFixture {
		p := fake.New()
		return providerFixture{p, p.LoseReply, p.FailDelete, p.SetUnavailable}
	})
}
