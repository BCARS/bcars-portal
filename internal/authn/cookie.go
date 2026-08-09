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
// The name travels with the configuration rather than being assumed, and every
// surface defaults to DefaultSessionCookieName when it is not set.
type SessionCookieConfig struct {
	// Name is the cookie name. Empty means DefaultSessionCookieName.
	Name string

	// AllowInsecure drops the Secure attribute so the cookie survives a
	// plaintext http://localhost session. Development only — the zero value
	// is the safe one, so any config that does not opt out gets Secure.
	AllowInsecure bool
}

// DefaultSessionCookieName is the one session cookie the whole application
// issues.
//
// One binary previously handed out two: the API set "bcars_session" while the
// admin UI hardcoded "portal_session". Both were backed by the same sessions
// table, but neither surface accepted the other's cookie, so a browser touching
// both had to sign in twice and anything calling /api/v1 from a UI page would
// have been silently unauthenticated (bcars-portal-6q6.3).
//
// Every surface falls back to this constant when no name is configured, so the
// two can no longer disagree by omission — only by an explicit contradiction a
// reader can see.
const DefaultSessionCookieName = "bcars_session"

// CookieName returns the configured name, or the default when unset.
func (c SessionCookieConfig) CookieName() string {
	if c.Name == "" {
		return DefaultSessionCookieName
	}
	return c.Name
}

// secure reports whether the Secure attribute should be set.
func (c SessionCookieConfig) secure() bool { return !c.AllowInsecure }

// Set builds the cookie that carries sessionID. A zero expiresAt produces a
// browser-session cookie (no MaxAge); otherwise MaxAge tracks the session's
// remaining lifetime.
func (c SessionCookieConfig) Set(sessionID string, expiresAt time.Time) *http.Cookie {
	cookie := &http.Cookie{
		Name:     c.CookieName(),
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
		Name:     c.CookieName(),
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   c.secure(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	}
}
