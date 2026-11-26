package web

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// spa serves the built frontend. Files under assets/ are served as they are
// (hashed names, cacheable); every other path returns index.html so the
// client-side router handles it.
func (s *Server) spa(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, http.StatusNotFound, "Endpoint tidak ditemukan.")
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if strings.HasPrefix(name, "assets/") || name == "favicon.svg" {
		if _, err := fs.Stat(s.Assets, name); err == nil {
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.ServeFileFS(w, r, s.Assets, name)
			return
		}
		http.NotFound(w, r)
		return
	}
	index, err := fs.ReadFile(s.Assets, "index.html")
	if err != nil {
		http.Error(w, "Frontend belum di-build. Jalankan: cd web && npm install && npm run build", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(index)
}
