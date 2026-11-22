//go:build shiftbox_ui

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEmbeddedBrowserAssets checks that the release build serves the actual
// generated application and a script referenced by its HTML.
func TestEmbeddedBrowserAssets(t *testing.T) {
	handler, err := browserAssets()
	if err != nil {
		t.Fatal(err)
	}
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if page.Code != 200 || !strings.Contains(page.Body.String(), "ShiftBox") {
		t.Fatalf("embedded home status = %d, want branded page", page.Code)
	}
	_, src, found := strings.Cut(page.Body.String(), `<script src="/_next/static/`)
	script, _, _ := strings.Cut(src, `"`)
	if !found || script == "" {
		t.Fatal("exported home did not reference a bundled script")
	}
	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/_next/static/"+script, nil))
	if asset.Code != 200 || asset.Body.Len() == 0 || !strings.Contains(asset.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("embedded script status = %d (%s), want JavaScript asset", asset.Code, asset.Header().Get("Content-Type"))
	}
}
