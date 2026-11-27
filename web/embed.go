// Package webui embeds the built SPA (web/dist, produced by `npm run build`).
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS is the content of web/dist: index.html and assets/.
var FS, _ = fs.Sub(dist, "dist")
