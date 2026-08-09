// Package batches implements the draft side of the treasurer's ledger: an open
// payment batch and its mutable entries.
//
// The defining property of this package is that nothing in it changes anyone's
// dues standing. A draft batch writes no payment and no coverage event; it is
// inert until posted. That is what makes a half-finished batch safe to leave on
// the treasurer's screen overnight.
package batches

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bcars/bcars-portal/internal/db"
	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/domain/idem"
)

// Batch states. `posted` and `abandoned` are terminal.
const (
	StateOpen      = "open"
	StatePosted    = "posted"
	StateAbandoned = "abandoned"
)

// Accepted payment methods. Phase 2 adds no online processor.
const (
	MethodCash  = "cash"
	MethodCheck = "check"
	MethodOther = "other"
)

// ISODate is the date layout used throughout the ledger.
const ISODate = "2006-01-02"

const isoTimestamp = "2006-01-02T15:04:05.000Z"

// DefaultLimit caps an unbounded batch listing.
const DefaultLimit = 50

// Idempotent operation names, scoped per actor in idempotency_records.
const (
	OpBatchCreate = "payment-batch-create"
	OpEntryCreate = "payment-batch-entry-create"
)

var (
	// ErrBatchNotOpen is returned for any mutation aimed at a posted or
	// abandoned batch. Both are terminal by design: the ledger's history must
	// not change under a report that already cited it.
	ErrBatchNotOpen = errors.New("batches: batch is not open")

	// ErrReasonRequired is returned when abandoning a batch without saying why.
	ErrReasonRequired = errors.New("batches: a reason is required")

	// ErrLabelRequired is returned for a blank batch label.
	ErrLabelRequired = errors.New("batches: a label is required")

	// ErrInvalidAmount is returned for a non-positive draft amount. A reversal
	// is negative, but a reversal is never a draft entry.
	ErrInvalidAmount = errors.New("batches: amount_cents must be greater than zero")

	// ErrInvalidMethod is returned for a method outside cash, check, and other.
	ErrInvalidMethod = errors.New("batches: method must be cash, check, or other")

	// ErrInvalidDate is returned for a malformed received-on or paid-through
	// date. An off-cycle date is not malformed: the treasurer records what
	// happened, and the club year-end is a convention rather than a rule.
	ErrInvalidDate = errors.New("batches: dates must be ISO YYYY-MM-DD")

	// ErrEntryNotInBatch is returned when an entry id does not belong to the
	// batch named in the path.
	ErrEntryNotInBatch = errors.New("batches: entry does not belong to that batch")
)

// Service manages draft batches and their entries.
type Service struct {
	DB *sql.DB
	Q  *sqlcgen.Queries
}

// NewService creates a batch service over database.
func NewService(database *sql.DB) *Service {
	return &Service{DB: database, Q: sqlcgen.New(database)}
}

// Totals are always calculated by the server. A client never submits one, so a
// mis-adding spreadsheet can never disagree with the ledger.
type Totals struct {
	EntryCount      int64
	CashCount       int64
	CashTotalCents  int64
	CheckCount      int64
	CheckTotalCents int64
	OtherCount      int64
	OtherTotalCents int64
	NetTotalCents   int64
}

// Entry is one mutable draft row.
type Entry struct {
	ID                int64
	BatchID           int64
	MembershipID      int64
	Sequence          int64
	AmountCents       int64
	Method            string
	Reference         string
	ReceivedOn        string
	ReceivedByOfficer string
	PaidThrough       string
	TreasurerNote     string
	Version           int64
	CreatedAt         string
	UpdatedAt         string
}

// Batch is a draft batch with its server-calculated totals.
type Batch struct {
	ID     int64
	Label  string
	State  string
	Totals Totals

	// DefaultAmountCents and DefaultPaidThrough persist the values the grid
	// prefills into a new row. They are convenience for the client only: every
	// submitted entry still carries its own explicit values, so a typed value
	// is never silently replaced by a default.
	DefaultAmountCents int64
	DefaultPaidThrough string

	OpenedByUserID int64
	OpenedAt       string

	PostedByUserID int64
	PostedAt       string

	AbandonedByUserID int64
	AbandonedAt       string
	AbandonReason     string

	// WorksheetRunID names the renewal sheet this batch was opened from, when
	// it was. A consumer that ignores it cannot show the treasurer the order
	// the paper is in, which is the only reason the link exists.
	WorksheetRunID int64

	Version   int64
	CreatedAt string
	UpdatedAt string

	// Entries is populated by Get, not by List.
	Entries []Entry
}

