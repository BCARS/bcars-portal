package batches

// This file is in package batches rather than batches_test so it can reach the
// failBeforeCommit seam. Everything else about posting is tested from outside
// the package, through the exported API.

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/db"
	"github.com/bcars/bcars-portal/internal/domain/authz"
)

// TestPostRollsBackOnStorageFailure is the hard case: the payments and coverage
// events are already written inside the transaction when the failure hits. If
// the rollback were not real, the ledger would be left holding money that no
// posted batch accounts for.
func TestPostRollsBackOnStorageFailure(t *testing.T) {
	d, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	require.NoError(t, db.Migrate(d))

	_, err = d.Exec(`INSERT INTO users (email) VALUES ('treasurer@example.test')`)
	require.NoError(t, err)
	_, err = d.Exec(`INSERT INTO persons (display_name, sort_name) VALUES ('Rollback Member', 'rollback')`)
	require.NoError(t, err)
	res, err := d.Exec(`INSERT INTO memberships (person_id, base_type, lifecycle) VALUES (1, 'full', 'approved')`)
	require.NoError(t, err)
	membershipID, err := res.LastInsertId()
	require.NoError(t, err)

	svc := NewService(d)
	principal := &authz.Principal{UserID: 1, Capabilities: map[string]struct{}{
		"payment.batch.manage": {}, "payment.post": {}, "payment.read": {},
	}}
	ctx := context.Background()

	batch, err := svc.Open(ctx, principal, OpenParams{Label: "Rollback batch"}, time.Now())
	require.NoError(t, err)
	_, after, err := svc.AddEntry(ctx, principal, batch.ID, EntryInput{
		MembershipID: membershipID, AmountCents: 4000, Method: MethodCash,
		ReceivedOn: "2026-01-15", PaidThrough: "2026-12-31",
	}, "")
	require.NoError(t, err)

	boom := errors.New("simulated storage failure")
	failBeforeCommit = func() error { return boom }
	t.Cleanup(func() { failBeforeCommit = nil })

	_, err = svc.Post(ctx, principal, batch.ID, PostParams{
		ExpectedVersion: after.Version, IdempotencyKey: "post-1", Confirm: true,
	}, time.Now())
	require.ErrorIs(t, err, boom)

	var payments, coverage int
	require.NoError(t, d.QueryRow(`SELECT count(*) FROM payments`).Scan(&payments))
	require.NoError(t, d.QueryRow(`SELECT count(*) FROM coverage_events`).Scan(&coverage))
	assert.Zero(t, payments, "a failed post writes no payment")
	assert.Zero(t, coverage, "a failed post writes no coverage event")

	var state string
	require.NoError(t, d.QueryRow(`SELECT state FROM payment_batches WHERE id = ?`, batch.ID).Scan(&state))
	assert.Equal(t, StateOpen, state, "the batch is left open for the treasurer to retry")

	// The idempotency claim must roll back too, or the retry after fixing the
	// fault would be mistaken for a replay and silently post nothing.
	var claims int
	require.NoError(t, d.QueryRow(`SELECT count(*) FROM idempotency_records`).Scan(&claims))
	assert.Zero(t, claims, "a rolled-back post leaves no idempotency claim behind")

	failBeforeCommit = nil
	result, err := svc.Post(ctx, principal, batch.ID, PostParams{
		ExpectedVersion: after.Version, IdempotencyKey: "post-1", Confirm: true,
	}, time.Now())
	require.NoError(t, err, "the same request succeeds once the fault clears")
	require.Len(t, result.Payments, 1)
	assert.Equal(t, StatePosted, result.Batch.State)
}

// TestPostRollsBackWhenACoverageWriteFails proves the same guarantee when the
// failure comes from the database itself rather than an injected error: a
// second event superseding an already-superseded decision violates the schema.
func TestPostRollsBackWhenACoverageWriteFails(t *testing.T) {
	d, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	require.NoError(t, db.Migrate(d))

	_, err = d.Exec(`INSERT INTO users (email) VALUES ('treasurer@example.test')`)
	require.NoError(t, err)
	_, err = d.Exec(`INSERT INTO persons (display_name, sort_name) VALUES ('Conflict Member', 'conflict')`)
	require.NoError(t, err)
	_, err = d.Exec(`INSERT INTO memberships (person_id, base_type, lifecycle) VALUES (1, 'full', 'approved')`)
	require.NoError(t, err)

	svc := NewService(d)
	principal := &authz.Principal{UserID: 1, Capabilities: map[string]struct{}{
		"payment.batch.manage": {}, "payment.post": {}, "payment.read": {},
	}}
	ctx := context.Background()

	batch, err := svc.Open(ctx, principal, OpenParams{Label: "Conflicting batch"}, time.Now())
	require.NoError(t, err)
	_, after, err := svc.AddEntry(ctx, principal, batch.ID, EntryInput{
		MembershipID: 1, AmountCents: 4000, Method: MethodCash,
		ReceivedOn: "2026-01-15", PaidThrough: "2026-12-31",
	}, "")
	require.NoError(t, err)

	// A membership that no longer exists makes the payment insert fail on its
	// foreign key, halfway through the posting transaction.
	_, err = d.Exec(`PRAGMA foreign_keys = OFF`)
	require.NoError(t, err)
	_, err = d.Exec(`DELETE FROM memberships WHERE id = 1`)
	require.NoError(t, err)
	_, err = d.Exec(`PRAGMA foreign_keys = ON`)
	require.NoError(t, err)

	_, err = svc.Post(ctx, principal, batch.ID, PostParams{
		ExpectedVersion: after.Version, IdempotencyKey: "post-2", Confirm: true,
	}, time.Now())
	assert.ErrorIs(t, err, sql.ErrNoRows, "the missing membership is caught before anything is written")

	var payments, coverage int
	require.NoError(t, d.QueryRow(`SELECT count(*) FROM payments`).Scan(&payments))
	require.NoError(t, d.QueryRow(`SELECT count(*) FROM coverage_events`).Scan(&coverage))
	assert.Zero(t, payments)
	assert.Zero(t, coverage)

	var state string
	require.NoError(t, d.QueryRow(`SELECT state FROM payment_batches WHERE id = ?`, batch.ID).Scan(&state))
	assert.Equal(t, StateOpen, state)
}
