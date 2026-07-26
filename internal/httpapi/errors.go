package httpapi

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// Structured error codes used across all API responses. These appear in the
// RFC 9457 "type" field so clients can distinguish error conditions without
// parsing the human-readable detail string.
const (
	// ErrCodeStale is returned when an optimistic concurrency check fails:
	// the client's If-Match version is older than the current row version.
	ErrCodeStale = "stale"

	// ErrCodeValidation is returned when the request body or parameters fail
	// schema or business-rule validation.
	ErrCodeValidation = "validation"

	// ErrCodeForbidden is returned when the caller is authenticated but lacks
	// the required capability for the operation.
	ErrCodeForbidden = "forbidden"

	// ErrCodeNotFoundOrForbidden is used in place of a plain 404 when the
	// resource might exist but the caller may not be allowed to know. Officers
	// see a distinct 404; members see this combined code.
	ErrCodeNotFoundOrForbidden = "not_found_or_forbidden"

	// ErrCodeConflict is returned when a state-machine transition or unique
	// constraint would be violated (e.g. applying for membership twice).
	ErrCodeConflict = "conflict"

	// ErrCodeIdempotencyMismatch is returned when the idempotency key was
	// already used for a request with a different body.
	ErrCodeIdempotencyMismatch = "idempotency_mismatch"
)

// ErrStale returns an RFC 9457 412 Precondition Failed response indicating
// that the client's version is out of date.
func ErrStale(detail string) error {
	return huma.NewError(http.StatusPreconditionFailed, detail)
}

// ErrForbidden returns a 403 Forbidden. Prefer this over a raw huma error so
// the code constant is applied consistently.
func ErrForbidden(detail string) error {
	return huma.NewError(http.StatusForbidden, detail)
}

// ErrConflict returns a 409 Conflict.
func ErrConflict(detail string) error {
	return huma.NewError(http.StatusConflict, detail)
}

// ErrIdempotencyMismatch returns a 422 Unprocessable Entity indicating the
// idempotency key was already used with a different payload.
func ErrIdempotencyMismatch(key string) error {
	return huma.NewError(http.StatusUnprocessableEntity,
		"idempotency key already used with a different request body: "+key)
}

// ErrNotImplemented returns a 501 stub response for endpoints that are
// declared in the OpenAPI baseline but not yet implemented.
func ErrNotImplemented() error {
	return huma.NewError(http.StatusNotImplemented, "not yet implemented")
}