// EntryInput carries the explicit values for a draft row.
type EntryInput struct {
	MembershipID      int64
	AmountCents       int64
	Method            string
	Reference         string
	ReceivedOn        string
	ReceivedByOfficer string
	PaidThrough       string
	TreasurerNote     string
}

func (e EntryInput) validate() error {
	if e.AmountCents <= 0 {
		return ErrInvalidAmount
	}
	switch e.Method {
	case MethodCash, MethodCheck, MethodOther:
	default:
		return fmt.Errorf("%w: got %q", ErrInvalidMethod, e.Method)
	}
	if _, err := time.Parse(ISODate, e.ReceivedOn); err != nil {
		return fmt.Errorf("%w: received_on %q", ErrInvalidDate, e.ReceivedOn)
	}
	if _, err := time.Parse(ISODate, e.PaidThrough); err != nil {
		return fmt.Errorf("%w: paid_through %q", ErrInvalidDate, e.PaidThrough)
	}
	return nil
}

// hash fingerprints an entry for idempotent retry.
func (e EntryInput) hash(batchID int64) string {
	return idem.Hash(
		strconv.FormatInt(batchID, 10),
		strconv.FormatInt(e.MembershipID, 10),
		strconv.FormatInt(e.AmountCents, 10),
		e.Method, e.Reference, e.ReceivedOn, e.ReceivedByOfficer,
		e.PaidThrough, e.TreasurerNote,
	)
}

// --- Batch lifecycle ---

// OpenParams describes a new batch.
type OpenParams struct {
	Label              string
	DefaultAmountCents int64
	DefaultPaidThrough string
	IdempotencyKey     string
}

// Open creates an open batch.
//
// "Save and finish later" needs no operation: every mutation to an open batch
// is already persisted, so closing the browser loses nothing.
func (s *Service) Open(ctx context.Context, p *authz.Principal, params OpenParams, now time.Time) (Batch, error) {
	if err := authz.Authorize(ctx, p, "payment.batch.manage", nil); err != nil {
		return Batch{}, err
	}
	if strings.TrimSpace(params.Label) == "" {
		return Batch{}, ErrLabelRequired
	}
	if params.DefaultAmountCents < 0 {
		return Batch{}, ErrInvalidAmount
	}
	if params.DefaultPaidThrough != "" {
		if _, err := time.Parse(ISODate, params.DefaultPaidThrough); err != nil {
			return Batch{}, fmt.Errorf("%w: default_paid_through %q", ErrInvalidDate, params.DefaultPaidThrough)
		}
	}
	if now.IsZero() {
		now = time.Now()
	}

	requestHash := idem.Hash(params.Label,
		strconv.FormatInt(params.DefaultAmountCents, 10), params.DefaultPaidThrough)

	var out Batch
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		claim, err := idem.Begin(ctx, q, p.UserID, OpBatchCreate, params.IdempotencyKey, requestHash)
		if err != nil {
			return err
		}
		if claim.Replay {
			row, err := q.GetPaymentBatch(ctx, claim.ResourceID)
			if err != nil {
				return err
			}
			out, err = s.assemble(ctx, q, row, false)
			return err
		}

		row, err := q.CreatePaymentBatch(ctx, sqlcgen.CreatePaymentBatchParams{
			Label:              params.Label,
			DefaultAmountCents: nullInt(params.DefaultAmountCents),
			DefaultPaidThrough: nullString(params.DefaultPaidThrough),
			OpenedBy:           p.UserID,
			OpenedAt:           now.UTC().Format(isoTimestamp),
		})
		if err != nil {
			return err
		}
		if err := claim.Complete(ctx, q, "payment_batch", row.ID); err != nil {
			return err
		}
		out, err = s.assemble(ctx, q, row, false)
		return err
	})
	return out, err
}

