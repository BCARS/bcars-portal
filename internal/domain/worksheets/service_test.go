package worksheets_test

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
	"github.com/bcars/bcars-portal/internal/domain/worksheets"
)

// asOf pins every judgement so the sheets are reproducible in tests too.
var asOf = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

func treasurer() *authz.Principal {
	return &authz.Principal{UserID: 1, Capabilities: map[string]struct{}{
		"dues.worksheet.manage": {}, "dues.read": {}, "member.read": {},
		"payment.batch.manage": {}, "payment.post": {}, "payment.read": {},
	}}
}

// noContact may build worksheets but cannot read member contact data.
func noContact() *authz.Principal {
	return &authz.Principal{UserID: 2, Capabilities: map[string]struct{}{
		"dues.worksheet.manage": {}, "dues.read": {},
	}}
}

func outsider() *authz.Principal {
	return &authz.Principal{UserID: 3, Capabilities: map[string]struct{}{"member.read": {}}}
}

type fixture struct {
	svc     *worksheets.Service
	batches *batches.Service
	db      *sql.DB
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	d, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	require.NoError(t, db.Migrate(d))
	// Three users so every test principal satisfies the actor foreign keys.
	for _, email := range []string{"treasurer@example.test", "nocontact@example.test", "outsider@example.test"} {
		_, err = d.Exec(`INSERT INTO users (email) VALUES (?)`, email)
		require.NoError(t, err)
	}
	return &fixture{svc: worksheets.NewService(d), batches: batches.NewService(d), db: d}
}

// member creates a person and approved membership, optionally with contact
// details and a coverage decision.
func (f *fixture) member(t *testing.T, name, callSign, paidThrough, email string) int64 {
	t.Helper()
	res, err := f.db.Exec(`INSERT INTO persons (display_name, sort_name, call_sign) VALUES (?, ?, ?)`,
		name, name, sql.NullString{String: callSign, Valid: callSign != ""})
	require.NoError(t, err)
	personID, err := res.LastInsertId()
	require.NoError(t, err)

	if email != "" {
		_, err = f.db.Exec(`
			INSERT INTO contact_methods (person_id, kind, value_raw, value_norm, is_primary)
			VALUES (?, 'email', ?, ?, 1)`, personID, email, email)
		require.NoError(t, err)
		_, err = f.db.Exec(`
			INSERT INTO contact_methods (person_id, kind, value_raw, value_norm)
			VALUES (?, 'phone', '555-000-0000', '5550000000')`, personID)
		require.NoError(t, err)
	}

	res, err = f.db.Exec(
		`INSERT INTO memberships (person_id, base_type, lifecycle) VALUES (?, 'full', 'approved')`,
		personID)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)

	if paidThrough != "" {
		_, err = f.db.Exec(`
			INSERT INTO coverage_events (membership_id, paid_through, reason_kind, decided_at)
			VALUES (?, ?, 'adjustment', '2026-01-01T00:00:00.000Z')`, id, paidThrough)
		require.NoError(t, err)
	}
	return id
}

func params(filter, order string) worksheets.CreateParams {
	return worksheets.CreateParams{
		AsOf: asOf, FilterKind: filter, SortOrder: order, WarningDays: 30,
	}
}

// TestOwesFilterCoversEveryStanding proves the sheet's purpose: it lists people
// who owe money, and nobody else.
func TestOwesFilterCoversEveryStanding(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	expired := f.member(t, "Expired Member", "W3EXP", "2025-12-31", "")
	unknown := f.member(t, "Unknown Member", "W3UNK", "", "")
	f.member(t, "Current Member", "W3CUR", "2026-12-31", "")
	f.member(t, "Expiring Member", "W3EXG", "2026-07-15", "")

	waived := f.member(t, "Waived Member", "W3WAV", "2020-12-31", "")
	_, err := f.db.Exec(`
		INSERT INTO honorary_grants (membership_id, starts_on, is_lifetime, reason, approved_by, approved_at)
		VALUES (?, '2020-01-01', 1, 'Long service', 1, '2020-01-01T00:00:00.000Z')`, waived)
	require.NoError(t, err)

	resigned := f.member(t, "Resigned Member", "W3RES", "", "")
	_, err = f.db.Exec(`UPDATE memberships SET lifecycle = 'resigned' WHERE id = ?`, resigned)
	require.NoError(t, err)

	_, rows, err := f.svc.Create(ctx, treasurer(), params(worksheets.FilterOwes, worksheets.SortLastName), asOf)
	require.NoError(t, err)

	got := map[int64]string{}
	for _, r := range rows {
		got[r.MembershipID] = r.DuesStatus
	}
	assert.Len(t, got, 2, "only expired and unknown owe money")
	assert.Equal(t, "expired", got[expired])
	assert.Equal(t, "unknown", got[unknown])
	assert.NotContains(t, got, waived, "a waived member owes nothing")
	assert.NotContains(t, got, resigned, "a resigned member is not chased for dues")
}

