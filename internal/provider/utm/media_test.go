package utm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jflatten/ShiftBox/internal/provider"
)

// TestPreservedMediaInventory rejects changes to the persistent disk, VM
// provenance, or drive order while allowing a replacement recovery image.
func TestPreservedMediaInventory(t *testing.T) {
	before := provider.Inventory{InstanceID: "vm", Power: provider.Stopped, Resources: []provider.OwnedResource{
		{ID: "vm", Type: "vm"}, {ID: "vm/disk", Type: "disk"}, {ID: "vm/media", Type: "media"},
	}}
	after := before
	after.Resources = append([]provider.OwnedResource(nil), before.Resources...)
	after.Resources[2].HostBytes = int64Ptr(16)
	if err := verifyMediaReplacement(before, after, "vm/media"); err != nil {
		t.Fatal(err)
	}
	after.Resources[1].ID = "vm/different-disk"
	if err := verifyMediaReplacement(before, after, "vm/media"); !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Fatalf("changed disk: %v", err)
	}
	if err := verifyMediaReplacement(before, before, "vm/disk"); !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Fatalf("persistent disk replacement: %v", err)
	}
	after.Resources[1], after.Resources[2] = before.Resources[2], before.Resources[1]
	if err := verifyMediaReplacement(before, after, "vm/media"); !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Fatalf("reordered drives: %v", err)
	}
	after = before
	after.ArtifactID = "other"
	if err := verifyMediaReplacement(before, after, "vm/media"); !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Fatalf("changed artifact: %v", err)
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
