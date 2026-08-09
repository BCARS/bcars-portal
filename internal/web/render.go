// Package web provides the server-rendered administrative UI for the BCARS portal.
// Templates use Go html/template with HTMX for progressive enhancement.
package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"path"
	"sort"
)

// layoutTemplate is the shared base other pages embed.
const layoutTemplate = "layout.html"

//go:embed templates/*.html
var templateFS embed.FS

// Renderer holds parsed templates ready for execution.
type Renderer struct {
	templates map[string]*template.Template
}

// NewRenderer parses all templates from the embedded filesystem.
// Each page template includes the layout as a base.
func NewRenderer() (*Renderer, error) {
	r := &Renderer{templates: make(map[string]*template.Template)}

	// Every embedded template is registered, discovered from the filesystem
	// rather than from a hand-maintained list. A list is how forgot_password,
	// reset_password and accept_invitation came to be embedded but never
	// parsed, so every recovery and invitation page failed to render at
	// runtime with no build-time signal.
	names, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("web: list templates: %w", err)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("web: no templates embedded")
	}

	for _, name := range names {
		page := path.Base(name)
		if page == layoutTemplate {
			continue // the layout is a base, never rendered on its own
		}

		src, err := templateFS.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("web: read %s: %w", page, err)
		}

		var tmpl *template.Template
		if bytes.Contains(src, []byte(`{{template "`+layoutTemplate+`"`)) {
			tmpl, err = template.ParseFS(templateFS, "templates/"+layoutTemplate, name)
		} else {
			tmpl, err = template.ParseFS(templateFS, name)
		}
		if err != nil {
			return nil, fmt.Errorf("web: parse %s: %w", page, err)
		}
		r.templates[page] = tmpl
	}

	return r, nil
}

// TemplateNames returns every registered template name, for tests that assert
// coverage of the embedded filesystem.
func (r *Renderer) TemplateNames() []string {
	out := make([]string, 0, len(r.templates))
	for name := range r.templates {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Render executes a named template with the given data and writes to w.
func (r *Renderer) Render(w io.Writer, name string, data any) error {
	tmpl, ok := r.templates[name]
	if !ok {
		return fmt.Errorf("web: no such template %q", name)
	}
	return tmpl.Execute(w, data)
}

// RenderHTTP renders a template to an HTTP response writer.
//
// The template is rendered into a buffer first: writing the status line before
// knowing whether the render succeeds makes the failure unreportable, since
// http.Error cannot change a status already sent. Buffering also avoids
// emitting a half-written page.
func (r *Renderer) RenderHTTP(w http.ResponseWriter, name string, status int, data any) {
	var buf bytes.Buffer
	if err := r.Render(&buf, name, data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}
