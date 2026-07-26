package httpapi

import "github.com/danielgtaylor/huma/v2"

// RegisterAll registers every API operation on api and panics if any
// operation is missing capability metadata. Call this before serving traffic.
//
// Callers are expected to follow up with VerifyAll(api) to catch any
// raw huma.Register calls that bypassed the metadata wrapper.
func RegisterAll(api huma.API) {
	RegisterSessions(api)
	RegisterMembers(api)
	RegisterContactMethods(api)
	RegisterMemberships(api)
	RegisterNotes(api)
	RegisterImports(api)
	RegisterExports(api)
	RegisterAudit(api)
	RegisterAdmin(api)
}
