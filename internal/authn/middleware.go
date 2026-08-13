package authn

import (
	"database/sql"
	"errors"
	"net/http"
)

// CapabilityLoader loads effective capabilities for a user ID.
type CapabilityLoader interface {
	EffectiveCapabilities(userID int64) (map[string]struct{}, error)
}

// RoleLoader optionally reports the role codes a user holds.
//
// It is a separate, optional interface rather than a second method on
// CapabilityLoader so that adding it breaks no existing implementation: a
// loader that does not provide roles simply yields a principal with none, and
// authorization is unaffected either way because it reads capabilities.
type RoleLoader interface {
	EffectiveRoles(userID int64) ([]string, error)
}

// Middleware resolves the session cookie → Principal and attaches it to the
// request context. Unauthenticated requests pass through with a nil principal
// (the authz layer decides whether to allow or deny).
// The cookie configuration is shared with the surfaces that set the cookie so
// the clearing cookie carries the same attributes; a mismatch leaves the
// original cookie in place.
func Middleware(store *SessionStore, capLoader CapabilityLoader, cookies SessionCookieConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// CookieName(), never the bare field: a config that leaves the
			// name unset must read the same cookie Set and Clear write, or the
			// middleware silently looks for a cookie named "".
			cookie, err := r.Cookie(cookies.CookieName())
			if err != nil {
				// No cookie — unauthenticated; let authz decide.
				next.ServeHTTP(w, r)
				return
			}

			sess, err := store.Get(cookie.Value)
			if err != nil {
				// Expired/revoked/invalid — clear the cookie and continue unauthed.
				http.SetCookie(w, cookies.Clear())
				next.ServeHTTP(w, r)
				return
			}

			// Touch the session (best-effort; don't block on error).
			_ = store.Touch(sess.ID)

			// Load user email.
			var email string
			_ = store.db.QueryRow("SELECT email FROM users WHERE id = ?", sess.UserID).Scan(&email)

			// Load capabilities.
			caps, err := capLoader.EffectiveCapabilities(sess.UserID)
			if err != nil {
				// Can't determine permissions — treat as unauthed.
				next.ServeHTTP(w, r)
				return
			}

			p := &Principal{
				UserID:       sess.UserID,
				Email:        email,
				Capabilities: caps,
				SessionID:    sess.ID,
			}

			// Roles are for the audit trail only. A failure to load them is
			// deliberately NOT treated the way a capability failure is: an
			// unreadable capability set means permissions are unknown and the
			// request continues unauthenticated, whereas unreadable roles mean
			// one audit column is thinner than usual. Refusing the request over
			// that would trade a working portal for a tidier log.
			if rl, ok := capLoader.(RoleLoader); ok {
				if roles, err := rl.EffectiveRoles(sess.UserID); err == nil {
					p.Roles = roles
				}
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
		})
	}
}

// SQLCapabilityLoader loads effective capabilities from the database using
// the union of role grants and direct capability grants.
type SQLCapabilityLoader struct {
	DB *sql.DB
}

// EffectiveCapabilities returns the set of capability codes the user holds
// through active role grants and direct capability grants.
func (l *SQLCapabilityLoader) EffectiveCapabilities(userID int64) (map[string]struct{}, error) {
	rows, err := l.DB.Query(`
		SELECT DISTINCT c.code
		FROM capabilities c
		WHERE c.code IN (
			SELECT rc.capability_code
			FROM user_role_grants urg
			JOIN role_capabilities rc ON rc.role_code = urg.role_code
			WHERE urg.user_id = ? AND urg.revoked_at IS NULL
			UNION
			SELECT ucg.capability_code
			FROM user_capability_grants ucg
			WHERE ucg.user_id = ? AND ucg.revoked_at IS NULL
		)
	`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	caps := make(map[string]struct{})
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		caps[code] = struct{}{}
	}
	return caps, errors.Join(rows.Err())
}

// EffectiveRoles returns the role codes the user currently holds.
//
// Direct capability grants are deliberately absent: they are not roles, and
// listing them here would make the audit column answer a different question
// than its name asks.
func (l *SQLCapabilityLoader) EffectiveRoles(userID int64) ([]string, error) {
	rows, err := l.DB.Query(
		`SELECT role_code FROM user_role_grants
		  WHERE user_id = ? AND revoked_at IS NULL
		  ORDER BY role_code`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var roles []string
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		roles = append(roles, code)
	}
	return roles, rows.Err()
}
