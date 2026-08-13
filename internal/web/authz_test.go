package web

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/authn"
	"github.com/bcars/bcars-portal/internal/db"
	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/mail"
)

// setupHandlerWithRoles builds a handler whose signed-in user holds exactly
// the given roles. Passing none produces an authenticated user with no
// capabilities, which is the case that used to reach every admin route.
func setupHandlerWithRoles(t *testing.T, roles ...string) *testEnv {
	t.Helper()
	d, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	require.NoError(t, db.Migrate(d))

	hash, err := authn.HashPassword("testpass", nil, authn.DefaultParams())
	require.NoError(t, err)
	_, err = d.Exec(`INSERT INTO users (email, password_hash, password_algo_params, is_active)
		VALUES ('test@test.local', ?, 'argon2id', 1)`, hash)
	require.NoError(t, err)

	for _, role := range roles {
		_, err = d.Exec(`INSERT INTO user_role_grants (user_id, role_code, granted_by, granted_at, reason)
			VALUES (1, ?, 1, strftime('%Y-%m-%dT%H:%M:%fZ','now'), 'test setup')`, role)
		require.NoError(t, err)
	}

	mailer := testMailer(t)
	h, err := NewHandler(d, HandlerConfig{Mailer: mailer, BaseURL: "http://portal.example"})
	require.NoError(t, err)
	h.testMailer = mailer

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	form := url.Values{"email": {"test@test.local"}, "password": {"testpass"}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusSeeOther, w.Code)

	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == authn.DefaultSessionCookieName {
			cookie = c
		}
	}
	require.NotNil(t, cookie)

	return &testEnv{h: h, mux: mux, cookie: cookie}
}

// webGuardedRoutes mirrors the API regression set on the admin UI: these are
// the routes an authenticated member could previously reach because
// requireAuth checked only for a session.
var webGuardedRoutes = []struct {
	name       string
	method     string
	target     string
	body       string
	capability string
}{
	{"import list", "GET", "/admin/imports", "", "import.upload"},
	{"import upload", "POST", "/admin/imports/upload", "", "import.upload"},
	{"import commit", "POST", "/admin/imports/1/commit", "", "import.commit"},
	{"import discard", "POST", "/admin/imports/1/discard", "", "import.upload"},
	{"member list", "GET", "/admin/members", "", "member.read"},
	{"member create", "POST", "/admin/members/new", "display_name=X&sort_name=X", "member.create"},
	{"member deactivate", "POST", "/admin/members/1/deactivate", "", "member.deactivate"},
	{"note create", "POST", "/admin/members/1/notes", "body=hello", "notes.write.officer"},
}

// TestWebRoutes_DenyAuthenticatedWithoutCapability is the web half of the
// bcars-portal-fmc.1 regression. The "member" role holds session.self.read and
// its own self-service capabilities, none of which reach an officer page.
func TestWebRoutes_DenyAuthenticatedWithoutCapability(t *testing.T) {
	e := setupHandlerWithRoles(t, "member")

	for _, rt := range webGuardedRoutes {
		t.Run(rt.name, func(t *testing.T) {
			req := e.authedRequest(rt.method, rt.target, rt.body)
			w := httptest.NewRecorder()
			e.mux.ServeHTTP(w, req)
			assert.Equal(t, http.StatusForbidden, w.Code,
				"%s %s must require %s, not merely a session", rt.method, rt.target, rt.capability)
		})
	}
}

