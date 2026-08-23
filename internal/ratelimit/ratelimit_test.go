package ratelimit

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/db/dbtest"
)

const testSecret = "ratelimit-test-secret-32-bytes!!"

// clock is a controllable time source, so window expiry is tested by advancing
// it rather than by sleeping.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newLimiter(t *testing.T) (*Limiter, *clock, *sql.DB) {
	t.Helper()
	d := dbtest.Open(t)

	c := &clock{t: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)}
	return New(d, Config{HashKey: []byte(testSecret), Now: c.now}), c, d
}

var testRule = Rule{
	Operation:    OpRecovery,
	Window:       15 * time.Minute,
	MaxPerSource: 3,
	MaxPerTarget: 3,
}

// TestBoundIsEnforcedPerSource proves one caller is cut off at the bound.
func TestBoundIsEnforcedPerSource(t *testing.T) {
	l, _, _ := newLimiter(t)
	ctx := context.Background()

	for i := 0; i < testRule.MaxPerSource; i++ {
		d := l.Allow(ctx, testRule, "source-a", "someone@example.test")
		assert.True(t, d.Allowed, "attempt %d must be allowed", i+1)
	}

	d := l.Allow(ctx, testRule, "source-a", "someone@example.test")
	assert.False(t, d.Allowed, "the attempt after the bound must be refused")
	assert.Equal(t, ReasonSource, d.Reason)
}

// TestBoundIsPerSourceNotGlobal proves one abusive caller cannot exhaust
// everyone else's allowance.
func TestBoundIsPerSourceNotGlobal(t *testing.T) {
	l, _, _ := newLimiter(t)
	ctx := context.Background()

	// Distinct targets, so only the per-source bound is in play.
	for i := 0; i < testRule.MaxPerSource+2; i++ {
		l.Allow(ctx, testRule, "noisy", "target-a@example.test")
	}

	d := l.Allow(ctx, testRule, "quiet", "target-b@example.test")
	assert.True(t, d.Allowed, "a different caller must keep its own allowance")
}

// TestUnknownSourceIsNotOneBucket proves an unidentifiable caller does not join
// a shared bucket. If it did, one attacker without a resolvable address could
// lock out every other such caller.
func TestUnknownSourceIsNotOneBucket(t *testing.T) {
	l, _, _ := newLimiter(t)
	ctx := context.Background()

	for i := 0; i < testRule.MaxPerSource+3; i++ {
		l.Allow(ctx, testRule, "", "target-a@example.test")
	}

	d := l.Allow(ctx, testRule, "", "target-b@example.test")
	assert.True(t, d.Allowed,
		"unknown sources must not be grouped together into one shared allowance")
}

// TestBoundIsEnforcedPerTarget proves a distributed probe cannot bury one
// member in recovery mail by rotating source addresses.
func TestBoundIsEnforcedPerTarget(t *testing.T) {
	l, _, _ := newLimiter(t)
	ctx := context.Background()

	const victim = "victim@example.test"
	for i, src := range []string{"src-1", "src-2", "src-3"} {
		d := l.Allow(ctx, testRule, src, victim)
		assert.True(t, d.Allowed, "attempt %d from a fresh source must be allowed", i+1)
	}

	d := l.Allow(ctx, testRule, "src-4", victim)
	assert.False(t, d.Allowed, "the target bound must hold across sources")
	assert.Equal(t, ReasonTarget, d.Reason)
}

// TestWindowExpires proves the bound is rolling, not permanent.
func TestWindowExpires(t *testing.T) {
	l, c, _ := newLimiter(t)
	ctx := context.Background()

	for i := 0; i < testRule.MaxPerSource; i++ {
		l.Allow(ctx, testRule, "source-a", "someone@example.test")
	}
	require.False(t, l.Allow(ctx, testRule, "source-a", "someone@example.test").Allowed)

	// Still inside the window.
	c.t = c.t.Add(testRule.Window - time.Minute)
	assert.False(t, l.Allow(ctx, testRule, "source-a", "someone@example.test").Allowed,
		"the bound must hold until the window actually passes")

	// Past the window, counting from the most recent attempt.
	c.t = c.t.Add(testRule.Window + time.Minute)
	assert.True(t, l.Allow(ctx, testRule, "source-a", "someone@example.test").Allowed,
		"the caller must recover once the window has passed")
}

// TestRefusedAttemptsStillCount proves hammering extends the block rather than
// draining it, which is the intended behaviour for sustained abuse.
func TestRefusedAttemptsStillCount(t *testing.T) {
	l, c, _ := newLimiter(t)
	ctx := context.Background()

	for i := 0; i < testRule.MaxPerSource+1; i++ {
		l.Allow(ctx, testRule, "source-a", "someone@example.test")
	}

	// A caller who keeps hammering near the end of the window refills it with
	// refused attempts.
	c.t = c.t.Add(testRule.Window - time.Minute)
	for i := 0; i < testRule.MaxPerSource; i++ {
		require.False(t, l.Allow(ctx, testRule, "source-a", "someone@example.test").Allowed)
	}

	// The original attempts have now aged out. The refused ones made while
	// blocked have not, so the block continues.
	c.t = c.t.Add(2 * time.Minute)
	assert.False(t, l.Allow(ctx, testRule, "source-a", "someone@example.test").Allowed,
		"attempts made while blocked keep the window full")

	// A caller who instead stops recovers, which is what makes this a rolling
	// window rather than an escalating ban.
	c.t = c.t.Add(testRule.Window + time.Minute)
	assert.True(t, l.Allow(ctx, testRule, "source-a", "someone@example.test").Allowed,
		"backing off for a full window clears the block")
}

