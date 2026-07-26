package authn

import "context"

// Principal represents the authenticated caller attached to each request.
type Principal struct {
	UserID       int64
	Email        string
	Capabilities map[string]struct{} // effective capability codes
	SessionID    string
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
