//go:build shiftbox_ui

package main

import (
	"errors"
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

func helperDiskImage() ([]byte, error) {
	files, err := ui.Assets()
	if err != nil {
		return nil, err
	}
	// The browser static handler excludes _shiftbox. Only the authenticated
	// download route serves this optional private MVP artifact.
	image, err := fs.ReadFile(files, "_shiftbox/ShiftBox-Local-Helper.dmg")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return image, err
}
