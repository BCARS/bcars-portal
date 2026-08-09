package treasury

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/authz"
)

// DefaultLimit caps an unbounded history page.
const DefaultLimit = 50

// ExportMaxRows bounds a single export so one request cannot try to render the
// whole ledger into memory. An export that hits it says so rather than
// truncating silently.
const ExportMaxRows = 10000

// ErrExportTooLarge is returned when a filter selects more rows than one export
// may carry. Silently returning the first ten thousand rows of the books would
// be worse than refusing.
var ErrExportTooLarge = errors.New("treasury: too many rows for one export; narrow the filters")

// Service reads treasury history and renders exports.
type Service struct {
	DB *sql.DB
	Q  *sqlcgen.Queries
}

// NewService creates a treasury reporting service.
func NewService(database *sql.DB) *Service {
	return &Service{DB: database, Q: sqlcgen.New(database)}
}

// LedgerEntry is one posted payment as the books show it.
type LedgerEntry struct {
	PaymentID         int64
	MembershipID      int64
	BatchID           int64
	DisplayName       string
	CallSign          string
	AmountCents       int64
	Method            string
	Reference         string
	ReceivedOn        string
	ReceivedByOfficer string
	EnteredByUserID   int64
	EnteredAt         string
	ReceiptCode       string
	EntryKind         string
	CorrectsPaymentID int64
	TreasurerNote     string
	// Superseded is true when a correction has already replaced this row.
	Superseded bool
}

// LedgerQuery filters the books view. Zero and empty values mean "no filter".
type LedgerQuery struct {
	MembershipID int64
	BatchID      int64
	Method       string
	ReceiptCode  string
	ReceivedFrom string
	ReceivedTo   string
	// EffectiveOnly hides reversals and superseded originals, leaving what the
	// club currently holds.
	EffectiveOnly bool
	Limit         int64
	Offset        int64
}

// Filters describes the query for an export header, in a stable order.
func (q LedgerQuery) Filters() []Filter {
	view := "all entries including reversals"
	if q.EffectiveOnly {
		view = "effective entries only"
	}
	out := []Filter{{Name: "view", Value: view}}
	if q.MembershipID != 0 {
		out = append(out, Filter{Name: "membership_id", Value: strconv.FormatInt(q.MembershipID, 10)})
	}
	if q.BatchID != 0 {
		out = append(out, Filter{Name: "batch_id", Value: strconv.FormatInt(q.BatchID, 10)})
	}
	if q.Method != "" {
		out = append(out, Filter{Name: "method", Value: q.Method})
	}
	if q.ReceiptCode != "" {
		out = append(out, Filter{Name: "receipt_code", Value: q.ReceiptCode})
	}
	if q.ReceivedFrom != "" {
		out = append(out, Filter{Name: "received_from", Value: q.ReceivedFrom})
	}
	if q.ReceivedTo != "" {
		out = append(out, Filter{Name: "received_to", Value: q.ReceivedTo})
	}
	return out
}

func (q LedgerQuery) params(limit, offset int64) sqlcgen.ListLedgerPaymentsParams {
	effective := int64(0)
	if q.EffectiveOnly {
		effective = 1
	}
	return sqlcgen.ListLedgerPaymentsParams{
		MembershipID:  q.MembershipID,
		BatchID:       q.BatchID,
		Method:        q.Method,
		ReceiptCode:   q.ReceiptCode,
		ReceivedFrom:  q.ReceivedFrom,
		ReceivedTo:    q.ReceivedTo,
		EffectiveOnly: effective,
		Lim:           limit,
		Off:           offset,
	}
}

// ListLedger returns posted payments, newest received first, with a stable tie
// break on id so paging cannot repeat or skip a row.
func (s *Service) ListLedger(ctx context.Context, p *authz.Principal, q LedgerQuery) ([]LedgerEntry, error) {
	if err := authz.Authorize(ctx, p, "payment.read", nil); err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	rows, err := s.Q.ListLedgerPayments(ctx, q.params(limit, q.Offset))
	if err != nil {
		return nil, err
	}
	out := make([]LedgerEntry, len(rows))
	for i, r := range rows {
		out[i] = ledgerEntryFromRow(r)
	}
	return out, nil
}

