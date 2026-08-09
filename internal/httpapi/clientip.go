package httpapi

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/clientip"
)

// Client-address resolution and hashing live in internal/clientip, because the
// admin UI needs the identical construction and cannot import this package.
// What remains here is the Huma adapter: the middleware that reaches into a
// huma.Context for the header and peer address.
//
// See the internal/clientip package comment for the keying decision.

// ClientIPConfig configures how the client address is obtained and hashed.
type ClientIPConfig = clientip.Config

// ClientIPMiddleware resolves the client address for every API request and
// stores its hash in the request context, where handlers read it with
// ClientIPHashFrom.
//
// It runs in the middleware layer because that is the only place with access to
// the request; handlers receive a bare context.Context.
func ClientIPMiddleware(cfg ClientIPConfig) func(huma.Context, func(huma.Context)) {
	hasher := clientip.NewHasher(cfg)
	return func(ctx huma.Context, next func(huma.Context)) {
		var forwarded string
		if h := hasher.Header(); h != "" {
			forwarded = ctx.Header(h)
		}
		hash := hasher.Hash(forwarded, ctx.RemoteAddr())
		next(huma.WithValue(ctx, clientip.ContextKey(), hash))
	}
}

// ClientIPHashFrom returns the hashed client address for the current request,
// or "" when no address was available or hashing is not configured.
func ClientIPHashFrom(ctx context.Context) string {
	return clientip.HashFrom(ctx)
}
