package treasury_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/db"
	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/domain/batches"
	"github.com/bcars/bcars-portal/internal/domain/treasury"
)

// generatedAt is pinned so exports are byte-comparable across runs.
var generatedAt = time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

func treasurer() *authz.Principal {
	return &authz.Principal{UserID: 1, Capabilities: map[string]struct{}{
		"payment.read": {}, "payment.export": {}, "payment.batch.manage": {},
		"payment.post": {}, "payment.correct": {},
	}}
}

// noExport holds history access but not the export capability.
func noExport() *authz.Principal {
	return &authz.Principal{UserID: 2, Capabilities: map[string]struct{}{"payment.read": {}}}
}

// officer holds member and dues access but nothing treasury.
func officer() *authz.Principal {
	return &authz.Principal{UserID: 3, Capabilities: map[string]struct{}{
		"member.read": {}, "dues.read": {},
	}}
}

type fixture struct {
	svc     *treasury.Service
	batches *batches.Service
	db      *sql.DB
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	d, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	require.NoError(t, db.Migrate(d))

	_, err = d.Exec(`INSERT INTO users (email) VALUES ('treasurer@example.test')`)
	require.NoError(t, err)

	return &fixture{svc: treasury.NewService(d), batches: batches.NewService(d), db: d}
}

func (f *fixture) member(t *testing.T, name, callSign string) int64 {
	t.Helper()
	res, err := f.db.Exec(
		`INSERT INTO persons (display_name, sort_name, call_sign) VALUES (?, ?, ?)`,
		name, name, sql.NullString{String: callSign, Valid: callSign != ""})
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

// postBatch posts a batch of entries and returns it.
func (f *fixture) postBatch(t *testing.T, label string, entries ...batches.EntryInput) batches.Posted {
	t.Helper()
	ctx := context.Background()
	b, err := f.batches.Open(ctx, treasurer(), batches.OpenParams{Label: label}, time.Now())
	require.NoError(t, err)

	version := b.Version
	for _, e := range entries {
		_, after, err := f.batches.AddEntry(ctx, treasurer(), b.ID, e, "")
		require.NoError(t, err)
		version = after.Version
	}
	posted, err := f.batches.Post(ctx, treasurer(), b.ID, batches.PostParams{
		ExpectedVersion: version, IdempotencyKey: "post-" + label, Confirm: true,
	}, time.Now())
	require.NoError(t, err)
	return posted
}

func entry(membershipID, cents int64, method, receivedOn string) batches.EntryInput {
	return batches.EntryInput{
		MembershipID: membershipID, AmountCents: cents, Method: method,
		ReceivedOn: receivedOn, PaidThrough: "2026-12-31",
	}
}

// TestLedgerEffectiveVersusAll proves the two views the books need: what the
// club currently holds, and the full audit trail behind it.
func TestLedgerEffectiveVersusAll(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Corrected Member", "W3ABC")

	posted := f.postBatch(t, "January", entry(m, 40000, "check", "2026-01-15"))
	_, err := f.batches.CorrectPayment(ctx, treasurer(), posted.Payments[0].ID, batches.CorrectParams{
		AmountCents: 4000, Method: "check", ReceivedOn: "2026-01-15",
		PaidThrough: "2026-12-31", Reason: "Typed 400 instead of 40",
		IdempotencyKey: "c1", Confirm: true,
	}, time.Now())
	require.NoError(t, err)

	all, err := f.svc.ListLedger(ctx, treasurer(), treasury.LedgerQuery{})
	require.NoError(t, err)
	require.Len(t, all, 3, "the full trail keeps the original, the reversal, and the replacement")

	effective, err := f.svc.ListLedger(ctx, treasurer(), treasury.LedgerQuery{EffectiveOnly: true})
	require.NoError(t, err)
	require.Len(t, effective, 1, "only the replacement is still in force")
	assert.Equal(t, int64(4000), effective[0].AmountCents)
	assert.Equal(t, "replacement", effective[0].EntryKind)
	assert.False(t, effective[0].Superseded)

	var supersededSeen bool
	for _, e := range all {
		if e.EntryKind == "original" {
			supersededSeen = e.Superseded
		}
	}
	assert.True(t, supersededSeen, "the original is marked as settled")
}

// TestLedgerFilters covers each filter the treasurer reconciles with.
func TestLedgerFilters(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := f.member(t, "Alpha Member", "W3AAA")
	c := f.member(t, "Charlie Member", "W3CCC")

	first := f.postBatch(t, "January",
		entry(a, 4000, "cash", "2026-01-15"),
		entry(c, 10000, "check", "2026-01-15"))
	f.postBatch(t, "February", entry(a, 2500, "other", "2026-02-20"))

	for _, tc := range []struct {
		name  string
		query treasury.LedgerQuery
		want  int
	}{
		{"by member", treasury.LedgerQuery{MembershipID: a}, 2},
		{"by batch", treasury.LedgerQuery{BatchID: first.Batch.ID}, 2},
		{"by method", treasury.LedgerQuery{Method: "check"}, 1},
		{"by receipt", treasury.LedgerQuery{ReceiptCode: first.Payments[0].ReceiptCode}, 1},
		{"from date", treasury.LedgerQuery{ReceivedFrom: "2026-02-01"}, 1},
		{"to date", treasury.LedgerQuery{ReceivedTo: "2026-01-31"}, 2},
		{"date window", treasury.LedgerQuery{ReceivedFrom: "2026-01-01", ReceivedTo: "2026-12-31"}, 3},
		{"combined", treasury.LedgerQuery{MembershipID: a, Method: "cash"}, 1},
		{"no filter", treasury.LedgerQuery{}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := f.svc.ListLedger(ctx, treasurer(), tc.query)
			require.NoError(t, err)
			assert.Len(t, rows, tc.want)
		})
	}
}

