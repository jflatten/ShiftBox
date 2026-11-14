// Package migrations embeds the application-owned schema history for both profiles.
package migrations

import "embed"

// Files is immutable release input consumed by the Goose migration runner.
//
//go:embed sqlite/*.sql postgres/*.sql
var Files embed.FS
