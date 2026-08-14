package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// The admin UI's JavaScript is vendored into the binary rather than fetched
// from a CDN at render time. A members portal that had to reach unpkg.com to
// draw a page handling member PII would break on an offline or firewalled
// deployment, leak a request per page load to a third party, and take that
// third party's supply chain as its own (bcars-portal-chp).
//
// Assets are named with the version they contain, so the URL changes whenever
// the bytes do and the response can be cached indefinitely.

// Only the files the browser asks for are embedded; the directory's README is
// documentation for maintainers, not a served asset.
//
//go:embed static/*.js static/*.txt
var staticFS embed.FS

// RouteStatic is the mount point for vendored assets. It is public: the sign-in
// page is served to anonymous callers and must be able to load the same assets
// as the rest of the portal.
const RouteStatic = "/static/"

// AssetHTMX is the URL of the vendored htmx build. layout.html references this
// path literally; TestLayoutReferencesVendoredAssets keeps the two in step.
const AssetHTMX = RouteStatic + "htmx-2.0.4.min.js"

// staticAssets is the embedded tree rooted at the asset directory, so request
// paths map to file names without the "static/" prefix leaking into URLs.
func staticAssets() fs.FS {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// Unreachable: the directory is embedded at compile time.
		panic("web: embedded static assets missing: " + err.Error())
	}
	return sub
}

// staticHandler serves the vendored assets. http.FileServerFS resolves only
// within the embedded filesystem, so a traversal attempt reaches nothing that
// was not compiled in.
func staticHandler() http.Handler {
	files := http.FileServerFS(staticAssets())
	return http.StripPrefix(RouteStatic, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No directory listings: the mount point exists to serve named files,
		// not to enumerate what the binary was built with.
		if r.URL.Path == "" || strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}

		// Every asset name carries its version, so a cached copy can never be
		// stale for the URL that named it.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		files.ServeHTTP(w, r)
	}))
}
