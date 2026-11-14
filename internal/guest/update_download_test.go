package guest

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jflatten/ShiftBox/internal/release"
)

// TestDownloadGuestUpdate verifies that the guest uses the fixed internal
// endpoint and only publishes a complete response signed by its trusted key.
func TestDownloadGuestUpdate(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	const body = "signed fixture candidate"
	metadata := release.GuestUpdateMetadata{Release: "current", Protocol: "1", Size: int64(len(body)), Digest: sha256.Sum256([]byte(body))}
	signature := ed25519.Sign(private, metadata.SignedBytes())
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fmt.Sprintf("update-%x", metadata.Digest))
	for _, test := range []struct {
		name, body string
		status     int
		product    string
		want       error
	}{
		{"redirect", "", 307, "current", errGuestUpdateDownload},
		{"denied", "", 403, "current", ErrGuestUnauthorized},
		{"corrupt", strings.Repeat("x", len(body)), 200, "current", errGuestUpdateInvalid},
		{"wrong release", body, 200, "other", errGuestUpdateInvalid},
		{"verified", body, 200, "current", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: guestUpdateTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet || r.URL.String() != "https://controller.test:8443"+UpdateDownloadPath {
					t.Fatalf("update request = %s %s, want fixed internal endpoint", r.Method, r.URL)
				}
				headers := make(http.Header)
				headers.Set("Location", "https://unrelated.test/artifact")
				headers.Set("Content-Type", "application/octet-stream")
				headers.Set("X-ShiftBox-Guest-Release", metadata.Release)
				headers.Set("X-ShiftBox-Guest-Protocol", metadata.Protocol)
				headers.Set("X-ShiftBox-Guest-Digest", hex.EncodeToString(metadata.Digest[:]))
				headers.Set("X-ShiftBox-Guest-Signature", base64.StdEncoding.EncodeToString(signature))
				return &http.Response{StatusCode: test.status, Header: headers, ContentLength: metadata.Size,
					Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})}
			got, err := downloadGuestUpdate(t.Context(), client, "https://controller.test:8443"+pollPath,
				dir, test.product, metadata.Protocol, public)
			if !errors.Is(err, test.want) {
				t.Fatalf("downloadGuestUpdate(%s) = %v, want %v", test.name, err, test.want)
			}
			file, err := os.ReadFile(path)
			if test.want == nil {
				if got != metadata.Digest || err != nil || string(file) != body {
					t.Errorf("downloaded artifact = %x/%q (%v), want verified bytes", got, file, err)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Errorf("rejected artifact left a staging file (%v)", err)
			}
		})
	}
}

type guestUpdateTransport func(*http.Request) (*http.Response, error)

func (f guestUpdateTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
