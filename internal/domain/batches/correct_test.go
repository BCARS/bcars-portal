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

// corrector holds every capability the correction path needs.
func corrector() *authz.Principal {
	return &authz.Principal{UserID: 1, Capabilities: map[string]struct{}{
		"payment.read": {}, "payment.batch.manage": {}, "payment.post": {},
		"payment.correct": {}, "dues.read": {}, "coverage.read": {},
	}}
}

// correctParams builds a correction that restates every money field.
func correctParams(cents int64, paidThrough, reason, key string) batches.CorrectParams {
	return batches.CorrectParams{
		AmountCents: cents, Method: batches.MethodCheck, ReceivedOn: "2026-01-15",
		PaidThrough: paidThrough, Reason: reason, IdempotencyKey: key, Confirm: true,
	}
}

// postOne posts a one-row batch and returns the payment.
func (f *fixture) postOne(t *testing.T, membershipID, cents int64, method string) batches.Payment {
	t.Helper()
	in := entry(membershipID, cents, method)
	result, err := f.svc.PostSinglePayment(context.Background(), corrector(), batches.SingleParams{
		Entry: in, IdempotencyKey: "seed-" + time.Now().Format("150405.000000000"), Confirm: true,
	}, time.Now())
	require.NoError(t, err)
	return result.Payments[0]
}

// TestCorrect400To40 is the reference case from the epic: a $400 mistype in a
// $510 batch becomes $40, and the batch's net ledger total becomes $150 without
// the original ever being touched.
func TestCorrect400To40(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	wrong := f.member(t, "Mistyped Member")
	other := f.member(t, "Correct Member")

	b := f.open(t, "Meeting night")
	// $400 typed where $40 was meant, plus $110 of other rows: $510 in all.
	_, _, err := f.svc.AddEntry(ctx, corrector(), b.ID, entry(wrong, 40000, batches.MethodCheck), "")
	require.NoError(t, err)
	_, _, err = f.svc.AddEntry(ctx, corrector(), b.ID, entry(other, 7000, batches.MethodCash), "")
	require.NoError(t, err)
	_, afterAdd, err := f.svc.AddEntry(ctx, corrector(), b.ID, entry(other, 4000, batches.MethodCash), "")
	require.NoError(t, err)

	posted, err := f.svc.Post(ctx, corrector(), b.ID, postParams(afterAdd.Version, "post-1"), time.Now())
	require.NoError(t, err)
	require.Equal(t, int64(51000), posted.Batch.Totals.NetTotalCents, "the batch as typed")

	original := posted.Payments[0]
	require.Equal(t, int64(40000), original.AmountCents)

	result, err := f.svc.CorrectPayment(ctx, corrector(), original.ID,
		correctParams(4000, "2026-12-31", "Typed 400 instead of 40", "correct-1"), time.Now())
	require.NoError(t, err)

	assert.Equal(t, int64(15000), result.LedgerTotals.NetTotalCents,
		"$510 batch corrected from $400 to $40 nets $150")
	assert.Equal(t, int64(4000), result.Effective.AmountCents)
	assert.Equal(t, "replacement", result.Effective.EntryKind)

	// The original is exactly where it was.
	var originalAmount int64
	require.NoError(t, f.db.QueryRow(
		`SELECT amount_cents FROM payments WHERE id = ?`, original.ID).Scan(&originalAmount))
	assert.Equal(t, int64(40000), originalAmount, "the original is never overwritten")

	require.Len(t, result.Chain, 3, "original, reversal, replacement")
	assert.Equal(t, int64(40000), result.Chain[0].AmountCents)
	assert.Equal(t, int64(-40000), result.Chain[1].AmountCents, "the reversal is signed")
	assert.Equal(t, "reversal", result.Chain[1].EntryKind)
	assert.Equal(t, int64(4000), result.Chain[2].AmountCents)

	require.Len(t, result.Corrections, 1)
	assert.Equal(t, "Typed 400 instead of 40", result.Corrections[0].Reason)
	assert.Equal(t, int64(1), result.Corrections[0].CorrectedByUserID)
	assert.NotEmpty(t, result.Corrections[0].CorrectedAt)

	// The draft entries still say what was typed on the night. That divergence
	// is the point: the correction is visible rather than tidied away.
	assert.Equal(t, int64(51000), result.Batch.Totals.NetTotalCents)
}

