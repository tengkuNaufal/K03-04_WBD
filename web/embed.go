// Package webui embeds the HTML templates and static assets.
package webui

import "embed"

//go:embed templates static
var FS embed.FS
