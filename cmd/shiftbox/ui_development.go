//go:build !shiftbox_ui

package main

import "net/http"

// Plain Go builds support backend development without an npm build.
func browserAssets() (http.Handler, error) { return nil, nil }
