//go:build !shiftbox_ui

package main

import (
	"io/fs"
	"net/http"
)

// Plain Go builds support backend development without an npm build.
func browserAssets() (http.Handler, error) { return nil, nil }

func loginFrontendAssets() (fs.FS, error) { return nil, nil }
