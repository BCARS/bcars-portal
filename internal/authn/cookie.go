package authn

import (
	"net/http"
	"time"
)

// SessionCookieConfig is the single source of session-cookie attributes for
// every surface that sets or clears a session cookie: the JSON API, the
// server-rendered admin UI, and the middleware that discards an invalid
// session. Each surface used to hardcode its own attributes, which is how the
// admin UI's cookie ended up without Secure while the API's had it.
//
// The two surfaces use different cookie names ("bcars_session" for the API,
// "portal_session" for the admin UI), so the name travels with the
// configuration rather than being assumed.
type SessionCookieConfig struct {
	// Name is the cookie name. Required.
	Name string

	// AllowInsecure drops the Secure attribute so the cookie survives a
	// plaintext http://localhost session. Development only — the zero value
	// is the safe one, so any config that does not opt out gets Secure.
	AllowInsecure bool
}

// secure reports whether the Secure attribute should be set.
func (c SessionCookieConfig) secure() bool { return !c.AllowInsecure }

// Set builds the cookie that carries sessionID. A zero expiresAt produces a
// browser-session cookie (no MaxAge); otherwise MaxAge tracks the session's
// remaining lifetime.
func (c SessionCookieConfig) Set(sessionID string, expiresAt time.Time) *http.Cookie {
	cookie := &http.Cookie{
		Name:     c.Name,
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		Secure:   c.secure(),
		SameSite: http.SameSiteLaxMode,
	}
	if !expiresAt.IsZero() {
		cookie.MaxAge = int(time.Until(expiresAt).Seconds())
	}
	return cookie
}

// Clear builds the cookie that deletes an existing session cookie. The
// attributes must match those used by Set or the browser keeps the original.
func (c SessionCookieConfig) Clear() *http.Cookie {
	return &http.Cookie{
		Name:     c.Name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   c.secure(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	}
}
