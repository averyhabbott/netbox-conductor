// Package webui embeds the compiled React frontend for serving from the Go binary.
package webui

import "embed"

//go:embed dist
var FS embed.FS
