package dues_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/db"
	"github.com/bcars/bcars-portal/internal/db/dbtest"
	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/domain/dues"
)

// asOf is the fixed judging date every test uses. Standing is only meaningful
// relative to an explicit date, so no test here depends on the wall clock.
var asOf = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

// treasurer holds every Phase 2 treasury capability.
func treasurer() *authz.Principal {
	return &authz.Principal{UserID: 1, Capabilities: map[string]struct{}{
		"dues.read": {}, "dues.rate.manage": {},
		"coverage.read": {}, "coverage.adjust": {},
	}}
}

// reader holds dues.read only, like a president or secretary.
func reader() *authz.Principal {
	return &authz.Principal{UserID: 2, Capabilities: map[string]struct{}{"dues.read": {}}}
}

// outsider holds no treasury capability at all.
func outsider() *authz.Principal {
	return &authz.Principal{UserID: 3, Capabilities: map[string]struct{}{"member.read": {}}}
}

type fixture struct {
	svc *dues.Service
	db  *sql.DB
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	d := dbtest.Open(t)
	var err error

	_, err = d.Exec(`INSERT INTO users (email) VALUES ('treasurer@example.test')`)
	require.NoError(t, err)

	return &fixture{svc: dues.NewService(d), db: d}
}

// member creates a person and an approved membership of the given base type.
func (f *fixture) member(t *testing.T, name, baseType string) int64 {
	t.Helper()
	res, err := f.db.Exec(`INSERT INTO persons (display_name, sort_name) VALUES (?, ?)`, name, name)
	require.NoError(t, err)
	personID, err := res.LastInsertId()
	require.NoError(t, err)

	res, err = f.db.Exec(
		`INSERT INTO memberships (person_id, base_type, lifecycle) VALUES (?, ?, 'approved')`,
		personID, baseType)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

// coverage writes a coverage event directly, standing in for a posted payment.
func (f *fixture) coverage(t *testing.T, membershipID int64, paidThrough string) int64 {
	t.Helper()
	res, err := f.db.Exec(`
		INSERT INTO coverage_events (membership_id, paid_through, reason_kind, decided_at)
		VALUES (?, ?, 'adjustment', '2026-01-01T00:00:00.000Z')`, membershipID, paidThrough)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

// honorary grants a waiver. endsOn == "" with lifetime false means open-ended.
func (f *fixture) honorary(t *testing.T, membershipID int64, lifetime bool, startsOn, endsOn string) {
	t.Helper()
	var ends any
	if endsOn != "" {
		ends = endsOn
	}
	life := 0
	if lifetime {
		life = 1
	}
	_, err := f.db.Exec(`
		INSERT INTO honorary_grants (membership_id, starts_on, ends_on, is_lifetime, reason,
			approved_by, approved_at)
		VALUES (?, ?, ?, ?, 'Long service', 1, '2020-01-01T00:00:00.000Z')`,
		membershipID, startsOn, ends, life)
	require.NoError(t, err)
}

func (f *fixture) standing(t *testing.T, membershipID int64, warningDays int) dues.Standing {
	t.Helper()
	st, err := f.svc.GetStanding(context.Background(), treasurer(), membershipID, asOf, warningDays)
	require.NoError(t, err)
	return st
}

// TestStandingStates walks every derived status against one fixed as-of date.
func TestStandingStates(t *testing.T) {
	f := newFixture(t)

	current := f.member(t, "Current Member", "full")
	f.coverage(t, current, "2026-12-31")

	expiring := f.member(t, "Expiring Member", "full")
	f.coverage(t, expiring, "2026-07-20") // 19 days out, inside a 30-day window

	expired := f.member(t, "Expired Member", "associate")
	f.coverage(t, expired, "2025-12-31")

	unknown := f.member(t, "Unknown Member", "full")

	for _, tc := range []struct {
		name         string
		membershipID int64
		want         string
		paidThrough  string
	}{
		{"current", current, dues.StatusCurrent, "2026-12-31"},
		{"expiring", expiring, dues.StatusExpiring, "2026-07-20"},
		{"expired", expired, dues.StatusExpired, "2025-12-31"},
		{"unknown", unknown, dues.StatusUnknown, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := f.standing(t, tc.membershipID, 30)
			assert.Equal(t, tc.want, st.Status)
			assert.Equal(t, tc.paidThrough, st.PaidThrough)
			assert.Equal(t, "2026-07-01", st.AsOf, "standing must report the date it was judged against")
			assert.Nil(t, st.Honorary)
		})
	}
}

// TestStandingBoundariesAreDeterministic pins the exact edges of the window so
// a later refactor cannot quietly shift a member from current to expired.
func TestStandingBoundariesAreDeterministic(t *testing.T) {
	f := newFixture(t)

	onAsOf := f.member(t, "Paid Through Today", "full")
	f.coverage(t, onAsOf, "2026-07-01") // exactly the as-of date

	dayBefore := f.member(t, "Lapsed Yesterday", "full")
	f.coverage(t, dayBefore, "2026-06-30")

	lastWarnDay := f.member(t, "Last Warning Day", "full")
	f.coverage(t, lastWarnDay, "2026-07-31") // exactly as-of + 30

	firstSafeDay := f.member(t, "First Safe Day", "full")
	f.coverage(t, firstSafeDay, "2026-08-01") // one day past the window

	assert.Equal(t, dues.StatusExpiring, f.standing(t, onAsOf, 30).Status,
		"paid through the as-of date is still covered, and inside the window")
	assert.Equal(t, dues.StatusExpired, f.standing(t, dayBefore, 30).Status)
	assert.Equal(t, dues.StatusExpiring, f.standing(t, lastWarnDay, 30).Status,
		"the last day of the window is still expiring")
	assert.Equal(t, dues.StatusCurrent, f.standing(t, firstSafeDay, 30).Status,
		"one day past the window is plain current")
}

// TestHonoraryPrecedence proves a waiver decides dues standing outright while
// leaving the underlying Full or Associate right untouched.
func TestHonoraryPrecedence(t *testing.T) {
	f := newFixture(t)

	lifetime := f.member(t, "Lifetime Honorary", "full")
	f.honorary(t, lifetime, true, "2020-01-01", "")
	// Coverage long expired: the waiver must still win.
	f.coverage(t, lifetime, "2019-12-31")

	fixedTerm := f.member(t, "Term Honorary", "associate")
	f.honorary(t, fixedTerm, false, "2026-01-01", "2026-12-31")

	expiredGrant := f.member(t, "Lapsed Honorary", "full")
	f.honorary(t, expiredGrant, false, "2024-01-01", "2024-12-31")
	f.coverage(t, expiredGrant, "2025-12-31")

	notYetStarted := f.member(t, "Future Honorary", "full")
	f.honorary(t, notYetStarted, true, "2027-01-01", "")

	revoked := f.member(t, "Revoked Honorary", "full")
	f.honorary(t, revoked, true, "2020-01-01", "")
	_, err := f.db.Exec(
		`UPDATE honorary_grants SET revoked_at = '2025-01-01T00:00:00.000Z' WHERE membership_id = ?`,
		revoked)
	require.NoError(t, err)

	t.Run("lifetime waiver beats expired coverage", func(t *testing.T) {
		st := f.standing(t, lifetime, 30)
		assert.Equal(t, dues.StatusHonoraryWaived, st.Status)
		require.NotNil(t, st.Honorary)
		assert.Equal(t, dues.HonoraryLifetime, st.Honorary.Kind)
		assert.Equal(t, "full", st.BaseType, "a waiver never changes base membership rights")
	})

	t.Run("fixed-term waiver reports its end date", func(t *testing.T) {
		st := f.standing(t, fixedTerm, 30)
		assert.Equal(t, dues.StatusHonoraryWaived, st.Status)
		require.NotNil(t, st.Honorary)
		assert.Equal(t, dues.HonoraryFixedTerm, st.Honorary.Kind)
		assert.Equal(t, "2026-12-31", st.Honorary.EndsOn)
		assert.Equal(t, "associate", st.BaseType)
	})

	t.Run("a lapsed grant does not waive", func(t *testing.T) {
		st := f.standing(t, expiredGrant, 30)
		assert.Equal(t, dues.StatusExpired, st.Status)
		assert.Nil(t, st.Honorary)
	})

	t.Run("a grant that has not started does not waive", func(t *testing.T) {
		st := f.standing(t, notYetStarted, 30)
		assert.Equal(t, dues.StatusUnknown, st.Status)
	})

	t.Run("a revoked grant does not waive", func(t *testing.T) {
		st := f.standing(t, revoked, 30)
		assert.Equal(t, dues.StatusUnknown, st.Status)
	})
}

// TestStandingUsesEffectiveCoverage proves a superseded decision stops counting
// while remaining in the history.
func TestStandingUsesEffectiveCoverage(t *testing.T) {
	f := newFixture(t)
	id := f.member(t, "Corrected Member", "full")
	original := f.coverage(t, id, "2025-12-31")

	_, err := f.db.Exec(`
		INSERT INTO coverage_events (membership_id, paid_through, reason_kind, reason,
			supersedes_event_id, decided_at)
		VALUES (?, '2026-12-31', 'adjustment', 'Typo in the original entry', ?,
			'2026-02-01T00:00:00.000Z')`, id, original)
	require.NoError(t, err)

	st := f.standing(t, id, 30)
	assert.Equal(t, dues.StatusCurrent, st.Status)
	assert.Equal(t, "2026-12-31", st.PaidThrough)

	events, err := f.svc.ListCoverageEvents(context.Background(), treasurer(), id, 50, 0)
	require.NoError(t, err)
	assert.Len(t, events, 2, "the superseded decision stays readable as history")
}

// TestListStandingFilterMatchesReportedStatus proves the SQL status filter and
// the SQL status expression cannot drift apart: whatever a filter returns must
// carry the status that was asked for.
func TestListStandingFilterMatchesReportedStatus(t *testing.T) {
	f := newFixture(t)

	f.coverage(t, f.member(t, "Anders Current", "full"), "2026-12-31")
	f.coverage(t, f.member(t, "Baker Expiring", "full"), "2026-07-15")
	f.coverage(t, f.member(t, "Chen Expired", "full"), "2024-12-31")
	f.member(t, "Diaz Unknown", "full")
	waived := f.member(t, "Evans Waived", "full")
	f.honorary(t, waived, true, "2020-01-01", "")

	for _, status := range []string{
		dues.StatusCurrent, dues.StatusExpiring, dues.StatusExpired,
		dues.StatusUnknown, dues.StatusHonoraryWaived,
	} {
		t.Run(status, func(t *testing.T) {
			rows, err := f.svc.ListStanding(context.Background(), treasurer(), dues.StandingQuery{
				AsOf: asOf, WarningDays: 30, Status: status,
			})
			require.NoError(t, err)
			require.Len(t, rows, 1, "exactly one fixture member should match %s", status)
			assert.Equal(t, status, rows[0].Status)
		})
	}

	t.Run("no filter returns every member", func(t *testing.T) {
		rows, err := f.svc.ListStanding(context.Background(), treasurer(), dues.StandingQuery{
			AsOf: asOf, WarningDays: 30,
		})
		require.NoError(t, err)
		assert.Len(t, rows, 5)
	})

	t.Run("unknown filter value is rejected", func(t *testing.T) {
		_, err := f.svc.ListStanding(context.Background(), treasurer(), dues.StandingQuery{
			AsOf: asOf, Status: "lapsed",
		})
		assert.ErrorIs(t, err, dues.ErrUnknownStatus)
	})
}

// TestListStandingSearchAndOrder proves search matches name or call sign and
// that the order is stable enough to paginate.
func TestListStandingSearchAndOrder(t *testing.T) {
	f := newFixture(t)
	f.member(t, "Zeta Last", "full")
	f.member(t, "Alpha First", "full")
	withCall := f.member(t, "Mid Member", "full")
	_, err := f.db.Exec(`UPDATE persons SET call_sign = 'W3ABC' WHERE id =
		(SELECT person_id FROM memberships WHERE id = ?)`, withCall)
	require.NoError(t, err)

	rows, err := f.svc.ListStanding(context.Background(), treasurer(), dues.StandingQuery{AsOf: asOf})
	require.NoError(t, err)
	require.Len(t, rows, 3)
	assert.Equal(t, "Alpha First", rows[0].DisplayName, "sorted by sort_name")
	assert.Equal(t, "Zeta Last", rows[2].DisplayName)

	byCall, err := f.svc.ListStanding(context.Background(), treasurer(), dues.StandingQuery{
		AsOf: asOf, Search: "W3AB",
	})
	require.NoError(t, err)
	require.Len(t, byCall, 1)
	assert.Equal(t, "Mid Member", byCall[0].DisplayName)

	page, err := f.svc.ListStanding(context.Background(), treasurer(), dues.StandingQuery{
		AsOf: asOf, Limit: 2, Offset: 2,
	})
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, "Zeta Last", page[0].DisplayName)
}

