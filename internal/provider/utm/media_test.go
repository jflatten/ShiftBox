package utm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jflatten/ShiftBox/internal/provider"
)

// TestPreservedMediaInventory rejects changes to either persistent disk, VM
// provenance, or the complete ordered drive list while allowing new media.
func TestPreservedMediaInventory(t *testing.T) {
	before := provider.Inventory{
		Ref: provider.WorkspaceRef{
			InstallationID: "installation", WorkspaceID: "workspace", InstanceID: "vm", OwnershipMarker: "owner", Generation: 3,
		},
		InstanceID: "vm", Power: provider.Stopped, Resources: []provider.OwnedResource{
			{ID: "vm", Type: "vm"},
			{ID: "vm/root-disk", Type: "disk", HostBytes: int64Ptr(32)},
			{ID: "vm/media", Type: "media"},
			{ID: "vm/data-disk", Type: "disk", HostBytes: int64Ptr(64)},
		},
	}
	after := before
	after.Resources = append([]provider.OwnedResource(nil), before.Resources...)
	after.Resources[2].HostBytes = int64Ptr(16)
	if err := verifyMediaReplacement(before, after, "vm/media"); err != nil {
		t.Fatalf("verifyMediaReplacement(changed media size) = %v, want success", err)
	}
	if err := verifyMediaReplacement(before, before, "vm/root-disk"); !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Errorf("verifyMediaReplacement(persistent disk as target) = %v, want %v", err, provider.ErrOutcomeUnknown)
	}
	for _, tc := range []struct {
		name   string
		change func(*provider.Inventory)
	}{
		{"changed root disk", func(got *provider.Inventory) { got.Resources[1].ID = "vm/different-disk" }},
		{"changed data disk size", func(got *provider.Inventory) { got.Resources[3].HostBytes = int64Ptr(96) }},
		{"omitted data disk", func(got *provider.Inventory) { got.Resources = got.Resources[:3] }},
		{"reordered drives", func(got *provider.Inventory) {
			got.Resources[1], got.Resources[2] = got.Resources[2], got.Resources[1]
		}},
		{"changed artifact", func(got *provider.Inventory) { got.ArtifactID = "other" }},
		{"changed ownership", func(got *provider.Inventory) { got.Ref.OwnershipMarker = "other" }},
		{"changed generation", func(got *provider.Inventory) { got.Ref.Generation++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := after
			candidate.Resources = append([]provider.OwnedResource(nil), after.Resources...)
			tc.change(&candidate)
			if err := verifyMediaReplacement(before, candidate, "vm/media"); !errors.Is(err, provider.ErrOutcomeUnknown) {
				t.Errorf("verifyMediaReplacement(%s) = %v, want %v", tc.name, err, provider.ErrOutcomeUnknown)
			}
		})
	}
}

// TestRecoveryMediaRejectsSymlink ensures UTM only receives a regular local
// image file for attachment.
func TestRecoveryMediaRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	image := filepath.Join(dir, "image.iso")
	if err := os.WriteFile(image, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.iso")
	if err := os.Symlink(image, link); err != nil {
		t.Fatal(err)
	}
	controller := &PowerController{
		inspect: func(context.Context, provider.WorkspaceRef) (provider.Inventory, error) {
			t.Fatal("invalid image reached UTM")
			return provider.Inventory{}, nil
		},
	}
	req := provider.EnrollmentRequest{Ref: provider.WorkspaceRef{InstanceID: "vm"},
		OperationID: "recovery", MediaPath: link, Eligible: true}
	if _, err := controller.ReplaceRecoveryMedia(context.Background(), req, "vm/media"); !errors.Is(err, provider.ErrInvalidState) {
		t.Fatalf("symlink: %v", err)
	}
}
