// Package worksheets builds the treasurer's printable renewal sheet.
//
// The printed sheet is a supported workflow, not decoration. A treasurer prints
// it, carries it to a meeting, writes on it, and enters the results later. For
// that to work the sheet must be reproducible months afterwards, so a run
// records what was asked for and its rows record what was printed, in the order
// it printed. Nothing here is a live view.
package worksheets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/domain/dues"
	"github.com/bcars/bcars-portal/internal/domain/idem"
)

// Filter kinds.
const (
	// FilterOwes selects everyone whose dues are not currently covered.
	FilterOwes = "owes"
	// FilterActive selects every active membership, covered or not.
	FilterActive = "active"
	// FilterUnpaidSinceRun selects the rows of an earlier sheet that still have
	// no payment posted against them.
	FilterUnpaidSinceRun = "unpaid_since_run"
)

// Sort orders.
const (
	SortLastName       = "last_name"
	SortCallSign       = "call_sign"
	SortLongestOverdue = "longest_overdue"
)

// DefaultLimit caps an unbounded listing.
const DefaultLimit = 50

// MaxRows bounds one sheet. A worksheet is paper; a run that would print
// thousands of pages is a mistake worth refusing.
const MaxRows = 2000

var (
	// ErrUnknownFilter is returned for a filter kind outside the three the
	// design defines.
	ErrUnknownFilter = errors.New("worksheets: unknown filter")
	// ErrUnknownSort is returned for an unsupported sort order.
	ErrUnknownSort = errors.New("worksheets: unknown sort order")
	// ErrSourceRunRequired is returned when a follow-up sheet names no prior run.
	ErrSourceRunRequired = errors.New("worksheets: unpaid_since_run needs a source run")
	// ErrTooManyRows is returned when a filter would print an unreasonable sheet.
	ErrTooManyRows = errors.New("worksheets: too many rows for one worksheet; narrow the filter")
	// ErrBatchNotEmpty is returned when seeding a batch that already has rows.
	ErrBatchNotEmpty = errors.New("worksheets: that batch already has entries")
)

const isoDate = "2006-01-02"
const isoTimestamp = "2006-01-02T15:04:05.000Z"

// Service creates and reads worksheet runs.
type Service struct {
	DB   *sql.DB
	Q    *sqlcgen.Queries
	dues *dues.Service
}

// NewService creates a worksheet service.
func NewService(database *sql.DB) *Service {
	return &Service{DB: database, Q: sqlcgen.New(database), dues: dues.NewService(database)}
}

// Run is a saved worksheet request.
type Run struct {
	ID           int64
	Label        string
	AsOf         string
	FilterKind   string
	SourceRunID  int64
	SortOrder    string
	IncludeEmail bool
	IncludePhone bool
	WarningDays  int64
	GeneratedBy  int64
	GeneratedAt  string
	RowCount     int64
}

// Row is one member as printed.
type Row struct {
	ID           int64
	RunID        int64
	Ordinal      int64
	MembershipID int64
	DisplayName  string
	CallSign     string
	BaseType     string
	DuesStatus   string
	PaidThrough  string
	Email        string
	Phone        string
	// EnteredSince is computed at read time: a payment has been posted for this
	// member since the sheet was generated. The snapshot is never rewritten to
	// record it, so an old sheet still says what it said.
	EnteredSince bool
}

// CreateParams describes a worksheet request.
type CreateParams struct {
	Label       string
	AsOf        time.Time
	FilterKind  string
	SourceRunID int64
	SortOrder   string
	// IncludeEmail and IncludePhone are requests, not grants. Contact columns
	// are authorized server-side and dropped when the caller may not read them.
	IncludeEmail bool
	IncludePhone bool
	WarningDays  int
}

func (p CreateParams) validate() error {
	switch p.FilterKind {
	case FilterOwes, FilterActive:
	case FilterUnpaidSinceRun:
		if p.SourceRunID == 0 {
			return ErrSourceRunRequired
		}
	default:
		return fmt.Errorf("%w: %q", ErrUnknownFilter, p.FilterKind)
	}
	switch p.SortOrder {
	case SortLastName, SortCallSign, SortLongestOverdue:
	default:
		return fmt.Errorf("%w: %q", ErrUnknownSort, p.SortOrder)
	}
	return nil
}

