package integration_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jflatten/ShiftBox/internal/auth"
)

// TestReleaseGatePreviewCannotDisableMembership verifies that even a valid
// management session and CSRF token cannot authorize a sibling preview origin.
func TestReleaseGatePreviewCannotDisableMembership(t *testing.T) {
	f := newRevocationFixture(t)
	mux := http.NewServeMux()
	mux.Handle("PATCH /api/v1/admin/users/{userId}", f.membershipHandler())
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	send := func(origin string, want int) {
		t.Helper()
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPatch,
			server.URL+"/api/v1/admin/users/"+f.owner, strings.NewReader(`{"membershipState":"disabled"}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Origin", origin)
		request.Header.Set("X-CSRF-Token", auth.CSRFToken(f.adminToken))
		request.Header.Set("If-Match", `"1"`)
		request.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: f.adminToken})
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("disable with origin %q returned HTTP %d, want %d", origin, response.StatusCode, want)
		}
	}
	send("https://preview-8080--"+f.ref.WorkspaceID+".preview.test", http.StatusForbidden)
	var state string
	if err := f.db.QueryRowContext(t.Context(), "SELECT membership_state FROM users WHERE id=$1", f.owner).Scan(&state); err != nil || state != "active" {
		t.Fatalf("membership after denied mutation = %q (%v), want active", state, err)
	}
	send(revocationManagementOrigin, http.StatusOK)
	if err := f.db.QueryRowContext(t.Context(), "SELECT membership_state FROM users WHERE id=$1", f.owner).Scan(&state); err != nil || state != "disabled" {
		t.Fatalf("membership after authorized mutation = %q (%v), want disabled", state, err)
	}
}
