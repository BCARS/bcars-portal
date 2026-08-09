package authn

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionCookieSecure pins the default: Secure is on unless a
// configuration explicitly opts out for plaintext local development.
func TestSessionCookieSecure(t *testing.T) {
	cases := []struct {
		name       string
		cfg        SessionCookieConfig
		wantSecure bool
	}{
		{"default is secure", SessionCookieConfig{Name: "custom_session"}, true},
		{"explicit opt-out", SessionCookieConfig{Name: "custom_session", AllowInsecure: true}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set := tc.cfg.Set("abc123", time.Now().Add(time.Hour))
			assert.Equal(t, tc.wantSecure, set.Secure, "Set")
			assert.Equal(t, "custom_session", set.Name)
			assert.True(t, set.HttpOnly)
			assert.Equal(t, http.SameSiteLaxMode, set.SameSite)
			assert.Positive(t, set.MaxAge)

			clear := tc.cfg.Clear()
			assert.Equal(t, tc.wantSecure, clear.Secure, "Clear")
			assert.True(t, clear.HttpOnly)
			assert.Equal(t, -1, clear.MaxAge)
			assert.Empty(t, clear.Value)
		})
	}
}

// A zero expiry means a browser-session cookie: no MaxAge, so it is not
// deleted on arrival the way a negative MaxAge would be.
func TestSessionCookieZeroExpiryHasNoMaxAge(t *testing.T) {
	c := SessionCookieConfig{Name: "custom_session"}.Set("abc123", time.Time{})
	assert.Equal(t, 0, c.MaxAge)
}

type nopCapLoader struct{}

func (nopCapLoader) EffectiveCapabilities(int64) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}

// TestMiddlewareClearsCookieSecurely covers the third set point: the
// middleware discarding an invalid session must clear the cookie with the
// same attributes it was set with.
func TestMiddlewareClearsCookieSecurely(t *testing.T) {
	cases := []struct {
		name       string
		cfg        SessionCookieConfig
		wantSecure bool
	}{
		{"default is secure", SessionCookieConfig{Name: "bcars_session"}, true},
		{"explicit opt-out", SessionCookieConfig{Name: "bcars_session", AllowInsecure: true}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testDB(t)
			h := Middleware(store, nopCapLoader{}, tc.cfg)(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.AddCookie(&http.Cookie{Name: tc.cfg.Name, Value: "not-a-session"})
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			var cleared *http.Cookie
			for _, c := range w.Result().Cookies() {
				if c.Name == tc.cfg.Name {
					cleared = c
				}
			}
			require.NotNil(t, cleared, "invalid session must clear the cookie")
			assert.Equal(t, -1, cleared.MaxAge)
			assert.Equal(t, tc.wantSecure, cleared.Secure)
		})
	}
}
