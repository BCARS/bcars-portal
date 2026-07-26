package httpapi

import (
	"context"
	"net/http"
	"strings"
)

// IdempotencyKeyHeader is the header clients send to make a write operation
// safe to retry. The value must be a client-generated UUID or ULID.
const IdempotencyKeyHeader = "Idempotency-Key"

// idempotencyKey is the context key used to store the extracted key.
type idempotencyKey struct{}

// IdempotencyKeyFrom returns the Idempotency-Key that the middleware extracted
// from the request, or the empty string if none was present.
func IdempotencyKeyFrom(ctx context.Context) string {
	if v, ok := ctx.Value(idempotencyKey{}).(string); ok {
		return v
	}
	return ""
}

// IdempotencyMiddleware extracts the Idempotency-Key header and stores it in
// the request context. It does NOT perform deduplication — that requires
// database access wired in WS3. Handlers call IdempotencyKeyFrom(ctx) to read
// the key and then consult the repository layer.
//
// Endpoints that require an idempotency key should validate the presence of a
// non-empty key themselves and return a 422 if absent.
func IdempotencyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(r.Header.Get(IdempotencyKeyHeader))
		if key != "" {
			r = r.WithContext(context.WithValue(r.Context(), idempotencyKey{}, key))
		}
		next.ServeHTTP(w, r)
	})
}
