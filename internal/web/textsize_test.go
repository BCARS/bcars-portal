package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// body runs a request as the signed-in test user and returns the response
// body, requiring a 200. Most assertions here are about what the HTML says.
// htmlTag returns the page's opening <html ...> element, which is where the
// text size actually has to land. Asserting on the bare attribute instead
// matches the ":root[data-text-size=\"large\"]" selector that every page
// carries in its stylesheet, so such an assertion passes no matter what the
// server decided -- which is how the first draft of these tests was green
// against a wrapper hard-coded to the base size.
func htmlTag(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, "<html")
	require.GreaterOrEqual(t, i, 0, "page has no <html> element")
	j := strings.Index(body[i:], ">")
	require.GreaterOrEqual(t, j, 0, "unterminated <html> element")
	return body[i : i+j+1]
}

func (e *testEnv) body(t *testing.T, method, target, form string) string {
	t.Helper()
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, e.authedRequest(method, target, form))
	require.Equal(t, http.StatusOK, w.Code, "%s %s", method, target)
	return w.Body.String()
}

// TestTextSizePreferenceReachesTheRenderedPage is the point of the whole
// feature, asserted through the surface an officer actually touches.
//
// It deliberately does not check that the column was written and, separately,
// that the layout can render an attribute. Both of those can pass while the
// preference reaches no page at all, which is exactly the shape of failure
// bcars-portal-9zm.1 shipped. The assertion is: save "larger", then load an
// ordinary page, and find the larger size in the HTML the server sent.
func TestTextSizePreferenceReachesTheRenderedPage(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")

	// Before anything is saved, pages render at the base size.
	assert.Equal(t, `<html lang="en" data-text-size="base">`,
		htmlTag(t, e.body(t, "GET", "/admin/", "")),
		"an officer who has expressed no preference gets the base size")

	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, e.authedRequest("POST", RouteTextSize, "text_size=large"))
	require.Equal(t, http.StatusOK, w.Code)

	// The dashboard is a plain officer page that knows nothing about the
	// preference. If the chrome wrapper is removed, this is what goes red.
	assert.Equal(t, `<html lang="en" data-text-size="large">`,
		htmlTag(t, e.body(t, "GET", "/admin/", "")),
		"a saved preference must reach a page that never asked for it")

	// And it survives the round trip, rather than being a value the response
	// echoed back without storing.
	assert.Equal(t, `<html lang="en" data-text-size="large">`,
		htmlTag(t, e.body(t, "GET", "/admin/members", "")),
		"the preference is stored, not echoed")
}

// TestTextSizeIsStoredPerUserNotPerBrowser is the reason the value lives on
// the user row: officers share machines, so the treasurer's choice must not
// follow the secretary who signs in next at the same laptop.
func TestTextSizeIsStoredPerUserNotPerBrowser(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")

	// A second officer on the same installation.
	_, err := e.h.db.Exec(`INSERT INTO users (email, password_hash, password_algo_params, is_active)
		VALUES ('other@test.local', 'x', 'argon2id', 1)`)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, e.authedRequest("POST", RouteTextSize, "text_size=large"))
	require.Equal(t, http.StatusOK, w.Code)

	var other string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT text_size FROM users WHERE email = 'other@test.local'`).Scan(&other))
	assert.Equal(t, textSizeBase, other,
		"one officer choosing larger type must not resize the portal for everyone else")
}

// TestTextSizeRefusesAnUnknownValue keeps a hand-made form post from storing a
// size the stylesheet has no rule for, which would render the portal at the
// browser default rather than at either designed size.
func TestTextSizeRefusesAnUnknownValue(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")

	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, e.authedRequest("POST", RouteTextSize, "text_size=enormous"))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)

	var stored string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT text_size FROM users WHERE email = 'test@test.local'`).Scan(&stored))
	assert.Equal(t, textSizeBase, stored, "a refused value must not be written")
}

