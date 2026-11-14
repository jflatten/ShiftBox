package guest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jflatten/ShiftBox/internal/release"
)

// TestStageGuestUpdate verifies that only an intact artifact signed by the
// trusted release key for the requested release and protocol reaches staging.
func TestStageGuestUpdate(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	wrongKey, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	binary := []byte("fixture guest executable")
	digest := sha256.Sum256(binary)
	metadata := release.GuestUpdateMetadata{Release: "2026.11.0", Protocol: "1", Size: int64(len(binary)), Digest: digest}
	signature := ed25519.Sign(private, metadata.SignedBytes())
	path := filepath.Join(t.TempDir(), "staged-agent")
	previous := []byte("previous verified staging")
	if err := os.WriteFile(path, previous, 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, release, protocol string
		key                     ed25519.PublicKey
		body                    []byte
		metadata                release.GuestUpdateMetadata
	}{
		{"untrusted signer", metadata.Release, metadata.Protocol, wrongKey, binary, metadata},
		{"wrong release", "2026.12.0", metadata.Protocol, public, binary, metadata},
		{"wrong protocol", metadata.Release, "2", public, binary, metadata},
		{"modified bytes", metadata.Release, metadata.Protocol, public, bytes.Repeat([]byte{'x'}, len(binary)), metadata},
		{"extra bytes", metadata.Release, metadata.Protocol, public, append(bytes.Clone(binary), 'x'), metadata},
		{"truncated download", metadata.Release, metadata.Protocol, public, binary[:len(binary)-1], metadata},
		{"relabeled download", "2026.12.0", metadata.Protocol, public, binary,
			release.GuestUpdateMetadata{Release: "2026.12.0", Protocol: metadata.Protocol, Size: metadata.Size, Digest: digest}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := stageGuestUpdate(t.Context(), path, test.release, test.protocol, test.key, test.metadata, signature, bytes.NewReader(test.body))
			if !errors.Is(err, errGuestUpdateInvalid) {
				t.Errorf("stageGuestUpdate(%s) = %v, want invalid artifact", test.name, err)
			}
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, previous) {
				t.Errorf("staging after %s = %q (%v), want previous artifact", test.name, got, err)
			}
		})
	}
	if err := stageGuestUpdate(t.Context(), path, metadata.Release, metadata.Protocol, public, metadata, signature, bytes.NewReader(binary)); err != nil {
		t.Fatalf("stageGuestUpdate(valid artifact) = %v, want success", err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, binary) {
		t.Errorf("staged update = %q (%v), want verified bytes", got, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Errorf("staged file = %v (%v), want private, non-executable file", info, err)
	}
}