// TestLedgerOrderIsStable proves paging cannot repeat or skip a row, which
// matters when the treasurer pages through a year of entries sharing a date.
func TestLedgerOrderIsStable(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Paged Member", "")

	var entries []batches.EntryInput
	for i := 0; i < 6; i++ {
		entries = append(entries, entry(m, 1000, "cash", "2026-01-15"))
	}
	f.postBatch(t, "Same day", entries...)

	seen := map[int64]bool{}
	for offset := int64(0); offset < 6; offset += 2 {
		page, err := f.svc.ListLedger(ctx, treasurer(), treasury.LedgerQuery{Limit: 2, Offset: offset})
		require.NoError(t, err)
		require.Len(t, page, 2)
		for _, e := range page {
			assert.False(t, seen[e.PaymentID], "payment %d appeared on two pages", e.PaymentID)
			seen[e.PaymentID] = true
		}
	}
	assert.Len(t, seen, 6, "every row appeared exactly once")
}

// TestReceiptIsStableAndPrintable proves a receipt carries what a printed slip
// needs and says plainly when a correction has replaced it.
func TestReceiptIsStableAndPrintable(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Receipt Member", "W3RCP")

	in := entry(m, 40000, "check", "2026-01-15")
	in.Reference = "1042"
	posted := f.postBatch(t, "January", in)
	paymentID := posted.Payments[0].ID

	receipt, err := f.svc.GetReceipt(ctx, treasurer(), paymentID)
	require.NoError(t, err)
	assert.NotEmpty(t, receipt.ReceiptCode)
	assert.Equal(t, "Receipt Member", receipt.DisplayName)
	assert.Equal(t, "W3RCP", receipt.CallSign)
	assert.Equal(t, "full", receipt.BaseType)
	assert.Equal(t, int64(40000), receipt.AmountCents)
	assert.Equal(t, "1042", receipt.Reference)
	assert.Equal(t, "2026-12-31", receipt.PaidThrough, "the coverage it bought")
	assert.False(t, receipt.Superseded)

	// Reprinting after a correction must not look current.
	_, err = f.batches.CorrectPayment(ctx, treasurer(), paymentID, batches.CorrectParams{
		AmountCents: 4000, Method: "check", ReceivedOn: "2026-01-15",
		PaidThrough: "2026-12-31", Reason: "Typed 400 instead of 40",
		IdempotencyKey: "c1", Confirm: true,
	}, time.Now())
	require.NoError(t, err)

	again, err := f.svc.GetReceipt(ctx, treasurer(), paymentID)
	require.NoError(t, err)
	assert.Equal(t, receipt.ReceiptCode, again.ReceiptCode, "the code is stable across reprints")
	assert.True(t, again.Superseded, "a reprint of a corrected receipt says so")
}