func TestActiveFilterIncludesEveryone(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.member(t, "Expired Member", "", "2025-12-31", "")
	f.member(t, "Current Member", "", "2026-12-31", "")

	_, rows, err := f.svc.Create(ctx, treasurer(), params(worksheets.FilterActive, worksheets.SortLastName), asOf)
	require.NoError(t, err)
	assert.Len(t, rows, 2)
}

// TestSortOrdersAreStable pins each print order, including where the awkward
// values land.
func TestSortOrdersAreStable(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.member(t, "Bella Zulu", "W3ZZZ", "2024-12-31", "")
	f.member(t, "Adam Alpha", "", "2025-12-31", "")
	f.member(t, "Cara Mike", "W3AAA", "", "")

	t.Run("last name", func(t *testing.T) {
		_, rows, err := f.svc.Create(ctx, treasurer(), params(worksheets.FilterOwes, worksheets.SortLastName), asOf)
		require.NoError(t, err)
		require.Len(t, rows, 3)
		assert.Equal(t, "Adam Alpha", rows[0].DisplayName)
		assert.Equal(t, "Cara Mike", rows[1].DisplayName)
		assert.Equal(t, "Bella Zulu", rows[2].DisplayName)
		for i, r := range rows {
			assert.Equal(t, int64(i+1), r.Ordinal, "ordinals are dense and start at 1")
		}
	})

	t.Run("call sign puts the unlicensed last", func(t *testing.T) {
		_, rows, err := f.svc.Create(ctx, treasurer(), params(worksheets.FilterOwes, worksheets.SortCallSign), asOf)
		require.NoError(t, err)
		assert.Equal(t, "W3AAA", rows[0].CallSign)
		assert.Equal(t, "W3ZZZ", rows[1].CallSign)
		assert.Empty(t, rows[2].CallSign, "a member with no call sign sorts last, not first")
	})

	t.Run("longest overdue puts never-recorded first", func(t *testing.T) {
		_, rows, err := f.svc.Create(ctx, treasurer(), params(worksheets.FilterOwes, worksheets.SortLongestOverdue), asOf)
		require.NoError(t, err)
		assert.Equal(t, "Cara Mike", rows[0].DisplayName, "no record at all is the most overdue")
		assert.Equal(t, "2024-12-31", rows[1].PaidThrough)
		assert.Equal(t, "2025-12-31", rows[2].PaidThrough)
	})
}

// TestSnapshotIsNotALiveView is the heart of the bead: reprinting an old sheet
// must reproduce the paper the treasurer actually carried.
func TestSnapshotIsNotALiveView(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Snapshot Member", "W3SNP", "2025-12-31", "")

	run, rows, err := f.svc.Create(ctx, treasurer(), params(worksheets.FilterOwes, worksheets.SortLastName), asOf)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "expired", rows[0].DuesStatus)

	// The member pays after the sheet was printed.
	_, err = f.batches.PostSinglePayment(ctx, treasurer(), batches.SingleParams{
		Entry: batches.EntryInput{
			MembershipID: m, AmountCents: 4000, Method: "cash",
			ReceivedOn: "2026-07-05", PaidThrough: "2026-12-31",
		},
		IdempotencyKey: "pay-1", Confirm: true,
	}, time.Now())
	require.NoError(t, err)

	reread, err := f.svc.Rows(ctx, treasurer(), run.ID, 50, 0)
	require.NoError(t, err)
	require.Len(t, reread, 1)
	assert.Equal(t, "expired", reread[0].DuesStatus,
		"the sheet still says what it said when it printed")
	assert.Equal(t, "2025-12-31", reread[0].PaidThrough)
	assert.True(t, reread[0].EnteredSince,
		"but the reprint marks the line as already entered")
}

