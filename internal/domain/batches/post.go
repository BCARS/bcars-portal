package batches

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/bcars/bcars-portal/internal/db"
	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/domain/idem"
)

// Posting is the one transition that turns drafts into money. Everything about
// it is all-or-nothing: either every entry becomes an immutable payment plus
// its explicit coverage event and the batch becomes posted, or the transaction
// rolls back and the batch is still open with nothing written.
//
// There is exactly one implementation. The single-payment convenience endpoint
// creates a one-row batch and calls straight into it rather than growing a
// second, subtly different ledger path.

// Idempotent operation names for posting.
const (
	OpBatchPost    = "payment-batch-post"
	OpSinglePayent = "payment-single-post"
)

// EntryKindOriginal marks a payment that is not part of a correction chain.
const EntryKindOriginal = "original"

var (
	// ErrEmptyBatch is returned when posting a batch with no entries. Posting
	// nothing is always a mistake, and silently succeeding would tell the
	// treasurer the money went in.
	ErrEmptyBatch = errors.New("batches: cannot post a batch with no entries")

	// ErrConfirmationRequired is returned when a post arrives without explicit
	// confirmation. Posting is irreversible except by correction, so it is
	// never the accidental result of a stray request.
	ErrConfirmationRequired = errors.New("batches: posting requires explicit confirmation")

	// ErrIdempotencyKeyRequired is returned when a post arrives without a key.
	// A retry that posted the same money twice would be much worse than a
	// rejected request.
	ErrIdempotencyKeyRequired = errors.New("batches: posting requires an Idempotency-Key")
)

// Payment is an immutable posted ledger row. There is deliberately no update or
// delete path: a posted payment is corrected by appending a reversal and a
// replacement, never edited.
type Payment struct {
	ID                int64
	MembershipID      int64
	BatchID           int64
	AmountCents       int64
	Method            string
	Reference         string
	ReceivedOn        string
	ReceivedByOfficer string
	EnteredByUserID   int64
	EnteredAt         string
	ReceiptCode       string
	EntryKind         string
	TreasurerNote     string
	CreatedAt         string
}

// Coverage is the paid-through decision a posted payment granted. Money
// received and coverage granted stay separate rows precisely so that either can
// be corrected without disturbing the other.
type Coverage struct {
	ID                int64
	MembershipID      int64
	PaidThrough       string
	ReasonKind        string
	PaymentID         int64
	SupersedesEventID int64
	DecidedByUserID   int64
	DecidedAt         string
}

// Posted is the result of a successful post: the now-terminal batch plus the
// ledger rows it produced, in entry order.
type Posted struct {
	Batch    Batch
	Payments []Payment
	Coverage []Coverage
}

// PostParams describes a posting request.
type PostParams struct {
	// ExpectedVersion is the batch version the client last read. Because every
	// entry mutation moves it, a stale value means someone edited a row after
	// this client last looked, and the post is refused.
	ExpectedVersion int64
	// IdempotencyKey is required.
	IdempotencyKey string
	// Confirm must be true. See ErrConfirmationRequired.
	Confirm bool
}

// Post converts every entry in an open batch into an immutable payment and an
// explicit coverage event, then marks the batch posted — atomically.
func (s *Service) Post(ctx context.Context, p *authz.Principal, batchID int64, params PostParams, now time.Time) (Posted, error) {
	if err := authz.Authorize(ctx, p, "payment.post", nil); err != nil {
		return Posted{}, err
	}
	if !params.Confirm {
		return Posted{}, ErrConfirmationRequired
	}
	if params.IdempotencyKey == "" {
		return Posted{}, ErrIdempotencyKeyRequired
	}
	if now.IsZero() {
		now = time.Now()
	}

	requestHash := idem.Hash(
		strconv.FormatInt(batchID, 10),
		strconv.FormatInt(params.ExpectedVersion, 10),
	)

	var out Posted
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		claim, err := idem.Begin(ctx, q, p.UserID, OpBatchPost, params.IdempotencyKey, requestHash)
		if err != nil {
			return err
		}
		if claim.Replay {
			out, err = s.loadPosted(ctx, q, claim.ResourceID)
			return err
		}

		out, err = s.postTx(ctx, q, p, batchID, params.ExpectedVersion, now)
		if err != nil {
			return err
		}
		return claim.Complete(ctx, q, "payment_batch", batchID)
	})
	return out, err
}