// Create generates and stores a worksheet run.
func (s *Service) Create(ctx context.Context, p *authz.Principal, params CreateParams, now time.Time) (Run, []Row, error) {
	if err := authz.Authorize(ctx, p, "dues.worksheet.manage", nil); err != nil {
		return Run{}, nil, err
	}
	if err := params.validate(); err != nil {
		return Run{}, nil, err
	}
	if now.IsZero() {
		now = time.Now()
	}
	asOf := params.AsOf
	if asOf.IsZero() {
		asOf = now
	}
	warningDays := params.WarningDays
	if warningDays <= 0 {
		warningDays = dues.DefaultWarningDays
	}

	// Contact columns are a server decision. A caller who cannot read member
	// contact data does not get it by asking for it on a worksheet.
	canReadContact := authz.Authorize(ctx, p, "member.read", nil) == nil
	includeEmail := params.IncludeEmail && canReadContact
	includePhone := params.IncludePhone && canReadContact

	standings, err := s.selectStandings(ctx, p, params, asOf, warningDays)
	if err != nil {
		return Run{}, nil, err
	}
	if len(standings) > MaxRows {
		return Run{}, nil, fmt.Errorf("%w: %d rows", ErrTooManyRows, len(standings))
	}
	sortStandings(standings, params.SortOrder)

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.Q.WithTx(tx)

	runRow, err := qtx.CreateWorksheetRun(ctx, sqlcgen.CreateWorksheetRunParams{
		Label:        nullString(params.Label),
		AsOf:         asOf.UTC().Format(isoDate),
		FilterKind:   params.FilterKind,
		SourceRunID:  nullInt(params.SourceRunID),
		SortOrder:    params.SortOrder,
		IncludeEmail: boolInt(includeEmail),
		IncludePhone: boolInt(includePhone),
		WarningDays:  int64(warningDays),
		GeneratedBy:  p.UserID,
		GeneratedAt:  now.UTC().Format(isoTimestamp),
	})
	if err != nil {
		return Run{}, nil, err
	}

	rows := make([]Row, 0, len(standings))
	for i, st := range standings {
		var email, phone string
		if includeEmail || includePhone {
			contact, err := qtx.GetPrimaryContact(ctx, st.PersonID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return Run{}, nil, err
			}
			if includeEmail {
				email = contact.Email
			}
			if includePhone {
				phone = contact.Phone
			}
		}

		created, err := qtx.CreateWorksheetRow(ctx, sqlcgen.CreateWorksheetRowParams{
			RunID:        runRow.ID,
			Ordinal:      int64(i + 1),
			MembershipID: st.MembershipID,
			DisplayName:  st.DisplayName,
			CallSign:     nullString(st.CallSign),
			BaseType:     st.BaseType,
			DuesStatus:   st.Status,
			PaidThrough:  nullString(st.PaidThrough),
			Email:        nullString(email),
			Phone:        nullString(phone),
		})
		if err != nil {
			return Run{}, nil, err
		}
		rows = append(rows, rowFromRecord(created))
	}

	if err := qtx.SetWorksheetRunRowCount(ctx, sqlcgen.SetWorksheetRunRowCountParams{
		RowCount: int64(len(rows)),
		ID:       runRow.ID,
	}); err != nil {
		return Run{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, nil, err
	}

	run := runFromRow(runRow)
	run.RowCount = int64(len(rows))
	return run, rows, nil
}

// selectStandings resolves the memberships a run should contain.
func (s *Service) selectStandings(ctx context.Context, p *authz.Principal, params CreateParams, asOf time.Time, warningDays int) ([]dues.Standing, error) {
	if params.FilterKind == FilterUnpaidSinceRun {
		if _, err := s.Q.GetWorksheetRun(ctx, params.SourceRunID); err != nil {
			return nil, err
		}
		ids, err := s.Q.ListWorksheetMembershipsUnpaidSince(ctx, params.SourceRunID)
		if err != nil {
			return nil, err
		}
		out := make([]dues.Standing, 0, len(ids))
		for _, id := range ids {
			st, err := s.dues.GetStanding(ctx, p, id, asOf, warningDays)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			out = append(out, st)
		}
		return out, nil
	}

	all, err := s.dues.ListStanding(ctx, p, dues.StandingQuery{
		AsOf:        asOf,
		WarningDays: warningDays,
		Limit:       MaxRows + 1,
	})
	if err != nil {
		return nil, err
	}
	if params.FilterKind == FilterActive {
		return all, nil
	}

	// FilterOwes: everyone not currently covered. A waived member owes nothing
	// and an expiring member has already paid, so neither belongs on a sheet
	// whose purpose is collecting money.
	owing := make([]dues.Standing, 0, len(all))
	for _, st := range all {
		if st.Status == dues.StatusExpired || st.Status == dues.StatusUnknown {
			owing = append(owing, st)
		}
	}
	return owing, nil
}

// sortStandings applies the requested print order. The order is stored on the
// run, so a batch created later can reuse it and the paper and the grid stay in
// step. Every comparison falls back to membership id so the order is total.
func sortStandings(rows []dues.Standing, order string) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		switch order {
		case SortCallSign:
			// Members without a call sign sort last rather than clustering at
			// the top under the empty string.
			ac, bc := a.CallSign == "", b.CallSign == ""
			if ac != bc {
				return bc
			}
			if a.CallSign != b.CallSign {
				return a.CallSign < b.CallSign
			}
		case SortLongestOverdue:
			// Empty paid-through means nothing was ever recorded, which is the
			// most overdue case there is.
			ae, be := a.PaidThrough == "", b.PaidThrough == ""
			if ae != be {
				return ae
			}
			if a.PaidThrough != b.PaidThrough {
				return a.PaidThrough < b.PaidThrough
			}
		default: // SortLastName
			an, bn := sortKey(a.DisplayName), sortKey(b.DisplayName)
			if an != bn {
				return an < bn
			}
		}
		return a.MembershipID < b.MembershipID
	})
}