// TestCorrectAmountLeavesCoverageAlone proves a money-only correction says
// nothing about how long the member is covered.
func TestCorrectAmountLeavesCoverageAlone(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Amount Only")
	original := f.postOne(t, m, 40000, batches.MethodCheck)

	before, err := f.dues.GetStanding(ctx, corrector(), m, asOf, 30)
	require.NoError(t, err)
	require.Equal(t, "2026-12-31", before.PaidThrough)

	var coverageBefore int
	require.NoError(t, f.db.QueryRow(`SELECT count(*) FROM coverage_events`).Scan(&coverageBefore))

	result, err := f.svc.CorrectPayment(ctx, corrector(), original.ID,
		correctParams(4000, "2026-12-31", "Amount only", "correct-1"), time.Now())
	require.NoError(t, err)

	assert.Nil(t, result.Coverage, "no new coverage event when the date is unchanged")
	assert.Equal(t, "2026-12-31", result.PaidThrough)

	var coverageAfter int
	require.NoError(t, f.db.QueryRow(`SELECT count(*) FROM coverage_events`).Scan(&coverageAfter))
	assert.Equal(t, coverageBefore, coverageAfter, "coverage history is untouched")

	after, err := f.dues.GetStanding(ctx, corrector(), m, asOf, 30)
	require.NoError(t, err)
	assert.Equal(t, dues.StatusCurrent, after.Status)
	assert.Equal(t, "2026-12-31", after.PaidThrough)
}

// TestCorrectPaidThroughSupersedesCoverage proves changing the date appends a
// superseding decision rather than editing the old one.
func TestCorrectPaidThroughSupersedesCoverage(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Date Changed")
	original := f.postOne(t, m, 4000, batches.MethodCash)

	result, err := f.svc.CorrectPayment(ctx, corrector(), original.ID,
		correctParams(4000, "2027-12-31", "Paid for two years, not one", "correct-1"), time.Now())
	require.NoError(t, err)

	require.NotNil(t, result.Coverage, "a changed date appends a coverage event")
	assert.Equal(t, "correction", result.Coverage.ReasonKind)
	assert.Equal(t, "2027-12-31", result.Coverage.PaidThrough)
	assert.NotZero(t, result.Coverage.SupersedesEventID)
	assert.Equal(t, result.Effective.ID, result.Coverage.PaymentID)
	assert.Equal(t, "2027-12-31", result.PaidThrough)

	events, err := f.dues.ListCoverageEvents(ctx, corrector(), m, 50, 0)
	require.NoError(t, err)
	assert.Len(t, events, 2, "the superseded decision stays readable")

	st, err := f.dues.GetStanding(ctx, corrector(), m, asOf, 30)
	require.NoError(t, err)
	assert.Equal(t, "2027-12-31", st.PaidThrough)
}

// TestRepeatedCorrectionTargetsTheReplacement proves a chain keeps growing and
// that the earlier rows all survive.
func TestRepeatedCorrectionTargetsTheReplacement(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Twice Corrected")
	original := f.postOne(t, m, 40000, batches.MethodCheck)

	first, err := f.svc.CorrectPayment(ctx, corrector(), original.ID,
		correctParams(4000, "2026-12-31", "First fix", "correct-1"), time.Now())
	require.NoError(t, err)

	t.Run("correcting the superseded original is refused", func(t *testing.T) {
		_, err := f.svc.CorrectPayment(ctx, corrector(), original.ID,
			correctParams(5000, "2026-12-31", "Wrong target", "correct-x"), time.Now())
		assert.ErrorIs(t, err, db.ErrStale, "the chain has moved on")
	})

	second, err := f.svc.CorrectPayment(ctx, corrector(), first.Effective.ID,
		batches.CorrectParams{
			AmountCents: 5000, Method: batches.MethodCash, ReceivedOn: "2026-01-15",
			PaidThrough: "2026-12-31", Reason: "Second fix: it was cash",
			ExpectedRevision: 1, IdempotencyKey: "correct-2", Confirm: true,
		}, time.Now())
	require.NoError(t, err)

	assert.Equal(t, int64(5000), second.Effective.AmountCents)
	assert.Equal(t, batches.MethodCash, second.Effective.Method)
	require.Len(t, second.Corrections, 2, "both corrections are recorded")
	require.Len(t, second.Chain, 5, "original plus two reversal/replacement pairs")

	// Every row in the chain still exists, and the net is the last replacement.
	var netForMember int64
	require.NoError(t, f.db.QueryRow(
		`SELECT COALESCE(SUM(amount_cents), 0) FROM payments WHERE membership_id = ?`,
		m).Scan(&netForMember))
	assert.Equal(t, int64(5000), netForMember,
		"reversals cancel their originals, leaving only what is in force")
}

