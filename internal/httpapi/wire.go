package httpapi

import (
	"database/sql"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/authn"
)

// Deps holds all service dependencies needed by API handlers.
// Add fields as handlers are wired up. Nil fields mean the handler
// returns 501 Not Implemented.
type Deps struct {
	DB               *sql.DB
	AuthService      *authn.AuthService
	SessionStore     *authn.SessionStore
	EmailLinkService *authn.EmailLinkService
	CookieName       string

	// AllowInsecureCookies drops the Secure attribute from the session
	// cookie. Development only, for serving the portal over plaintext
	// http://localhost; the zero value keeps Secure on.
	AllowInsecureCookies bool

	// EmailLinkTTL mirrors the lifetime configured on EmailLinkService so
	// responses can report when an invitation expires.
	EmailLinkTTL time.Duration
}

// InvitationTTL returns the configured link lifetime, defaulting to 24h.
func (d Deps) InvitationTTL() time.Duration {
	if d.EmailLinkTTL == 0 {
		return 24 * time.Hour
	}
	return d.EmailLinkTTL
}

// RegisterAll registers every API operation on api and panics if any
// operation is missing capability metadata. Call this before serving traffic.
//
// Callers are expected to follow up with VerifyAll(api) to catch any
// raw huma.Register calls that bypassed the metadata wrapper.
func RegisterAll(api huma.API, deps Deps) {
	RegisterSessions(api, deps)
	RegisterMembers(api, deps)
	RegisterContactMethods(api, deps)
	RegisterMemberships(api, deps)
	RegisterDues(api, deps)
	RegisterBatches(api, deps)
	RegisterPayments(api, deps)
	RegisterCorrections(api, deps)
	RegisterTreasury(api, deps)
	RegisterWorksheets(api, deps)
	RegisterNotes(api, deps)
	RegisterImports(api, deps)
	RegisterExports(api, deps)
	RegisterAudit(api, deps)
	RegisterAdmin(api, deps)
	RegisterInvitations(api, deps)
	RegisterChangeRequests(api, deps)
}
