// Package audit records security-relevant events to the audit_events table.
//
// Events are emitted generically by the transport layer from the AuditAction
// declared on each operation, rather than by individual handlers. That keeps
// coverage tied to registration metadata instead of to handler discipline.
package audit

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"time"

	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/obs"
)

// Outcome values recorded in audit_events.outcome.
const (
	OutcomeSuccess = "success"
	OutcomeFailure = "failure"
	OutcomeDenied  = "denied"
)

// Reason codes recorded in audit_events.reason_code for denied outcomes.
const (
	ReasonUnauthenticated     = "unauthenticated"
	ReasonMissingCapability   = "missing_capability"
	ReasonNoMetadata          = "no_operation_metadata"
	ReasonRateLimited         = "rate_limited"
	ReasonMissingConfirmation = "missing_confirmation"
)

// OutcomeForStatus maps a response status to an audit outcome.
//
// It lives here because both transports record the same events into the same
// table, and they previously carried byte-identical private copies of this
// mapping — two places obliged to agree with nothing forcing them to. A rule
// added to one copy would silently not apply to the other surface.
//
// 401, 403, and 429 are recorded as denials rather than failures: each is the
// server refusing a request it understood, and grouping them lets an operator
// read refusals without sifting them out of genuine errors.
func OutcomeForStatus(status int) string {
	switch {
	case status == http.StatusUnauthorized,
		status == http.StatusForbidden,
		status == http.StatusTooManyRequests:
		return OutcomeDenied
	case status >= 400:
		return OutcomeFailure
	default:
		return OutcomeSuccess
	}
}

// Event is a single audit record. Zero-valued optional fields are stored NULL.
type Event struct {
	// Action is the dot-separated event name (e.g. "member.create").
	Action string
	// ActorUserID is the acting user; 0 means unauthenticated.
	ActorUserID int64
	// ResourceKind and ResourceID identify the target, when known.
	ResourceKind string
	ResourceID   int64
	// Outcome is one of OutcomeSuccess, OutcomeFailure, OutcomeDenied.
	Outcome string
	// ReasonCode explains a denial or failure.
	ReasonCode string
	// DetailJSON carries structured, non-PII context (method, path, status).
	DetailJSON string
}

// --- Resource stamping ---
//
// The generic emitter knows the operation but not always the resource: a
// create has no {id} path parameter, and the new row's ID only exists inside
// the handler. Handlers stamp it here so the audit event can name what was
// touched. Stamping is optional and fails safe — an unstamped create is
// recorded without a resource id, never dropped.

type resourceSlot struct {
	Kind string
	ID   int64
}

type resourceCtxKey struct{}

// WithResourceSlot returns a context carrying a mutable slot for StampResource.
// The transport layer installs it before calling the handler.
func WithResourceSlot(ctx context.Context) context.Context {
	return context.WithValue(ctx, resourceCtxKey{}, &resourceSlot{})
}

// StampResource records the resource a handler acted on. Calling it without an
// installed slot is a no-op, so handlers need no knowledge of the transport.
func StampResource(ctx context.Context, kind string, id int64) {
	if slot, ok := ctx.Value(resourceCtxKey{}).(*resourceSlot); ok {
		slot.Kind = kind
		slot.ID = id
	}
}

// StampedResource returns the resource stamped during this request, if any.
func StampedResource(ctx context.Context) (kind string, id int64, ok bool) {
	slot, found := ctx.Value(resourceCtxKey{}).(*resourceSlot)
	if !found || (slot.Kind == "" && slot.ID == 0) {
		return "", 0, false
	}
	return slot.Kind, slot.ID, true
}

// Recorder writes audit events.
type Recorder interface {
	// Record persists e. Implementations must not block the caller on
	// failure; they report errors through their own logger.
	Record(ctx context.Context, e Event)
}

// SQLRecorder writes audit events to the database.
type SQLRecorder struct {
	q   *sqlcgen.Queries
	log *slog.Logger
}

// NewSQLRecorder returns a Recorder backed by db. It returns nil if db is nil,
// which callers must treat as "auditing disabled" — see Record's nil handling.
func NewSQLRecorder(db *sql.DB, log *slog.Logger) *SQLRecorder {
	if db == nil {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	return &SQLRecorder{q: sqlcgen.New(db), log: log}
}

// Record writes e to audit_events.
//
// A write failure is logged at ERROR and does not fail the request: for a
// mutation that already committed, failing the response would misreport what
// happened. Losing an audit row is itself a defect, so the log line is the
// alerting signal.
func (r *SQLRecorder) Record(ctx context.Context, e Event) {
	if r == nil {
		return
	}

	// Detach from the request context so a client disconnect or a cancelled
	// handler context cannot drop the audit write.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	params := sqlcgen.CreateAuditEventParams{
		OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
		Action:     e.Action,
		Outcome:    e.Outcome,
	}
	if id := obs.RequestIDFrom(ctx); id != "" {
		params.RequestID = sql.NullString{String: id, Valid: true}
	}
	if e.ActorUserID != 0 {
		params.ActorUserID = sql.NullInt64{Int64: e.ActorUserID, Valid: true}
	}
	if e.ResourceKind != "" {
		params.ResourceKind = sql.NullString{String: e.ResourceKind, Valid: true}
	}
	if e.ResourceID != 0 {
		params.ResourceID = sql.NullInt64{Int64: e.ResourceID, Valid: true}
	}
	if e.ReasonCode != "" {
		params.ReasonCode = sql.NullString{String: e.ReasonCode, Valid: true}
	}
	if e.DetailJSON != "" {
		params.DetailJson = sql.NullString{String: e.DetailJSON, Valid: true}
	}

	if _, err := r.q.CreateAuditEvent(writeCtx, params); err != nil {
		r.log.Error("audit write failed",
			slog.String("action", e.Action),
			slog.String("outcome", e.Outcome),
			slog.Int64("actor_user_id", e.ActorUserID),
			slog.String("error", err.Error()),
		)
	}
}
