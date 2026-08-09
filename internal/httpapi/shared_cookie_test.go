package httpapi_test

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/authn"
	"github.com/bcars/bcars-portal/internal/httpapi"
	"github.com/bcars/bcars-portal/internal/mail"
)

// One binary used to hand out two session cookies: the API set "bcars_session"
// and the admin UI hardcoded "portal_session". Both were backed by the same
// sessions table, but neither surface accepted the other's cookie
// (bcars-portal-6q6.3).
//
// These tests drive one assembled router and carry a cookie ACROSS surfaces,
// which is the only way to observe the defect: each surface passed its own
// tests throughout, because each was internally consistent.

const sharedTestPassword = "correcthorsebatterystaple"

type sharedEnv struct {
	db      *sql.DB
	handler http.Handler
}

func setupSharedCookieEnv(t *testing.T) *sharedEnv {
	t.Helper()
	d := openTestDB(t)

	// Deliberately NOT naming the cookie. Both surfaces must land on the same
	// default; if either restated a name of its own, this is where it shows.
	cookies := authn.SessionCookieConfig{AllowInsecure: true}

	store := authn.NewSessionStore(d, authn.SessionConfig{
		CookieName: cookies.CookieName(),
		TTL:        time.Hour,
	})
	authSvc := authn.NewAuthService(d, store, nil)
	mailer := mail.NewFilelogSender(t.TempDir())

	handler, api := httpapi.NewRouter(httpapi.Config{
		Version:              "test",
		DB:                   d,
		Mailer:               mailer,
		BaseURL:              "http://portal.example",
		AllowInsecureCookies: true,
	})
	capLoader := &authn.SQLCapabilityLoader{DB: d}
	wrapped := authn.Middleware(store, capLoader, cookies)(handler)

	httpapi.RegisterAll(api, httpapi.Deps{
		DB:                   d,
		AuthService:          authSvc,
		SessionStore:         store,
		AllowInsecureCookies: true,
	})
	require.NoError(t, httpapi.VerifyAll(api))

	hash, err := authn.HashPassword(sharedTestPassword, nil, authn.DefaultParams())
	require.NoError(t, err)
	_, err = d.Exec(`INSERT INTO users (email, password_hash, is_active) VALUES (?, ?, 1)`,
		"officer@bcars.org", hash)
	require.NoError(t, err)
	_, err = d.Exec(`INSERT INTO user_role_grants (user_id, role_code, granted_by, granted_at)
		VALUES (1, 'administrator', 1, datetime('now'))`)
	require.NoError(t, err)

	return &sharedEnv{db: d, handler: wrapped}
}

// signInViaAPI returns the cookie the JSON endpoint issued.
func (e *sharedEnv) signInViaAPI(t *testing.T) *http.Cookie {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/sessions",
		strings.NewReader(`{"email":"officer@bcars.org","password":"`+sharedTestPassword+`"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, r)
	require.Less(t, w.Code, 300, "API sign-in failed: %s", w.Body.String())

	cookies := w.Result().Cookies()
	require.NotEmpty(t, cookies, "API sign-in set no cookie")
	return cookies[0]
}

// signInViaUI returns the cookie the login form issued.
func (e *sharedEnv) signInViaUI(t *testing.T) *http.Cookie {
	t.Helper()
	form := url.Values{"email": {"officer@bcars.org"}, "password": {sharedTestPassword}}
	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, r)
	require.Equal(t, http.StatusSeeOther, w.Code, "UI sign-in failed: %s", w.Body.String())

	cookies := w.Result().Cookies()
	require.NotEmpty(t, cookies, "UI sign-in set no cookie")
	return cookies[0]
}

func (e *sharedEnv) get(t *testing.T, path string, c *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, r)
	return w
}

// TestBothSurfacesIssueOneCookieName is the direct statement of the fix.
func TestBothSurfacesIssueOneCookieName(t *testing.T) {
	e := setupSharedCookieEnv(t)

	apiCookie := e.signInViaAPI(t)
	uiCookie := e.signInViaUI(t)

	assert.Equal(t, authn.DefaultSessionCookieName, apiCookie.Name)
	assert.Equal(t, apiCookie.Name, uiCookie.Name,
		"one binary must not hand out two session cookies")
}

// TestAPISessionAuthenticatesAdminUI is the first half of the bead's
// reproduction: sign in through the API, then request an admin page.
func TestAPISessionAuthenticatesAdminUI(t *testing.T) {
	e := setupSharedCookieEnv(t)

	w := e.get(t, "/admin/", e.signInViaAPI(t))
	assert.Equal(t, http.StatusOK, w.Code,
		"a session established through the API must authenticate an admin UI request")
	assert.NotContains(t, w.Header().Get("Location"), "/login",
		"the admin UI must not bounce an API session back to the login page")
}

// TestUISessionAuthenticatesAPI is the reverse half.
func TestUISessionAuthenticatesAPI(t *testing.T) {
	e := setupSharedCookieEnv(t)

	w := e.get(t, "/api/v1/sessions/current", e.signInViaUI(t))
	assert.Equal(t, http.StatusOK, w.Code,
		"a session established through the admin UI must authenticate an API request")
	assert.Contains(t, w.Body.String(), "officer@bcars.org",
		"the API must resolve the same principal the UI signed in")
}

// TestSignOutFromOneSurfaceClearsTheOther proves the shared cookie is genuinely
// one session rather than two that happen to share a name.
func TestSignOutFromOneSurfaceClearsTheOther(t *testing.T) {
	e := setupSharedCookieEnv(t)
	cookie := e.signInViaUI(t)

	require.Equal(t, http.StatusOK, e.get(t, "/api/v1/sessions/current", cookie).Code)

	r := httptest.NewRequest(http.MethodPost, "/logout", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, r)
	require.Less(t, w.Code, 400, "logout failed: %s", w.Body.String())

	assert.Equal(t, http.StatusUnauthorized, e.get(t, "/api/v1/sessions/current", cookie).Code,
		"signing out of the UI must end the session the API sees")
}

// TestNoSurfaceRestatesTheCookieName guards the regression at its source: this
// assembly configures no name anywhere, so if either surface reintroduced a
// constant of its own, one of these sign-ins would issue a different cookie.
func TestNoSurfaceRestatesTheCookieName(t *testing.T) {
	e := setupSharedCookieEnv(t)

	for _, c := range []*http.Cookie{e.signInViaAPI(t), e.signInViaUI(t)} {
		assert.Equal(t, authn.DefaultSessionCookieName, c.Name)
		assert.NotEqual(t, "portal_session", c.Name,
			"the admin UI's old hardcoded cookie name must not come back")
	}
}
