package batches_test

import (
	"context"
	"database/sql"
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

var asOf = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

func treasurer() *authz.Principal {
	return &authz.Principal{UserID: 1, Capabilities: map[string]struct{}{
		"payment.read": {}, "payment.batch.manage": {}, "dues.read": {},
	}}
}

// reader holds payment.read but cannot change a batch.
func reader() *authz.Principal {
	return &authz.Principal{UserID: 2, Capabilities: map[string]struct{}{"payment.read": {}}}
}

// outsider holds member access but no treasury capability.
func outsider() *authz.Principal {
	return &authz.Principal{UserID: 3, Capabilities: map[string]struct{}{"member.read": {}}}
}

type fixture struct {
	svc  *batches.Service
	dues *dues.Service
	db   *sql.DB
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	d, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	require.NoError(t, db.Migrate(d))

	_, err = d.Exec(`INSERT INTO users (email) VALUES ('treasurer@example.test')`)
	require.NoError(t, err)

	return &fixture{svc: batches.NewService(d), dues: dues.NewService(d), db: d}
}

func (f *fixture) member(t *testing.T, name string) int64 {
	t.Helper()
	res, err := f.db.Exec(`INSERT INTO persons (display_name, sort_name) VALUES (?, ?)`, name, name)
	require.NoError(t, err)
	personID, err := res.LastInsertId()
	require.NoError(t, err)

	res, err = f.db.Exec(
		`INSERT INTO memberships (person_id, base_type, lifecycle) VALUES (?, 'full', 'approved')`,
		personID)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

func (f *fixture) open(t *testing.T, label string) batches.Batch {
	t.Helper()
	b, err := f.svc.Open(context.Background(), treasurer(), batches.OpenParams{
		Label: label, DefaultAmountCents: 4000, DefaultPaidThrough: "2026-12-31",
	}, time.Now())
	require.NoError(t, err)
	return b
}

func entry(membershipID int64, cents int64, method string) batches.EntryInput {
	return batches.EntryInput{
		MembershipID: membershipID,
		AmountCents:  cents,
		Method:       method,
		ReceivedOn:   "2026-01-15",
		PaidThrough:  "2026-12-31",
	}
}

// TestDraftIsolation is the central property of this package: a fully populated
// draft batch writes no payment, no coverage event, and moves nobody's dues
// standing until it is posted.
func TestDraftIsolation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Draft Member")

	before, err := f.dues.GetStanding(ctx, treasurer(), m, asOf, 30)
	require.NoError(t, err)
	require.Equal(t, dues.StatusUnknown, before.Status)

	b := f.open(t, "January dues")
	_, _, err = f.svc.AddEntry(ctx, treasurer(), b.ID, entry(m, 4000, batches.MethodCash), "")
	require.NoError(t, err)
	_, _, err = f.svc.AddEntry(ctx, treasurer(), b.ID, entry(m, 4000, batches.MethodCheck), "")
	require.NoError(t, err)

	var payments, coverage int
	require.NoError(t, f.db.QueryRow(`SELECT count(*) FROM payments`).Scan(&payments))
	require.NoError(t, f.db.QueryRow(`SELECT count(*) FROM coverage_events`).Scan(&coverage))
	assert.Zero(t, payments, "a draft batch creates no payment")
	assert.Zero(t, coverage, "a draft batch creates no coverage event")

	after, err := f.dues.GetStanding(ctx, treasurer(), m, asOf, 30)
	require.NoError(t, err)
	assert.Equal(t, dues.StatusUnknown, after.Status, "drafting must not change dues standing")
	assert.Empty(t, after.PaidThrough)
}

// TestServerCalculatedTotals proves the server adds up the batch, broken down
// by method, and that a client never supplies a total.
func TestServerCalculatedTotals(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := f.member(t, "Alpha Member")
	c := f.member(t, "Charlie Member")
	b := f.open(t, "Mixed methods")

	for _, e := range []batches.EntryInput{
		entry(a, 4000, batches.MethodCash),
		entry(c, 2500, batches.MethodCash),
		entry(a, 10000, batches.MethodCheck),
		entry(c, 750, batches.MethodOther),
	} {
		_, _, err := f.svc.AddEntry(ctx, treasurer(), b.ID, e, "")
		require.NoError(t, err)
	}

	got, err := f.svc.Get(ctx, treasurer(), b.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(4), got.Totals.EntryCount)
	assert.Equal(t, int64(2), got.Totals.CashCount)
	assert.Equal(t, int64(6500), got.Totals.CashTotalCents)
	assert.Equal(t, int64(1), got.Totals.CheckCount)
	assert.Equal(t, int64(10000), got.Totals.CheckTotalCents)
	assert.Equal(t, int64(1), got.Totals.OtherCount)
	assert.Equal(t, int64(750), got.Totals.OtherTotalCents)
	assert.Equal(t, int64(17250), got.Totals.NetTotalCents)
}

// TestEveryEntryMutationMovesTheBatchVersion is what makes a later stale post
// detectable: the batch ETag must change whenever its rows do.
func TestEveryEntryMutationMovesTheBatchVersion(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Versioned Member")
	b := f.open(t, "Version tracking")
	start := b.Version

	e, afterAdd, err := f.svc.AddEntry(ctx, treasurer(), b.ID, entry(m, 4000, batches.MethodCash), "")
	require.NoError(t, err)
	assert.Greater(t, afterAdd.Version, start, "adding a row moves the batch version")

	edited := entry(m, 5000, batches.MethodCheck)
	edited.Reference = "1042"
	_, afterEdit, err := f.svc.UpdateEntry(ctx, treasurer(), b.ID, e.ID, edited, e.Version)
	require.NoError(t, err)
	assert.Greater(t, afterEdit.Version, afterAdd.Version, "editing a row moves the batch version")

	afterDelete, err := f.svc.DeleteEntry(ctx, treasurer(), b.ID, e.ID, e.Version+1)
	require.NoError(t, err)
	assert.Greater(t, afterDelete.Version, afterEdit.Version, "removing a row moves the batch version")
	assert.Zero(t, afterDelete.Totals.EntryCount)
}

func TestEntryStaleWriteIsRejected(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Contended Member")
	b := f.open(t, "Concurrent editing")

	e, _, err := f.svc.AddEntry(ctx, treasurer(), b.ID, entry(m, 4000, batches.MethodCash), "")
	require.NoError(t, err)

	_, _, err = f.svc.UpdateEntry(ctx, treasurer(), b.ID, e.ID, entry(m, 9000, batches.MethodCash), e.Version)
	require.NoError(t, err)

	// A second officer still holding the original version loses.
	_, _, err = f.svc.UpdateEntry(ctx, treasurer(), b.ID, e.ID, entry(m, 1000, batches.MethodCash), e.Version)
	assert.ErrorIs(t, err, db.ErrStale)

	_, err = f.svc.DeleteEntry(ctx, treasurer(), b.ID, e.ID, e.Version)
	assert.ErrorIs(t, err, db.ErrStale)
}

// TestSequenceIsStable proves removing a row does not renumber the others, so a
// printed sheet and the grid keep agreeing.
func TestSequenceIsStable(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Sequenced Member")
	b := f.open(t, "Stable order")

	var ids []int64
	var versions []int64
	for i := 0; i < 3; i++ {
		e, _, err := f.svc.AddEntry(ctx, treasurer(), b.ID, entry(m, 1000, batches.MethodCash), "")
		require.NoError(t, err)
		assert.Equal(t, int64(i+1), e.Sequence)
		ids = append(ids, e.ID)
		versions = append(versions, e.Version)
	}

	_, err := f.svc.DeleteEntry(ctx, treasurer(), b.ID, ids[1], versions[1])
	require.NoError(t, err)

	got, err := f.svc.Get(ctx, treasurer(), b.ID)
	require.NoError(t, err)
	require.Len(t, got.Entries, 2)
	assert.Equal(t, int64(1), got.Entries[0].Sequence)
	assert.Equal(t, int64(3), got.Entries[1].Sequence, "row 3 keeps its number")

	// The next row continues past the gap rather than reusing 2.
	e, _, err := f.svc.AddEntry(ctx, treasurer(), b.ID, entry(m, 1000, batches.MethodCash), "")
	require.NoError(t, err)
	assert.Equal(t, int64(4), e.Sequence)
}

// TestIdempotentEntryCreate proves a retried add does not duplicate the row.
func TestIdempotentEntryCreate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Retried Member")
	b := f.open(t, "Retry safety")
	in := entry(m, 4000, batches.MethodCash)

	first, _, err := f.svc.AddEntry(ctx, treasurer(), b.ID, in, "key-1")
	require.NoError(t, err)

	second, batch, err := f.svc.AddEntry(ctx, treasurer(), b.ID, in, "key-1")
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID, "a retry returns the original row")
	assert.Equal(t, int64(1), batch.Totals.EntryCount, "and does not add a second one")

	t.Run("the same key with a different body is refused", func(t *testing.T) {
		other := entry(m, 9999, batches.MethodCash)
		_, _, err := f.svc.AddEntry(ctx, treasurer(), b.ID, other, "key-1")
		assert.ErrorIs(t, err, idem.ErrKeyReused)
	})

	t.Run("a different key adds a real second row", func(t *testing.T) {
		_, batch, err := f.svc.AddEntry(ctx, treasurer(), b.ID, in, "key-2")
		require.NoError(t, err)
		assert.Equal(t, int64(2), batch.Totals.EntryCount)
	})
}

