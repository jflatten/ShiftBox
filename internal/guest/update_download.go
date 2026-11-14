package guest

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/jflatten/ShiftBox/internal/release"
)

var errGuestUpdateDownload = errors.New("controller did not provide the assigned guest update")

// downloadGuestUpdate uses the authenticated poll origin and the locally
// trusted publisher key. The response cannot choose a staging path or redirect.
func downloadGuestUpdate(ctx context.Context, client *http.Client, pollEndpoint, stateDir, productRelease, protocol string,
	key ed25519.PublicKey,
) ([sha256.Size]byte, error) {
	var noDigest [sha256.Size]byte
	info, err := os.Lstat(stateDir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 || !filepath.IsAbs(stateDir) ||
		client == nil || !validPollEndpoint(pollEndpoint) || len(key) != ed25519.PublicKeySize {
		return noDigest, errGuestUpdateInvalid
	}
	endpoint, _ := url.Parse(pollEndpoint)
	endpoint.Path, endpoint.RawPath = UpdateDownloadPath, ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return noDigest, errGuestUpdateInvalid
	}
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := noRedirect.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return noDigest, ctx.Err()
		}
		return noDigest, errGuestUpdateDownload
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return noDigest, ErrGuestUnauthorized
	default:
		return noDigest, errGuestUpdateDownload
	}
	digest, digestErr := hex.DecodeString(response.Header.Get("X-ShiftBox-Guest-Digest"))
	signature, sigErr := base64.StdEncoding.DecodeString(response.Header.Get("X-ShiftBox-Guest-Signature"))
	if response.Header.Get("Content-Type") != "application/octet-stream" || response.Uncompressed ||
		digestErr != nil || len(digest) != sha256.Size || sigErr != nil || len(signature) != ed25519.SignatureSize {
		return noDigest, errGuestUpdateInvalid
	}
	metadata := release.GuestUpdateMetadata{
		Release: response.Header.Get("X-ShiftBox-Guest-Release"), Protocol: response.Header.Get("X-ShiftBox-Guest-Protocol"),
		Size: response.ContentLength, Digest: [sha256.Size]byte(digest),
	}
	if err := stageGuestUpdate(ctx, guestUpdateBinaryPath(stateDir, metadata.Digest), productRelease, protocol, key, metadata, signature, response.Body); err != nil {
		return noDigest, err
	}
	return metadata.Digest, nil
}

func guestUpdateBinaryPath(dir string, digest [sha256.Size]byte) string {
	return filepath.Join(dir, fmt.Sprintf("update-%x", digest))
}
