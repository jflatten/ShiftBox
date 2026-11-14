package guest

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

const maxGuestUpdateBytes = 64 << 20

var errGuestUpdateInvalid = errors.New("guest update signature, compatibility, or contents could not be verified")

type guestUpdateMetadata struct {
	Release  string
	Protocol string
	Size     int64
	Digest   [sha256.Size]byte
}

func (m guestUpdateMetadata) signedBytes() []byte {
	return fmt.Appendf(nil, "shiftbox-guest-update-v1\n%q\n%q\n%d\n%s\n",
		m.Release, m.Protocol, m.Size, hex.EncodeToString(m.Digest[:]))
}

// stageGuestUpdate publishes a private copy only after verifying the release
// signature and every byte. The activation step must validate it again before
// handing it to the supervisor.
func stageGuestUpdate(ctx context.Context, path, release, protocol string, key ed25519.PublicKey,
	metadata guestUpdateMetadata, signature []byte, source io.Reader,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !filepath.IsAbs(path) || source == nil || strings.TrimSpace(release) == "" || len(release) > 128 ||
		strings.TrimSpace(protocol) == "" || len(protocol) > 32 || metadata.Release != release || metadata.Protocol != protocol ||
		metadata.Size < 1 || metadata.Size > maxGuestUpdateBytes ||
		len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, metadata.signedBytes(), signature) {
		return errGuestUpdateInvalid
	}
	return replacePrivateState(path, func(destination io.Writer) error {
		digest := sha256.New()
		content := io.LimitReader(source, metadata.Size+1)
		size, err := io.Copy(io.MultiWriter(destination, digest), guestUpdateReader{ctx, content})
		if err != nil {
			return fmt.Errorf("copy guest update: %w", err)
		}
		if size != metadata.Size || [sha256.Size]byte(digest.Sum(nil)) != metadata.Digest {
			return errGuestUpdateInvalid
		}
		return ctx.Err()
	})
}

type guestUpdateReader struct {
	ctx context.Context
	io.Reader
}

func (r guestUpdateReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(buffer)
}
