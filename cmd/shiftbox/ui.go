//go:build shiftbox_ui

package main

import (
	"net/http"

	"github.com/jflatten/ShiftBox/internal/web"
	"github.com/jflatten/ShiftBox/ui"
)

func browserAssets() (http.Handler, error) {
	files, err := ui.Assets()
	if err != nil {
		return nil, err
	}
	return web.NewStaticHandler(files)
}
