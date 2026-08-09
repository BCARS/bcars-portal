package httpapi_test

import (
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

// setupCookieAPI wires the real router with the session endpoints so the
// assertions cover the shipping handlers, not just the cookie helper.
func setupCookieAPI(t *testing.T, allowInsecure bool) *httptest.Server {
	t.Helper()
	d := openTestDB(t)

	cookieName := "bcars_session"
	store := authn.NewSessionStore(d, authn.SessionConfig{
		CookieName: cookieName,
		TTL:        time.Hour,
	})
	authSvc := authn.NewAuthService(d, store, nil)

	handler, api := httpapi.NewRouter(httpapi.Config{
		Version:              "test",
		DB:                   d,
		AllowInsecureCookies: allowInsecure,
	})
	capLoader := &authn.SQLCapabilityLoader{DB: d}
	wrapped := authn.Middleware(store, capLoader, authn.SessionCookieConfig{
		Name:          cookieName,
		AllowInsecure: allowInsecure,
	})(handler)

	httpapi.RegisterAll(api, httpapi.Deps{
		DB:                   d,
		AuthService:          authSvc,
		SessionStore:         store,
		CookieName:           cookieName,
		AllowInsecureCookies: allowInsecure,
	})
	require.NoError(t, httpapi.VerifyAll(api))

	hash, err := authn.HashPassword("correcthorsebatterystaple", nil, authn.DefaultParams())
	require.NoError(t, err)
	_, err = d.Exec(
		`INSERT INTO users (email, password_hash, is_active) VALUES (?, ?, 1)`,
		"admin@bcars.org", hash,
	)
	require.NoError(t, err)
	_, err = d.Exec(
		`INSERT INTO user_role_grants (user_id, role_code, granted_by, granted_at) VALUES (1, 'webmaster', 1, datetime('now'))`,
	)
	require.NoError(t, err)

	ts := httptest.NewServer(wrapped)
	t.Cleanup(ts.Close)
	return ts
}

func apiSessionCookie(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == "bcars_session" {
			return c
		}
	}
	t.Fatal("no bcars_session cookie in response")
	return nil
}

// TestAPISessionCookieSecure asserts Secure at the API set and clear points:
// sign-in issues the cookie and sign-out clears it. Both come from the same
// shared configuration as the admin UI.
func TestAPISessionCookieSecure(t *testing.T) {
	cases := []struct {
		name          string
		allowInsecure bool
		wantSecure    bool
	}{
		{"default is secure", false, true},
		{"explicit opt-out", true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := setupCookieAPI(t, tc.allowInsecure)

			body := `{"email":"admin@bcars.org","password":"correcthorsebatterystaple"}`
			resp, err := ts.Client().Post(ts.URL+"/api/v1/sessions", "application/json", strings.NewReader(body))
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode)

			signin := apiSessionCookie(t, resp)
			assert.Equal(t, tc.wantSecure, signin.Secure, "sign-in cookie")
			assert.True(t, signin.HttpOnly)

			req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/sessions/current", nil)
			require.NoError(t, err)
			req.AddCookie(signin)
			out, err := ts.Client().Do(req)
			require.NoError(t, err)
			defer out.Body.Close()
			require.Equal(t, http.StatusNoContent, out.StatusCode)

			cleared := apiSessionCookie(t, out)
			assert.Equal(t, tc.wantSecure, cleared.Secure, "sign-out cookie")
			assert.Equal(t, -1, cleared.MaxAge)
		})
	}
}
