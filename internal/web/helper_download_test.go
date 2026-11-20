package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/web"
)

// TestHelperDownloadServesOnlyTheStagedArchive checks browser authentication,
// attachment metadata, and the missing-artifact response for an unstaged build.
func TestHelperDownloadServesOnlyTheStagedArchive(t *testing.T) {
	db, now := readySSHWorkspace(t)
	const origin = "https://management.test"
	token := connectionGrantSession(t, db, now, "user", "helper-download")
	credential := issueWorkspaceSSHFixtureCredential(t, db, now, "user", "helper-download-proxy")
	request := func(handler http.Handler, base, cookie, bearer string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, base+"/api/v1/ssh/helper/download", nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: cookie})
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	data := []byte("PK\x03\x04fixture archive contents")
	handler := web.NewWorkspaceSSHAPI(db, nil, nil, nil, func() time.Time { return now }, origin, data)
	if res := request(handler, origin, "", credential); res.Code != http.StatusUnauthorized {
		t.Errorf("helper credential downloaded archive: %d, want 401", res.Code)
	}
	if res := request(handler, "http://management.test", token, ""); res.Code != http.StatusForbidden {
		t.Errorf("browser over HTTP downloaded archive: %d, want 403", res.Code)
	}
	res := request(handler, origin, token, "")
	if res.Code != http.StatusOK || res.Body.String() != string(data) ||
		res.Header().Get("Content-Type") != "application/zip" || res.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(res.Header().Get("Content-Disposition"), "ShiftBox-Local-Helper.zip") {
		t.Fatalf("download = %d %v %q, want the staged ZIP attachment", res.Code, res.Header(), res.Body.String())
	}
	unstaged := web.NewWorkspaceSSHAPI(db, nil, nil, nil, func() time.Time { return now }, origin, nil)
	if res := request(unstaged, origin, token, ""); res.Code != http.StatusNotFound {
		t.Errorf("unstaged download = %d, want 404", res.Code)
	}
}