func ledgerEntryFromRow(r sqlcgen.ListLedgerPaymentsRow) LedgerEntry {
	return LedgerEntry{
		PaymentID:         r.PaymentID,
		MembershipID:      r.MembershipID,
		BatchID:           r.BatchID.Int64,
		DisplayName:       r.DisplayName,
		CallSign:          r.CallSign.String,
		AmountCents:       r.AmountCents,
		Method:            r.Method,
		Reference:         r.Reference.String,
		ReceivedOn:        r.ReceivedOn,
		ReceivedByOfficer: r.ReceivedByOfficer.String,
		EnteredByUserID:   r.EnteredBy,
		EnteredAt:         r.EnteredAt,
		ReceiptCode:       r.ReceiptCode,
		EntryKind:         r.EntryKind,
		CorrectsPaymentID: r.CorrectsPaymentID.Int64,
		TreasurerNote:     r.TreasurerNote.String,
		Superseded:        r.IsSuperseded == 1,
	}
}

// Receipt is the printable record of one payment. The code is stable and
// non-secret: it identifies the payment but grants no access to it, so it is
// safe to print on a slip a member walks away with.
type Receipt struct {
	ReceiptCode       string
	PaymentID         int64
	MembershipID      int64
	DisplayName       string
	CallSign          string
	BaseType          string
	AmountCents       int64
	Method            string
	Reference         string
	ReceivedOn        string
	ReceivedByOfficer string
	EnteredAt         string
	EntryKind         string
	BatchID           int64
	// PaidThrough is the coverage this payment granted, when it granted any.
	PaidThrough string
	// Superseded is true when a correction has replaced this payment; a
	// reprint of a corrected receipt must say so rather than look current.
	Superseded bool
}

// GetReceipt returns the printable record for one payment.
func (s *Service) GetReceipt(ctx context.Context, p *authz.Principal, paymentID int64) (Receipt, error) {
	if err := authz.Authorize(ctx, p, "payment.read", nil); err != nil {
		return Receipt{}, err
	}
	row, err := s.Q.GetPaymentWithMember(ctx, paymentID)
	if err != nil {
		return Receipt{}, err
	}

	receipt := Receipt{
		ReceiptCode:       row.ReceiptCode,
		PaymentID:         row.ID,
		MembershipID:      row.MembershipID,
		DisplayName:       row.DisplayName,
		CallSign:          row.CallSign.String,
		BaseType:          row.BaseType,
		AmountCents:       row.AmountCents,
		Method:            row.Method,
		Reference:         row.Reference.String,
		ReceivedOn:        row.ReceivedOn,
		ReceivedByOfficer: row.ReceivedByOfficer.String,
		EnteredAt:         row.EnteredAt,
		EntryKind:         row.EntryKind,
		BatchID:           row.BatchID.Int64,
	}

	if event, err := s.Q.GetCoverageEventByPayment(ctx, sql.NullInt64{Int64: paymentID, Valid: true}); err == nil {
		receipt.PaidThrough = event.PaidThrough
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, err
	}

	if _, err := s.Q.GetPaymentCorrectionByOriginal(ctx, paymentID); err == nil {
		receipt.Superseded = true
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, err
	}
	return receipt, nil
}

// ActivityEntry is one plain-language line in a batch's history. The wording is
// what an officer would say out loud, because the audience is an officer
// reading "what happened to this batch" rather than a developer reading a log.
type ActivityEntry struct {
	// Kind is "opened", "posted", "abandoned", or "corrected".
	Kind        string
	At          string
	ActorUserID int64
	// Summary is the human sentence.
	Summary string
	// Reason carries the officer's own words, when they gave any.
	Reason string
}

