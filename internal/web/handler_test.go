package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/authn"
	"github.com/bcars/bcars-portal/internal/db"
	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
)

// testEnv bundles the handler, mux, and a session cookie for authenticated requests.
type testEnv struct {
	h      *Handler
	mux    *http.ServeMux
	cookie *http.Cookie
}

func setupHandler(t *testing.T) *testEnv {
	t.Helper()
	d, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	require.NoError(t, db.Migrate(d))

	// Create a test user with password and admin role.
	hash, err := authn.HashPassword("testpass", nil, authn.DefaultParams())
	require.NoError(t, err)
	_, err = d.Exec(`INSERT INTO users (email, password_hash, password_algo_params, is_active) VALUES ('test@test.local', ?, 'argon2id', 1)`, hash)
	require.NoError(t, err)

	// Grant administrator role so the user has all capabilities.
	_, err = d.Exec(`INSERT INTO user_role_grants (user_id, role_code, granted_by, granted_at, reason)
		VALUES (1, 'administrator', 1, strftime('%Y-%m-%dT%H:%M:%fZ','now'), 'test setup')`)
	require.NoError(t, err)

	h, err := NewHandler(d, HandlerConfig{Mailer: testMailer(t)})
	require.NoError(t, err)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Sign in to get a session cookie.
	form := url.Values{"email": {"test@test.local"}, "password": {"testpass"}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusSeeOther, w.Code, "login should redirect")

	var sessionCookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == authn.DefaultSessionCookieName {
			sessionCookie = c
			break
		}
	}
	require.NotNil(t, sessionCookie, "session cookie must be set after login")

	return &testEnv{h: h, mux: mux, cookie: sessionCookie}
}

// authedRequest creates a request with the session cookie attached.
func (e *testEnv) authedRequest(method, target string, body ...string) *http.Request {
	var req *http.Request
	if len(body) > 0 {
		req = httptest.NewRequest(method, target, strings.NewReader(body[0]))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	req.AddCookie(e.cookie)
	return req
}

func TestLoginRequired(t *testing.T) {
	d, err := db.Open(":memory:")
	require.NoError(t, err)
	defer d.Close()
	require.NoError(t, db.Migrate(d))

	h, err := NewHandler(d, HandlerConfig{Mailer: testMailer(t)})
	require.NoError(t, err)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Unauthenticated request to admin should redirect to login.
	req := httptest.NewRequest("GET", "/admin/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/login", w.Header().Get("Location"))
}

func TestLoginPage(t *testing.T) {
	d, err := db.Open(":memory:")
	require.NoError(t, err)
	defer d.Close()
	require.NoError(t, db.Migrate(d))

	h, err := NewHandler(d, HandlerConfig{Mailer: testMailer(t)})
	require.NoError(t, err)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/login", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Sign In")
}

func TestLoginBadPassword(t *testing.T) {
	d, err := db.Open(":memory:")
	require.NoError(t, err)
	defer d.Close()
	require.NoError(t, db.Migrate(d))

	hash, err := authn.HashPassword("correct", nil, authn.DefaultParams())
	require.NoError(t, err)
	_, err = d.Exec(`INSERT INTO users (email, password_hash, password_algo_params, is_active) VALUES ('u@test.local', ?, 'argon2id', 1)`, hash)
	require.NoError(t, err)

	h, err := NewHandler(d, HandlerConfig{Mailer: testMailer(t)})
	require.NoError(t, err)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	form := url.Values{"email": {"u@test.local"}, "password": {"wrong"}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "Invalid email or password")
}

func TestDashboard(t *testing.T) {
	e := setupHandler(t)

	req := e.authedRequest("GET", "/admin/")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Dashboard")
	assert.Contains(t, w.Body.String(), "Total Members")
}

func TestMemberListEmpty(t *testing.T) {
	e := setupHandler(t)

	req := e.authedRequest("GET", "/admin/members")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "No members found")
}

func TestMemberCreateAndView(t *testing.T) {
	e := setupHandler(t)

	// GET new form.
	req := e.authedRequest("GET", "/admin/members/new")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Create Member")

	// POST create.
	form := url.Values{
		"display_name": {"Alice Test"},
		"sort_name":    {"Test, Alice"},
		"call_sign":    {"KA1AAA"},
		"base_type":    {"full"},
	}
	req = e.authedRequest("POST", "/admin/members/new", form.Encode())
	w = httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Contains(t, w.Header().Get("Location"), "/admin/members/1")

	// GET detail.
	req = e.authedRequest("GET", "/admin/members/1")
	w = httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Alice Test")
	assert.Contains(t, w.Body.String(), "KA1AAA")

	// GET list should show member.
	req = e.authedRequest("GET", "/admin/members")
	w = httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Alice Test")
}

