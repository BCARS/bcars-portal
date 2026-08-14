package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vendoredDigests pins the bytes of every vendored asset. The same digests are
// recorded in internal/web/static/README.md; a mismatch means the file was
// modified, truncated, or replaced without the provenance being updated.
var vendoredDigests = map[string]string{
	"htmx-2.0.4.min.js": "e209dda5c8235479f3166defc7750e1dbcd5a5c1808b7792fc2e6733768fb447",
}

// scriptSrc matches the src of every <script> element in a rendered page.
var scriptSrc = regexp.MustCompile(`<script[^>]+src="([^"]+)"`)

// TestPagesLoadScriptsFromThisPortalOnly is the point of the change, asserted
// through the surface a browser touches: render a real page, take every script
// the page tells the browser to fetch, and require each one to be same-origin
// and actually served by this binary.
//
// It deliberately does not assert "layout.html does not contain unpkg.com" and,
// separately, "a /static/ route exists". Both of those pass while a page still
// points at an asset the binary does not serve — the shape of failure recorded
// in docs/lessons-learned.md. The assertion here is end to end: the src in the
// HTML is fetched back through the same mux.
func TestPagesLoadScriptsFromThisPortalOnly(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")

	for _, page := range []string{"/admin/", "/admin/members"} {
		body := e.body(t, "GET", page, "")
		matches := scriptSrc.FindAllStringSubmatch(body, -1)
		require.NotEmpty(t, matches, "%s loads no script at all; this test would assert nothing", page)

		for _, m := range matches {
			src := m[1]
			require.Truef(t, strings.HasPrefix(src, "/"),
				"%s loads %q from another origin; assets must ship in the binary", page, src)
			require.Falsef(t, strings.HasPrefix(src, "//"),
				"%s loads %q protocol-relative, which is still a third-party fetch", page, src)

			w := httptest.NewRecorder()
			e.mux.ServeHTTP(w, httptest.NewRequest("GET", src, nil))
			require.Equalf(t, http.StatusOK, w.Code,
				"%s references %s, which this binary does not serve", page, src)
			assert.NotEmptyf(t, w.Body.Bytes(), "%s served empty", src)
		}
	}
}

// TestHTMXIsServedFromTheBinary checks that the asset the pages reference is
// the real library rather than a placeholder that happens to return 200.
func TestHTMXIsServedFromTheBinary(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")

	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, httptest.NewRequest("GET", AssetHTMX, nil))
	require.Equal(t, http.StatusOK, w.Code)

	body := w.Body.String()
	assert.Contains(t, body, "htmx", "the served asset is not htmx")
	assert.Greater(t, len(body), 20_000, "the served asset is too small to be the htmx build")
	assert.Contains(t, w.Header().Get("Content-Type"), "javascript",
		"a script served as the wrong type is refused by the browser")
	assert.Contains(t, w.Header().Get("Cache-Control"), "immutable",
		"version-named assets should be cacheable indefinitely")
}

// TestStaticAssetsAreServedToAnonymousCallers guards a failure that only shows
// up on the page where it matters most: sign-in is rendered before any session
// exists, so an asset behind the capability check would leave that page — and
// every recovery page — without its script.
func TestStaticAssetsAreServedToAnonymousCallers(t *testing.T) {
	e := setupHandler(t)

	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, httptest.NewRequest("GET", AssetHTMX, nil))
	require.Equal(t, http.StatusOK, w.Code, "a signed-out browser must still load assets")
}

// TestStaticRouteServesNothingBeyondTheEmbeddedFiles covers the two ways a file
// mount leaks: walking out of it, and listing what is in it.
func TestStaticRouteServesNothingBeyondTheEmbeddedFiles(t *testing.T) {
	e := setupHandler(t)

	for _, target := range []string{
		RouteStatic,
		RouteStatic + "../handler.go",
		RouteStatic + "..%2fhandler.go",
		RouteStatic + "nope.js",
	} {
		w := httptest.NewRecorder()
		e.mux.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
		assert.NotEqualf(t, http.StatusOK, w.Code, "%s must not serve content", target)
		assert.NotContainsf(t, w.Body.String(), "package web",
			"%s reached repository source", target)
	}
}

// TestVendoredAssetDigests fails the test gate, rather than the browser, when a
// vendored file no longer matches the provenance recorded for it.
func TestVendoredAssetDigests(t *testing.T) {
	assets := staticAssets()

	names, err := fs.Glob(assets, "*.js")
	require.NoError(t, err)
	require.NotEmpty(t, names, "no assets are embedded")

	for _, name := range names {
		want, recorded := vendoredDigests[name]
		require.Truef(t, recorded,
			"%s is embedded but has no recorded digest; add it here and to static/README.md", name)

		data, err := fs.ReadFile(assets, name)
		require.NoError(t, err)
		sum := sha256.Sum256(data)
		assert.Equalf(t, want, hex.EncodeToString(sum[:]),
			"%s does not match its recorded digest", name)
	}

	// Every vendored library keeps its license alongside it.
	_, err = fs.ReadFile(assets, "LICENSE-htmx.txt")
	assert.NoError(t, err, "htmx's license must ship with the code it covers")
}
