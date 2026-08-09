// Package ratelimit bounds how often an unauthenticated operation may be
// invoked, per caller source and per target, over a rolling window.
//
// It is deliberately generic. Password recovery (bcars-portal-fmc.20) is the
// first consumer; passwordless member sign-in and blind public correction
// intake reuse the same mechanism and the same uniform behaviour rather than
// growing parallel limiters with their own subtly different rules.
//
// # THE ENUMERATION RULE
//
// The limiter counts REQUESTS, not sends, and it never consults whether the
// target exists before deciding. That is the whole point. A naive per-address
// limiter that only counts real addresses is worse than no limiter: the
// difference between "you were limited" and "you were not" answers the
// question the endpoint's uniform 204 exists to hide. Here, a caller at the
// limit is refused identically whether they are probing a real member or a
// made-up address.
//
// Callers must therefore invoke Allow BEFORE any existence lookup, and must
// map a refusal to the same response for every target.
package ratelimit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"

	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
)

// Operations bounded by this limiter. Named here so two consumers cannot
// disagree about the string that namespaces their counts.
const (
	OpRecovery      = "auth.recovery.request"
	OpMemberSignIn  = "auth.member.signin.request"
	OpPublicRequest = "change_request.public.submit"
)

// targetHashLabel domain-separates the target subkey from the client-address
// subkey and from password hashing, so one derived key cannot be used to attack
// another's inputs.
const targetHashLabel = "bcars-portal/rate-limit-target/v1"

// targetHashBytes is the stored truncated HMAC length.
const targetHashBytes = 16

// Rule is one operation's bound.
type Rule struct {
	// Operation namespaces the counts. Use one of the Op constants.
	Operation string
	// Window is the rolling period the counts cover.
	Window time.Duration
	// MaxPerSource bounds attempts from one caller address. Zero disables the
	// per-source bound.
	MaxPerSource int
	// MaxPerTarget bounds attempts against one target across all callers, so a
	// distributed probe cannot bury one member in recovery mail. Zero disables
	// the per-target bound.
	MaxPerTarget int
}

// RecoveryRule is the shipped bound for password recovery.
//
// Five attempts per source per fifteen minutes is far above what a person who
// forgot a password does and far below what mailbombing needs. The per-target
// bound is what protects a member when the attempts come from many addresses.
var RecoveryRule = Rule{
	Operation:    OpRecovery,
	Window:       15 * time.Minute,
	MaxPerSource: 5,
	MaxPerTarget: 5,
}

// Limiter records attempts and decides whether one more is allowed.
type Limiter struct {
	q   *sqlcgen.Queries
	key []byte
	now func() time.Time
}

// Config configures a Limiter.
type Config struct {
	// HashKey is the secret the target subkey is derived from. It is the same
	// secret the client-address hash uses; see internal/clientip for why one
	// secret rather than several. Empty falls back to an unkeyed digest, which
	// still groups correctly but is guessable — acceptable only in the
	// development mode that already runs without a pepper.
	HashKey []byte

	// Now overrides the clock. Tests use it to advance past a window; leave nil
	// in production.
	Now func() time.Time
}

// New returns a Limiter backed by db, or nil if db is nil. A nil *Limiter is
// usable and allows everything, so a consumer assembled without a database
// keeps working rather than refusing every request.
func New(db *sql.DB, cfg Config) *Limiter {
	if db == nil {
		return nil
	}
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Limiter{q: sqlcgen.New(db), key: deriveTargetKey(cfg.HashKey), now: now}
}

// Decision is the result of an Allow call.
type Decision struct {
	// Allowed is false when the caller has exceeded a bound.
	Allowed bool
	// Reason names which bound was hit: "source" or "target". Empty when
	// allowed. It is for the server's own logs and audit reason code, and must
	// never be echoed to the caller.
	Reason string
}

// Bound reasons.
const (
	ReasonSource = "source"
	ReasonTarget = "target"
)