// TestTextSizeSurvivesAnUnreadablePreference confirms a failure to read the
// preference costs the caller their type size and not their page.
func TestTextSizeRejectsAnUnknownStoredValue(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")

	// Reach past the CHECK constraint the way a bad migration or a hand-edited
	// database would.
	_, err := e.h.db.Exec(`PRAGMA ignore_check_constraints = ON`)
	require.NoError(t, err)
	_, err = e.h.db.Exec(`UPDATE users SET text_size = 'gigantic' WHERE id = 1`)
	require.NoError(t, err)

	assert.Equal(t, `<html lang="en" data-text-size="base">`,
		htmlTag(t, e.body(t, "GET", "/admin/", "")),
		"an unrenderable stored size falls back to base rather than emitting itself")
}

// TestTextSizePageIsReachableByAMember keeps type size available to the role
// most likely to need it. A member holds no officer capability, so a page
// guarded by one would refuse them.
func TestTextSizePageIsReachableByAMember(t *testing.T) {
	e := setupHandlerWithRoles(t, "member")

	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, e.authedRequest("GET", RouteTextSize, ""))
	require.Equal(t, http.StatusOK, w.Code)

	body := w.Body.String()
	assert.NotContains(t, body, "/admin/",
		"the preference page is shared, so it must not offer a member an officer link")
	assert.Contains(t, body, RouteMemberHome,
		"a member returns to their own landing")
}

// TestEveryLayoutPageIsChromeWrapped guards the wrapper itself. A page that
// embeds a base but is rendered unwrapped fails at execution, and the failure
// surfaces as a 500 on that one page rather than at build time.
func TestEveryLayoutPageIsChromeWrapped(t *testing.T) {
	r, err := NewRenderer()
	require.NoError(t, err)

	names, err := fs.Glob(templateFS, "templates/*.html")
	require.NoError(t, err)

	var wrapped int
	for _, full := range names {
		page := path.Base(full)
		if isShared(page) {
			continue
		}
		src, err := templateFS.ReadFile(full)
		require.NoError(t, err)

		assert.Equal(t, layoutFor(src) != "", r.HasLayout(page),
			"page %s must be chrome-wrapped exactly when it embeds a base", page)
		if r.HasLayout(page) {
			wrapped++
		}
	}
	assert.NotZero(t, wrapped, "the officer and member shells both have pages")
}

// hexColour matches a literal colour where CSS accepts one: after a property
// colon or a shorthand space, and ending the value rather than running on into
// a word. Anchoring it this way keeps a URL fragment such as '#add-row' from
// reading as the colour #add.
var hexColour = regexp.MustCompile(`[:\s]#[0-9a-fA-F]{3,8}\s*[;)!}"']`)

// TestTemplatesDoNotHardCodeColours is the token pass expressed as an
// invariant rather than as a one-off cleanup.
//
// Seven files each carried their own copy of the palette, which is how the
// page background came to be #f5f5f5 on some screens and #f7f7f5 on others
// with no single line looking wrong. Adding a literal to a page is how that
// starts again, so it fails here instead.
func TestTemplatesDoNotHardCodeColours(t *testing.T) {
	names, err := fs.Glob(templateFS, "templates/*.html")
	require.NoError(t, err)

	for _, full := range names {
		page := path.Base(full)
		if page == tokensTemplate {
			continue // the one file that is allowed to name a colour
		}
		src, err := templateFS.ReadFile(full)
		require.NoError(t, err)

		for i, line := range strings.Split(string(src), "\n") {
			m := strings.Trim(hexColour.FindString(line), " \t:;)!}\"'")
			if m == "" {
				continue
			}
			// #fff and #000 stay literal: pure white and pure black on a
			// printed sheet or a dark header are not palette choices, and
			// tokenising them reads worse than it reads now.
			if strings.EqualFold(m, "#fff") || strings.EqualFold(m, "#000") ||
				strings.EqualFold(m, "#ffffff") || strings.EqualFold(m, "#000000") {
				continue
			}
			t.Errorf("%s:%d names the colour %s directly; add a token in %s and use var()",
				page, i+1, m, tokensTemplate)
		}
	}
}
