//go:build shiftbox_ui

package main

import (
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestEmbeddedMacOSHelper makes the container build reject an absent or
// malformed disk image before the controller image is published. The native
// packaging test mounts the installer to verify its contents and signature.
func TestEmbeddedMacOSHelper(t *testing.T) {
	if os.Getenv("SHIFTBOX_REQUIRE_MACOS_HELPER") != "1" {
		t.Skip("the container build supplies the native macOS helper disk image")
	}
	data, err := helperDiskImage()
	if err != nil {
		t.Fatal(err)
	}
	const trailerSize = 512
	if len(data) <= trailerSize {
		t.Fatalf("embedded macOS helper DMG missing or truncated (%d bytes)", len(data))
	}
	trailer := data[len(data)-trailerSize:]
	if string(trailer[:4]) != "koly" || binary.BigEndian.Uint32(trailer[8:12]) != trailerSize {
		t.Fatal("embedded macOS helper has no valid UDIF disk image trailer")
	}
}

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
