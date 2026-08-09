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
)

// setupCookieHandler builds a real admin-UI handler with a known user, so the
// assertions run against the shipping login/logout handlers rather than the
// cookie helper alone.
func setupCookieHandler(t *testing.T, allowInsecure bool) *http.ServeMux {
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

	h, err := NewHandler(d, HandlerConfig{
		Mailer:               testMailer(t),
		AllowInsecureCookies: allowInsecure,
	})
	require.NoError(t, err)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux
}

func sessionCookieFrom(t *testing.T, res *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range res.Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in response", sessionCookieName)
	return nil
}

// TestWebSessionCookieSecure asserts Secure at both admin-UI set points:
// login (which issues the cookie) and logout (which clears it). Secure is on
// by default and off only when the deployment explicitly opts out.
func TestWebSessionCookieSecure(t *testing.T) {
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
			mux := setupCookieHandler(t, tc.allowInsecure)

			form := url.Values{"email": {"test@test.local"}, "password": {"testpass"}}
			req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			require.Equal(t, http.StatusSeeOther, w.Code)

			login := sessionCookieFrom(t, w.Result())
			assert.Equal(t, tc.wantSecure, login.Secure, "login cookie")
			assert.True(t, login.HttpOnly)
			assert.NotEmpty(t, login.Value)

			req = httptest.NewRequest(http.MethodPost, "/logout", nil)
			req.AddCookie(login)
			w = httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			require.Equal(t, http.StatusSeeOther, w.Code)

			out := sessionCookieFrom(t, w.Result())
			assert.Equal(t, tc.wantSecure, out.Secure, "logout cookie")
			assert.Equal(t, -1, out.MaxAge)
			assert.Empty(t, out.Value)
		})
	}
}