// Get returns one batch with its entries and server-calculated totals.
func (s *Service) Get(ctx context.Context, p *authz.Principal, id int64) (Batch, error) {
	if err := authz.Authorize(ctx, p, "payment.read", nil); err != nil {
		return Batch{}, err
	}
	row, err := s.Q.GetPaymentBatch(ctx, id)
	if err != nil {
		return Batch{}, err
	}
	return s.assemble(ctx, s.Q, row, true)
}

// List returns batches, newest first, optionally filtered by state.
func (s *Service) List(ctx context.Context, p *authz.Principal, state string, limit, offset int64) ([]Batch, error) {
	if err := authz.Authorize(ctx, p, "payment.read", nil); err != nil {
		return nil, err
	}
	switch state {
	case "", StateOpen, StatePosted, StateAbandoned:
	default:
		return nil, fmt.Errorf("batches: unknown state %q", state)
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	rows, err := s.Q.ListPaymentBatches(ctx, sqlcgen.ListPaymentBatchesParams{
		StateFilter: state,
		Lim:         limit,
		Off:         offset,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Batch, len(rows))
	for i, row := range rows {
		b, err := s.assemble(ctx, s.Q, row, false)
		if err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}

// UpdateParams changes an open batch's label and persisted new-row defaults.
type UpdateParams struct {
	Label              string
	DefaultAmountCents int64
	DefaultPaidThrough string
	ExpectedVersion    int64
}

// Update changes an open batch's metadata and defaults.
func (s *Service) Update(ctx context.Context, p *authz.Principal, id int64, params UpdateParams) (Batch, error) {
	if err := authz.Authorize(ctx, p, "payment.batch.manage", nil); err != nil {
		return Batch{}, err
	}
	if strings.TrimSpace(params.Label) == "" {
		return Batch{}, ErrLabelRequired
	}
	if params.DefaultAmountCents < 0 {
		return Batch{}, ErrInvalidAmount
	}
	if params.DefaultPaidThrough != "" {
		if _, err := time.Parse(ISODate, params.DefaultPaidThrough); err != nil {
			return Batch{}, fmt.Errorf("%w: default_paid_through %q", ErrInvalidDate, params.DefaultPaidThrough)
		}
	}

	var out Batch
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := requireOpen(ctx, q, id); err != nil {
			return err
		}
		row, err := q.UpdatePaymentBatchDefaults(ctx, sqlcgen.UpdatePaymentBatchDefaultsParams{
			Label:              params.Label,
			DefaultAmountCents: nullInt(params.DefaultAmountCents),
			DefaultPaidThrough: nullString(params.DefaultPaidThrough),
			ID:                 id,
			Version:            params.ExpectedVersion,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return db.ErrStale
		}
		if err != nil {
			return err
		}
		out, err = s.assemble(ctx, q, row, true)
		return err
	})
	return out, err
}

// Abandon closes an open batch without posting it. It is terminal and audited,
// and the reason is required so the history explains itself later.
func (s *Service) Abandon(ctx context.Context, p *authz.Principal, id int64, reason string, expectedVersion int64, now time.Time) (Batch, error) {
	if err := authz.Authorize(ctx, p, "payment.batch.manage", nil); err != nil {
		return Batch{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return Batch{}, ErrReasonRequired
	}
	if now.IsZero() {
		now = time.Now()
	}

	var out Batch
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := requireOpen(ctx, q, id); err != nil {
			return err
		}
		row, err := q.MarkPaymentBatchAbandoned(ctx, sqlcgen.MarkPaymentBatchAbandonedParams{
			AbandonedBy:   sql.NullInt64{Int64: p.UserID, Valid: true},
			AbandonedAt:   sql.NullString{String: now.UTC().Format(isoTimestamp), Valid: true},
			AbandonReason: nullString(reason),
			ID:            id,
			Version:       expectedVersion,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return db.ErrStale
		}
		if err != nil {
			return err
		}
		out, err = s.assemble(ctx, q, row, true)
		return err
	})
	return out, err
}

// --- Entries ---

// AddEntry appends a draft row and bumps the batch version.
//
// The batch version moves on every entry mutation so that a browser holding a
// stale batch ETag cannot post a batch whose rows changed under it.
func (s *Service) AddEntry(ctx context.Context, p *authz.Principal, batchID int64, in EntryInput, idempotencyKey string) (Entry, Batch, error) {
	if err := authz.Authorize(ctx, p, "payment.batch.manage", nil); err != nil {
		return Entry{}, Batch{}, err
	}
	if err := in.validate(); err != nil {
		return Entry{}, Batch{}, err
	}

	var (
		entry Entry
		batch Batch
	)
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := requireOpen(ctx, q, batchID); err != nil {
			return err
		}
		if _, err := q.GetMembership(ctx, in.MembershipID); err != nil {
			return err
		}

		claim, err := idem.Begin(ctx, q, p.UserID, OpEntryCreate, idempotencyKey, in.hash(batchID))
		if err != nil {
			return err
		}
		if claim.Replay {
			row, err := q.GetPaymentBatchEntry(ctx, claim.ResourceID)
			if err != nil {
				return err
			}
			entry = entryFromRow(row)
			batch, err = s.load(ctx, q, batchID, true)
			return err
		}

		seq, err := q.NextPaymentBatchEntrySequence(ctx, batchID)
		if err != nil {
			return err
		}
		row, err := q.CreatePaymentBatchEntry(ctx, sqlcgen.CreatePaymentBatchEntryParams{
			BatchID:           batchID,
			MembershipID:      in.MembershipID,
			Sequence:          seq,
			AmountCents:       in.AmountCents,
			Method:            in.Method,
			Reference:         nullString(in.Reference),
			ReceivedOn:        in.ReceivedOn,
			ReceivedByOfficer: nullString(in.ReceivedByOfficer),
			PaidThrough:       in.PaidThrough,
			TreasurerNote:     nullString(in.TreasurerNote),
		})
		if err != nil {
			return err
		}
		if err := claim.Complete(ctx, q, "payment_batch_entry", row.ID); err != nil {
			return err
		}
		if _, err := q.TouchPaymentBatch(ctx, batchID); err != nil {
			return err
		}
		entry = entryFromRow(row)
		batch, err = s.load(ctx, q, batchID, true)
		return err
	})
	return entry, batch, err
}

