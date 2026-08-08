package httpapi

import (
	"database/sql"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/authn"
)

// Deps holds all service dependencies needed by API handlers.
// Add fields as handlers are wired up. Nil fields mean the handler
// returns 501 Not Implemented.
type Deps struct {
	DB           *sql.DB
	AuthService  *authn.AuthService
	SessionStore *authn.SessionStore
	CookieName   string
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
	RegisterNotes(api, deps)
	RegisterImports(api, deps)
	RegisterExports(api, deps)
	RegisterAudit(api, deps)
	RegisterAdmin(api, deps)
}
