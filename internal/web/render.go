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

// layoutTemplate is the officer UI's shared base.
const layoutTemplate = "layout.html"

// memberLayoutTemplate is the member UI's base. The two surfaces do not share
// one: the officer chrome links to /admin/ pages a member is refused, and a
// layout that had to ask which caller it was rendering for would be one
// forgotten condition away from advertising them (bcars-portal-4ux.11).
const memberLayoutTemplate = "member_layout.html"

// layoutTemplates are the bases. A base is never rendered on its own, and a
// page names the one it embeds, so adding a base here is all it takes for
// pages to use it.
var layoutTemplates = []string{layoutTemplate, memberLayoutTemplate}

// layoutFor reports which base a page embeds, or "" when it is self-contained.
func layoutFor(src []byte) string {
	for _, base := range layoutTemplates {
		if bytes.Contains(src, []byte(`{{template "`+base+`"`)) {
			return base
		}
	}
	return ""
}

func isLayout(page string) bool {
	for _, base := range layoutTemplates {
		if page == base {
			return true
		}
	}
	return false
}

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
		if isLayout(page) {
			continue // a base is never rendered on its own
		}

		src, err := templateFS.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("web: read %s: %w", page, err)
		}

		var tmpl *template.Template
		if base := layoutFor(src); base != "" {
			tmpl, err = template.ParseFS(templateFS, "templates/"+base, name)
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
