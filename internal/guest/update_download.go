package guest

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/jflatten/ShiftBox/internal/release"
)

var errGuestUpdateDownload = errors.New("controller did not provide the assigned guest update")

const stageGuestUpdateCommand = "stage-guest-update"
const problemGuestUpdateStage = "guest.update_stage_failed"
const guestUpdatePublisherKeyPath = "/usr/local/share/shiftbox/guest-update.pub"

func loadGuestUpdatePublisherKey(path string) (ed25519.PublicKey, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() != ed25519.PublicKeySize {
		return nil, errGuestUpdateInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errGuestUpdateInvalid
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errGuestUpdateInvalid
	}
	key := make(ed25519.PublicKey, ed25519.PublicKeySize)
	if _, err := io.ReadFull(file, key); err != nil {
		return nil, errGuestUpdateInvalid
	}
	return key, nil
}

func validStagedUpdateDigest(value string) bool {
	data, err := hex.DecodeString(value)
	return err == nil && len(data) == sha256.Size && [sha256.Size]byte(data) != ([sha256.Size]byte{})
}

func (j *localJournal) stageUpdate(ctx context.Context, dispatch guestDispatch, download func(context.Context, string) ([sha256.Size]byte, error)) error {
	if err := j.accept(dispatch); err != nil {
		return err
	}
	next := *j.state.Operation
	if next.Phase != localPending {
		return nil
	}
	var digest [sha256.Size]byte
	var err error
	if download != nil {
		digest, err = download(ctx, dispatch.ProductRelease)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || digest == ([sha256.Size]byte{}) {
		next.Phase, next.Problem = localFailed, problemGuestUpdateStage
	} else {
		next.Phase, next.UpdateDigest = localSucceeded, hex.EncodeToString(digest[:])
	}
	return j.saveOperation(next)
}

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