// TestIdempotentBatchOpen proves a retried open does not leave two batches.
func TestIdempotentBatchOpen(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	params := batches.OpenParams{Label: "February dues", IdempotencyKey: "open-1"}

	first, err := f.svc.Open(ctx, treasurer(), params, time.Now())
	require.NoError(t, err)
	second, err := f.svc.Open(ctx, treasurer(), params, time.Now())
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)

	all, err := f.svc.List(ctx, treasurer(), "", 50, 0)
	require.NoError(t, err)
	assert.Len(t, all, 1)
}

// TestTerminalBatchRejectsEveryMutation walks each mutation against a batch
// that has been abandoned, and again against one that has been posted.
func TestTerminalBatchRejectsEveryMutation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Terminal Member")

	for _, tc := range []struct {
		name string
		make func(t *testing.T) (batchID, entryID, entryVersion int64)
	}{
		{"abandoned", func(t *testing.T) (int64, int64, int64) {
			b := f.open(t, "To abandon")
			e, after, err := f.svc.AddEntry(ctx, treasurer(), b.ID, entry(m, 4000, batches.MethodCash), "")
			require.NoError(t, err)
			_, err = f.svc.Abandon(ctx, treasurer(), b.ID, "Duplicate of the paper sheet", after.Version, time.Now())
			require.NoError(t, err)
			return b.ID, e.ID, e.Version
		}},
		{"posted", func(t *testing.T) (int64, int64, int64) {
			b := f.open(t, "To post")
			e, _, err := f.svc.AddEntry(ctx, treasurer(), b.ID, entry(m, 4000, batches.MethodCash), "")
			require.NoError(t, err)
			// pma.4 owns real posting; this stands in for its end state.
			_, err = f.db.Exec(`
				UPDATE payment_batches
				SET state = 'posted', posted_by = 1, posted_at = '2026-02-01T00:00:00.000Z'
				WHERE id = ?`, b.ID)
			require.NoError(t, err)
			return b.ID, e.ID, e.Version
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			batchID, entryID, entryVersion := tc.make(t)
			current, err := f.svc.Get(ctx, treasurer(), batchID)
			require.NoError(t, err)

			_, err = f.svc.Update(ctx, treasurer(), batchID, batches.UpdateParams{
				Label: "Renamed", ExpectedVersion: current.Version,
			})
			assert.ErrorIs(t, err, batches.ErrBatchNotOpen, "defaults")

			_, _, err = f.svc.AddEntry(ctx, treasurer(), batchID, entry(m, 100, batches.MethodCash), "")
			assert.ErrorIs(t, err, batches.ErrBatchNotOpen, "add")

			_, _, err = f.svc.UpdateEntry(ctx, treasurer(), batchID, entryID,
				entry(m, 100, batches.MethodCash), entryVersion)
			assert.ErrorIs(t, err, batches.ErrBatchNotOpen, "edit")

			_, err = f.svc.DeleteEntry(ctx, treasurer(), batchID, entryID, entryVersion)
			assert.ErrorIs(t, err, batches.ErrBatchNotOpen, "remove")

			_, err = f.svc.Abandon(ctx, treasurer(), batchID, "Again", current.Version, time.Now())
			assert.ErrorIs(t, err, batches.ErrBatchNotOpen, "abandon")
		})
	}
}

