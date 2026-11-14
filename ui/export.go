//go:build shiftbox_ui

// Package ui contains the browser assets embedded in a release build.
package ui

import (
	"embed"
	"io/fs"
)

// Next exports route payloads and hashed assets under names that start with
// underscores. Include those along with the HTML and branding.
//
//go:embed all:out
var export embed.FS

// Assets returns the built browser application rooted at its export directory.
func Assets() (fs.FS, error) { return fs.Sub(export, "out") }
