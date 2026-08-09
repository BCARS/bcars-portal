// Package idem provides exactly-once semantics for retryable writes.
//
// The database, not process memory, owns replay detection. A treasurer whose
// browser retries after a timeout, or a tool that retries after the server
// restarts, must not create a second batch or post the same money twice.
//
// Keys are scoped by actor and operation, so two officers may independently use
// the same client-generated key, and one officer may use one key for a batch
// creation and another operation without collision.
package idem

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
)

// ErrKeyReused is returned when a key was already used by this actor for this
// operation but with a different request body. Replaying it would silently do
// different work under the guise of a retry, so it is refused.
var ErrKeyReused = errors.New("idem: idempotency key already used with a different request")

// Claim is the result of claiming a key.
type Claim struct {
	// Replay is true when this exact request was already carried out. The
	// caller must return the existing resource rather than repeating the work.
	Replay bool
	// ResourceKind and ResourceID identify what the original request produced.
	// Only meaningful when Replay is true.
	ResourceKind string
	ResourceID   int64
	// recordID is the row to stamp once the work completes.
	recordID int64
}

// Hash produces the request fingerprint stored alongside a key. Callers pass
// the fields that define the work; two requests that differ in any of them are
// different requests.
func Hash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// Begin claims key for actor and operation.
//
// It returns a Claim with Replay set when the same actor already completed this
// exact request, ErrKeyReused when the key was used for a different request,
// and otherwise a fresh claim the caller completes with Complete.
//
// Pass the transaction's Queries so the claim and the work commit together.
func Begin(ctx context.Context, q *sqlcgen.Queries, actorUserID int64, operation, key, requestHash string) (Claim, error) {
	if key == "" {
		// No key supplied: the caller accepts at-least-once semantics.
		return Claim{}, nil
	}

	existing, err := q.GetIdempotencyRecord(ctx, sqlcgen.GetIdempotencyRecordParams{
		ActorUserID:    actorUserID,
		Operation:      operation,
		IdempotencyKey: key,
	})
	switch {
	case err == nil:
		if existing.RequestHash != requestHash {
			return Claim{}, fmt.Errorf("%w: operation %q", ErrKeyReused, operation)
		}
		return Claim{
			Replay:       true,
			ResourceKind: existing.ResourceKind.String,
			ResourceID:   existing.ResourceID.Int64,
		}, nil
	case errors.Is(err, sql.ErrNoRows):
		// Fall through and claim it.
	default:
		return Claim{}, err
	}

	rec, err := q.CreateIdempotencyRecord(ctx, sqlcgen.CreateIdempotencyRecordParams{
		ActorUserID:    actorUserID,
		Operation:      operation,
		IdempotencyKey: key,
		RequestHash:    requestHash,
	})
	if err != nil {
		return Claim{}, err
	}
	return Claim{recordID: rec.ID}, nil
}

// Complete records which resource the claimed work produced. It is a no-op for
// an unkeyed request or a replay.
func (c Claim) Complete(ctx context.Context, q *sqlcgen.Queries, kind string, id int64) error {
	if c.recordID == 0 || c.Replay {
		return nil
	}
	return q.CompleteIdempotencyRecord(ctx, sqlcgen.CompleteIdempotencyRecordParams{
		ResourceKind: sql.NullString{String: kind, Valid: kind != ""},
		ResourceID:   sql.NullInt64{Int64: id, Valid: id != 0},
		ID:           c.recordID,
	})
}
