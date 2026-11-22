package web

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/release"
)

// TestGuestUpdateDownloadRechecksAuthority proves that a revoked assignment
// cannot disclose the artifact even when its load has already started.
func TestGuestUpdateDownloadRechecksAuthority(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		body := []byte("pinned executable")
		resolve := func(context.Context, *x509.Certificate, time.Time) (string, error) { return "fixture", nil }
		load := func(_ context.Context, id string) (release.GuestUpdate, error) {
			if id != "fixture" {
				t.Errorf("artifact selection = %q, want fixture", id)
			}
			if revoked {
				resolve = func(context.Context, *x509.Certificate, time.Time) (string, error) {
					return "", guest.ErrGuestUnauthorized
				}
			}
			return release.GuestUpdate{Metadata: release.GuestUpdateMetadata{Release: id, Protocol: "1", Size: int64(len(body)),
				Digest: sha256.Sum256(body)}, Binary: body}, nil
		}
		handler := GuestUpdateHandler(func(ctx context.Context, leaf *x509.Certificate, now time.Time) (string, error) {
			return resolve(ctx, leaf, now)
		}, load, time.Now)
		req := httptest.NewRequest(http.MethodGet, guest.UpdateDownloadPath, nil)
		req.TLS = &tls.ConnectionState{Version: tls.VersionTLS13, VerifiedChains: [][]*x509.Certificate{{{}}}}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if revoked {
			if response.Code != 403 || response.Body.String() == string(body) {
				t.Errorf("revoked download = %d, want denial without executable", response.Code)
			}
		} else if response.Code != 200 || response.Body.String() != string(body) || response.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("authorized download = %d/%q, want pinned executable with no caching", response.Code, response.Body.String())
		}
	}
}
