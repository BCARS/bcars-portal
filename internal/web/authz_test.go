package web

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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
		if c.Name == sessionCookieName {
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
// bcars-portal-fmc.1 regression. The "member" role holds only
// session.self.read.
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

// TestAdminRoutesAllDeclareKnownCapability locks the route table to the
// capability catalog, so a new admin route cannot ship with a typo'd or
// invented capability that no role can ever hold.
func TestAdminRoutesAllDeclareKnownCapability(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")

	routes := e.h.AdminRoutes()
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