// TestWebRoutes_RedirectUnauthenticated confirms anonymous callers still get
// the login redirect rather than a 403.
func TestWebRoutes_RedirectUnauthenticated(t *testing.T) {
	e := setupHandlerWithRoles(t, "member")

	for _, rt := range webGuardedRoutes {
		t.Run(rt.name, func(t *testing.T) {
			req := httptest.NewRequest(rt.method, rt.target, strings.NewReader(rt.body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			e.mux.ServeHTTP(w, req)
			assert.Equal(t, http.StatusSeeOther, w.Code)
			assert.Equal(t, "/login", w.Header().Get("Location"))
		})
	}
}

// TestWebRoutes_AllowWithCapability proves enforcement is not blanket denial.
func TestWebRoutes_AllowWithCapability(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")

	for _, rt := range webGuardedRoutes {
		t.Run(rt.name, func(t *testing.T) {
			req := e.authedRequest(rt.method, rt.target, rt.body)
			w := httptest.NewRecorder()
			e.mux.ServeHTTP(w, req)
			assert.NotEqual(t, http.StatusForbidden, w.Code,
				"administrator holds %s and must not be denied", rt.capability)
		})
	}
}

// TestImportCommitNeedsCommitCapability checks the boundary that matters most
// on the import flow: upload access must not confer commit access.
func TestImportCommitNeedsCommitCapability(t *testing.T) {
	e := setupHandlerWithRoles(t)

	_, err := e.h.db.Exec(
		`INSERT INTO user_capability_grants (user_id, capability_code, granted_by, granted_at)
		 VALUES (1, 'import.upload', 1, datetime('now'))`)
	require.NoError(t, err)

	req := e.authedRequest("GET", "/admin/imports")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	assert.NotEqual(t, http.StatusForbidden, w.Code, "import.upload was granted")

	req = e.authedRequest("POST", "/admin/imports/1/commit", "")
	w = httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code, "import.upload must not imply import.commit")
}

// TestGuardedRoutesAllDeclareKnownCapability locks the route table to the
// capability catalog, so a new route cannot ship with a typo'd or invented
// capability that no role can ever hold. It sweeps the officer and member
// tables together, because RegisterRoutes registers them together.
func TestGuardedRoutesAllDeclareKnownCapability(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")

	routes := e.h.GuardedRoutes()
	require.NotEmpty(t, routes)

	seen := map[string]bool{}
	for _, rt := range routes {
		require.NotEmpty(t, rt.Capability, "route %q declares no capability", rt.Pattern)
		_, ok := authz.ByCode(rt.Capability)
		assert.True(t, ok, "route %q requires unknown capability %q", rt.Pattern, rt.Capability)
		assert.False(t, seen[rt.Pattern], "duplicate route pattern %q", rt.Pattern)
		seen[rt.Pattern] = true
	}
}

// TestWebDenialIsAudited ensures a blocked admin request leaves a trail.
func TestWebDenialIsAudited(t *testing.T) {
	e := setupHandlerWithRoles(t, "member")

	req := e.authedRequest("POST", "/admin/imports/1/commit", "")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)

	var outcome, reason string
	var actor sql.NullInt64
	err := e.h.db.QueryRow(
		`SELECT outcome, reason_code, actor_user_id FROM audit_events WHERE action = 'import.commit'`,
	).Scan(&outcome, &reason, &actor)
	require.NoError(t, err, "denial must be audited")
	assert.Equal(t, "denied", outcome)
	assert.Equal(t, "missing_capability", reason)
	assert.Equal(t, int64(1), actor.Int64)
}

// TestWebSuccessIsAudited covers the generic emission path for the UI.
func TestWebSuccessIsAudited(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")

	form := url.Values{
		"display_name": {"Ada Lovelace"},
		"sort_name":    {"Lovelace, Ada"},
		"base_type":    {"full"},
	}
	req := e.authedRequest("POST", "/admin/members/new", form.Encode())
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusSeeOther, w.Code, "create should redirect on success")

	var outcome, kind string
	var resourceID sql.NullInt64
	err := e.h.db.QueryRow(
		`SELECT outcome, resource_kind, resource_id FROM audit_events WHERE action = 'member.create'`,
	).Scan(&outcome, &kind, &resourceID)
	require.NoError(t, err)
	assert.Equal(t, "success", outcome)
	assert.Equal(t, "person", kind)
	assert.NotZero(t, resourceID.Int64)
}

// testMailer gives the handler a real sender so recovery and invitation flows
// can be exercised, and so a test can read what was sent.
func testMailer(t *testing.T) *mail.FilelogSender {
	t.Helper()
	return mail.NewFilelogSender(t.TempDir())
}

