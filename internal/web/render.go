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

// tokensTemplate holds the design tokens and shared base styles. It is parsed
// into every template, base and standalone alike, because the standalone
// sign-in and recovery pages need the same palette and type scale as the rest
// of the portal and previously carried their own copies of it.
const tokensTemplate = "tokens.html"

// isShared reports whether a template is machinery rather than a page: a base
// layout or the token partial. Neither is ever rendered on its own.
func isShared(page string) bool {
	return isLayout(page) || page == tokensTemplate
}

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
	// layouts records which pages embed a base. Only those are chrome-wrapped:
	// a standalone page is its own document and its data reaches the template
	// unwrapped, so login, recovery and print pages keep referencing their own
	// fields directly.
	layouts map[string]bool
}

// chrome carries the values a base layout needs around a page's own view model.
//
// The layout reads .TextSize; the page is handed .Page, so within
// {{define "content"}} the dot is still the handler's own data struct and no
// page template has to know it is wrapped. This is why the preference did not
// need a field added to each of the 60-odd view structs declared inside the
// handlers, which is both a large diff and one forgotten struct away from a
// page that renders at the wrong size.
type chrome struct {
	// Page is the handler's view model, and the dot inside "content".
	Page any
	// TextSize is the caller's stored size: "base" or "large". It lands in a
	// data-text-size attribute on <html>, so the size is settled by the markup
	// the server sent rather than applied by a script after first paint.
	TextSize string
	// Nav says which surfaces this caller can reach, so a header can offer the
	// way across to the other one and only to a caller who has one. It arrives
	// here rather than on each view model for the same reason TextSize does:
	// the alternative is a field added to sixty-odd structs, one of which would
	// be forgotten.
	Nav navLinks
}

// HasLayout reports whether a page embeds a base layout and is therefore
// chrome-wrapped when rendered. Exported for tests that assert every
// layout-embedding page receives the chrome the layout dereferences.
func (r *Renderer) HasLayout(page string) bool { return r.layouts[page] }

// NewRenderer parses all templates from the embedded filesystem.
// Each page template includes the layout as a base.
func NewRenderer() (*Renderer, error) {
	r := &Renderer{
		templates: make(map[string]*template.Template),
		layouts:   make(map[string]bool),
	}

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
		if isShared(page) {
			continue // machinery, never rendered on its own
		}

		src, err := templateFS.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("web: read %s: %w", page, err)
		}

		// The token partial is parsed into every page so that {{template
		// "tokens"}} resolves whether the page has a base layout or is
		// standalone. It is listed last on purpose: ParseFS names the returned
		// template after its FIRST file, and that is the one Execute runs, so
		// leading with the partial renders the partial and nothing else.
		tokens := "templates/" + tokensTemplate

		var tmpl *template.Template
		if base := layoutFor(src); base != "" {
			r.layouts[page] = true
			tmpl, err = template.ParseFS(templateFS, "templates/"+base, name, tokens)
		} else {
			tmpl, err = template.ParseFS(templateFS, name, tokens)
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

// RenderPage renders a page, wrapping its view model in chrome when the page
// embeds a base layout. Handlers call this rather than RenderHTTP so a page
// cannot reach the layout without the values the layout dereferences.
func (r *Renderer) RenderPage(w http.ResponseWriter, name string, status int, data any, textSize string, nav navLinks) {
	if r.layouts[name] {
		data = chrome{Page: data, TextSize: textSize, Nav: nav}
	}
	r.RenderHTTP(w, name, status, data)
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
