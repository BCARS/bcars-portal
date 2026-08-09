// Package dues derives membership dues standing and records the decisions that
// change it.
//
// The central rule of this package is that money received and coverage granted
// are separate facts. Nothing here infers a paid-through date from an amount,
// and nothing infers an amount from a date. A treasurer states both.
//
// Standing itself is never stored. It is computed as of an explicit date so
// that tests, worksheets, and reports are reproducible.
package dues

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bcars/bcars-portal/internal/db"
	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/authz"
)

// Derived standing values. `expiring` is a classification applied at query
// time against a caller-supplied window, not a state any row carries.
const (
	StatusHonoraryWaived = "honorary_waived"
	StatusCurrent        = "current"
	StatusExpiring       = "expiring"
	StatusExpired        = "expired"
	StatusUnknown        = "unknown"
)

// Honorary kinds reported alongside StatusHonoraryWaived.
const (
	HonoraryLifetime  = "lifetime"
	HonoraryFixedTerm = "fixed_term"
)

// DefaultWarningDays is the look-ahead used to classify a current membership as
// expiring when the caller does not supply a window.
const DefaultWarningDays = 60

// DefaultLimit caps an unbounded standing or coverage list.
const DefaultLimit = 50

var (
	// ErrReasonRequired is returned when a decision that changes coverage
	// arrives without a plain-language reason. The reason is what makes the
	// append-only history readable later.
	ErrReasonRequired = errors.New("dues: a reason is required")

	// ErrInvalidDate is returned for a paid-through value that is not an ISO
	// YYYY-MM-DD date. Note that an off-cycle date is NOT invalid: the
	// treasurer records what happened, including historical and mid-year
	// dates. See the owner decision in docs/phase-2-design.md.
	ErrInvalidDate = errors.New("dues: paid-through must be an ISO YYYY-MM-DD date")

	// ErrRateExists is returned when creating a rate for a year that already
	// has one. Revising it requires presenting the version being replaced.
	ErrRateExists = errors.New("dues: a rate for that year already exists")

	// ErrUnknownStatus is returned for a standing filter value that is not one
	// of the derived statuses.
	ErrUnknownStatus = errors.New("dues: unknown standing status")
)

// ISODate is the date layout used throughout the ledger.
const ISODate = "2006-01-02"

// isoTimestamp is the UTC timestamp layout used by every table in this schema.
const isoTimestamp = "2006-01-02T15:04:05.000Z"

// Service reads derived dues standing and appends coverage decisions.
type Service struct {
	DB *sql.DB
	Q  *sqlcgen.Queries
}

// NewService creates a dues service over database.
func NewService(database *sql.DB) *Service {
	return &Service{DB: database, Q: sqlcgen.New(database)}
}

// Honorary describes an active honorary waiver.
type Honorary struct {
	// Kind is HonoraryLifetime or HonoraryFixedTerm.
	Kind string
	// EndsOn is set only for a fixed-term grant with a known end date.
	EndsOn string
}

// Standing is the safe derived summary of a membership's dues position. It
// deliberately carries no amount, method, reference, receipt, or treasurer
// note: those are restricted payment detail, and this type is returned to every
// caller holding dues.read.
type Standing struct {
	MembershipID int64
	PersonID     int64
	DisplayName  string
	CallSign     string
	// BaseType is the underlying Full or Associate membership right. An
	// honorary waiver changes dues standing and never changes this.
	BaseType  string
	Lifecycle string
	Status    string
	// PaidThrough is empty when no coverage decision has ever been recorded.
	PaidThrough string
	AsOf        string
	WarningDays int
	// Honorary is non-nil only when Status is StatusHonoraryWaived.
	Honorary *Honorary
	// CoverageEventID identifies the effective decision, for callers that want
	// to read its history. Zero when there is none.
	CoverageEventID int64
}

// StandingQuery filters a standing list.
type StandingQuery struct {
	// AsOf is the date standing is judged against. Zero means today (UTC).
	AsOf time.Time
	// WarningDays is the expiring look-ahead. Zero means DefaultWarningDays.
	WarningDays int
	// Status filters to one derived status. Empty returns every status.
	Status string
	// Search matches display name or call sign.
	Search string
	// MembershipID restricts to one membership. Zero means all.
	MembershipID int64
	// IncludeEnded keeps rejected, resigned, and deceased memberships, which
	// the working list excludes.
	IncludeEnded bool
	Limit        int64
	Offset       int64
}

