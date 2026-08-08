// Package web provides the server-rendered administrative UI for the BCARS portal.
// Templates use Go html/template with HTMX for progressive enhancement.
package web

import (
	"embed"
	"html/template"
	"io"
	"net/http"
)

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

	pages := []string{
		"dashboard.html",
		"members.html",
		"member_detail.html",
		"member_form.html",
		"imports.html",
		"import_detail.html",
		"contact_form.html",
	}

	for _, page := range pages {
		tmpl, err := template.ParseFS(templateFS,
			"templates/layout.html",
			"templates/"+page,
		)
		if err != nil {
			return nil, err
		}
		r.templates[page] = tmpl
	}

	// Standalone templates (no layout).
	standalone := []string{"login.html"}
	for _, page := range standalone {
		tmpl, err := template.ParseFS(templateFS, "templates/"+page)
		if err != nil {
			return nil, err
		}
		r.templates[page] = tmpl
	}

	return r, nil
}

// Render executes a named template with the given data and writes to w.
func (r *Renderer) Render(w io.Writer, name string, data any) error {
	tmpl, ok := r.templates[name]
	if !ok {
		return &template.Error{}
	}
	return tmpl.Execute(w, data)
}

// RenderHTTP renders a template to an HTTP response writer.
func (r *Renderer) RenderHTTP(w http.ResponseWriter, name string, status int, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := r.Render(w, name, data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}