// UpdateEntry edits a draft row in place. Only open batches allow this; a
// posted payment is corrected, never edited.
func (s *Service) UpdateEntry(ctx context.Context, p *authz.Principal, batchID, entryID int64, in EntryInput, expectedVersion int64) (Entry, Batch, error) {
	if err := authz.Authorize(ctx, p, "payment.batch.manage", nil); err != nil {
		return Entry{}, Batch{}, err
	}
	if err := in.validate(); err != nil {
		return Entry{}, Batch{}, err
	}

	var (
		entry Entry
		batch Batch
	)
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := requireOpen(ctx, q, batchID); err != nil {
			return err
		}
		existing, err := q.GetPaymentBatchEntry(ctx, entryID)
		if err != nil {
			return err
		}
		if existing.BatchID != batchID {
			return ErrEntryNotInBatch
		}
		if _, err := q.GetMembership(ctx, in.MembershipID); err != nil {
			return err
		}

		row, err := q.UpdatePaymentBatchEntry(ctx, sqlcgen.UpdatePaymentBatchEntryParams{
			MembershipID:      in.MembershipID,
			AmountCents:       in.AmountCents,
			Method:            in.Method,
			Reference:         nullString(in.Reference),
			ReceivedOn:        in.ReceivedOn,
			ReceivedByOfficer: nullString(in.ReceivedByOfficer),
			PaidThrough:       in.PaidThrough,
			TreasurerNote:     nullString(in.TreasurerNote),
			ID:                entryID,
			Version:           expectedVersion,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return db.ErrStale
		}
		if err != nil {
			return err
		}
		if _, err := q.TouchPaymentBatch(ctx, batchID); err != nil {
			return err
		}
		entry = entryFromRow(row)
		batch, err = s.load(ctx, q, batchID, true)
		return err
	})
	return entry, batch, err
}

