package batches_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/db"
	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/domain/batches"
	"github.com/bcars/bcars-portal/internal/domain/dues"
	"github.com/bcars/bcars-portal/internal/domain/idem"
)

// poster holds every capability the posting path needs.
func poster() *authz.Principal {
	return &authz.Principal{UserID: 1, Capabilities: map[string]struct{}{
		"payment.read": {}, "payment.batch.manage": {}, "payment.post": {}, "dues.read": {},
	}}
}

func postParams(version int64, key string) batches.PostParams {
	return batches.PostParams{ExpectedVersion: version, IdempotencyKey: key, Confirm: true}
}

// TestPostChangesStandingOnlyAfterCommit is the property the whole draft model
// exists to provide: standing is untouched while the batch is open, and moves
// exactly once when it posts.
func TestPostChangesStandingOnlyAfterCommit(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Posting Member")
	b := f.open(t, "January dues")

	_, afterAdd, err := f.svc.AddEntry(ctx, poster(), b.ID, entry(m, 4000, batches.MethodCash), "")
	require.NoError(t, err)

	before, err := f.dues.GetStanding(ctx, poster(), m, asOf, 30)
	require.NoError(t, err)
	require.Equal(t, dues.StatusUnknown, before.Status, "an open batch changes nothing")

	result, err := f.svc.Post(ctx, poster(), b.ID, postParams(afterAdd.Version, "post-1"), time.Now())
	require.NoError(t, err)

	assert.Equal(t, batches.StatePosted, result.Batch.State)
	assert.Equal(t, int64(1), result.Batch.PostedByUserID)
	assert.NotEmpty(t, result.Batch.PostedAt)
	require.Len(t, result.Payments, 1)
	require.Len(t, result.Coverage, 1)

	pay := result.Payments[0]
	assert.Equal(t, int64(4000), pay.AmountCents)
	assert.Equal(t, "original", pay.EntryKind)
	assert.Equal(t, int64(1), pay.EnteredByUserID)
	assert.NotEmpty(t, pay.ReceiptCode)

	cov := result.Coverage[0]
	assert.Equal(t, "payment", cov.ReasonKind)
	assert.Equal(t, pay.ID, cov.PaymentID)
	assert.Equal(t, "2026-12-31", cov.PaidThrough)

	after, err := f.dues.GetStanding(ctx, poster(), m, asOf, 30)
	require.NoError(t, err)
	assert.Equal(t, dues.StatusCurrent, after.Status)
	assert.Equal(t, "2026-12-31", after.PaidThrough)
}

// TestPostIsAllOrNothingAcrossMembers proves a multi-row batch moves every
// intended standing, exactly once each.
func TestPostIsAllOrNothingAcrossMembers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := f.member(t, "Alpha Payer")
	c := f.member(t, "Charlie Payer")
	b := f.open(t, "Meeting night")

	_, _, err := f.svc.AddEntry(ctx, poster(), b.ID, entry(a, 4000, batches.MethodCash), "")
	require.NoError(t, err)
	_, afterAdd, err := f.svc.AddEntry(ctx, poster(), b.ID, entry(c, 10000, batches.MethodCheck), "")
	require.NoError(t, err)

	result, err := f.svc.Post(ctx, poster(), b.ID, postParams(afterAdd.Version, "post-1"), time.Now())
	require.NoError(t, err)
	require.Len(t, result.Payments, 2)
	require.Len(t, result.Coverage, 2)

	for _, id := range []int64{a, c} {
		st, err := f.dues.GetStanding(ctx, poster(), id, asOf, 30)
		require.NoError(t, err)
		assert.Equal(t, dues.StatusCurrent, st.Status)
	}

	var coverageRows int
	require.NoError(t, f.db.QueryRow(`SELECT count(*) FROM coverage_events`).Scan(&coverageRows))
	assert.Equal(t, 2, coverageRows, "one coverage event per entry, not per member per query")
}

