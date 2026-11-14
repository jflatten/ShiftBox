package utm

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/release"
)

// TestQCOWCapacity reads guest-visible size from the public image header
// and refuses images that rely on a backing file or external data.
func TestQCOWCapacity(t *testing.T) {
	header := make([]byte, 104)
	copy(header, "QFI\xfb")
	binary.BigEndian.PutUint32(header[4:], 3)
	binary.BigEndian.PutUint32(header[20:], 16)
	binary.BigEndian.PutUint64(header[24:], 64<<20)
	binary.BigEndian.PutUint32(header[100:], uint32(len(header)))
	for _, tc := range []struct {
		name     string
		change   func([]byte)
		accepted bool
	}{
		{name: "standalone", accepted: true},
		{name: "backing file", change: func(b []byte) { binary.BigEndian.PutUint64(b[8:], 104) }},
		{name: "external data", change: func(b []byte) { binary.BigEndian.PutUint64(b[72:], 4) }},
		{name: "zero capacity", change: func(b []byte) { binary.BigEndian.PutUint64(b[24:], 0) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := append([]byte(nil), header...)
			if tc.change != nil {
				tc.change(input)
			}
			root, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { root.Close() })
			if err := os.WriteFile(filepath.Join(root.Name(), "drive.qcow2"), input, 0600); err != nil {
				t.Fatal(err)
			}
			size, err := qcowCapacity(root, "drive.qcow2")
			if tc.accepted && (err != nil || size != 64<<20) {
				t.Errorf("qcowCapacity = %d, %v, want 64 MiB", size, err)
			}
			if !tc.accepted && !errors.Is(err, provider.ErrCapabilityUnsupported) {
				t.Errorf("qcowCapacity = %d, %v, want unsupported", size, err)
			}
		})
	}
}

// TestStagedDiskMatchesAllocation verifies the requested capacity against
// the copied image before asking UTM to import it.
func TestStagedDiskMatchesAllocation(t *testing.T) {
	directory := t.TempDir()
	header := make([]byte, 104)
	copy(header, "QFI\xfb")
	binary.BigEndian.PutUint32(header[4:], 3)
	binary.BigEndian.PutUint32(header[20:], 16)
	binary.BigEndian.PutUint64(header[24:], 64<<20)
	binary.BigEndian.PutUint32(header[100:], uint32(len(header)))
	if err := os.WriteFile(filepath.Join(directory, "disk.qcow2"), header, 0600); err != nil {
		t.Fatal(err)
	}
	artifact := release.Artifact{Bundle: directory, Manifest: release.ArtifactManifest{
		Resources: []release.ArtifactResource{{Path: "source.utm/disk.qcow2", Type: "disk"}},
	}}
	if err := verifyStagedDisk(context.Background(), artifact, 64<<20); err != nil {
		t.Errorf("verifyStagedDisk(64 MiB) = %v, want success", err)
	}
	if err := verifyStagedDisk(context.Background(), artifact, 128<<20); !errors.Is(err, provider.ErrCapabilityUnsupported) {
		t.Errorf("verifyStagedDisk(128 MiB) = %v, want unsupported", err)
	}
}
