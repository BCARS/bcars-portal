package web

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A screen is orphaned when nothing a caller can click leads to it. Registering
// a route, a capability and a handler is enough to make a screen work and not
// enough to make it findable, and nothing noticed the difference: payment
// batches and renewal worksheets were linked only from the dashboard, so the
// Treasury page was a dead end for months, and "/" served a plaintext 404 so the
// portal had no front door at all (bcars-portal-8yj).
//
// These tests walk the links, starting where a signed-in caller starts, and
// assert that every section screen is arrived at rather than typed.

// hrefPattern extracts link targets from rendered HTML.
var hrefPattern = regexp.MustCompile(`href="([^"]*)"`)

// crawl follows links breadth-first from start and returns every path that
// answered 200, along with every internal link seen on those pages.
//
// It follows only what a browser would follow on its own: same-origin GET
// links. Query strings are kept while crawling — a filtered list is a different
// page — but the recorded path is the bare one, because reachability is a
// question about screens, not about their filters.
func crawl(t *testing.T, mux *http.ServeMux, cookie *http.Cookie, start string) (visited map[string]bool, linked map[string]bool) {
	return crawlWithin(t, mux, cookie, start, func(string) bool { return true })
}

// crawlWithin is crawl, following only links the follow predicate accepts.
//
// Confinement is what makes a claim about a SECTION mean anything. Every page
// carries the header navigation, which links the dashboard, and the dashboard
// links everything — so an unconfined crawl started anywhere reaches the whole
// officer surface and would report the reported bug as fixed while Treasury was
// still a dead end. This test file's first draft did exactly that.
func crawlWithin(t *testing.T, mux *http.ServeMux, cookie *http.Cookie, start string, follow func(string) bool) (visited map[string]bool, linked map[string]bool) {
	t.Helper()

	visited = map[string]bool{}
	linked = map[string]bool{}
	seen := map[string]bool{start: true}
	queue := []string{start}

	for len(queue) > 0 {
		target := queue[0]
		queue = queue[1:]

		req := httptest.NewRequest("GET", target, nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		path := strings.SplitN(target, "?", 2)[0]
		if w.Code != http.StatusOK {
			// A link that leads somewhere unrenderable is not a way to reach a
			// screen. Recording it as reached would let a broken link satisfy
			// the assertion below.
			continue
		}
		visited[path] = true

		for _, m := range hrefPattern.FindAllStringSubmatch(w.Body.String(), -1) {
			href := m[1]
			// Off-site, in-page and non-navigational targets are not links to
			// screens of this portal.
			if href == "" || strings.HasPrefix(href, "#") ||
				strings.HasPrefix(href, "http") || strings.HasPrefix(href, "//") ||
				strings.HasPrefix(href, "javascript:") || strings.HasPrefix(href, "mailto:") {
				continue
			}
			if !strings.HasPrefix(href, "/") || strings.HasPrefix(href, RouteStatic) {
				continue
			}
			linked[strings.SplitN(href, "?", 2)[0]] = true
			if !follow(strings.SplitN(href, "?", 2)[0]) {
				continue
			}
			if !seen[href] {
				seen[href] = true
				queue = append(queue, href)
			}
		}
	}
	return visited, linked
}

// sectionRoutes returns the GET routes that name a screen in their own right —
// the ones a caller is expected to navigate TO.
//
// Parameterised routes are excluded on purpose and are covered separately by
// linkedRoutePatterns below: /admin/members/{id} is reached from the member
// list, and asserting a crawl reaches it would only assert that the fixture
// happens to contain a member.
func sectionRoutes(routes []GuardedRoute, prefix string) []string {
	var out []string
	for _, rt := range routes {
		if !strings.HasPrefix(rt.Pattern, "GET ") {
			continue
		}
		path := strings.TrimPrefix(rt.Pattern, "GET ")
		if strings.Contains(path, "{") || !strings.HasPrefix(path, prefix) {
			continue
		}
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func TestEveryOfficerScreenIsReachableFromTheNavigation(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")

	visited, _ := crawl(t, e.mux, e.cookie, "/admin/")

	// GuardedRoutes, not AdminRoutes: the officer surface is assembled from
	// several tables, and a check that read only one of them would be blind to
	// exactly the screens most recently added.
	for _, route := range sectionRoutes(e.h.GuardedRoutes(), "/admin/") {
		assert.Truef(t, visited[route],
			"%s is registered but nothing reachable from /admin/ links to it; "+
				"an officer can only get there by typing the URL", route)
	}
}

// TestTreasuryReachesItsOwnScreens is the reported symptom, asserted where it
// was felt. The officer crawl above would also pass if batches and worksheets
// were reachable only from the dashboard, which is exactly the state that was
// shipped: Treasury is where a treasurer goes, and from there both were dead
// ends.
func TestTreasuryReachesItsOwnScreens(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")

	// Confined to the treasury section on purpose: reaching a worksheet by
	// going out to the dashboard and back is the very complaint.
	visited, _ := crawlWithin(t, e.mux, e.cookie, "/admin/treasury", func(path string) bool {
		return strings.HasPrefix(path, "/admin/treasury")
	})

	for _, route := range []string{
		"/admin/treasury/standing",
		"/admin/treasury/batches",
		"/admin/treasury/worksheets",
	} {
		assert.Truef(t, visited[route],
			"%s cannot be reached from /admin/treasury without going back to the dashboard", route)
	}
}

func TestEveryMemberScreenIsReachableFromTheNavigation(t *testing.T) {
	e := setupDirectoryEligibleMember(t)

	visited, _ := crawl(t, e.mux, e.cookie, RouteMemberHome)

	for _, route := range sectionRoutes(e.h.GuardedRoutes(), "/member/") {
		assert.Truef(t, visited[route],
			"%s is registered but nothing reachable from %s links to it", route, RouteMemberHome)
	}

	// The shared preference screen is in the member navigation too; a member
	// who cannot find it cannot change the text size, which is the one
	// accessibility control the portal offers.
	assert.True(t, visited[RouteTextSize],
		"the text size preference is not reachable from the member navigation")
}

// TestEveryDetailScreenIsLinkedFromSomewhere covers the routes the crawls
// deliberately skip: those with an {id}. A crawl cannot reach them without a
// fixture holding one of every record type, and such a fixture goes stale more
// quietly than the thing it would be guarding.
//
// This is therefore a deliberately WEAKER check, and it is worth being precise
// about what it does not prove. It proves only that some template contains a
// link to the screen. It does not prove that the template is reachable, that
// the link renders, or that the caller may follow it. What it catches is a
// detail screen that NOTHING links to — the state a newly added screen is in
// before anyone wires it up. The section-level crawls above are the strong
// checks; this one closes the gap they cannot cover cheaply.
func TestEveryDetailScreenIsLinkedFromSomewhere(t *testing.T) {
	e := setupHandler(t)

	names, err := fs.Glob(templateFS, "templates/*.html")
	require.NoError(t, err)
	require.NotEmpty(t, names, "no templates embedded; this test would assert nothing")

	sources := make([]string, 0, len(names))
	for _, name := range names {
		src, err := templateFS.ReadFile(name)
		require.NoError(t, err)
		sources = append(sources, string(src))
	}

	for _, rt := range e.h.GuardedRoutes() {
		if !strings.HasPrefix(rt.Pattern, "GET ") {
			continue
		}
		path := strings.TrimPrefix(rt.Pattern, "GET ")
		if !strings.Contains(path, "{") {
			continue
		}
		// The literal part of the route, up to its first parameter, is what any
		// link to it must begin with.
		prefix := path[:strings.Index(path, "{")]

		var found bool
		for _, src := range sources {
			if strings.Contains(src, `href="`+prefix) {
				found = true
				break
			}
		}
		assert.Truef(t, found,
			"%s is registered but no template links to it at all; it can only be opened by typing an id", path)
	}
}

// setupDirectoryEligibleMember returns a signed-in member who can see every
// member screen, including the directory.
//
// Directory eligibility is not a capability: it is an active grant to a person
// holding an active, approved FULL membership. An Associate holds
// directory.read and is still refused the listing, which is why the home page
// asks the service rather than the principal (bcars-portal-4ux.12). A crawl run
// as an ineligible member would find no directory link and would be right not
// to — so the fixture makes the member eligible, and the assertion is about
// whether the link exists for someone who should have it.
func setupDirectoryEligibleMember(t *testing.T) *memberTestEnv {
	t.Helper()

	e := setupMemberEnv(t)
	personID := e.grant(t, "Reachability Member")

	_, err := e.h.db.Exec(
		`INSERT INTO memberships (person_id, base_type, lifecycle, joined_on)
		 VALUES (?, 'full', 'approved', '2026-01-01')`, personID)
	require.NoError(t, err)

	e.testEnv.cookie = e.signInMember(t)
	return e
}

// TestTheRootPathIsAFrontDoor covers the report that reaching the portal meant
// knowing to type /login: "/" answered with net/http's plaintext 404.
func TestTheRootPathIsAFrontDoor(t *testing.T) {
	t.Run("anonymous is sent to sign in", func(t *testing.T) {
		e := setupHandler(t)

		w := httptest.NewRecorder()
		e.mux.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))

		require.Equal(t, http.StatusSeeOther, w.Code, "the root must not be a dead end")
		assert.Equal(t, RouteLogin, w.Header().Get("Location"))
	})

	t.Run("an officer is sent to the officer landing", func(t *testing.T) {
		e := setupHandlerWithRoles(t, "administrator")

		w := httptest.NewRecorder()
		e.mux.ServeHTTP(w, e.authedRequest("GET", "/"))

		require.Equal(t, http.StatusSeeOther, w.Code)
		assert.Equal(t, "/admin/", w.Header().Get("Location"))
	})

	t.Run("a member is sent to their own landing", func(t *testing.T) {
		e := setupDirectoryEligibleMember(t)

		w := e.getAs(t, "/", e.testEnv.cookie)

		require.Equal(t, http.StatusSeeOther, w.Code)
		assert.Equal(t, RouteMemberHome, w.Header().Get("Location"),
			"a member must not be sent to the officer dashboard")
	})

	// "GET /" is net/http's catch-all. Without an explicit guard it answers for
	// every unclaimed path, which would turn every typo and stale link into a
	// redirect to the dashboard — a 200-shaped answer for a page that does not
	// exist.
	t.Run("an unknown path is still not found", func(t *testing.T) {
		e := setupHandlerWithRoles(t, "administrator")

		for _, path := range []string{"/nope", "/admin-typo", "/member-typo/x"} {
			w := httptest.NewRecorder()
			e.mux.ServeHTTP(w, e.authedRequest("GET", path))
			assert.Equalf(t, http.StatusNotFound, w.Code,
				"%s must be a not-found, not a redirect", path)
		}
	})
}

// The not-found page is the one screen every wrong URL reaches, so it is the
// screen most likely to be seen by someone who is lost — including someone not
// signed in. These tests pin what it says and, more importantly, what chrome it
// wears (bcars-portal-i4a).

func TestUnknownPathsUnderAPrefixAreNotFound(t *testing.T) {
	officer := setupHandlerWithRoles(t, "administrator")

	// "GET /admin/" and "GET /member/" are net/http prefix patterns: without an
	// explicit check they answer for everything beneath them, so a mistyped
	// path renders a real page with a 200 and looks like it worked.
	for _, path := range []string{
		"/admin/nonexistent",
		"/admin/nope/deeper",
		"/admin/treasury-typo",
		"/member/nonexistent",
		"/member/records-typo",
	} {
		w := httptest.NewRecorder()
		officer.mux.ServeHTTP(w, officer.authedRequest("GET", path))
		assert.Equalf(t, http.StatusNotFound, w.Code,
			"%s renders a page instead of reporting that it does not exist", path)
		assert.NotContainsf(t, w.Body.String(), "Active Memberships",
			"%s served the dashboard's content under its status", path)
	}

	// A path with dot segments is cleaned and redirected by net/http before any
	// handler sees it; following that redirect is what reaches the check above.
	// Asserted here so the 307 is understood as ServeMux behaviour rather than
	// mistaken for a route answering.
	w0 := httptest.NewRecorder()
	officer.mux.ServeHTTP(w0, officer.authedRequest("GET", "/admin/members/../nope"))
	assert.Equal(t, http.StatusTemporaryRedirect, w0.Code)
	assert.Equal(t, "/admin/nope", w0.Header().Get("Location"))

	// The prefixes themselves still work.
	w := httptest.NewRecorder()
	officer.mux.ServeHTTP(w, officer.authedRequest("GET", "/admin/"))
	assert.Equal(t, http.StatusOK, w.Code, "the dashboard itself must still render")

	member := setupDirectoryEligibleMember(t)
	w = member.getAs(t, RouteMemberHome, member.testEnv.cookie)
	assert.Equal(t, http.StatusOK, w.Code, "the member landing itself must still render")
}

// TestAMemberReachingTheOfficerDashboardIsStillRedirected guards the behaviour
// the not-found check sits in front of: the redirect is chosen before the path
// check would matter, and a member must not receive a 404 for /admin/ when what
// they should get is their own landing.
func TestAMemberReachingTheOfficerDashboardIsStillRedirected(t *testing.T) {
	e := setupDirectoryEligibleMember(t)

	w := e.getAs(t, "/admin/", e.testEnv.cookie)

	require.Equal(t, http.StatusSeeOther, w.Code, "a member must be redirected, not refused")
	assert.Equal(t, RouteMemberHome, w.Header().Get("Location"))
}

func TestTheNotFoundPageWearsTheCallersChrome(t *testing.T) {
	t.Run("a signed-out visitor gets no navigation at all", func(t *testing.T) {
		e := setupHandler(t)

		w := httptest.NewRecorder()
		e.mux.ServeHTTP(w, httptest.NewRequest("GET", "/nope", nil))
		require.Equal(t, http.StatusNotFound, w.Code)

		body := w.Body.String()
		// The officer header advertises screens this caller is not signed in
		// to, and offers a Sign Out button to someone who is not signed in.
		for _, leaked := range []string{
			`href="/admin/members"`, `href="/admin/treasury"`, `href="/admin/imports"`, "Sign Out",
		} {
			assert.NotContainsf(t, body, leaked,
				"the public not-found page shows %q from the officer navigation", leaked)
		}
		assert.Contains(t, body, `href="`+RouteLogin+`"`,
			"the only way out offered to a signed-out visitor must be the sign-in page")
	})

	t.Run("an officer keeps the officer navigation", func(t *testing.T) {
		e := setupHandlerWithRoles(t, "administrator")

		w := httptest.NewRecorder()
		e.mux.ServeHTTP(w, e.authedRequest("GET", "/nope"))
		require.Equal(t, http.StatusNotFound, w.Code)

		body := w.Body.String()
		assert.Contains(t, body, `href="/admin/members"`, "an officer should still be able to navigate")
		assert.Contains(t, body, `href="/admin/"`)
	})

	t.Run("a member gets the member navigation and their own landing", func(t *testing.T) {
		e := setupDirectoryEligibleMember(t)

		w := e.getAs(t, "/nope", e.testEnv.cookie)
		require.Equal(t, http.StatusNotFound, w.Code)

		body := w.Body.String()
		assert.NotContains(t, body, `href="/admin/members"`,
			"a member must not be shown officer navigation on an error page")
		assert.Contains(t, body, `href="`+RouteMemberHome+`"`,
			"the way out offered to a member must be their own landing")
	})
}

// TestAnUnknownAPIPathAnswersAsAnAPI covers a regression introduced with the
// front door: the catch-all that gave browsers a real page also began handing
// HTML to API clients, which cannot tell a missing endpoint from a broken
// deployment when the body is a web page.
func TestAnUnknownAPIPathAnswersAsAnAPI(t *testing.T) {
	e := setupHandler(t)

	for _, tc := range []struct{ name, path, accept string }{
		{"an api path", "/api/v1/nope", ""},
		{"a json client", "/nope", "application/json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.path, nil)
			if tc.accept != "" {
				req.Header.Set("Accept", tc.accept)
			}
			w := httptest.NewRecorder()
			e.mux.ServeHTTP(w, req)

			require.Equal(t, http.StatusNotFound, w.Code)
			assert.Equal(t, "application/problem+json", w.Header().Get("Content-Type"))
			assert.NotContains(t, w.Body.String(), "<!DOCTYPE html>",
				"an API client received a web page")

			var problem map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &problem),
				"the body must be a problem document")
			assert.Equal(t, float64(http.StatusNotFound), problem["status"])
		})
	}

	// A browser asking for HTML still gets the page.
	req := httptest.NewRequest("GET", "/nope", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	assert.Contains(t, w.Body.String(), "<!DOCTYPE html>", "a browser must still get the page")
}