// TestPostTotalsMatchTheLedger proves the batch totals a treasurer reconciled
// against are the same numbers the ledger now holds.
func TestPostTotalsMatchTheLedger(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := f.member(t, "Alpha Total")
	c := f.member(t, "Charlie Total")
	b := f.open(t, "Reconciliation")

	for _, e := range []batches.EntryInput{
		entry(a, 4000, batches.MethodCash),
		entry(c, 2500, batches.MethodCash),
		entry(a, 10000, batches.MethodCheck),
		entry(c, 750, batches.MethodOther),
	} {
		_, _, err := f.svc.AddEntry(ctx, poster(), b.ID, e, "")
		require.NoError(t, err)
	}
	current, err := f.svc.Get(ctx, poster(), b.ID)
	require.NoError(t, err)

	result, err := f.svc.Post(ctx, poster(), b.ID, postParams(current.Version, "post-1"), time.Now())
	require.NoError(t, err)

	var ledgerTotal, ledgerCount int64
	require.NoError(t, f.db.QueryRow(
		`SELECT COALESCE(SUM(amount_cents), 0), COUNT(*) FROM payments WHERE batch_id = ?`,
		b.ID).Scan(&ledgerTotal, &ledgerCount))

	assert.Equal(t, int64(17250), result.Batch.Totals.NetTotalCents)
	assert.Equal(t, result.Batch.Totals.NetTotalCents, ledgerTotal,
		"the reconciled batch total must equal the posted ledger sum")
	assert.Equal(t, result.Batch.Totals.EntryCount, ledgerCount)
	assert.Equal(t, int64(6500), result.Batch.Totals.CashTotalCents)
	assert.Equal(t, int64(10000), result.Batch.Totals.CheckTotalCents)
}

// TestPostChainsCoverageForRepeatedMembers proves two rows for one member in a
// single batch produce a chain rather than a fork.
func TestPostChainsCoverageForRepeatedMembers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Twice Payer")
	b := f.open(t, "Two rows, one member")

	first := entry(m, 4000, batches.MethodCash)
	first.PaidThrough = "2026-12-31"
	second := entry(m, 4000, batches.MethodCash)
	second.PaidThrough = "2027-12-31"

	_, _, err := f.svc.AddEntry(ctx, poster(), b.ID, first, "")
	require.NoError(t, err)
	_, afterAdd, err := f.svc.AddEntry(ctx, poster(), b.ID, second, "")
	require.NoError(t, err)

	result, err := f.svc.Post(ctx, poster(), b.ID, postParams(afterAdd.Version, "post-1"), time.Now())
	require.NoError(t, err)
	require.Len(t, result.Coverage, 2)

	assert.Zero(t, result.Coverage[0].SupersedesEventID, "the first decision supersedes nothing")
	assert.Equal(t, result.Coverage[0].ID, result.Coverage[1].SupersedesEventID,
		"the second supersedes the first written here, not a stale prior decision")

	st, err := f.dues.GetStanding(ctx, poster(), m, asOf, 30)
	require.NoError(t, err)
	assert.Equal(t, "2027-12-31", st.PaidThrough, "the later decision is the effective one")
}

// TestPostSupersedesPriorCoverage proves posting replaces the decision that was
// effective before, without erasing it.
func TestPostSupersedesPriorCoverage(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Renewing Member")
	_, err := f.db.Exec(`
		INSERT INTO coverage_events (membership_id, paid_through, reason_kind, decided_at)
		VALUES (?, '2025-12-31', 'legacy_import', '2025-01-01T00:00:00.000Z')`, m)
	require.NoError(t, err)

	b := f.open(t, "Renewal")
	_, afterAdd, err := f.svc.AddEntry(ctx, poster(), b.ID, entry(m, 4000, batches.MethodCash), "")
	require.NoError(t, err)

	result, err := f.svc.Post(ctx, poster(), b.ID, postParams(afterAdd.Version, "post-1"), time.Now())
	require.NoError(t, err)
	assert.NotZero(t, result.Coverage[0].SupersedesEventID)

	var total int
	require.NoError(t, f.db.QueryRow(
		`SELECT count(*) FROM coverage_events WHERE membership_id = ?`, m).Scan(&total))
	assert.Equal(t, 2, total, "the prior decision stays readable as history")
}