// TestUnpaidSinceRunFollowsUp proves the follow-up sheet carries forward only
// the members who still have not paid.
func TestUnpaidSinceRunFollowsUp(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	paid := f.member(t, "Paid Member", "W3PD1", "2025-12-31", "")
	stillOwing := f.member(t, "Owing Member", "W3OWE", "2025-12-31", "")

	first, rows, err := f.svc.Create(ctx, treasurer(), params(worksheets.FilterOwes, worksheets.SortLastName), asOf)
	require.NoError(t, err)
	require.Len(t, rows, 2)

	_, err = f.batches.PostSinglePayment(ctx, treasurer(), batches.SingleParams{
		Entry: batches.EntryInput{
			MembershipID: paid, AmountCents: 4000, Method: "cash",
			ReceivedOn: "2026-07-05", PaidThrough: "2026-12-31",
		},
		IdempotencyKey: "pay-1", Confirm: true,
	}, time.Now())
	require.NoError(t, err)

	followUp := params(worksheets.FilterUnpaidSinceRun, worksheets.SortLastName)
	followUp.SourceRunID = first.ID
	_, second, err := f.svc.Create(ctx, treasurer(), followUp, asOf)
	require.NoError(t, err)
	require.Len(t, second, 1, "only the member who still has not paid carries forward")
	assert.Equal(t, stillOwing, second[0].MembershipID)
}

// TestContactColumnsAreServerAuthorized proves asking for contact data is not
// the same as being allowed to see it.
func TestContactColumnsAreServerAuthorized(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.member(t, "Contactable Member", "W3CON", "2025-12-31", "member@example.test")

	withContact := params(worksheets.FilterOwes, worksheets.SortLastName)
	withContact.IncludeEmail = true
	withContact.IncludePhone = true

	run, rows, err := f.svc.Create(ctx, treasurer(), withContact, asOf)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.True(t, run.IncludeEmail)
	assert.Equal(t, "member@example.test", rows[0].Email)
	assert.Equal(t, "555-000-0000", rows[0].Phone)
	assert.NotEmpty(t, run.GeneratedAt, "the good-as-of stamp for that contact data")

	deniedRun, deniedRows, err := f.svc.Create(ctx, noContact(), withContact, asOf)
	require.NoError(t, err)
	require.Len(t, deniedRows, 1)
	assert.False(t, deniedRun.IncludeEmail, "the run records that the column was refused")
	assert.False(t, deniedRun.IncludePhone)
	assert.Empty(t, deniedRows[0].Email, "asking does not grant")
	assert.Empty(t, deniedRows[0].Phone)
}