// TestCorrectRequiresCurrentRevision proves a client working from a stale view
// of the chain is refused.
func TestCorrectRequiresCurrentRevision(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Revision Guarded")
	original := f.postOne(t, m, 40000, batches.MethodCheck)

	revision, err := f.svc.ChainRevision(ctx, corrector(), original.ID)
	require.NoError(t, err)
	assert.Zero(t, revision, "an uncorrected chain is revision 0")

	stale := correctParams(4000, "2026-12-31", "Stale", "correct-stale")
	stale.ExpectedRevision = 3
	_, err = f.svc.CorrectPayment(ctx, corrector(), original.ID, stale, time.Now())
	assert.ErrorIs(t, err, db.ErrStale)

	var payments int
	require.NoError(t, f.db.QueryRow(`SELECT count(*) FROM payments`).Scan(&payments))
	assert.Equal(t, 1, payments, "a refused correction writes nothing")

	_, err = f.svc.CorrectPayment(ctx, corrector(), original.ID,
		correctParams(4000, "2026-12-31", "Correct revision", "correct-ok"), time.Now())
	require.NoError(t, err)

	revision, err = f.svc.ChainRevision(ctx, corrector(), original.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), revision, "the chain revision moves with the correction")
}

// TestCorrectIsIdempotent proves a retried correction does not append a second
// reversal and replacement.
func TestCorrectIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Retried Correction")
	original := f.postOne(t, m, 40000, batches.MethodCheck)
	params := correctParams(4000, "2026-12-31", "Typed 400 instead of 40", "correct-1")

	first, err := f.svc.CorrectPayment(ctx, corrector(), original.ID, params, time.Now())
	require.NoError(t, err)
	second, err := f.svc.CorrectPayment(ctx, corrector(), original.ID, params, time.Now())
	require.NoError(t, err)

	assert.Equal(t, first.Effective.ID, second.Effective.ID)
	assert.Equal(t, first.LedgerTotals.NetTotalCents, second.LedgerTotals.NetTotalCents)

	var payments, corrections int
	require.NoError(t, f.db.QueryRow(`SELECT count(*) FROM payments`).Scan(&payments))
	require.NoError(t, f.db.QueryRow(`SELECT count(*) FROM payment_corrections`).Scan(&corrections))
	assert.Equal(t, 3, payments, "a retry appends no second pair")
	assert.Equal(t, 1, corrections)

	t.Run("the same key with different values is refused", func(t *testing.T) {
		other := correctParams(9999, "2026-12-31", "Different", "correct-1")
		_, err := f.svc.CorrectPayment(ctx, corrector(), original.ID, other, time.Now())
		assert.ErrorIs(t, err, idem.ErrKeyReused)
	})
}