// TestPostIsIdempotent proves a retry returns the original result rather than
// posting the money a second time.
func TestPostIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Retried Payer")
	b := f.open(t, "Retry safety")
	_, afterAdd, err := f.svc.AddEntry(ctx, poster(), b.ID, entry(m, 4000, batches.MethodCash), "")
	require.NoError(t, err)

	first, err := f.svc.Post(ctx, poster(), b.ID, postParams(afterAdd.Version, "post-1"), time.Now())
	require.NoError(t, err)

	second, err := f.svc.Post(ctx, poster(), b.ID, postParams(afterAdd.Version, "post-1"), time.Now())
	require.NoError(t, err)
	require.Len(t, second.Payments, 1)
	assert.Equal(t, first.Payments[0].ID, second.Payments[0].ID)
	assert.Equal(t, first.Payments[0].ReceiptCode, second.Payments[0].ReceiptCode,
		"a reprint is the same receipt, not a new number")

	var payments, coverage int
	require.NoError(t, f.db.QueryRow(`SELECT count(*) FROM payments`).Scan(&payments))
	require.NoError(t, f.db.QueryRow(`SELECT count(*) FROM coverage_events`).Scan(&coverage))
	assert.Equal(t, 1, payments, "a retry posts no second payment")
	assert.Equal(t, 1, coverage)

	st, err := f.dues.GetStanding(ctx, poster(), m, asOf, 30)
	require.NoError(t, err)
	assert.Equal(t, dues.StatusCurrent, st.Status)
}

func TestPostWithReusedKeyAndDifferentBodyConflicts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Key Reuser")
	b := f.open(t, "Key reuse")
	_, afterAdd, err := f.svc.AddEntry(ctx, poster(), b.ID, entry(m, 4000, batches.MethodCash), "")
	require.NoError(t, err)

	_, err = f.svc.Post(ctx, poster(), b.ID, postParams(afterAdd.Version, "post-1"), time.Now())
	require.NoError(t, err)

	// Same key, different expected version: a different request wearing a
	// retry's clothes.
	_, err = f.svc.Post(ctx, poster(), b.ID, postParams(afterAdd.Version+5, "post-1"), time.Now())
	assert.ErrorIs(t, err, idem.ErrKeyReused)
}

// TestPostRejectsStaleBatch proves an edit made after the client last looked
// stops the post, writing nothing.
func TestPostRejectsStaleBatch(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Stale Payer")
	b := f.open(t, "Stale post")
	_, afterFirst, err := f.svc.AddEntry(ctx, poster(), b.ID, entry(m, 4000, batches.MethodCash), "")
	require.NoError(t, err)

	// Another officer adds a row, moving the batch version.
	_, _, err = f.svc.AddEntry(ctx, poster(), b.ID, entry(m, 1000, batches.MethodCash), "")
	require.NoError(t, err)

	_, err = f.svc.Post(ctx, poster(), b.ID, postParams(afterFirst.Version, "post-1"), time.Now())
	assert.ErrorIs(t, err, db.ErrStale)

	var payments, coverage int
	require.NoError(t, f.db.QueryRow(`SELECT count(*) FROM payments`).Scan(&payments))
	require.NoError(t, f.db.QueryRow(`SELECT count(*) FROM coverage_events`).Scan(&coverage))
	assert.Zero(t, payments, "a stale post writes nothing")
	assert.Zero(t, coverage)

	current, err := f.svc.Get(ctx, poster(), b.ID)
	require.NoError(t, err)
	assert.Equal(t, batches.StateOpen, current.State)
}