// TestAdminRoutePatternsAreWellFormed guards against a mangled route pattern.
//
// An editing slip once turned "GET /admin/treasury/worksheets" into a pattern
// containing a raw tab. The route still registered, but the real path then fell
// through to the "GET /admin/" dashboard pattern and its far weaker
// session.self.read requirement, so a page guarded by dues.worksheet.manage
// silently became readable by any signed-in member. Nothing failed to compile.
func TestAdminRoutePatternsAreWellFormed(t *testing.T) {
	e := setupHandlerWithRoles(t)

	seen := map[string]bool{}
	for _, rt := range e.h.GuardedRoutes() {
		t.Run(rt.Pattern, func(t *testing.T) {
			method, path, ok := strings.Cut(rt.Pattern, " ")
			require.True(t, ok, "a pattern must be \"METHOD /path\"")
			assert.Contains(t, []string{"GET", "POST", "PUT", "PATCH", "DELETE"}, method)

			assert.True(t, strings.HasPrefix(path, "/admin/") || strings.HasPrefix(path, RouteMemberHome),
				"a guarded route must live under /admin/ or %s", RouteMemberHome)
			assert.NotContains(t, path, "//", "no empty path segment")
			for _, r := range rt.Pattern {
				assert.False(t, unicode.IsControl(r) || r == '\t',
					"pattern %q contains a control character", rt.Pattern)
			}
			// A trailing segment that is neither a word nor a {placeholder}
			// is how the tab slipped through unnoticed.
			for _, seg := range strings.Split(strings.Trim(path, "/"), "/") {
				assert.NotEmpty(t, strings.TrimSpace(seg), "empty segment in %q", path)
			}

			assert.False(t, seen[rt.Pattern], "duplicate pattern %q", rt.Pattern)
			seen[rt.Pattern] = true

			assert.NotEmpty(t, rt.Capability, "every guarded route declares a capability")
		})
	}
}

// TestMemberRoutesCarryNoOfficerCapability keeps the two tables from drifting
// into each other. A member route requiring member.read or dues.read would
// hand an ordinary member the administrative reads ADR-0010 withholds, and it
// would do so without any single line looking wrong.
func TestMemberRoutesCarryNoOfficerCapability(t *testing.T) {
	e := setupHandlerWithRoles(t)

	memberCaps := map[string]bool{}
	rows, err := e.h.db.Query(
		`SELECT capability_code FROM role_capabilities WHERE role_code = 'member'`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var code string
		require.NoError(t, rows.Scan(&code))
		memberCaps[code] = true
	}
	require.NoError(t, rows.Err())
	require.NotEmpty(t, memberCaps)

	routes := e.h.MemberRoutes()
	require.NotEmpty(t, routes, "the member surface must have at least one route")
	for _, rt := range routes {
		assert.True(t, strings.HasPrefix(rt.Pattern, "GET "+RouteMemberHome) ||
			strings.Contains(rt.Pattern, " "+RouteMemberHome),
			"a member route must live under %s, got %q", RouteMemberHome, rt.Pattern)
		assert.True(t, memberCaps[rt.Capability],
			"member route %q requires %q, which the member role does not hold",
			rt.Pattern, rt.Capability)
	}
}

// TestWebDenialRecordsTheActorsRoles is the admin UI half of fmc.16. A denial
// on the server-rendered surface must answer the same question the API's does:
// was this somebody signed out, or somebody signed in without permission?
func TestWebDenialRecordsTheActorsRoles(t *testing.T) {
	e := setupHandlerWithRoles(t, "member")

	// Signed in, without the capability.
	req := e.authedRequest("POST", "/admin/imports/1/commit", "")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)

	var roles, reason string
	require.NoError(t, e.h.db.QueryRow(`
		SELECT coalesce(actor_role_codes, ''), reason_code FROM audit_events
		 WHERE action = 'import.commit' AND outcome = 'denied'`).Scan(&roles, &reason))
	assert.Equal(t, "member", roles, "the denial records what the caller actually was")
	assert.Equal(t, "missing_capability", reason)

	// Signed out, against the same route.
	anon := httptest.NewRequest("POST", "/admin/members/1/deactivate", nil)
	w = httptest.NewRecorder()
	e.mux.ServeHTTP(w, anon)
	require.Equal(t, http.StatusSeeOther, w.Code)

	var anonRoles sql.NullString
	require.NoError(t, e.h.db.QueryRow(`
		SELECT actor_role_codes, reason_code FROM audit_events
		 WHERE action = 'member.deactivate' AND outcome = 'denied'`).Scan(&anonRoles, &reason))
	assert.False(t, anonRoles.Valid, "an unauthenticated denial records no roles")
	assert.Equal(t, "unauthenticated", reason)
}

// TestWebSuccessRecordsTheActorsRoles covers the non-denial path.
func TestWebSuccessRecordsTheActorsRoles(t *testing.T) {
	e := setupHandlerWithRoles(t, "secretary")

	req := e.authedRequest("POST", "/admin/members/new",
		"display_name=Audited+Person&sort_name=audited+person&base_type=full")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusSeeOther, w.Code, "the member must be created")

	var roles string
	require.NoError(t, e.h.db.QueryRow(`
		SELECT coalesce(actor_role_codes, '') FROM audit_events
		 WHERE action = 'member.create' AND outcome = 'success'`).Scan(&roles))
	assert.Equal(t, "secretary", roles)
}