// TestBatchActivityReadsAsPlainLanguage proves the log answers "what happened
// to this batch" in words an officer would use.
func TestBatchActivityReadsAsPlainLanguage(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Activity Member", "")

	posted := f.postBatch(t, "Meeting night", entry(m, 40000, "check", "2026-01-15"))
	_, err := f.batches.CorrectPayment(ctx, treasurer(), posted.Payments[0].ID, batches.CorrectParams{
		AmountCents: 4000, Method: "check", ReceivedOn: "2026-01-15",
		PaidThrough: "2026-12-31", Reason: "Typed 400 instead of 40",
		IdempotencyKey: "c1", Confirm: true,
	}, time.Now())
	require.NoError(t, err)

	activity, err := f.svc.BatchActivity(ctx, treasurer(), posted.Batch.ID)
	require.NoError(t, err)
	require.Len(t, activity, 3)

	assert.Equal(t, "opened", activity[0].Kind)
	assert.Contains(t, activity[0].Summary, "Meeting night")

	assert.Equal(t, "posted", activity[1].Kind)
	assert.Contains(t, activity[1].Summary, "$400.00", "the posted total as it stood")

	assert.Equal(t, "corrected", activity[2].Kind)
	assert.Contains(t, activity[2].Summary, "Activity Member")
	assert.Contains(t, activity[2].Summary, "$400.00")
	assert.Contains(t, activity[2].Summary, "$40.00")
	assert.Equal(t, "Typed 400 instead of 40", activity[2].Reason)
}

func TestBatchActivityRecordsAbandonment(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	b, err := f.batches.Open(ctx, treasurer(), batches.OpenParams{Label: "Abandoned"}, time.Now())
	require.NoError(t, err)
	_, err = f.batches.Abandon(ctx, treasurer(), b.ID, "Duplicate of the paper sheet", b.Version, time.Now())
	require.NoError(t, err)

	activity, err := f.svc.BatchActivity(ctx, treasurer(), b.ID)
	require.NoError(t, err)
	require.Len(t, activity, 2)
	assert.Equal(t, "abandoned", activity[1].Kind)
	assert.Equal(t, "Duplicate of the paper sheet", activity[1].Reason)
}

// TestExportIsDeterministicAndSafe proves the export states its filters, is
// byte-identical across runs, formats money without float error, and cannot
// carry a formula into a spreadsheet.
func TestExportIsDeterministicAndSafe(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Export Member", "W3EXP")

	// A treasurer note that a spreadsheet would happily execute.
	in := entry(m, 40000, "check", "2026-01-15")
	in.Reference = "1042"
	in.TreasurerNote = `=HYPERLINK("http://evil.test","click me")`
	f.postBatch(t, "January", in)

	first, err := f.svc.ExportLedger(ctx, treasurer(), treasury.LedgerQuery{Method: "check"}, generatedAt)
	require.NoError(t, err)
	second, err := f.svc.ExportLedger(ctx, treasurer(), treasury.LedgerQuery{Method: "check"}, generatedAt)
	require.NoError(t, err)
	assert.Equal(t, first.CSV, second.CSV, "the same filters over the same data are byte-identical")

	assert.Equal(t, 1, first.RowCount)
	assert.Contains(t, first.CSV, "# generated_at,2026-08-09T12:00:00.000Z")
	assert.Contains(t, first.CSV, "# filter.method,check", "the export states what it filtered on")
	assert.Contains(t, first.CSV, "# filter.view,all entries including reversals")
	assert.Contains(t, first.CSV, "# row_count,1")

	assert.Contains(t, first.CSV, "400.00", "integer cents render exactly")
	assert.NotContains(t, first.CSV, "399.99")
	assert.NotContains(t, first.CSV, "400.0000")

	assert.Contains(t, first.CSV, `'=HYPERLINK`, "a formula cell is neutralized")
	assert.NotContains(t, first.CSV, "\n=HYPERLINK", "and never left executable")
	assert.Contains(t, first.CSV, "1042", "an ordinary reference is untouched")
}

