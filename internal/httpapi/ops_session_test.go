package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/authn"
	"github.com/bcars/bcars-portal/internal/httpapi"
)

// setupSessionTest creates a test server with auth wired up and a seeded user.
func setupSessionTest(t *testing.T) (*httptest.Server, *authn.SessionStore) {
	t.Helper()
	d := openTestDB(t)

	cookieName := "bcars_session"
	store := authn.NewSessionStore(d, authn.SessionConfig{
		CookieName: cookieName,
		TTL:        1 * time.Hour,
	})
	authSvc := authn.NewAuthService(d, store, nil)

	handler, api := httpapi.NewRouter(httpapi.Config{Version: "test", DB: d})

	// Wire the authn middleware so principal is available.
	capLoader := &authn.SQLCapabilityLoader{DB: d}
	wrappedHandler := authn.Middleware(store, capLoader, cookieName)(handler)

	httpapi.RegisterAll(api, httpapi.Deps{
		DB:           d,
		AuthService:  authSvc,
		SessionStore: store,
		CookieName:   cookieName,
	})
	require.NoError(t, httpapi.VerifyAll(api))

	// Seed a test user with a known password.
	hash, err := authn.HashPassword("correcthorsebatterystaple", nil, authn.DefaultParams())
	require.NoError(t, err)
	_, err = d.Exec(
		`INSERT INTO users (email, password_hash, is_active) VALUES (?, ?, 1)`,
		"admin@bcars.org", hash,
	)
	require.NoError(t, err)

	// Grant session.self.read capability through webmaster role.
	_, err = d.Exec(
		`INSERT INTO user_role_grants (user_id, role_code, granted_by, granted_at) VALUES (1, 'webmaster', 1, datetime('now'))`,
	)
	require.NoError(t, err)

	ts := httptest.NewServer(wrappedHandler)
	t.Cleanup(ts.Close)
	return ts, store
}

func TestSignInSuccess(t *testing.T) {
	ts, _ := setupSessionTest(t)

	body := `{"email":"admin@bcars.org","password":"correcthorsebatterystaple"}`
	resp, err := ts.Client().Post(ts.URL+"/api/v1/sessions", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result struct {
		UserID       int64    `json:"user_id"`
		Email        string   `json:"email"`
		Capabilities []string `json:"capabilities"`
		ExpiresAt    string   `json:"expires_at"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, int64(1), result.UserID)
	assert.Equal(t, "admin@bcars.org", result.Email)
	assert.NotEmpty(t, result.ExpiresAt)

	// Should set a session cookie.
	cookies := resp.Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "bcars_session" {
			sessionCookie = c
			break
		}
	}
	assert.NotNil(t, sessionCookie, "should set session cookie")
	assert.True(t, sessionCookie.HttpOnly)
}

func TestSignInInvalidCredentials(t *testing.T) {
	ts, _ := setupSessionTest(t)

	body := `{"email":"admin@bcars.org","password":"wrongpassword"}`
	resp, err := ts.Client().Post(ts.URL+"/api/v1/sessions", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestSignInUnknownEmail(t *testing.T) {
	ts, _ := setupSessionTest(t)

	// Should return 401 without revealing whether the email exists.
	body := `{"email":"nobody@bcars.org","password":"somepassword1234"}`
	resp, err := ts.Client().Post(ts.URL+"/api/v1/sessions", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestCurrentSessionAuthenticated(t *testing.T) {
	ts, _ := setupSessionTest(t)

	// Sign in first to get a session cookie.
	body := `{"email":"admin@bcars.org","password":"correcthorsebatterystaple"}`
	signInResp, err := ts.Client().Post(ts.URL+"/api/v1/sessions", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer signInResp.Body.Close()
	require.Equal(t, http.StatusOK, signInResp.StatusCode)

	// Use the cookie jar from the test server client.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/sessions/current", nil)
	// Copy cookies from sign-in response.
	for _, c := range signInResp.Cookies() {
		req.AddCookie(c)
	}
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result struct {
		UserID int64  `json:"user_id"`
		Email  string `json:"email"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, int64(1), result.UserID)
	assert.Equal(t, "admin@bcars.org", result.Email)
}

func TestCurrentSessionAnonymous(t *testing.T) {
	ts, _ := setupSessionTest(t)

	resp, err := ts.Client().Get(ts.URL + "/api/v1/sessions/current")
	require.NoError(t, err)
	defer resp.Body.Close()

	// Without authentication, should get 401.
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestSignOutRevokesSession(t *testing.T) {
	ts, store := setupSessionTest(t)

	// Sign in.
	body := `{"email":"admin@bcars.org","password":"correcthorsebatterystaple"}`
	signInResp, err := ts.Client().Post(ts.URL+"/api/v1/sessions", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer signInResp.Body.Close()
	require.Equal(t, http.StatusOK, signInResp.StatusCode)

	cookies := signInResp.Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "bcars_session" {
			sessionCookie = c
			break
		}
	}
	require.NotNil(t, sessionCookie)

	// Sign out.
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/sessions/current", nil)
	req.AddCookie(sessionCookie)
	signOutResp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer signOutResp.Body.Close()

	assert.Equal(t, http.StatusNoContent, signOutResp.StatusCode)

	// Session should be revoked.
	_, err = store.Get(sessionCookie.Value)
	assert.Error(t, err, "session should be revoked after sign-out")

	// Clear cookie should be set.
	for _, c := range signOutResp.Cookies() {
		if c.Name == "bcars_session" {
			assert.Equal(t, -1, c.MaxAge, "should clear the session cookie")
		}
	}
}
