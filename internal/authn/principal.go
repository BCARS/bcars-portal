package authn

import "context"

// Principal represents the authenticated caller attached to each request.
type Principal struct {
	UserID       int64
	Email        string
	Capabilities map[string]struct{} // effective capability codes
	SessionID    string

	// Roles are the role codes held at the time the session was resolved,
	// carried for the audit trail rather than for authorization.
	//
	// Nothing decides anything from this field: capability checks read
	// Capabilities, which is already the union of role-granted and
	// directly-granted codes. It exists because "who was this person when they
	// did that" is a question an officer asks months later, by which time the
	// roles may have changed and the answer is no longer derivable.
	Roles []string
}

// HasCapability returns true if the principal holds the given capability code.
func (p *Principal) HasCapability(code string) bool {
	if p == nil || p.Capabilities == nil {
		return false
	}
	_, ok := p.Capabilities[code]
	return ok
}

type ctxKey struct{}

// WithPrincipal attaches a Principal to the context.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// PrincipalFrom retrieves the Principal from the context, or nil if absent.
func PrincipalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(ctxKey{}).(*Principal)
	return p
}
