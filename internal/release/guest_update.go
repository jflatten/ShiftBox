package release

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
)

// MaxGuestUpdateBytes bounds the controller artifact and guest staging file.
const MaxGuestUpdateBytes = 64 << 20

// ErrGuestUpdateUnavailable indicates missing or inconsistent local release files.
var ErrGuestUpdateUnavailable = errors.New("pinned guest update is unavailable")

// GuestUpdateMetadata binds the executable bytes to a product release and protocol.
type GuestUpdateMetadata struct {
	Release  string
	Protocol string
	Size     int64
	Digest   [sha256.Size]byte
}

// SignedBytes returns the versioned message signed by the release publisher.
func (m GuestUpdateMetadata) SignedBytes() []byte {
	return fmt.Appendf(nil, "shiftbox-guest-update-v1\n%q\n%q\n%d\n%s\n",
		m.Release, m.Protocol, m.Size, hex.EncodeToString(m.Digest[:]))
}

// GuestUpdate carries pinned bytes and the detached publisher signature.
type GuestUpdate struct {
	Metadata          GuestUpdateMetadata
	Signature, Binary []byte
}

// ReadLocalGuestUpdate reads the agent and detached signature beside the private
// manifest. The guest independently verifies the signature against its trusted key.
func ReadLocalGuestUpdate(ctx context.Context, root, id, protocol string) (GuestUpdate, error) {
	manifest, err := ReadLocalMigrationRelease(root, id)
	if err != nil || protocol != manifest.ProtocolVersion || manifest.Guest.Version != id {
		return GuestUpdate{}, ErrGuestUpdateUnavailable
	}
	folder := filepath.Join(root, id)
	signature, err := readGuestReleaseFile(ctx, filepath.Join(folder, "shiftbox-agent.sig"), ed25519.SignatureSize)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return GuestUpdate{}, ErrGuestUpdateUnavailable
	}
	binary, err := readGuestReleaseFile(ctx, filepath.Join(folder, "shiftbox-agent"), MaxGuestUpdateBytes)
	digest := sha256.Sum256(binary)
	if err != nil || len(binary) == 0 || hex.EncodeToString(digest[:]) != manifest.Guest.SHA256 {
		return GuestUpdate{}, ErrGuestUpdateUnavailable
	}
	return GuestUpdate{Metadata: GuestUpdateMetadata{Release: id, Protocol: protocol, Size: int64(len(binary)), Digest: digest},
		Signature: signature, Binary: binary}, nil
}

func readGuestReleaseFile(ctx context.Context, path string, limit int64) ([]byte, error) {
	file, err := openPrivateReleaseFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(artifactReader{ctx, file}, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, ErrGuestUpdateUnavailable
	}
	return data, nil
}