// DeleteEntry removes a draft row. Remaining sequences are left alone so the
// order a treasurer already read off a printed sheet does not shift.
func (s *Service) DeleteEntry(ctx context.Context, p *authz.Principal, batchID, entryID, expectedVersion int64) (Batch, error) {
	if err := authz.Authorize(ctx, p, "payment.batch.manage", nil); err != nil {
		return Batch{}, err
	}

	var batch Batch
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := requireOpen(ctx, q, batchID); err != nil {
			return err
		}
		existing, err := q.GetPaymentBatchEntry(ctx, entryID)
		if err != nil {
			return err
		}
		if existing.BatchID != batchID {
			return ErrEntryNotInBatch
		}
		res, err := q.DeletePaymentBatchEntry(ctx, sqlcgen.DeletePaymentBatchEntryParams{
			ID:      entryID,
			Version: expectedVersion,
		})
		if err := db.CheckVersion(res, err); err != nil {
			return err
		}
		if _, err := q.TouchPaymentBatch(ctx, batchID); err != nil {
			return err
		}
		batch, err = s.load(ctx, q, batchID, true)
		return err
	})
	return batch, err
}

// --- Internals ---

// requireOpen rejects a mutation aimed at a terminal batch, and reports a
// missing batch as sql.ErrNoRows.
func requireOpen(ctx context.Context, q *sqlcgen.Queries, id int64) error {
	row, err := q.GetPaymentBatch(ctx, id)
	if err != nil {
		return err
	}
	if row.State != StateOpen {
		return fmt.Errorf("%w: state is %s", ErrBatchNotOpen, row.State)
	}
	return nil
}

// inTx runs fn inside a transaction so an entry write and the batch version
// bump commit together or not at all.
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

func (s *Service) load(ctx context.Context, q *sqlcgen.Queries, id int64, withEntries bool) (Batch, error) {
	row, err := q.GetPaymentBatch(ctx, id)
	if err != nil {
		return Batch{}, err
	}
	return s.assemble(ctx, q, row, withEntries)
}

func (s *Service) assemble(ctx context.Context, q *sqlcgen.Queries, row sqlcgen.PaymentBatch, withEntries bool) (Batch, error) {
	totals, err := q.GetPaymentBatchTotals(ctx, row.ID)
	if err != nil {
		return Batch{}, err
	}
	b := Batch{
		ID:                 row.ID,
		Label:              row.Label,
		State:              row.State,
		DefaultAmountCents: row.DefaultAmountCents.Int64,
		DefaultPaidThrough: row.DefaultPaidThrough.String,
		OpenedByUserID:     row.OpenedBy,
		OpenedAt:           row.OpenedAt,
		PostedByUserID:     row.PostedBy.Int64,
		PostedAt:           row.PostedAt.String,
		AbandonedByUserID:  row.AbandonedBy.Int64,
		AbandonedAt:        row.AbandonedAt.String,
		AbandonReason:      row.AbandonReason.String,
		WorksheetRunID:     row.WorksheetRunID.Int64,
		Version:            row.Version,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
		Totals: Totals{
			EntryCount:      totals.EntryCount,
			CashCount:       totals.CashCount,
			CashTotalCents:  totals.CashTotalCents,
			CheckCount:      totals.CheckCount,
			CheckTotalCents: totals.CheckTotalCents,
			OtherCount:      totals.OtherCount,
			OtherTotalCents: totals.OtherTotalCents,
			NetTotalCents:   totals.NetTotalCents,
		},
	}
	if withEntries {
		rows, err := q.ListPaymentBatchEntries(ctx, row.ID)
		if err != nil {
			return Batch{}, err
		}
		b.Entries = make([]Entry, len(rows))
		for i, e := range rows {
			b.Entries[i] = entryFromRow(e)
		}
	}
	return b, nil
}

func entryFromRow(e sqlcgen.PaymentBatchEntry) Entry {
	return Entry{
		ID:                e.ID,
		BatchID:           e.BatchID,
		MembershipID:      e.MembershipID,
		Sequence:          e.Sequence,
		AmountCents:       e.AmountCents,
		Method:            e.Method,
		Reference:         e.Reference.String,
		ReceivedOn:        e.ReceivedOn,
		ReceivedByOfficer: e.ReceivedByOfficer.String,
		PaidThrough:       e.PaidThrough,
		TreasurerNote:     e.TreasurerNote.String,
		Version:           e.Version,
		CreatedAt:         e.CreatedAt,
		UpdatedAt:         e.UpdatedAt,
	}
}

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func nullInt(v int64) sql.NullInt64 {
	return sql.NullInt64{Int64: v, Valid: v != 0}
}