// SingleParams describes the one-payment convenience contract.
type SingleParams struct {
	Entry EntryInput
	// Label names the server-created batch so the payment is still traceable
	// to a batch in every later report. Empty gets a sensible default.
	Label          string
	IdempotencyKey string
	Confirm        bool
}

// PostSinglePayment records and posts one payment.
//
// It is a convenience contract, not a second ledger: it creates a one-row batch
// and runs the same posting transaction, so a single payment and a batched one
// are indistinguishable in the ledger afterwards.
func (s *Service) PostSinglePayment(ctx context.Context, p *authz.Principal, params SingleParams, now time.Time) (Posted, error) {
	if err := authz.Authorize(ctx, p, "payment.post", nil); err != nil {
		return Posted{}, err
	}
	if !params.Confirm {
		return Posted{}, ErrConfirmationRequired
	}
	if params.IdempotencyKey == "" {
		return Posted{}, ErrIdempotencyKeyRequired
	}
	if err := params.Entry.validate(); err != nil {
		return Posted{}, err
	}
	if now.IsZero() {
		now = time.Now()
	}
	label := params.Label
	if label == "" {
		label = fmt.Sprintf("Single payment %s", now.UTC().Format(ISODate))
	}

	requestHash := params.Entry.hash(0)

	var out Posted
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		claim, err := idem.Begin(ctx, q, p.UserID, OpSinglePayent, params.IdempotencyKey, requestHash)
		if err != nil {
			return err
		}
		if claim.Replay {
			out, err = s.loadPosted(ctx, q, claim.ResourceID)
			return err
		}

		if _, err := q.GetMembership(ctx, params.Entry.MembershipID); err != nil {
			return err
		}

		batch, err := q.CreatePaymentBatch(ctx, sqlcgen.CreatePaymentBatchParams{
			Label:              label,
			DefaultAmountCents: nullInt(params.Entry.AmountCents),
			DefaultPaidThrough: nullString(params.Entry.PaidThrough),
			OpenedBy:           p.UserID,
			OpenedAt:           now.UTC().Format(isoTimestamp),
		})
		if err != nil {
			return err
		}
		if _, err := q.CreatePaymentBatchEntry(ctx, sqlcgen.CreatePaymentBatchEntryParams{
			BatchID:           batch.ID,
			MembershipID:      params.Entry.MembershipID,
			Sequence:          1,
			AmountCents:       params.Entry.AmountCents,
			Method:            params.Entry.Method,
			Reference:         nullString(params.Entry.Reference),
			ReceivedOn:        params.Entry.ReceivedOn,
			ReceivedByOfficer: nullString(params.Entry.ReceivedByOfficer),
			PaidThrough:       params.Entry.PaidThrough,
			TreasurerNote:     nullString(params.Entry.TreasurerNote),
		}); err != nil {
			return err
		}

		out, err = s.postTx(ctx, q, p, batch.ID, batch.Version, now)
		if err != nil {
			return err
		}
		return claim.Complete(ctx, q, "payment_batch", batch.ID)
	})
	return out, err
}

// failBeforeCommit lets a test inject a storage failure after the ledger rows
// are written but before the batch is marked posted, proving the rollback
// really does leave zero payments and zero coverage events behind. It is nil in
// every production path; see post_internal_test.go.
var failBeforeCommit func() error