func TestPostValidation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Validated Payer")

	t.Run("an empty batch posts nothing", func(t *testing.T) {
		b := f.open(t, "Empty")
		_, err := f.svc.Post(ctx, poster(), b.ID, postParams(b.Version, "post-empty"), time.Now())
		assert.ErrorIs(t, err, batches.ErrEmptyBatch)
	})

	t.Run("confirmation is required", func(t *testing.T) {
		b := f.open(t, "Unconfirmed")
		_, after, err := f.svc.AddEntry(ctx, poster(), b.ID, entry(m, 4000, batches.MethodCash), "")
		require.NoError(t, err)
		_, err = f.svc.Post(ctx, poster(), b.ID, batches.PostParams{
			ExpectedVersion: after.Version, IdempotencyKey: "post-x", Confirm: false,
		}, time.Now())
		assert.ErrorIs(t, err, batches.ErrConfirmationRequired)
	})

	t.Run("an idempotency key is required", func(t *testing.T) {
		b := f.open(t, "Keyless")
		_, after, err := f.svc.AddEntry(ctx, poster(), b.ID, entry(m, 4000, batches.MethodCash), "")
		require.NoError(t, err)
		_, err = f.svc.Post(ctx, poster(), b.ID, batches.PostParams{
			ExpectedVersion: after.Version, Confirm: true,
		}, time.Now())
		assert.ErrorIs(t, err, batches.ErrIdempotencyKeyRequired)
	})

	t.Run("a posted batch cannot be posted again", func(t *testing.T) {
		b := f.open(t, "Twice posted")
		_, after, err := f.svc.AddEntry(ctx, poster(), b.ID, entry(m, 4000, batches.MethodCash), "")
		require.NoError(t, err)
		posted, err := f.svc.Post(ctx, poster(), b.ID, postParams(after.Version, "post-once"), time.Now())
		require.NoError(t, err)

		_, err = f.svc.Post(ctx, poster(), b.ID, postParams(posted.Batch.Version, "post-twice"), time.Now())
		assert.ErrorIs(t, err, batches.ErrBatchNotOpen)
	})

	t.Run("an abandoned batch cannot be posted", func(t *testing.T) {
		b := f.open(t, "Abandoned then posted")
		_, after, err := f.svc.AddEntry(ctx, poster(), b.ID, entry(m, 4000, batches.MethodCash), "")
		require.NoError(t, err)
		abandoned, err := f.svc.Abandon(ctx, poster(), b.ID, "Wrong sheet", after.Version, time.Now())
		require.NoError(t, err)

		_, err = f.svc.Post(ctx, poster(), b.ID, postParams(abandoned.Version, "post-abandoned"), time.Now())
		assert.ErrorIs(t, err, batches.ErrBatchNotOpen)
	})
}

// TestPostRequiresCapability proves the batch manager who drafted the rows
// cannot also post them without payment.post.
func TestPostRequiresCapability(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Guarded Payer")
	b := f.open(t, "Guarded post")
	_, after, err := f.svc.AddEntry(ctx, poster(), b.ID, entry(m, 4000, batches.MethodCash), "")
	require.NoError(t, err)

	drafter := &authz.Principal{UserID: 2, Capabilities: map[string]struct{}{
		"payment.read": {}, "payment.batch.manage": {},
	}}
	_, err = f.svc.Post(ctx, drafter, b.ID, postParams(after.Version, "post-1"), time.Now())
	assert.ErrorIs(t, err, authz.ErrDenied)

	_, err = f.svc.PostSinglePayment(ctx, drafter, batches.SingleParams{
		Entry: entry(m, 4000, batches.MethodCash), IdempotencyKey: "single-1", Confirm: true,
	}, time.Now())
	assert.ErrorIs(t, err, authz.ErrDenied)
}

// --- Single payment ---

// TestSinglePaymentUsesTheSamePrimitive proves a one-off payment lands in the
// ledger exactly as a batched one does, batch and all.
func TestSinglePaymentUsesTheSamePrimitive(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Single Payer")

	in := entry(m, 4000, batches.MethodCheck)
	in.Reference = "1042"
	result, err := f.svc.PostSinglePayment(ctx, poster(), batches.SingleParams{
		Entry: in, IdempotencyKey: "single-1", Confirm: true,
	}, time.Now())
	require.NoError(t, err)

	assert.Equal(t, batches.StatePosted, result.Batch.State, "the server-created batch is posted too")
	require.Len(t, result.Batch.Entries, 1)
	assert.Equal(t, int64(1), result.Batch.Entries[0].Sequence)
	require.Len(t, result.Payments, 1)
	require.Len(t, result.Coverage, 1)

	pay := result.Payments[0]
	assert.Equal(t, result.Batch.ID, pay.BatchID, "the payment is still traceable to a batch")
	assert.Equal(t, "1042", pay.Reference)
	assert.NotEmpty(t, pay.ReceiptCode)
	assert.Equal(t, "payment", result.Coverage[0].ReasonKind)

	st, err := f.dues.GetStanding(ctx, poster(), m, asOf, 30)
	require.NoError(t, err)
	assert.Equal(t, dues.StatusCurrent, st.Status)
	assert.Equal(t, "2026-12-31", st.PaidThrough)

	assert.Equal(t, int64(4000), result.Batch.Totals.NetTotalCents)
	assert.Equal(t, int64(1), result.Batch.Totals.CheckCount)
}

func TestSinglePaymentIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Single Retry")
	params := batches.SingleParams{
		Entry: entry(m, 4000, batches.MethodCash), IdempotencyKey: "single-1", Confirm: true,
	}

	first, err := f.svc.PostSinglePayment(ctx, poster(), params, time.Now())
	require.NoError(t, err)
	second, err := f.svc.PostSinglePayment(ctx, poster(), params, time.Now())
	require.NoError(t, err)

	assert.Equal(t, first.Payments[0].ID, second.Payments[0].ID)

	var payments, batchCount int
	require.NoError(t, f.db.QueryRow(`SELECT count(*) FROM payments`).Scan(&payments))
	require.NoError(t, f.db.QueryRow(`SELECT count(*) FROM payment_batches`).Scan(&batchCount))
	assert.Equal(t, 1, payments, "a retry posts no second payment")
	assert.Equal(t, 1, batchCount, "nor leaves an orphan batch behind")
}

func TestSinglePaymentValidation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Single Validated")

	t.Run("confirmation is required", func(t *testing.T) {
		_, err := f.svc.PostSinglePayment(ctx, poster(), batches.SingleParams{
			Entry: entry(m, 4000, batches.MethodCash), IdempotencyKey: "s1",
		}, time.Now())
		assert.ErrorIs(t, err, batches.ErrConfirmationRequired)
	})

	t.Run("a key is required", func(t *testing.T) {
		_, err := f.svc.PostSinglePayment(ctx, poster(), batches.SingleParams{
			Entry: entry(m, 4000, batches.MethodCash), Confirm: true,
		}, time.Now())
		assert.ErrorIs(t, err, batches.ErrIdempotencyKeyRequired)
	})

	t.Run("the row is validated", func(t *testing.T) {
		_, err := f.svc.PostSinglePayment(ctx, poster(), batches.SingleParams{
			Entry: entry(m, 0, batches.MethodCash), IdempotencyKey: "s2", Confirm: true,
		}, time.Now())
		assert.ErrorIs(t, err, batches.ErrInvalidAmount)
	})

	t.Run("a rejected payment leaves no batch behind", func(t *testing.T) {
		var batchCount int
		require.NoError(t, f.db.QueryRow(`SELECT count(*) FROM payment_batches`).Scan(&batchCount))
		assert.Zero(t, batchCount)
	})
}

// TestPostedPaymentsAreImmutable documents at the schema level what the API
// omits: there is no update path for a posted payment.
func TestPostedPaymentsAreImmutable(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Immutable Payer")

	result, err := f.svc.PostSinglePayment(ctx, poster(), batches.SingleParams{
		Entry: entry(m, 40000, batches.MethodCheck), IdempotencyKey: "single-1", Confirm: true,
	}, time.Now())
	require.NoError(t, err)

	// A zero amount is refused by the schema, so even a direct write cannot
	// quietly neutralise a posted row.
	_, err = f.db.Exec(`UPDATE payments SET amount_cents = 0 WHERE id = ?`, result.Payments[0].ID)
	assert.Error(t, err)

	// And the receipt code stays unique, so a second row cannot impersonate it.
	_, err = f.db.Exec(`
		INSERT INTO payments (membership_id, amount_cents, method, received_on,
			entered_by, entered_at, receipt_code, entry_kind)
		VALUES (?, 100, 'cash', '2026-01-15', 1, '2026-01-15T00:00:00.000Z', ?, 'original')`,
		m, result.Payments[0].ReceiptCode)
	assert.Error(t, err)
}