// TestAbandonKeepsTheRecord proves abandonment is audited in the data itself:
// the reason, the actor, and the rows all survive.
func TestAbandonKeepsTheRecord(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Abandoned Member")
	b := f.open(t, "Abandoned batch")
	_, after, err := f.svc.AddEntry(ctx, treasurer(), b.ID, entry(m, 4000, batches.MethodCash), "")
	require.NoError(t, err)

	done, err := f.svc.Abandon(ctx, treasurer(), b.ID,
		"Entered twice; keeping the other batch", after.Version, time.Now())
	require.NoError(t, err)
	assert.Equal(t, batches.StateAbandoned, done.State)
	assert.Equal(t, "Entered twice; keeping the other batch", done.AbandonReason)
	assert.Equal(t, int64(1), done.AbandonedByUserID)
	assert.NotEmpty(t, done.AbandonedAt)
	assert.Len(t, done.Entries, 1, "the abandoned rows stay readable")

	t.Run("a reason is required", func(t *testing.T) {
		other := f.open(t, "Needs a reason")
		_, err := f.svc.Abandon(ctx, treasurer(), other.ID, "  ", other.Version, time.Now())
		assert.ErrorIs(t, err, batches.ErrReasonRequired)
	})
}

// TestOpenAndResume covers the "save and finish later" property: an open batch
// needs no explicit save, and its defaults persist for the next row.
func TestOpenAndResume(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Resumed Member")

	b := f.open(t, "Monday night")
	assert.Equal(t, batches.StateOpen, b.State)
	assert.Equal(t, int64(4000), b.DefaultAmountCents)
	assert.Equal(t, "2026-12-31", b.DefaultPaidThrough)

	_, after, err := f.svc.AddEntry(ctx, treasurer(), b.ID, entry(m, 4000, batches.MethodCash), "")
	require.NoError(t, err)

	updated, err := f.svc.Update(ctx, treasurer(), b.ID, batches.UpdateParams{
		Label:              "Monday night, continued",
		DefaultAmountCents: 5000,
		DefaultPaidThrough: "2027-12-31",
		ExpectedVersion:    after.Version,
	})
	require.NoError(t, err)
	assert.Equal(t, "Monday night, continued", updated.Label)
	assert.Equal(t, int64(5000), updated.DefaultAmountCents)

	// Resuming later finds the same batch, rows intact.
	resumed, err := f.svc.Get(ctx, treasurer(), b.ID)
	require.NoError(t, err)
	assert.Equal(t, batches.StateOpen, resumed.State)
	assert.Len(t, resumed.Entries, 1)

	t.Run("a stale default change is rejected", func(t *testing.T) {
		_, err := f.svc.Update(ctx, treasurer(), b.ID, batches.UpdateParams{
			Label: "Stale", ExpectedVersion: after.Version,
		})
		assert.ErrorIs(t, err, db.ErrStale)
	})
}