func (q StandingQuery) asOf() time.Time {
	if q.AsOf.IsZero() {
		return time.Now().UTC()
	}
	return q.AsOf.UTC()
}

func (q StandingQuery) warningDays() int {
	if q.WarningDays <= 0 {
		return DefaultWarningDays
	}
	return q.WarningDays
}

// ListStanding returns derived standing for the memberships matching q.
func (s *Service) ListStanding(ctx context.Context, p *authz.Principal, q StandingQuery) ([]Standing, error) {
	if err := authz.Authorize(ctx, p, "dues.read", nil); err != nil {
		return nil, err
	}
	if q.Status != "" && !validStatus(q.Status) {
		return nil, fmt.Errorf("%w: %q", ErrUnknownStatus, q.Status)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}

	asOf := q.asOf()
	warningDays := q.warningDays()
	warnThrough := asOf.AddDate(0, 0, warningDays)

	includeEnded := int64(0)
	if q.IncludeEnded {
		includeEnded = 1
	}

	rows, err := s.Q.ListDuesStanding(ctx, sqlcgen.ListDuesStandingParams{
		AsOf:         asOf.Format(ISODate),
		WarnThrough:  warnThrough.Format(ISODate),
		IncludeEnded: includeEnded,
		MembershipID: q.MembershipID,
		Search:       q.Search,
		StatusFilter: q.Status,
		Lim:          limit,
		Off:          q.Offset,
	})
	if err != nil {
		return nil, err
	}

	out := make([]Standing, len(rows))
	for i, r := range rows {
		out[i] = standingFromRow(r, asOf.Format(ISODate), warningDays)
	}
	return out, nil
}

// GetStanding returns derived standing for one membership, including one whose
// lifecycle has ended. Returns sql.ErrNoRows when the membership is unknown.
func (s *Service) GetStanding(ctx context.Context, p *authz.Principal, membershipID int64, asOf time.Time, warningDays int) (Standing, error) {
	rows, err := s.ListStanding(ctx, p, StandingQuery{
		AsOf:         asOf,
		WarningDays:  warningDays,
		MembershipID: membershipID,
		IncludeEnded: true,
		Limit:        1,
	})
	if err != nil {
		return Standing{}, err
	}
	if len(rows) == 0 {
		return Standing{}, sql.ErrNoRows
	}
	return rows[0], nil
}

func standingFromRow(r sqlcgen.ListDuesStandingRow, asOf string, warningDays int) Standing {
	st := Standing{
		MembershipID:    r.MembershipID,
		PersonID:        r.PersonID,
		DisplayName:     r.DisplayName,
		CallSign:        r.CallSign.String,
		BaseType:        r.BaseType,
		Lifecycle:       r.Lifecycle,
		Status:          r.Status,
		PaidThrough:     r.PaidThrough.String,
		AsOf:            asOf,
		WarningDays:     warningDays,
		CoverageEventID: r.CoverageEventID.Int64,
	}
	if r.Status == StatusHonoraryWaived {
		h := &Honorary{Kind: HonoraryFixedTerm}
		if r.HonoraryIsLifetime == 1 {
			h.Kind = HonoraryLifetime
		} else if r.HonoraryIsOpenEnded == 0 {
			h.EndsOn = r.HonoraryEndsOn
		}
		st.Honorary = h
	}
	return st
}

func validStatus(s string) bool {
	switch s {
	case StatusHonoraryWaived, StatusCurrent, StatusExpiring, StatusExpired, StatusUnknown:
		return true
	}
	return false
}

// --- Coverage history ---

// ListCoverageEvents returns the append-only paid-through history, newest
// first. Superseded events remain visible: they are how the history explains
// itself.
func (s *Service) ListCoverageEvents(ctx context.Context, p *authz.Principal, membershipID, limit, offset int64) ([]sqlcgen.CoverageEvent, error) {
	if err := authz.Authorize(ctx, p, "coverage.read", nil); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	return s.Q.ListCoverageEventsPage(ctx, sqlcgen.ListCoverageEventsPageParams{
		MembershipID: membershipID,
		Limit:        limit,
		Offset:       offset,
	})
}

