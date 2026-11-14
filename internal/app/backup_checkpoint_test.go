package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/backup"
)

// TestCheckpointManagementPausesMutationsAndSignIn verifies the browser-side
// gate without holding an operation event stream open across a checkpoint.
func TestCheckpointManagementPausesMutationsAndSignIn(t *testing.T) {
	fence := &backup.WriteFence{}
	resume, err := fence.Pause(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer resume()
	handler := CheckpointManagement(fence, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, item := range []struct {
		method, path string
		want         int
	}{
		{"POST", "/api/v1/workspaces", 503},
		{"GET", "/api/v1/git/authorization/callback", 503},
		{"GET", "/auth/callback", 503},
		{"GET", "/dex/keys", 503},
		{"GET", "/api/v1/operations/fixture/events", 204},
		{"GET", "/workspaces/", 204},
	} {
		ctx, stop := context.WithTimeout(t.Context(), 20*time.Millisecond)
		req := httptest.NewRequestWithContext(ctx, item.method, item.path, nil)
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		stop()
		if out.Code != item.want {
			t.Errorf("%s %s during checkpoint = %d; want %d", item.method, item.path, out.Code, item.want)
		}
	}
}
