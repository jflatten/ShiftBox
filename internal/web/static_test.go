package web

import (
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// TestStaticExport verifies exported page and asset routing, including the
// boundaries around API routes, directories, and unsupported methods.
func TestStaticExport(t *testing.T) {
	files := fstest.MapFS{
		"index.html":         {Data: []byte("<h1>ShiftBox</h1>")},
		"setup/index.html":   {Data: []byte("<h1>Setup</h1>")},
		"_next/static/a.js":  {Data: []byte("const fixture = true;")},
		"setup/index.txt":    {Data: []byte("navigation")},
		"api/v1/index.html":  {Data: []byte("private")},
		"brand/favicon.ico":  {Data: []byte("icon")},
		".hidden/index.html": {Data: []byte("hidden")},
	}
	handler, err := NewStaticHandler(files)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, body, cache string
		status                    int
	}{
		{"GET", "/", "ShiftBox", "no-cache", 200},
		{"GET", "/setup", "Setup", "no-cache", 200},
		{"GET", "/setup/", "Setup", "no-cache", 200},
		{"GET", "/setup/index.txt", "navigation", "no-cache", 200},
		{"GET", "/_next/static/a.js", "const fixture", "public, max-age=31536000, immutable", 200},
		{"HEAD", "/brand/favicon.ico", "", "no-cache", 200},
		{"POST", "/", "", "", 405},
		{"GET", "/api/v1/", "", "", 404},
		{"GET", "/brand/", "", "", 404},
		{"GET", "/.hidden/", "", "", 404},
		{"GET", "/missing/", "", "", 404},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.status || w.Header().Get("Cache-Control") != tc.cache ||
				(tc.body != "" && !strings.Contains(w.Body.String(), tc.body)) ||
				(tc.method == "HEAD" && w.Body.Len() != 0) {
				t.Errorf("static %s %s = %d %q %q, want %d cache %q body containing %q",
					tc.method, tc.path, w.Code, w.Body, w.Header().Get("Cache-Control"), tc.status, tc.cache, tc.body)
			}
		})
	}
}

// TestStaticExportRequiresBuiltIndex catches incomplete release inputs at startup.
func TestStaticExportRequiresBuiltIndex(t *testing.T) {
	if _, err := NewStaticHandler(fstest.MapFS{}); err == nil {
		t.Fatal("NewStaticHandler(empty export) succeeded")
	}
}