// TestExportIncludesReversalsWithSignedAmounts proves the books export shows a
// correction rather than hiding it.
func TestExportIncludesReversalsWithSignedAmounts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Corrected Export", "")

	posted := f.postBatch(t, "January", entry(m, 40000, "check", "2026-01-15"))
	_, err := f.batches.CorrectPayment(ctx, treasurer(), posted.Payments[0].ID, batches.CorrectParams{
		AmountCents: 4000, Method: "check", ReceivedOn: "2026-01-15",
		PaidThrough: "2026-12-31", Reason: "Typed 400 instead of 40",
		IdempotencyKey: "c1", Confirm: true,
	}, time.Now())
	require.NoError(t, err)

	all, err := f.svc.ExportLedger(ctx, treasurer(), treasury.LedgerQuery{}, generatedAt)
	require.NoError(t, err)
	assert.Equal(t, 3, all.RowCount)
	assert.Contains(t, all.CSV, "'-400.00", "the reversal is signed, and quoted so a sheet cannot evaluate it")

	effective, err := f.svc.ExportLedger(ctx, treasurer(),
		treasury.LedgerQuery{EffectiveOnly: true}, generatedAt)
	require.NoError(t, err)
	assert.Equal(t, 1, effective.RowCount)
	assert.Contains(t, effective.CSV, "# filter.view,effective entries only")
	assert.NotContains(t, effective.CSV, "-400.00")
}

func TestExportBatchNamesItsFile(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Batch Export", "")
	posted := f.postBatch(t, "January", entry(m, 4000, "cash", "2026-01-15"))

	export, err := f.svc.ExportBatch(ctx, treasurer(), posted.Batch.ID, generatedAt)
	require.NoError(t, err)
	assert.Contains(t, export.Filename, "bcars-batch-")
	assert.Equal(t, 1, export.RowCount)
	assert.Contains(t, export.CSV, "# filter.batch_id,")

	_, err = f.svc.ExportBatch(ctx, treasurer(), 999, generatedAt)
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

// TestExportHeaderRowCountMatchesData guards against a header that claims a
// different number of rows than it carries.
func TestExportHeaderRowCountMatchesData(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Counted Member", "")
	f.postBatch(t, "January",
		entry(m, 1000, "cash", "2026-01-15"),
		entry(m, 2000, "cash", "2026-01-15"))

	export, err := f.svc.ExportLedger(ctx, treasurer(), treasury.LedgerQuery{}, generatedAt)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimRight(export.CSV, "\n"), "\n")
	var dataLines int
	var pastHeader bool
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "#"), l == "":
			continue
		case !pastHeader:
			pastHeader = true // the column header row
		default:
			dataLines++
		}
	}
	assert.Equal(t, export.RowCount, dataLines, "the stated row count matches the rows present")
	assert.Equal(t, 2, dataLines)
}

// TestAuthorization proves the treasury boundary: history needs payment.read,
// exports need payment.export, and an ordinary officer gets neither.
func TestAuthorization(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.member(t, "Guarded Member", "")
	posted := f.postBatch(t, "January", entry(m, 4000, "cash", "2026-01-15"))

	t.Run("payment.read may read history but not export", func(t *testing.T) {
		_, err := f.svc.ListLedger(ctx, noExport(), treasury.LedgerQuery{})
		assert.NoError(t, err)
		_, err = f.svc.GetReceipt(ctx, noExport(), posted.Payments[0].ID)
		assert.NoError(t, err)
		_, err = f.svc.BatchActivity(ctx, noExport(), posted.Batch.ID)
		assert.NoError(t, err)

		_, err = f.svc.ExportLedger(ctx, noExport(), treasury.LedgerQuery{}, generatedAt)
		assert.ErrorIs(t, err, authz.ErrDenied)
		_, err = f.svc.ExportBatch(ctx, noExport(), posted.Batch.ID, generatedAt)
		assert.ErrorIs(t, err, authz.ErrDenied)
	})

	t.Run("an ordinary officer sees nothing here", func(t *testing.T) {
		_, err := f.svc.ListLedger(ctx, officer(), treasury.LedgerQuery{})
		assert.ErrorIs(t, err, authz.ErrDenied)
		_, err = f.svc.GetReceipt(ctx, officer(), posted.Payments[0].ID)
		assert.ErrorIs(t, err, authz.ErrDenied)
		_, err = f.svc.BatchActivity(ctx, officer(), posted.Batch.ID)
		assert.ErrorIs(t, err, authz.ErrDenied)
		_, err = f.svc.ExportLedger(ctx, officer(), treasury.LedgerQuery{}, generatedAt)
		assert.ErrorIs(t, err, authz.ErrDenied)
	})
}