func TestMemberSearch(t *testing.T) {
	e := setupHandler(t)

	// Create two members.
	for _, name := range []string{"Alice Test", "Bob Sample"} {
		form := url.Values{
			"display_name": {name},
			"sort_name":    {name},
			"base_type":    {"full"},
		}
		req := e.authedRequest("POST", "/admin/members/new", form.Encode())
		w := httptest.NewRecorder()
		e.mux.ServeHTTP(w, req)
		require.Equal(t, http.StatusSeeOther, w.Code)
	}

	// Search for Alice.
	req := e.authedRequest("GET", "/admin/members?q=Alice")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Alice Test")
	assert.NotContains(t, w.Body.String(), "Bob Sample")
}

func TestMemberEditAndUpdate(t *testing.T) {
	e := setupHandler(t)

	// Create.
	form := url.Values{
		"display_name": {"Old Name"},
		"sort_name":    {"Name, Old"},
		"base_type":    {"full"},
	}
	req := e.authedRequest("POST", "/admin/members/new", form.Encode())
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusSeeOther, w.Code)

	// GET edit form.
	req = e.authedRequest("GET", "/admin/members/1/edit")
	w = httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Old Name")

	// POST update.
	form = url.Values{
		"display_name": {"New Name"},
		"sort_name":    {"Name, New"},
		"version":      {"1"},
	}
	req = e.authedRequest("POST", "/admin/members/1/edit", form.Encode())
	w = httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusSeeOther, w.Code)

	// Verify update.
	req = e.authedRequest("GET", "/admin/members/1")
	w = httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	assert.Contains(t, w.Body.String(), "New Name")
}

func TestMemberDeactivateReactivate(t *testing.T) {
	e := setupHandler(t)

	// Create.
	form := url.Values{"display_name": {"Test"}, "sort_name": {"Test"}, "base_type": {"full"}}
	req := e.authedRequest("POST", "/admin/members/new", form.Encode())
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)

	// Deactivate.
	form = url.Values{"version": {"1"}}
	req = e.authedRequest("POST", "/admin/members/1/deactivate", form.Encode())
	w = httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusSeeOther, w.Code)

	// Verify deactivated.
	req = e.authedRequest("GET", "/admin/members/1")
	w = httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	assert.Contains(t, w.Body.String(), "Deactivated")

	// Reactivate.
	form = url.Values{"version": {"2"}}
	req = e.authedRequest("POST", "/admin/members/1/reactivate", form.Encode())
	w = httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusSeeOther, w.Code)
}

func TestMembershipApprove(t *testing.T) {
	e := setupHandler(t)

	// Create member (creates pending membership).
	form := url.Values{"display_name": {"Test"}, "sort_name": {"Test"}, "base_type": {"full"}}
	req := e.authedRequest("POST", "/admin/members/new", form.Encode())
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)

	// View detail — should show pending.
	req = e.authedRequest("GET", "/admin/members/1")
	w = httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	assert.Contains(t, w.Body.String(), "Pending")

	// Approve.
	form = url.Values{"version": {"1"}, "base_type": {"full"}}
	req = e.authedRequest("POST", "/admin/members/1/memberships/1/approve", form.Encode())
	w = httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusSeeOther, w.Code)

	// View detail — should show approved.
	req = e.authedRequest("GET", "/admin/members/1")
	w = httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	assert.Contains(t, w.Body.String(), "Approved")
}

func TestNoteCreate(t *testing.T) {
	e := setupHandler(t)

	// Create member.
	form := url.Values{"display_name": {"Test"}, "sort_name": {"Test"}, "base_type": {"full"}}
	req := e.authedRequest("POST", "/admin/members/new", form.Encode())
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)

	// Add note.
	form = url.Values{"body": {"Test note content"}, "category": {"general"}}
	req = e.authedRequest("POST", "/admin/members/1/notes", form.Encode())
	w = httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusSeeOther, w.Code)

	// View detail — should show note.
	req = e.authedRequest("GET", "/admin/members/1")
	w = httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	assert.Contains(t, w.Body.String(), "Test note content")
}

func TestImportListEmpty(t *testing.T) {
	e := setupHandler(t)

	req := e.authedRequest("GET", "/admin/imports")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Import Review")
	assert.Contains(t, w.Body.String(), "No import runs yet")
}

func TestTemplateRendering(t *testing.T) {
	r, err := NewRenderer()
	require.NoError(t, err)

	tests := []struct {
		name string
		data any
	}{
		{"dashboard.html", dashboardData{}},
		{"members.html", memberListData{}},
		{"member_form.html", memberFormData{}},
		{"imports.html", struct {
			Runs    []sqlcgen.ImportRun
			Error   string
			Success string
		}{}},
		{"login.html", loginData{}},
		// Recovery and invitation pages: embedded but never registered before
		// bcars-portal-fmc.4, so every one of these failed at runtime.
		{"forgot_password.html", struct {
			Error   string
			Success bool
		}{}},
		{"reset_password.html", struct {
			Token   string
			Error   string
			Success bool
		}{}},
		{"accept_invitation.html", struct {
			Token string
			Error string
		}{}},
		{"error.html", errorPageData{}},
	}
	for _, tt := range tests {
		w := httptest.NewRecorder()
		r.RenderHTTP(w, tt.name, http.StatusOK, tt.data)
		assert.Equal(t, http.StatusOK, w.Code, "template %s should render", tt.name)
	}
}
