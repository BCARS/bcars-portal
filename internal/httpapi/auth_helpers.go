package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/authn"
	"github.com/bcars/bcars-portal/internal/db"
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

// mapDomainError converts domain-layer errors to Huma HTTP errors.
func mapDomainError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, authz.ErrUnauthenticated) {
		return huma.NewError(http.StatusUnauthorized, "not authenticated")
	}
	if errors.Is(err, authz.ErrDenied) {
		return huma.NewError(http.StatusForbidden, "insufficient capabilities")
	}
	if errors.Is(err, db.ErrStale) {
		return ErrStale("resource was modified by another request; re-fetch and retry")
	}
	if errors.Is(err, sql.ErrNoRows) {
		return huma.NewError(http.StatusNotFound, "resource not found")
	}
	return huma.NewError(http.StatusInternalServerError, err.Error())
}