func TestCorrectValidation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Validated Correction")
	original := f.postOne(t, m, 40000, batches.MethodCheck)

	t.Run("a reason is required", func(t *testing.T) {
		_, err := f.svc.CorrectPayment(ctx, corrector(), original.ID,
			correctParams(4000, "2026-12-31", "   ", "c1"), time.Now())
		assert.ErrorIs(t, err, batches.ErrReasonRequired)
	})

	t.Run("confirmation is required", func(t *testing.T) {
		params := correctParams(4000, "2026-12-31", "No confirm", "c2")
		params.Confirm = false
		_, err := f.svc.CorrectPayment(ctx, corrector(), original.ID, params, time.Now())
		assert.ErrorIs(t, err, batches.ErrConfirmationRequired)
	})

	t.Run("an idempotency key is required", func(t *testing.T) {
		params := correctParams(4000, "2026-12-31", "No key", "")
		_, err := f.svc.CorrectPayment(ctx, corrector(), original.ID, params, time.Now())
		assert.ErrorIs(t, err, batches.ErrIdempotencyKeyRequired)
	})

	t.Run("the replacement amount must be positive", func(t *testing.T) {
		_, err := f.svc.CorrectPayment(ctx, corrector(), original.ID,
			correctParams(0, "2026-12-31", "Zero", "c3"), time.Now())
		assert.ErrorIs(t, err, batches.ErrInvalidAmount)
	})

	t.Run("an off-cycle paid-through is accepted", func(t *testing.T) {
		result, err := f.svc.CorrectPayment(ctx, corrector(), original.ID,
			correctParams(4000, "2026-06-30", "Prorated by agreement", "c4"), time.Now())
		require.NoError(t, err)
		assert.Equal(t, "2026-06-30", result.PaidThrough)
	})

	t.Run("nothing was written by any refused correction", func(t *testing.T) {
		var corrections int
		require.NoError(t, f.db.QueryRow(`SELECT count(*) FROM payment_corrections`).Scan(&corrections))
		assert.Equal(t, 1, corrections, "only the accepted one landed")
	})
}

// TestCorrectARversalIsRefused proves the bookkeeping row is not itself a
// payment anyone can restate.
func TestCorrectARversalIsRefused(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Reversal Target")
	original := f.postOne(t, m, 40000, batches.MethodCheck)

	result, err := f.svc.CorrectPayment(ctx, corrector(), original.ID,
		correctParams(4000, "2026-12-31", "First fix", "c1"), time.Now())
	require.NoError(t, err)

	var reversalID int64
	for _, p := range result.Chain {
		if p.EntryKind == "reversal" {
			reversalID = p.ID
		}
	}
	require.NotZero(t, reversalID)

	_, err = f.svc.CorrectPayment(ctx, corrector(), reversalID,
		correctParams(100, "2026-12-31", "Nope", "c2"), time.Now())
	assert.ErrorIs(t, err, batches.ErrNotAnOriginalOrReplacement)
}

// TestCorrectRequiresCapability proves posting does not imply correcting.
func TestCorrectRequiresCapability(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Guarded Correction")
	original := f.postOne(t, m, 40000, batches.MethodCheck)

	poster := &authz.Principal{UserID: 2, Capabilities: map[string]struct{}{
		"payment.read": {}, "payment.post": {}, "payment.batch.manage": {},
	}}
	_, err := f.svc.CorrectPayment(ctx, poster, original.ID,
		correctParams(4000, "2026-12-31", "Not allowed", "c1"), time.Now())
	assert.ErrorIs(t, err, authz.ErrDenied)

	reader := &authz.Principal{UserID: 3, Capabilities: map[string]struct{}{"dues.read": {}}}
	_, err = f.svc.GetPaymentChain(ctx, reader, original.ID)
	assert.ErrorIs(t, err, authz.ErrDenied, "dues.read does not open payment detail")
}

// TestPaymentChainReadIsStable proves reading a chain changes nothing and
// reports the revision a correction must present.
func TestPaymentChainReadIsStable(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Chain Reader")
	original := f.postOne(t, m, 40000, batches.MethodCheck)

	chain, err := f.svc.GetPaymentChain(ctx, corrector(), original.ID)
	require.NoError(t, err)
	require.Len(t, chain.Chain, 1)
	assert.Empty(t, chain.Corrections)
	assert.Equal(t, int64(40000), chain.LedgerTotals.NetTotalCents)
	assert.Equal(t, "2026-12-31", chain.PaidThrough)

	_, err = f.svc.CorrectPayment(ctx, corrector(), original.ID,
		correctParams(4000, "2026-12-31", "Fix", "c1"), time.Now())
	require.NoError(t, err)

	// Reading from any point in the chain gives the same effective payment.
	for _, id := range []int64{original.ID, chain.Effective.ID} {
		got, err := f.svc.GetPaymentChain(ctx, corrector(), id)
		require.NoError(t, err)
		assert.Equal(t, int64(4000), got.Effective.AmountCents)
		assert.Equal(t, int64(4000), got.LedgerTotals.NetTotalCents)
	}
}
