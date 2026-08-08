package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/authn"
	"github.com/bcars/bcars-portal/internal/domain/authz"
)

// toAuthzPrincipal converts an authn.Principal to an authz.Principal.
func toAuthzPrincipal(p *authn.Principal) *authz.Principal {
	if p == nil {
		return nil
	}
	return &authz.Principal{
		UserID:       p.UserID,
		Capabilities: p.Capabilities,
	}
}

// requirePrincipal extracts the authn principal from context and returns
// an authz principal. Returns a Huma error if unauthenticated.
func requirePrincipal(ctx context.Context) (*authz.Principal, error) {
	p := authn.PrincipalFrom(ctx)
	if p == nil {
		return nil, huma.NewError(http.StatusUnauthorized, "not authenticated")
	}
	return toAuthzPrincipal(p), nil
}