// TestDefaultsAreNotSubstituted proves the server never fills a row from the
// batch defaults. Defaults prefill the client's form; the row carries explicit
// values, so a typed value is never silently replaced.
func TestDefaultsAreNotSubstituted(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Explicit Member")
	b := f.open(t, "Explicit rows") // defaults: 4000, 2026-12-31

	explicit := entry(m, 1500, batches.MethodCash)
	explicit.PaidThrough = "2027-12-31"
	e, _, err := f.svc.AddEntry(ctx, treasurer(), b.ID, explicit, "")
	require.NoError(t, err)
	assert.Equal(t, int64(1500), e.AmountCents, "the typed amount wins over the default")
	assert.Equal(t, "2027-12-31", e.PaidThrough, "the typed date wins over the default")

	t.Run("an incomplete row is refused rather than defaulted", func(t *testing.T) {
		missing := batches.EntryInput{
			MembershipID: m, Method: batches.MethodCash, ReceivedOn: "2026-01-15",
			PaidThrough: "2026-12-31",
		}
		_, _, err := f.svc.AddEntry(ctx, treasurer(), b.ID, missing, "")
		assert.ErrorIs(t, err, batches.ErrInvalidAmount)
	})
}

func TestEntryValidation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Validated Member")
	b := f.open(t, "Validation")

	t.Run("amount must be positive", func(t *testing.T) {
		_, _, err := f.svc.AddEntry(ctx, treasurer(), b.ID, entry(m, 0, batches.MethodCash), "")
		assert.ErrorIs(t, err, batches.ErrInvalidAmount)
		_, _, err = f.svc.AddEntry(ctx, treasurer(), b.ID, entry(m, -100, batches.MethodCash), "")
		assert.ErrorIs(t, err, batches.ErrInvalidAmount)
	})

	t.Run("method must be known", func(t *testing.T) {
		_, _, err := f.svc.AddEntry(ctx, treasurer(), b.ID, entry(m, 4000, "venmo"), "")
		assert.ErrorIs(t, err, batches.ErrInvalidMethod)
	})

	t.Run("dates must be ISO", func(t *testing.T) {
		bad := entry(m, 4000, batches.MethodCash)
		bad.ReceivedOn = "01/15/2026"
		_, _, err := f.svc.AddEntry(ctx, treasurer(), b.ID, bad, "")
		assert.ErrorIs(t, err, batches.ErrInvalidDate)
	})

	t.Run("an off-cycle paid-through is accepted", func(t *testing.T) {
		offCycle := entry(m, 4000, batches.MethodCash)
		offCycle.PaidThrough = "2026-06-30"
		e, _, err := f.svc.AddEntry(ctx, treasurer(), b.ID, offCycle, "")
		require.NoError(t, err)
		assert.Equal(t, "2026-06-30", e.PaidThrough)
	})

	t.Run("the membership must exist", func(t *testing.T) {
		_, _, err := f.svc.AddEntry(ctx, treasurer(), b.ID, entry(999, 4000, batches.MethodCash), "")
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("an entry from another batch is not found here", func(t *testing.T) {
		other := f.open(t, "Other batch")
		e, _, err := f.svc.AddEntry(ctx, treasurer(), other.ID, entry(m, 4000, batches.MethodCash), "")
		require.NoError(t, err)

		_, _, err = f.svc.UpdateEntry(ctx, treasurer(), b.ID, e.ID,
			entry(m, 4000, batches.MethodCash), e.Version)
		assert.ErrorIs(t, err, batches.ErrEntryNotInBatch)
	})
}

// TestAuthorization proves payment.read cannot change a batch and that a caller
// with neither capability sees nothing at all.
func TestAuthorization(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Guarded Member")
	b := f.open(t, "Guarded batch")
	e, _, err := f.svc.AddEntry(ctx, treasurer(), b.ID, entry(m, 4000, batches.MethodCash), "")
	require.NoError(t, err)

	t.Run("payment.read may read", func(t *testing.T) {
		got, err := f.svc.Get(ctx, reader(), b.ID)
		require.NoError(t, err)
		assert.Len(t, got.Entries, 1)
		_, err = f.svc.List(ctx, reader(), "", 50, 0)
		assert.NoError(t, err)
	})

	t.Run("payment.read may not change anything", func(t *testing.T) {
		_, err := f.svc.Open(ctx, reader(), batches.OpenParams{Label: "Nope"}, time.Now())
		assert.ErrorIs(t, err, authz.ErrDenied)
		_, _, err = f.svc.AddEntry(ctx, reader(), b.ID, entry(m, 100, batches.MethodCash), "")
		assert.ErrorIs(t, err, authz.ErrDenied)
		_, _, err = f.svc.UpdateEntry(ctx, reader(), b.ID, e.ID, entry(m, 100, batches.MethodCash), e.Version)
		assert.ErrorIs(t, err, authz.ErrDenied)
		_, err = f.svc.DeleteEntry(ctx, reader(), b.ID, e.ID, e.Version)
		assert.ErrorIs(t, err, authz.ErrDenied)
		_, err = f.svc.Abandon(ctx, reader(), b.ID, "Nope", b.Version, time.Now())
		assert.ErrorIs(t, err, authz.ErrDenied)
		_, err = f.svc.Update(ctx, reader(), b.ID, batches.UpdateParams{Label: "Nope", ExpectedVersion: b.Version})
		assert.ErrorIs(t, err, authz.ErrDenied)
	})

	t.Run("a member reader sees no batch at all", func(t *testing.T) {
		_, err := f.svc.Get(ctx, outsider(), b.ID)
		assert.ErrorIs(t, err, authz.ErrDenied)
		_, err = f.svc.List(ctx, outsider(), "", 50, 0)
		assert.ErrorIs(t, err, authz.ErrDenied)
	})
}

func TestListFiltersByState(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	openBatch := f.open(t, "Still open")
	toAbandon := f.open(t, "Abandoned one")
	_, err := f.svc.Abandon(ctx, treasurer(), toAbandon.ID, "Not needed", toAbandon.Version, time.Now())
	require.NoError(t, err)

	got, err := f.svc.List(ctx, treasurer(), batches.StateOpen, 50, 0)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, openBatch.ID, got[0].ID)
	assert.Empty(t, got[0].Entries, "the list omits entries; the detail carries them")

	got, err = f.svc.List(ctx, treasurer(), batches.StateAbandoned, 50, 0)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, toAbandon.ID, got[0].ID)

	got, err = f.svc.List(ctx, treasurer(), "", 50, 0)
	require.NoError(t, err)
	assert.Len(t, got, 2)

	_, err = f.svc.List(ctx, treasurer(), "half-done", 50, 0)
	assert.Error(t, err)
}

func TestOpenValidation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	_, err := f.svc.Open(ctx, treasurer(), batches.OpenParams{Label: "   "}, time.Now())
	assert.ErrorIs(t, err, batches.ErrLabelRequired)

	_, err = f.svc.Open(ctx, treasurer(), batches.OpenParams{
		Label: "Bad default", DefaultPaidThrough: "31/12/2026",
	}, time.Now())
	assert.ErrorIs(t, err, batches.ErrInvalidDate)
}

func TestGetUnknownBatch(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Get(context.Background(), treasurer(), 999)
	assert.ErrorIs(t, err, sql.ErrNoRows)
}