// AdjustCoverage appends an independent paid-through decision, superseding the
// currently effective one. History is never rewritten: the prior event stays
// readable and keeps its own actor, timestamp, and reason.
//
// This is deliberately independent of any payment. A treasurer waives a month,
// fixes a typo from years ago, or extends coverage as a goodwill decision
// without money changing hands.
func (s *Service) AdjustCoverage(ctx context.Context, p *authz.Principal, membershipID int64, paidThrough, reason string, now time.Time) (sqlcgen.CoverageEvent, error) {
	if err := authz.Authorize(ctx, p, "coverage.adjust", nil); err != nil {
		return sqlcgen.CoverageEvent{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return sqlcgen.CoverageEvent{}, ErrReasonRequired
	}
	// The date must be a real ISO date. It need NOT be December 31: the club
	// year-end is a convention, not an invariant, and refusing an off-cycle
	// date would stop a treasurer recording what actually happened.
	if _, err := time.Parse(ISODate, paidThrough); err != nil {
		return sqlcgen.CoverageEvent{}, fmt.Errorf("%w: %q", ErrInvalidDate, paidThrough)
	}
	if now.IsZero() {
		now = time.Now()
	}

	if _, err := s.Q.GetMembership(ctx, membershipID); err != nil {
		return sqlcgen.CoverageEvent{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return sqlcgen.CoverageEvent{}, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.Q.WithTx(tx)

	// Supersede the decision that is effective right now, if there is one.
	// Two concurrent adjustments both naming the same predecessor collide on
	// the unique index, which surfaces as a stale write rather than a silently
	// lost decision.
	var supersedes sql.NullInt64
	prior, err := qtx.GetEffectiveCoverageEvent(ctx, membershipID)
	switch {
	case err == nil:
		supersedes = sql.NullInt64{Int64: prior.ID, Valid: true}
	case errors.Is(err, sql.ErrNoRows):
		// First decision for this membership.
	default:
		return sqlcgen.CoverageEvent{}, err
	}

	event, err := qtx.CreateCoverageEvent(ctx, sqlcgen.CreateCoverageEventParams{
		MembershipID:      membershipID,
		PaidThrough:       paidThrough,
		ReasonKind:        "adjustment",
		Reason:            sql.NullString{String: reason, Valid: true},
		SupersedesEventID: supersedes,
		DecidedBy:         sql.NullInt64{Int64: p.UserID, Valid: p.UserID != 0},
		DecidedAt:         now.UTC().Format(isoTimestamp),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return sqlcgen.CoverageEvent{}, db.ErrStale
		}
		return sqlcgen.CoverageEvent{}, err
	}
	if err := tx.Commit(); err != nil {
		return sqlcgen.CoverageEvent{}, err
	}
	return event, nil
}

// isUniqueViolation reports whether err is a SQLite uniqueness failure. The
// pure-Go driver does not export a typed error for this, so the message is the
// only signal available.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "UNIQUE CONSTRAINT FAILED")
}

// --- Dues rates ---

// ListRates returns every effective-year rate, newest year first.
func (s *Service) ListRates(ctx context.Context, p *authz.Principal) ([]sqlcgen.DuesRate, error) {
	if err := authz.Authorize(ctx, p, "dues.read", nil); err != nil {
		return nil, err
	}
	return s.Q.ListDuesRates(ctx)
}

// GetRate returns the rate for one year.
func (s *Service) GetRate(ctx context.Context, p *authz.Principal, year int64) (sqlcgen.DuesRate, error) {
	if err := authz.Authorize(ctx, p, "dues.read", nil); err != nil {
		return sqlcgen.DuesRate{}, err
	}
	return s.Q.GetDuesRate(ctx, year)
}

// SetRate creates or revises the rate for a year.
//
// expectedVersion == 0 means "create": it fails with ErrRateExists if the year
// already has a rate, so a create can never silently clobber one. A revision
// must present the version it read, and a mismatch returns db.ErrStale.
//
// A rate informs suggestions. It never validates a payment amount.
func (s *Service) SetRate(ctx context.Context, p *authz.Principal, year, amountCents int64, note string, expectedVersion int64, now time.Time) (sqlcgen.DuesRate, error) {
	if err := authz.Authorize(ctx, p, "dues.rate.manage", nil); err != nil {
		return sqlcgen.DuesRate{}, err
	}
	if amountCents < 0 {
		return sqlcgen.DuesRate{}, fmt.Errorf("dues: amount_cents must not be negative")
	}
	if now.IsZero() {
		now = time.Now()
	}
	setAt := now.UTC().Format(isoTimestamp)
	noteVal := sql.NullString{String: note, Valid: note != ""}

	if expectedVersion == 0 {
		rate, err := s.Q.InsertDuesRate(ctx, sqlcgen.InsertDuesRateParams{
			Year:        year,
			AmountCents: amountCents,
			Note:        noteVal,
			SetBy:       p.UserID,
			SetAt:       setAt,
		})
		if err != nil && isUniqueViolation(err) {
			return sqlcgen.DuesRate{}, ErrRateExists
		}
		return rate, err
	}

	rate, err := s.Q.UpdateDuesRate(ctx, sqlcgen.UpdateDuesRateParams{
		Year:        year,
		AmountCents: amountCents,
		Note:        noteVal,
		SetBy:       p.UserID,
		SetAt:       setAt,
		Version:     expectedVersion,
	})
	if errors.Is(err, sql.ErrNoRows) {
		// Either the year does not exist or the version is stale. Distinguish
		// the two so the caller can report the right thing.
		if _, getErr := s.Q.GetDuesRate(ctx, year); errors.Is(getErr, sql.ErrNoRows) {
			return sqlcgen.DuesRate{}, sql.ErrNoRows
		}
		return sqlcgen.DuesRate{}, db.ErrStale
	}
	return rate, err
}

// --- Suggestions ---

// Suggestion is one non-binding choice a client may offer the treasurer. The
// client submits whichever paid-through date the treasurer actually chose, and
// a typed value is never silently replaced by one of these.
type Suggestion struct {
	// PaidThrough is the coverage date this choice would grant.
	PaidThrough  string
	Label        string
	YearsCovered int
	// AmountCents is the rate-derived total. It is a display hint only; no
	// rule binds this amount to this date.
	AmountCents int64
	// RateKnown is false when any covered year has no rate on file, in which
	// case AmountCents is zero and the treasurer supplies the amount.
	RateKnown   bool
	Explanation string
}

// Suggestions is the response envelope. Binding is always false: it exists so
// that a client cannot mistake this for validation.
type Suggestions struct {
	AsOf    string
	Binding bool
	Choices []Suggestion
	Note    string
}

// SuggestFor returns calendar- and rate-based choices as of a date.
//
// It reads rates and computes dates. It mutates nothing, and it never asserts
// that a particular amount buys a particular date.
func (s *Service) SuggestFor(ctx context.Context, p *authz.Principal, asOf time.Time) (Suggestions, error) {
	if err := authz.Authorize(ctx, p, "dues.read", nil); err != nil {
		return Suggestions{}, err
	}
	if asOf.IsZero() {
		asOf = time.Now()
	}
	asOf = asOf.UTC()
	year := asOf.Year()

	rates := map[int]int64{}
	known := map[int]bool{}
	all, err := s.Q.ListDuesRates(ctx)
	if err != nil {
		return Suggestions{}, err
	}
	for _, r := range all {
		rates[int(r.Year)] = r.AmountCents
		known[int(r.Year)] = true
	}

	// Sum the rates for the years a choice covers, starting from the current
	// calendar year. A missing year makes the whole total unknown rather than
	// quietly understating it.
	total := func(through int) (int64, bool) {
		var sum int64
		for y := year; y <= through; y++ {
			if !known[y] {
				return 0, false
			}
			sum += rates[y]
		}
		return sum, true
	}

	out := Suggestions{
		AsOf:    asOf.Format(ISODate),
		Binding: false,
		Note: "Suggestions are display hints. Submit the paid-through date the " +
			"treasurer chose; the server does not derive it from the amount.",
	}

	for _, spec := range []struct {
		through int
		label   string
		explain string
	}{
		{year, fmt.Sprintf("Through the end of %d", year),
			fmt.Sprintf("Covers the remainder of the %d dues year.", year)},
		{year + 1, fmt.Sprintf("Through the end of %d", year+1),
			fmt.Sprintf("Covers %d and %d.", year, year+1)},
		{year + 2, fmt.Sprintf("Through the end of %d", year+2),
			fmt.Sprintf("Covers %d through %d.", year, year+2)},
	} {
		amount, rateKnown := total(spec.through)
		explanation := spec.explain
		if !rateKnown {
			explanation += " No rate is on file for every year covered, so the treasurer supplies the amount."
		}
		out.Choices = append(out.Choices, Suggestion{
			PaidThrough:  fmt.Sprintf("%d-12-31", spec.through),
			Label:        spec.label,
			YearsCovered: spec.through - year + 1,
			AmountCents:  amount,
			RateKnown:    rateKnown,
			Explanation:  explanation,
		})
	}
	return out, nil
}
