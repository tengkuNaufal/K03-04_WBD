package web

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"dbmp/internal/authz"
)

type Page struct {
	Title     string
	User      *authz.Principal
	CSRF      string
	Flash     string
	FlashKind string
	Error     string
	Data      any
}

var funcs = template.FuncMap{
	"fmtTime": func(t any) string {
		switch v := t.(type) {
		case time.Time:
			return v.Local().Format("2006-01-02 15:04:05")
		case *time.Time:
			if v != nil {
				return v.Local().Format("2006-01-02 15:04:05")
			}
		}
		return "-"
	},
	"add":     func(a, b int) int { return a + b },
	"canEdit": func(l authz.Level) bool { return authz.Allows(l, authz.ActionEdit) },
	"urlPath": url.PathEscape,
}

// Renderer holds one template set (layout + page) per page.
type Renderer struct{ pages map[string]*template.Template }

func NewRenderer(fsys fs.FS) (*Renderer, error) {
	files, err := fs.Glob(fsys, "templates/pages/*.html")
	if err != nil {
		return nil, err
	}
	r := &Renderer{pages: map[string]*template.Template{}}
	for _, f := range files {
		t, err := template.New("").Funcs(funcs).ParseFS(fsys, "templates/layout.html", f)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", f, err)
		}
		r.pages[strings.TrimSuffix(path.Base(f), ".html")] = t
	}
	return r, nil
}

func (r *Renderer) Render(w http.ResponseWriter, status int, name string, p Page) error {
	t, ok := r.pages[name]
	if !ok {
		return fmt.Errorf("unknown page %q", name)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", p); err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, err := buf.WriteTo(w)
	return err
}
