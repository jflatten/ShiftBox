//go:build shiftbox_ui

package main

import (
	"io/fs"
	"net/http"

	"github.com/jflatten/ShiftBox/internal/web"
	"github.com/jflatten/ShiftBox/ui"
)

func loginFrontendAssets() (fs.FS, error) {
	files, err := ui.Assets()
	if err != nil {
		return nil, err
	}
	return fs.Sub(files, "dex/frontend")
}

func browserAssets() (http.Handler, error) {
	files, err := ui.Assets()
	if err != nil {
		return nil, err
	}
	return web.NewStaticHandler(files)
}