// TestOperationsAreSeparateBuckets proves the limiter is reusable: member
// sign-in and public intake will not consume recovery's allowance.
func TestOperationsAreSeparateBuckets(t *testing.T) {
	l, _, _ := newLimiter(t)
	ctx := context.Background()

	for i := 0; i < testRule.MaxPerSource+2; i++ {
		l.Allow(ctx, testRule, "source-a", "someone@example.test")
	}

	signIn := testRule
	signIn.Operation = OpMemberSignIn
	d := l.Allow(ctx, signIn, "source-a", "someone@example.test")
	assert.True(t, d.Allowed, "a different operation has its own allowance")
}

// TestTargetIsNormalized proves trivially different spellings share a bucket,
// so case or whitespace cannot multiply an attacker's allowance.
func TestTargetIsNormalized(t *testing.T) {
	l, _, _ := newLimiter(t)
	ctx := context.Background()

	const victim = "victim@example.test"
	l.Allow(ctx, testRule, "src-1", victim)
	l.Allow(ctx, testRule, "src-2", "  VICTIM@Example.TEST  ")
	l.Allow(ctx, testRule, "src-3", "Victim@example.test")

	d := l.Allow(ctx, testRule, "src-4", victim)
	assert.False(t, d.Allowed, "case and whitespace variants are one target")
	assert.Equal(t, ReasonTarget, d.Reason)
}

// TestTargetIsNotStoredInPlaintext proves the abuse control does not become a
// log of every address anyone probed. The rows for unknown addresses belong to
// people who are not members.
func TestTargetIsNotStoredInPlaintext(t *testing.T) {
	l, _, d := newLimiter(t)
	ctx := context.Background()

	const probed = "not-a-member@example.test"
	l.Allow(ctx, testRule, "src-1", probed)

	var n int
	require.NoError(t, d.QueryRow(
		`SELECT count(*) FROM request_attempts WHERE target_hash = ?`, probed).Scan(&n))
	assert.Zero(t, n, "the raw address must not be stored")

	require.NoError(t, d.QueryRow(`SELECT count(*) FROM request_attempts`).Scan(&n))
	assert.Equal(t, 1, n, "the attempt itself must still be recorded")
}

// TestEveryAttemptIsRecorded proves the count never depends on whether the
// target exists: the limiter records before anything looks it up, so a known
// and an unknown address produce identical rows.
func TestEveryAttemptIsRecorded(t *testing.T) {
	l, _, d := newLimiter(t)
	ctx := context.Background()

	l.Allow(ctx, testRule, "src-1", "known@example.test")
	l.Allow(ctx, testRule, "src-1", "unknown@example.test")

	var allowed int
	require.NoError(t, d.QueryRow(
		`SELECT count(*) FROM request_attempts WHERE outcome = 'allowed'`).Scan(&allowed))
	assert.Equal(t, 2, allowed, "both attempts are recorded regardless of the target")
}

// TestNilLimiterAllows proves an assembly without a database keeps working
// rather than refusing everything.
func TestNilLimiterAllows(t *testing.T) {
	var l *Limiter
	assert.True(t, l.Allow(context.Background(), testRule, "src", "someone@example.test").Allowed)
	assert.NoError(t, l.Prune(context.Background(), time.Hour))
	assert.Nil(t, New(nil, Config{}))
}

// TestPruneRemovesOldAttempts proves the table has a retention story.
func TestPruneRemovesOldAttempts(t *testing.T) {
	l, c, d := newLimiter(t)
	ctx := context.Background()

	l.Allow(ctx, testRule, "src-1", "someone@example.test")
	c.t = c.t.Add(2 * time.Hour)
	l.Allow(ctx, testRule, "src-1", "someone@example.test")

	require.NoError(t, l.Prune(ctx, time.Hour))

	var n int
	require.NoError(t, d.QueryRow(`SELECT count(*) FROM request_attempts`).Scan(&n))
	assert.Equal(t, 1, n, "only the attempt inside the retention period survives")
}

// TestZeroBoundDisables proves a rule may opt out of either dimension.
func TestZeroBoundDisables(t *testing.T) {
	l, _, _ := newLimiter(t)
	ctx := context.Background()

	sourceOnly := Rule{Operation: OpRecovery, Window: time.Hour, MaxPerSource: 2}
	l.Allow(ctx, sourceOnly, "src-1", "victim@example.test")
	l.Allow(ctx, sourceOnly, "src-2", "victim@example.test")
	l.Allow(ctx, sourceOnly, "src-3", "victim@example.test")

	assert.True(t, l.Allow(ctx, sourceOnly, "src-4", "victim@example.test").Allowed,
		"a zero per-target bound must not limit by target")
}
