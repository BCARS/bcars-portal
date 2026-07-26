package authz

import (
	"context"
	"errors"
)

// Principal represents the authenticated caller for authorization decisions.
type Principal struct {
	UserID       int64
	Capabilities map[string]struct{}
}

// Resource represents the target of an authorization check.
type Resource struct {
	Kind            string // e.g. "person", "membership", "note"
	ID              int64
	CreatedByUserID int64 // for ownership checks (trustee/activities_manager)
}

var (
	ErrDenied          = errors.New("authz: access denied")
	ErrUnauthenticated = errors.New("authz: unauthenticated")
)

// Authorize checks whether principal may perform action on resource.
// Returns nil if allowed, ErrUnauthenticated if principal is nil,
// or ErrDenied if the principal lacks the required capability.
func Authorize(_ context.Context, principal *Principal, action string, resource *Resource) error {
	if principal == nil {
		return ErrUnauthenticated
	}

	// Check the capability catalog to ensure the action is valid.
	cap, ok := ByCode(action)
	if !ok {
		// Unknown action → deny by default.
		return ErrDenied
	}
	_ = cap

	if !hasCapability(principal, action) {
		return ErrDenied
	}

	return nil
}

// AuthorizePublic is a no-op check for public endpoints. Always returns nil.
func AuthorizePublic() error {
	return nil
}

func hasCapability(p *Principal, code string) bool {
	if p == nil || p.Capabilities == nil {
		return false
	}
	_, ok := p.Capabilities[code]
	return ok
}