// BatchActivity returns the plain-language history of a batch.
func (s *Service) BatchActivity(ctx context.Context, p *authz.Principal, batchID int64) ([]ActivityEntry, error) {
	if err := authz.Authorize(ctx, p, "payment.read", nil); err != nil {
		return nil, err
	}
	batch, err := s.Q.GetPaymentBatch(ctx, batchID)
	if err != nil {
		return nil, err
	}

	out := []ActivityEntry{{
		Kind:        "opened",
		At:          batch.OpenedAt,
		ActorUserID: batch.OpenedBy,
		Summary:     fmt.Sprintf("Opened the batch %q.", batch.Label),
	}}

	if batch.PostedAt.Valid {
		// Deliberately the frozen draft entries, not the current ledger. This
		// line says what was posted that night; a later correction gets its own
		// line rather than quietly rewriting this one.
		totals, err := s.Q.GetPaymentBatchTotals(ctx, batchID)
		if err != nil {
			return nil, err
		}
		out = append(out, ActivityEntry{
			Kind:        "posted",
			At:          batch.PostedAt.String,
			ActorUserID: batch.PostedBy.Int64,
			Summary: fmt.Sprintf("Posted %d payments totalling $%s.",
				totals.EntryCount, Cents(totals.NetTotalCents)),
		})
	}

	if batch.AbandonedAt.Valid {
		out = append(out, ActivityEntry{
			Kind:        "abandoned",
			At:          batch.AbandonedAt.String,
			ActorUserID: batch.AbandonedBy.Int64,
			Summary:     "Abandoned the batch without posting it.",
			Reason:      batch.AbandonReason.String,
		})
	}

	corrections, err := s.Q.ListCorrectionsByBatch(ctx, sql.NullInt64{Int64: batchID, Valid: true})
	if err != nil {
		return nil, err
	}
	for _, c := range corrections {
		out = append(out, ActivityEntry{
			Kind:        "corrected",
			At:          c.CorrectedAt,
			ActorUserID: c.CorrectedBy,
			Summary: fmt.Sprintf("Changed %s's payment %s from $%s to $%s.",
				c.DisplayName, c.OriginalReceiptCode,
				Cents(c.OriginalAmountCents), Cents(c.ReplacementAmountCents)),
			Reason: c.Reason,
		})
	}
	return out, nil
}

// ExportLedger renders the books to CSV.
func (s *Service) ExportLedger(ctx context.Context, p *authz.Principal, q LedgerQuery, generatedAt time.Time) (Export, error) {
	if err := authz.Authorize(ctx, p, "payment.export", nil); err != nil {
		return Export{}, err
	}
	if generatedAt.IsZero() {
		generatedAt = time.Now()
	}

	// Read one past the ceiling so a too-large export is refused rather than
	// quietly truncated.
	rows, err := s.Q.ListLedgerPayments(ctx, q.params(ExportMaxRows+1, 0))
	if err != nil {
		return Export{}, err
	}
	if len(rows) > ExportMaxRows {
		return Export{}, fmt.Errorf("%w: more than %d rows", ErrExportTooLarge, ExportMaxRows)
	}

	header := []string{
		"Receipt Code", "Received On", "Member", "Call Sign", "Amount",
		"Method", "Reference", "Entry Kind", "Superseded", "Batch ID",
		"Received By", "Entered At", "Treasurer Note",
	}
	data := make([][]string, len(rows))
	for i, r := range rows {
		e := ledgerEntryFromRow(r)
		data[i] = []string{
			e.ReceiptCode, e.ReceivedOn, e.DisplayName, e.CallSign, Cents(e.AmountCents),
			e.Method, e.Reference, e.EntryKind, yesNo(e.Superseded),
			batchRef(e.BatchID), e.ReceivedByOfficer, e.EnteredAt, e.TreasurerNote,
		}
	}

	stamp := generatedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	return Export{
		Filename:       "bcars-treasury-" + generatedAt.UTC().Format("20060102") + ".csv",
		GeneratedAt:    stamp,
		AppliedFilters: q.Filters(),
		RowCount:       len(rows),
		CSV:            writeCSV(stamp, q.Filters(), header, data),
	}, nil
}

// ExportBatch renders one batch's posted rows to CSV.
func (s *Service) ExportBatch(ctx context.Context, p *authz.Principal, batchID int64, generatedAt time.Time) (Export, error) {
	if err := authz.Authorize(ctx, p, "payment.export", nil); err != nil {
		return Export{}, err
	}
	if _, err := s.Q.GetPaymentBatch(ctx, batchID); err != nil {
		return Export{}, err
	}
	export, err := s.ExportLedger(ctx, p, LedgerQuery{BatchID: batchID}, generatedAt)
	if err != nil {
		return Export{}, err
	}
	export.Filename = fmt.Sprintf("bcars-batch-%d.csv", batchID)
	return export, nil
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func batchRef(id int64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}