// Allow records this attempt and reports whether it may proceed.
//
// sourceHash is the caller's hashed address from internal/clientip, or "" when
// unknown. target is the raw target (an email address); it is normalized and
// hashed here so callers never have to remember to do either.
//
// It must be called BEFORE any lookup of whether target exists. A nil Limiter
// allows everything.
//
// A database error allows the request. A limiter that fails closed would turn
// a transient database problem into a total outage of password recovery, which
// is a worse failure than a brief gap in abuse control.
func (l *Limiter) Allow(ctx context.Context, rule Rule, sourceHash, target string) Decision {
	if l == nil {
		return Decision{Allowed: true}
	}

	now := l.now().UTC()
	since := now.Add(-rule.Window).Format(time.RFC3339Nano)

	source := sql.NullString{String: sourceHash, Valid: sourceHash != ""}
	targetHash := l.hashTarget(target)

	decision := Decision{Allowed: true}

	// An unknown source is NOT a bucket. Grouping every unidentifiable caller
	// together would let one of them exhaust the allowance for all of them.
	if rule.MaxPerSource > 0 && source.Valid {
		n, err := l.q.CountRequestAttemptsBySource(ctx, sqlcgen.CountRequestAttemptsBySourceParams{
			Operation:   rule.Operation,
			SourceHash:  source,
			AttemptedAt: since,
		})
		if err == nil && n >= int64(rule.MaxPerSource) {
			decision = Decision{Allowed: false, Reason: ReasonSource}
		}
	}

	if decision.Allowed && rule.MaxPerTarget > 0 && targetHash.Valid {
		n, err := l.q.CountRequestAttemptsByTarget(ctx, sqlcgen.CountRequestAttemptsByTargetParams{
			Operation:   rule.Operation,
			TargetHash:  targetHash,
			AttemptedAt: since,
		})
		if err == nil && n >= int64(rule.MaxPerTarget) {
			decision = Decision{Allowed: false, Reason: ReasonTarget}
		}
	}

	outcome := "allowed"
	if !decision.Allowed {
		outcome = "limited"
	}
	// Recorded whatever the decision, so a refused attempt still counts against
	// the window and hammering extends the block instead of draining it.
	_, _ = l.q.RecordRequestAttempt(ctx, sqlcgen.RecordRequestAttemptParams{
		Operation:   rule.Operation,
		SourceHash:  source,
		TargetHash:  targetHash,
		Outcome:     outcome,
		AttemptedAt: now.Format(time.RFC3339Nano),
	})

	return decision
}

// Prune deletes attempts older than olderThan. Nothing schedules this yet; it
// exists so the table has a defined retention story rather than growing without
// bound forever.
func (l *Limiter) Prune(ctx context.Context, olderThan time.Duration) error {
	if l == nil {
		return nil
	}
	cutoff := l.now().UTC().Add(-olderThan).Format(time.RFC3339Nano)
	return l.q.DeleteRequestAttemptsBefore(ctx, cutoff)
}

// hashTarget normalizes and hashes a target. An empty target yields NULL rather
// than a hash of "", so "no target" cannot collide with a real one.
func (l *Limiter) hashTarget(target string) sql.NullString {
	norm := NormalizeTarget(target)
	if norm == "" {
		return sql.NullString{}
	}
	var sum []byte
	if len(l.key) > 0 {
		mac := hmac.New(sha256.New, l.key)
		mac.Write([]byte(norm))
		sum = mac.Sum(nil)
	} else {
		d := sha256.Sum256([]byte(norm))
		sum = d[:]
	}
	return sql.NullString{String: hex.EncodeToString(sum[:targetHashBytes]), Valid: true}
}

// NormalizeTarget canonicalises a target so that trivially different spellings
// of one address share a bucket. Exported because a caller that wants to know
// whether two targets group together should ask rather than reimplement it.
func NormalizeTarget(target string) string {
	return strings.ToLower(strings.TrimSpace(target))
}

// deriveTargetKey turns the configured secret into the target subkey. Returns
// nil for an empty secret.
func deriveTargetKey(secret []byte) []byte {
	if len(secret) == 0 {
		return nil
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(targetHashLabel))
	return mac.Sum(nil)
}