// TestBatchFromWorksheetPreservesOrder proves the link exists and that it
// deliberately creates no entries.
func TestBatchFromWorksheetPreservesOrder(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.member(t, "Bella Zulu", "", "2024-12-31", "")
	f.member(t, "Adam Alpha", "", "2025-12-31", "")

	run, rows, err := f.svc.Create(ctx, treasurer(), params(worksheets.FilterOwes, worksheets.SortLastName), asOf)
	require.NoError(t, err)
	require.Len(t, rows, 2)

	batch, err := f.batches.Open(ctx, treasurer(), batches.OpenParams{Label: "From the sheet"}, time.Now())
	require.NoError(t, err)
	require.NoError(t, f.svc.LinkBatch(ctx, treasurer(), run.ID, batch.ID))

	var linked sql.NullInt64
	require.NoError(t, f.db.QueryRow(
		`SELECT worksheet_run_id FROM payment_batches WHERE id = ?`, batch.ID).Scan(&linked))
	assert.Equal(t, run.ID, linked.Int64)

	after, err := f.batches.Get(ctx, treasurer(), batch.ID)
	require.NoError(t, err)
	assert.Zero(t, after.Totals.EntryCount,
		"linking a sheet creates no entries; inventing rows would be inventing payments")

	// The order to present is the sheet's order, which is stored.
	assert.Equal(t, "Adam Alpha", rows[0].DisplayName)
	assert.Equal(t, int64(1), rows[0].Ordinal)

	t.Run("a batch that already has rows is refused", func(t *testing.T) {
		other, err := f.batches.Open(ctx, treasurer(), batches.OpenParams{Label: "Already started"}, time.Now())
		require.NoError(t, err)
		_, _, err = f.batches.AddEntry(ctx, treasurer(), other.ID, batches.EntryInput{
			MembershipID: rows[0].MembershipID, AmountCents: 4000, Method: "cash",
			ReceivedOn: "2026-07-05", PaidThrough: "2026-12-31",
		}, "")
		require.NoError(t, err)

		err = f.svc.LinkBatch(ctx, treasurer(), run.ID, other.ID)
		assert.ErrorIs(t, err, worksheets.ErrBatchNotEmpty)
	})
}

func TestPagination(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, name := range []string{"Alpha One", "Bravo Two", "Charlie Three", "Delta Four"} {
		f.member(t, name, "", "2025-12-31", "")
	}
	run, _, err := f.svc.Create(ctx, treasurer(), params(worksheets.FilterOwes, worksheets.SortLastName), asOf)
	require.NoError(t, err)

	seen := map[int64]bool{}
	for offset := int64(0); offset < 4; offset += 2 {
		page, err := f.svc.Rows(ctx, treasurer(), run.ID, 2, offset)
		require.NoError(t, err)
		require.Len(t, page, 2)
		for _, r := range page {
			assert.False(t, seen[r.ID], "row %d appeared twice", r.ID)
			seen[r.ID] = true
		}
	}
	assert.Len(t, seen, 4)

	runs, err := f.svc.List(ctx, treasurer(), 50, 0)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, int64(4), runs[0].RowCount)
}

func TestValidation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	_, _, err := f.svc.Create(ctx, treasurer(), params("everybody", worksheets.SortLastName), asOf)
	assert.ErrorIs(t, err, worksheets.ErrUnknownFilter)

	_, _, err = f.svc.Create(ctx, treasurer(), params(worksheets.FilterOwes, "by vibes"), asOf)
	assert.ErrorIs(t, err, worksheets.ErrUnknownSort)

	_, _, err = f.svc.Create(ctx, treasurer(),
		params(worksheets.FilterUnpaidSinceRun, worksheets.SortLastName), asOf)
	assert.ErrorIs(t, err, worksheets.ErrSourceRunRequired)

	missing := params(worksheets.FilterUnpaidSinceRun, worksheets.SortLastName)
	missing.SourceRunID = 999
	_, _, err = f.svc.Create(ctx, treasurer(), missing, asOf)
	assert.ErrorIs(t, err, sql.ErrNoRows)

	_, err = f.svc.Get(ctx, treasurer(), 999)
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

// TestAuthorization proves worksheets are treasury-only.
func TestAuthorization(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.member(t, "Guarded Member", "", "2025-12-31", "")
	run, _, err := f.svc.Create(ctx, treasurer(), params(worksheets.FilterOwes, worksheets.SortLastName), asOf)
	require.NoError(t, err)

	_, _, err = f.svc.Create(ctx, outsider(), params(worksheets.FilterOwes, worksheets.SortLastName), asOf)
	assert.ErrorIs(t, err, authz.ErrDenied)
	_, err = f.svc.Get(ctx, outsider(), run.ID)
	assert.ErrorIs(t, err, authz.ErrDenied)
	_, err = f.svc.Rows(ctx, outsider(), run.ID, 50, 0)
	assert.ErrorIs(t, err, authz.ErrDenied)
	_, err = f.svc.List(ctx, outsider(), 50, 0)
	assert.ErrorIs(t, err, authz.ErrDenied)
	assert.ErrorIs(t, f.svc.LinkBatch(ctx, outsider(), run.ID, 1), authz.ErrDenied)
}