// postTx is the single posting primitive. Both the batch endpoint and the
// single-payment endpoint run exactly this.
func (s *Service) postTx(ctx context.Context, q *sqlcgen.Queries, p *authz.Principal, batchID, expectedVersion int64, now time.Time) (Posted, error) {
	batch, err := q.GetPaymentBatch(ctx, batchID)
	if err != nil {
		return Posted{}, err
	}
	if batch.State != StateOpen {
		return Posted{}, fmt.Errorf("%w: state is %s", ErrBatchNotOpen, batch.State)
	}

	entries, err := q.ListPaymentBatchEntries(ctx, batchID)
	if err != nil {
		return Posted{}, err
	}
	if len(entries) == 0 {
		return Posted{}, ErrEmptyBatch
	}

	// Validate every row before writing any of them. A batch that would fail
	// halfway is refused outright rather than rolled back after the fact.
	for _, e := range entries {
		in := EntryInput{
			MembershipID: e.MembershipID,
			AmountCents:  e.AmountCents,
			Method:       e.Method,
			ReceivedOn:   e.ReceivedOn,
			PaidThrough:  e.PaidThrough,
		}
		if err := in.validate(); err != nil {
			return Posted{}, fmt.Errorf("entry %d: %w", e.Sequence, err)
		}
		if _, err := q.GetMembership(ctx, e.MembershipID); err != nil {
			return Posted{}, fmt.Errorf("entry %d: %w", e.Sequence, err)
		}
	}

	enteredAt := now.UTC().Format(isoTimestamp)
	out := Posted{
		Payments: make([]Payment, 0, len(entries)),
		Coverage: make([]Coverage, 0, len(entries)),
	}

	// Two rows for the same member in one batch must chain, not fork: the
	// second decision supersedes the first one written here, not the one that
	// was effective before the batch started.
	latestCoverage := map[int64]int64{}

	for _, e := range entries {
		payment, err := q.CreatePayment(ctx, sqlcgen.CreatePaymentParams{
			MembershipID:      e.MembershipID,
			BatchID:           sql.NullInt64{Int64: batchID, Valid: true},
			AmountCents:       e.AmountCents,
			Method:            e.Method,
			Reference:         e.Reference,
			ReceivedOn:        e.ReceivedOn,
			ReceivedByOfficer: e.ReceivedByOfficer,
			EnteredBy:         p.UserID,
			EnteredAt:         enteredAt,
			ReceiptCode:       ReceiptCode(batchID, e.Sequence),
			EntryKind:         EntryKindOriginal,
			TreasurerNote:     e.TreasurerNote,
		})
		if err != nil {
			return Posted{}, err
		}

		supersedes, err := s.effectiveCoverageID(ctx, q, e.MembershipID, latestCoverage)
		if err != nil {
			return Posted{}, err
		}

		event, err := q.CreateCoverageEvent(ctx, sqlcgen.CreateCoverageEventParams{
			MembershipID:      e.MembershipID,
			PaidThrough:       e.PaidThrough,
			ReasonKind:        "payment",
			Reason:            sql.NullString{String: "Granted by a posted payment.", Valid: true},
			PaymentID:         sql.NullInt64{Int64: payment.ID, Valid: true},
			SupersedesEventID: supersedes,
			DecidedBy:         sql.NullInt64{Int64: p.UserID, Valid: p.UserID != 0},
			DecidedAt:         enteredAt,
		})
		if err != nil {
			return Posted{}, err
		}
		latestCoverage[e.MembershipID] = event.ID

		out.Payments = append(out.Payments, paymentFromRow(payment))
		out.Coverage = append(out.Coverage, coverageFromRow(event))
	}

	if failBeforeCommit != nil {
		if err := failBeforeCommit(); err != nil {
			return Posted{}, err
		}
	}

	// The version guard is deliberately last: if another officer edited a row
	// while this post was in flight, everything above rolls back.
	posted, err := q.MarkPaymentBatchPosted(ctx, sqlcgen.MarkPaymentBatchPostedParams{
		PostedBy: sql.NullInt64{Int64: p.UserID, Valid: true},
		PostedAt: sql.NullString{String: enteredAt, Valid: true},
		ID:       batchID,
		Version:  expectedVersion,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Posted{}, db.ErrStale
	}
	if err != nil {
		return Posted{}, err
	}

	out.Batch, err = s.assemble(ctx, q, posted, true)
	if err != nil {
		return Posted{}, err
	}
	return out, nil
}

// effectiveCoverageID returns the coverage event this batch's next decision for
// membershipID should supersede, preferring one written earlier in this same
// transaction.
func (s *Service) effectiveCoverageID(ctx context.Context, q *sqlcgen.Queries, membershipID int64, written map[int64]int64) (sql.NullInt64, error) {
	if id, ok := written[membershipID]; ok {
		return sql.NullInt64{Int64: id, Valid: true}, nil
	}
	prior, err := q.GetEffectiveCoverageEvent(ctx, membershipID)
	switch {
	case err == nil:
		return sql.NullInt64{Int64: prior.ID, Valid: true}, nil
	case errors.Is(err, sql.ErrNoRows):
		return sql.NullInt64{}, nil
	default:
		return sql.NullInt64{}, err
	}
}

// ReceiptCode builds the printable identifier for a posted payment. It is
// derived from the batch and the row's stable sequence, so the same payment
// always prints the same receipt and a reprint is not a new number.
func ReceiptCode(batchID, sequence int64) string {
	return fmt.Sprintf("RCPT-%06d-%03d", batchID, sequence)
}

// loadPosted rebuilds the result of an earlier post, for an idempotent replay.
func (s *Service) loadPosted(ctx context.Context, q *sqlcgen.Queries, batchID int64) (Posted, error) {
	row, err := q.GetPaymentBatch(ctx, batchID)
	if err != nil {
		return Posted{}, err
	}
	batch, err := s.assemble(ctx, q, row, true)
	if err != nil {
		return Posted{}, err
	}
	payments, err := q.ListPaymentsByBatch(ctx, sql.NullInt64{Int64: batchID, Valid: true})
	if err != nil {
		return Posted{}, err
	}

	out := Posted{Batch: batch}
	for _, pay := range payments {
		out.Payments = append(out.Payments, paymentFromRow(pay))
		events, err := q.ListCoverageEventsByMembership(ctx, pay.MembershipID)
		if err != nil {
			return Posted{}, err
		}
		for _, e := range events {
			if e.PaymentID.Valid && e.PaymentID.Int64 == pay.ID {
				out.Coverage = append(out.Coverage, coverageFromRow(e))
			}
		}
	}
	return out, nil
}

func paymentFromRow(p sqlcgen.Payment) Payment {
	return Payment{
		ID:                p.ID,
		MembershipID:      p.MembershipID,
		BatchID:           p.BatchID.Int64,
		AmountCents:       p.AmountCents,
		Method:            p.Method,
		Reference:         p.Reference.String,
		ReceivedOn:        p.ReceivedOn,
		ReceivedByOfficer: p.ReceivedByOfficer.String,
		EnteredByUserID:   p.EnteredBy,
		EnteredAt:         p.EnteredAt,
		ReceiptCode:       p.ReceiptCode,
		EntryKind:         p.EntryKind,
		TreasurerNote:     p.TreasurerNote.String,
		CreatedAt:         p.CreatedAt,
	}
}

func coverageFromRow(e sqlcgen.CoverageEvent) Coverage {
	return Coverage{
		ID:                e.ID,
		MembershipID:      e.MembershipID,
		PaidThrough:       e.PaidThrough,
		ReasonKind:        e.ReasonKind,
		PaymentID:         e.PaymentID.Int64,
		SupersedesEventID: e.SupersedesEventID.Int64,
		DecidedByUserID:   e.DecidedBy.Int64,
		DecidedAt:         e.DecidedAt,
	}
}