// sortKey approximates a last-name key from a display name.
func sortKey(name string) string {
	fields := strings.Fields(strings.ToLower(name))
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1] + " " + strings.Join(fields[:len(fields)-1], " ")
}

// Get returns a run.
func (s *Service) Get(ctx context.Context, p *authz.Principal, runID int64) (Run, error) {
	if err := authz.Authorize(ctx, p, "dues.worksheet.manage", nil); err != nil {
		return Run{}, err
	}
	row, err := s.Q.GetWorksheetRun(ctx, runID)
	if err != nil {
		return Run{}, err
	}
	return runFromRow(row), nil
}

// List returns saved runs, newest first.
func (s *Service) List(ctx context.Context, p *authz.Principal, limit, offset int64) ([]Run, error) {
	if err := authz.Authorize(ctx, p, "dues.worksheet.manage", nil); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	rows, err := s.Q.ListWorksheetRuns(ctx, sqlcgen.ListWorksheetRunsParams{Limit: limit, Offset: offset})
	if err != nil {
		return nil, err
	}
	out := make([]Run, len(rows))
	for i, r := range rows {
		out[i] = runFromRow(r)
	}
	return out, nil
}

// Rows returns a run's rows in print order, marking those a payment has been
// posted against since the sheet was generated.
func (s *Service) Rows(ctx context.Context, p *authz.Principal, runID, limit, offset int64) ([]Row, error) {
	if err := authz.Authorize(ctx, p, "dues.worksheet.manage", nil); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	rows, err := s.Q.ListWorksheetRows(ctx, sqlcgen.ListWorksheetRowsParams{
		RunID: runID, Limit: limit, Offset: offset,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Row, len(rows))
	for i, r := range rows {
		out[i] = Row{
			ID: r.RowID, RunID: r.RunID, Ordinal: r.Ordinal,
			MembershipID: r.MembershipID, DisplayName: r.DisplayName,
			CallSign: r.CallSign.String, BaseType: r.BaseType,
			DuesStatus: r.DuesStatus, PaidThrough: r.PaidThrough.String,
			Email: r.Email.String, Phone: r.Phone.String,
			EnteredSince: r.EnteredSince == 1,
		}
	}
	return out, nil
}

// OpBatchFromRun is the idempotent operation name for the worksheet handoff.
const OpBatchFromRun = "worksheet-batch-open"

// OpenBatchForRun creates a batch for a worksheet run and links the two in one
// transaction under one idempotency key.
//
// Doing it in two steps left two failure modes: a retried "Enter this sheet
// now" opened a second empty batch, and a link that failed after the batch was
// created left an orphan. Both are avoided by making creation and linkage the
// same operation.
//
// It creates no entries. Seeding the grid means seeding the ordered work queue,
// not payment rows: inventing an amount from a worksheet would be inventing a
// payment.
func (s *Service) OpenBatchForRun(ctx context.Context, p *authz.Principal, runID int64, label, idempotencyKey string, now time.Time) (int64, error) {
	if err := authz.Authorize(ctx, p, "payment.batch.manage", nil); err != nil {
		return 0, err
	}
	if now.IsZero() {
		now = time.Now()
	}

	run, err := s.Q.GetWorksheetRun(ctx, runID)
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(label) == "" {
		label = defaultBatchLabel(run, now)
	}
	requestHash := idem.Hash(strconv.FormatInt(runID, 10), label)

	var batchID int64
	err = s.inTx(ctx, func(qtx *sqlcgen.Queries) error {
		claim, err := idem.Begin(ctx, qtx, p.UserID, OpBatchFromRun, idempotencyKey, requestHash)
		if err != nil {
			return err
		}
		if claim.Replay {
			batchID = claim.ResourceID
			return nil
		}

		batch, err := qtx.CreatePaymentBatch(ctx, sqlcgen.CreatePaymentBatchParams{
			Label:    label,
			OpenedBy: p.UserID,
			OpenedAt: now.UTC().Format(isoTimestamp),
		})
		if err != nil {
			return err
		}
		if err := qtx.SetPaymentBatchWorksheetRun(ctx, sqlcgen.SetPaymentBatchWorksheetRunParams{
			WorksheetRunID: nullInt(runID),
			ID:             batch.ID,
		}); err != nil {
			return err
		}
		batchID = batch.ID
		return claim.Complete(ctx, qtx, "payment_batch", batch.ID)
	})
	return batchID, err
}

// defaultBatchLabel names a batch after the sheet it came from, so a treasurer
// recognises it in a list months later.
func defaultBatchLabel(run sqlcgen.DuesWorksheetRun, now time.Time) string {
	if run.Label.Valid && run.Label.String != "" {
		return run.Label.String
	}
	return fmt.Sprintf("Sheet %d, entered %s", run.ID, now.UTC().Format(isoDate))
}

// inTx runs fn in a transaction.
func (s *Service) inTx(ctx context.Context, fn func(*sqlcgen.Queries) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(s.Q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit()
}

// LinkBatch records that a batch was created from a run, so a later print can
// tell which lines have since been entered.
//
// It does not create entries: a treasurer types the amounts that were actually
// written on the paper, and inventing rows from a worksheet would be inventing
// payments. The link preserves the order the client should present.
func (s *Service) LinkBatch(ctx context.Context, p *authz.Principal, runID, batchID int64) error {
	if err := authz.Authorize(ctx, p, "payment.batch.manage", nil); err != nil {
		return err
	}
	if _, err := s.Q.GetWorksheetRun(ctx, runID); err != nil {
		return err
	}
	batch, err := s.Q.GetPaymentBatch(ctx, batchID)
	if err != nil {
		return err
	}
	if batch.State != "open" {
		return fmt.Errorf("worksheets: batch %d is %s", batchID, batch.State)
	}
	totals, err := s.Q.GetPaymentBatchTotals(ctx, batchID)
	if err != nil {
		return err
	}
	if totals.EntryCount != 0 {
		return ErrBatchNotEmpty
	}
	return s.Q.SetPaymentBatchWorksheetRun(ctx, sqlcgen.SetPaymentBatchWorksheetRunParams{
		WorksheetRunID: nullInt(runID),
		ID:             batchID,
	})
}

func runFromRow(r sqlcgen.DuesWorksheetRun) Run {
	return Run{
		ID:           r.ID,
		Label:        r.Label.String,
		AsOf:         r.AsOf,
		FilterKind:   r.FilterKind,
		SourceRunID:  r.SourceRunID.Int64,
		SortOrder:    r.SortOrder,
		IncludeEmail: r.IncludeEmail == 1,
		IncludePhone: r.IncludePhone == 1,
		WarningDays:  r.WarningDays,
		GeneratedBy:  r.GeneratedBy,
		GeneratedAt:  r.GeneratedAt,
		RowCount:     r.RowCount,
	}
}

func rowFromRecord(r sqlcgen.DuesWorksheetRow) Row {
	return Row{
		ID: r.ID, RunID: r.RunID, Ordinal: r.Ordinal,
		MembershipID: r.MembershipID, DisplayName: r.DisplayName,
		CallSign: r.CallSign.String, BaseType: r.BaseType,
		DuesStatus: r.DuesStatus, PaidThrough: r.PaidThrough.String,
		Email: r.Email.String, Phone: r.Phone.String,
	}
}

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func nullInt(v int64) sql.NullInt64 {
	return sql.NullInt64{Int64: v, Valid: v != 0}
}

func boolInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}