// TestListStandingExcludesEndedMemberships proves the working list leaves out
// resigned and deceased members while a direct lookup still finds them.
func TestListStandingExcludesEndedMemberships(t *testing.T) {
	f := newFixture(t)
	active := f.member(t, "Active Member", "full")
	resigned := f.member(t, "Resigned Member", "full")
	_, err := f.db.Exec(`UPDATE memberships SET lifecycle = 'resigned' WHERE id = ?`, resigned)
	require.NoError(t, err)

	rows, err := f.svc.ListStanding(context.Background(), treasurer(), dues.StandingQuery{AsOf: asOf})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, active, rows[0].MembershipID)

	st, err := f.svc.GetStanding(context.Background(), treasurer(), resigned, asOf, 30)
	require.NoError(t, err)
	assert.Equal(t, "resigned", st.Lifecycle)
}

func TestGetStandingUnknownMembership(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.GetStanding(context.Background(), treasurer(), 999, asOf, 30)
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

// --- Coverage adjustment ---

// TestAdjustCoverageAppends proves an adjustment supersedes rather than edits,
// and that the prior decision keeps its own record.
func TestAdjustCoverageAppends(t *testing.T) {
	f := newFixture(t)
	id := f.member(t, "Adjusted Member", "full")
	original := f.coverage(t, id, "2025-12-31")

	event, err := f.svc.AdjustCoverage(context.Background(), treasurer(), id,
		"2026-12-31", "Waived the lapse after the club meeting", time.Now())
	require.NoError(t, err)
	assert.Equal(t, "adjustment", event.ReasonKind)
	assert.Equal(t, original, event.SupersedesEventID.Int64)
	assert.Equal(t, int64(1), event.DecidedBy.Int64)

	var priorPaidThrough string
	require.NoError(t, f.db.QueryRow(
		`SELECT paid_through FROM coverage_events WHERE id = ?`, original).Scan(&priorPaidThrough))
	assert.Equal(t, "2025-12-31", priorPaidThrough, "history is never rewritten")

	st := f.standing(t, id, 30)
	assert.Equal(t, dues.StatusCurrent, st.Status)
	assert.Equal(t, "2026-12-31", st.PaidThrough)
}

func TestAdjustCoverageFirstDecision(t *testing.T) {
	f := newFixture(t)
	id := f.member(t, "New Member", "full")

	event, err := f.svc.AdjustCoverage(context.Background(), treasurer(), id,
		"2026-12-31", "Initial coverage recorded from the paper roster", time.Now())
	require.NoError(t, err)
	assert.False(t, event.SupersedesEventID.Valid, "the first decision supersedes nothing")
}

// TestAdjustCoverageAcceptsOffCycleDate pins the owner's decision: the server
// records what actually happened rather than enforcing the club's year-end
// convention. See bd memory dues-year-end-not-validated.
func TestAdjustCoverageAcceptsOffCycleDate(t *testing.T) {
	f := newFixture(t)
	id := f.member(t, "Off Cycle Member", "full")

	event, err := f.svc.AdjustCoverage(context.Background(), treasurer(), id,
		"2026-06-30", "Prorated to the mid-year date the treasurer agreed", time.Now())
	require.NoError(t, err)
	assert.Equal(t, "2026-06-30", event.PaidThrough)
}

func TestAdjustCoverageValidation(t *testing.T) {
	f := newFixture(t)
	id := f.member(t, "Validated Member", "full")
	ctx := context.Background()

	t.Run("reason is required", func(t *testing.T) {
		_, err := f.svc.AdjustCoverage(ctx, treasurer(), id, "2026-12-31", "   ", time.Now())
		assert.ErrorIs(t, err, dues.ErrReasonRequired)
	})

	t.Run("the date must be a real ISO date", func(t *testing.T) {
		_, err := f.svc.AdjustCoverage(ctx, treasurer(), id, "12/31/2026", "Bad format", time.Now())
		assert.ErrorIs(t, err, dues.ErrInvalidDate)
	})

	t.Run("unknown membership is not found", func(t *testing.T) {
		_, err := f.svc.AdjustCoverage(ctx, treasurer(), 999, "2026-12-31", "No such member", time.Now())
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("nothing was written by a rejected request", func(t *testing.T) {
		var n int
		require.NoError(t, f.db.QueryRow(
			`SELECT count(*) FROM coverage_events WHERE membership_id = ?`, id).Scan(&n))
		assert.Zero(t, n)
	})
}

// TestAdjustCoverageConcurrentLosesNothing proves two adjustments racing on the
// same predecessor cannot both win silently.
func TestAdjustCoverageConcurrentLosesNothing(t *testing.T) {
	f := newFixture(t)
	id := f.member(t, "Contended Member", "full")
	original := f.coverage(t, id, "2025-12-31")
	ctx := context.Background()

	_, err := f.svc.AdjustCoverage(ctx, treasurer(), id, "2026-12-31", "First writer", time.Now())
	require.NoError(t, err)

	// A second writer that still believes `original` is effective would have to
	// supersede it again, which the schema refuses.
	_, err = f.db.Exec(`
		INSERT INTO coverage_events (membership_id, paid_through, reason_kind, reason,
			supersedes_event_id, decided_at)
		VALUES (?, '2027-12-31', 'adjustment', 'Second writer', ?, '2026-03-01T00:00:00.000Z')`,
		id, original)
	assert.Error(t, err, "a second event superseding the same decision must be refused")
}

// --- Rates ---

func TestSetRateCreateAndRevise(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	created, err := f.svc.SetRate(ctx, treasurer(), 2026, 4000, "Board approved", 0, time.Now())
	require.NoError(t, err)
	assert.Equal(t, int64(4000), created.AmountCents)
	assert.Equal(t, int64(1), created.Version)

	t.Run("a second create is refused", func(t *testing.T) {
		_, err := f.svc.SetRate(ctx, treasurer(), 2026, 5000, "", 0, time.Now())
		assert.ErrorIs(t, err, dues.ErrRateExists)
	})

	t.Run("revision requires the current version", func(t *testing.T) {
		_, err := f.svc.SetRate(ctx, treasurer(), 2026, 5000, "", 99, time.Now())
		assert.ErrorIs(t, err, db.ErrStale)
	})

	t.Run("revision with the current version wins", func(t *testing.T) {
		revised, err := f.svc.SetRate(ctx, treasurer(), 2026, 5000, "Raised for 2026", created.Version, time.Now())
		require.NoError(t, err)
		assert.Equal(t, int64(5000), revised.AmountCents)
		assert.Equal(t, int64(2), revised.Version)
	})

	t.Run("revising a year with no rate is not found", func(t *testing.T) {
		_, err := f.svc.SetRate(ctx, treasurer(), 2030, 5000, "", 1, time.Now())
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})
}

// --- Suggestions ---

// TestSuggestionsAreNonBinding proves suggestions read rates, explain
// themselves, mutate nothing, and never tie an amount to a date as a rule.
func TestSuggestionsAreNonBinding(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.svc.SetRate(ctx, treasurer(), 2026, 4000, "", 0, time.Now())
	require.NoError(t, err)
	_, err = f.svc.SetRate(ctx, treasurer(), 2027, 4500, "", 0, time.Now())
	require.NoError(t, err)

	s, err := f.svc.SuggestFor(ctx, reader(), asOf)
	require.NoError(t, err)
	assert.False(t, s.Binding, "suggestions must announce that they are not validation")
	require.Len(t, s.Choices, 3)

	assert.Equal(t, "2026-12-31", s.Choices[0].PaidThrough)
	assert.Equal(t, int64(4000), s.Choices[0].AmountCents)
	assert.True(t, s.Choices[0].RateKnown)

	assert.Equal(t, "2027-12-31", s.Choices[1].PaidThrough)
	assert.Equal(t, int64(8500), s.Choices[1].AmountCents, "two covered years sum their own rates")

	assert.Equal(t, "2028-12-31", s.Choices[2].PaidThrough)
	assert.False(t, s.Choices[2].RateKnown, "2028 has no rate on file")
	assert.Zero(t, s.Choices[2].AmountCents, "an unknown total is zero, never a guess")
	assert.Contains(t, s.Choices[2].Explanation, "treasurer supplies the amount")

	for _, c := range s.Choices {
		assert.NotEmpty(t, c.Explanation)
	}

	var events int
	require.NoError(t, f.db.QueryRow(`SELECT count(*) FROM coverage_events`).Scan(&events))
	assert.Zero(t, events, "reading suggestions must not write coverage")
}

// --- Authorization ---

// TestAuthorization proves the capability split the design requires: an
// executive officer reads safe standing, and everything that changes or
// exposes more requires its own capability.
func TestAuthorization(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := f.member(t, "Guarded Member", "full")
	f.coverage(t, id, "2026-12-31")

	t.Run("dues.read reads standing", func(t *testing.T) {
		_, err := f.svc.GetStanding(ctx, reader(), id, asOf, 30)
		assert.NoError(t, err)
		_, err = f.svc.ListStanding(ctx, reader(), dues.StandingQuery{AsOf: asOf})
		assert.NoError(t, err)
		_, err = f.svc.SuggestFor(ctx, reader(), asOf)
		assert.NoError(t, err)
		_, err = f.svc.ListRates(ctx, reader())
		assert.NoError(t, err)
	})

	t.Run("dues.read alone cannot read coverage history", func(t *testing.T) {
		_, err := f.svc.ListCoverageEvents(ctx, reader(), id, 50, 0)
		assert.ErrorIs(t, err, authz.ErrDenied)
	})

	t.Run("dues.read alone cannot adjust coverage", func(t *testing.T) {
		_, err := f.svc.AdjustCoverage(ctx, reader(), id, "2027-12-31", "Not allowed", time.Now())
		assert.ErrorIs(t, err, authz.ErrDenied)
	})

	t.Run("dues.read alone cannot set a rate", func(t *testing.T) {
		_, err := f.svc.SetRate(ctx, reader(), 2026, 4000, "", 0, time.Now())
		assert.ErrorIs(t, err, authz.ErrDenied)
	})

	t.Run("a member reader with no dues capability is denied everything", func(t *testing.T) {
		_, err := f.svc.GetStanding(ctx, outsider(), id, asOf, 30)
		assert.ErrorIs(t, err, authz.ErrDenied)
		_, err = f.svc.ListStanding(ctx, outsider(), dues.StandingQuery{AsOf: asOf})
		assert.ErrorIs(t, err, authz.ErrDenied)
	})
}
