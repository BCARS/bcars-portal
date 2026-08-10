package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// Confirmation for consequential operations.
//
// WHAT THIS IS
//
// An operation declaring ConfirmExplicit is refused unless the request carries
// ConfirmHeader with an affirmative value. The check runs in AuthzMiddleware
// beside the capability check, so the declaration in Register IS the
// enforcement — the same property that keeps capabilities honest.
//
// WHAT THIS IS NOT
//
// It is not proof that a human confirmed anything. A header is a client
// assertion of intent, exactly as the hand-rolled `"confirm": true` body field
// it replaces was. What it buys is that a consequential operation cannot be
// reached by a request that did not deliberately opt in, and that the opt-in
// is uniform rather than reinvented per handler.
//
// Genuine proof of human intent needs step-up re-authentication, which this
// deliberately does not claim to be. See docs/adr/0011-confirmation-control.md.
//
// WHY A HEADER RATHER THAN A BODY FIELD
//
// Not every consequential operation has a body — a DELETE or a POST .../abandon
// may carry nothing — so a body field cannot express this uniformly. A header
// also keeps intent out of the domain payload, so a request body stays a
// description of what to write rather than a mix of data and control flags.

// ConfirmHeader carries the caller's explicit intent.
const ConfirmHeader = "X-Confirm"

// confirmedCtxKey keys the resolved confirmation in the request context.
type confirmedCtxKey struct{}

// ConfirmedFrom reports whether this request carried an affirmative
// ConfirmHeader.
//
// Handlers pass it into domain calls that take a confirmation parameter rather
// than passing a literal true. The middleware has already refused unconfirmed
// requests, so it is always true in practice — but wiring the real value means
// that if the middleware were ever removed or reordered, the domain guard
// refuses instead of silently accepting. A literal true would turn that
// backstop into decoration.
func ConfirmedFrom(ctx context.Context) bool {
	v, _ := ctx.Value(confirmedCtxKey{}).(bool)
	return v
}

// withConfirmed returns ctx carrying the resolved confirmation.
func withConfirmed(ctx context.Context, confirmed bool) context.Context {
	return context.WithValue(ctx, confirmedCtxKey{}, confirmed)
}

// requestIsConfirmed reports whether the request carries an affirmative
// ConfirmHeader.
//
// Only unambiguous affirmatives count. A header present but set to "false", or
// to something unparseable, is NOT a confirmation: a client that sends
// X-Confirm: false is stating the opposite, and treating mere presence as
// consent would make the control trivially satisfiable by accident.
func requestIsConfirmed(ctx huma.Context) bool {
	switch strings.ToLower(strings.TrimSpace(ctx.Header(ConfirmHeader))) {
	case "true", "yes", "1":
		return true
	default:
		return false
	}
}

// confirmationStatus is the response status for a refused confirmation.
//
// 428 Precondition Required, not 400: the request is well-formed and the caller
// is authorized, but the server requires it to be made conditional on stated
// intent. It is deliberately distinct from the 412 an If-Match mismatch
// produces, so "you did not confirm" and "someone else changed the row" are not
// the same signal to a client.
const confirmationStatus = http.StatusPreconditionRequired
