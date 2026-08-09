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

// Corrections are how a posted payment changes. There is no update and no
// delete: the ledger appends a signed reversal of what was recorded and a
// positive replacement of what should have been, links the three, and keeps the
// original exactly as it was. A treasurer looking back can always see both what
// was entered and what it became, and why.

// OpPaymentCorrect is the idempotent operation name for a correction.
const OpPaymentCorrect = "payment-correct"

// Payment entry kinds appended by a correction.
const (
	EntryKindReversal    = "reversal"
	EntryKindReplacement = "replacement"
)

var (
	// ErrPaymentSuperseded is returned when the targeted payment has already
	// been corrected. A repeat correction targets the current replacement, not
	// a row that history has moved past.
	ErrPaymentSuperseded = errors.New("batches: this payment has already been corrected")

	// ErrNotAnOriginalOrReplacement is returned when a client tries to correct
	// a reversal. A reversal is bookkeeping, not a payment anyone can restate.
	ErrNotAnOriginalOrReplacement = errors.New("batches: a reversal cannot be corrected")
)

// Correction is the immutable record linking an original payment, its reversal,
// and its replacement.
type Correction struct {
	ID                   int64
	OriginalPaymentID    int64
	ReversalPaymentID    int64
	ReplacementPaymentID int64
	Reason               string
	CorrectedByUserID    int64
	CorrectedAt          string
}

// LedgerTotals are the net sums over a batch's posted payments.
//
// These deliberately differ from a posted batch's draft Totals once a
// correction lands: the entries record what the treasurer typed on the night,
// and the ledger records what the club actually holds. Showing both is the
// point — a correction should be visible, not tidied away.
type LedgerTotals struct {
	PaymentCount    int64
	NetTotalCents   int64
	CashTotalCents  int64
	CheckTotalCents int64
	OtherTotalCents int64
}

// Corrected is the result of a correction.
type Corrected struct {
	// Effective is the payment now in force — the replacement.
	Effective Payment
	// Chain is every payment in this correction chain, oldest first, including
	// the original and the reversal.
	Chain []Payment
	// Corrections are the correction records themselves, oldest first.
	Corrections []Correction
	// Coverage is the new superseding coverage event, non-nil only when the
	// request actually changed paid-through.
	Coverage *Coverage
	// PaidThrough is the coverage date in force after the correction, whether
	// or not this correction changed it.
	PaidThrough string
	// Batch carries the recomputed ledger totals for the batch this payment
	// belongs to.
	Batch        Batch
	LedgerTotals LedgerTotals
	MembershipID int64
}

// CorrectParams describes a correction request. Every money field is restated
// in full rather than patched, so the replacement is a complete statement of
// what the payment should have been.
type CorrectParams struct {
	AmountCents       int64
	Method            string
	Reference         string
	ReceivedOn        string
	ReceivedByOfficer string
	TreasurerNote     string

	// PaidThrough is stated explicitly. Passing the value the payment already
	// grants leaves the coverage decision untouched; passing a different one
	// appends a superseding coverage event.
	PaidThrough string

	// Reason is required. It is what makes the chain readable a year later.
	Reason string

	// ExpectedRevision is the correction-chain revision the client last read,
	// as returned in the payment's ETag. Zero means an uncorrected chain.
	ExpectedRevision int64

	IdempotencyKey string
	Confirm        bool
}

func (p CorrectParams) validate() error {
	if strings.TrimSpace(p.Reason) == "" {
		return ErrReasonRequired
	}
	in := EntryInput{
		MembershipID: 1, // not part of a correction; the chain fixes the member
		AmountCents:  p.AmountCents,
		Method:       p.Method,
		ReceivedOn:   p.ReceivedOn,
		PaidThrough:  p.PaidThrough,
	}
	return in.validate()
}

// ChainRevision reports how many corrections have been applied to the chain
// containing paymentID. It is the payment resource's ETag: correcting a payment
// requires presenting the revision the client last saw, so a client working
// from a stale view of the chain is refused rather than silently correcting the
// wrong row.
func (s *Service) ChainRevision(ctx context.Context, p *authz.Principal, paymentID int64) (int64, error) {
	if err := authz.Authorize(ctx, p, "payment.read", nil); err != nil {
		return 0, err
	}
	_, revision, err := s.effectivePayment(ctx, s.Q, paymentID)
	return revision, err
}

// effectivePayment walks a chain forward from paymentID to the payment now in
// force, returning it and the number of corrections traversed from the chain
// root. It also walks backwards first, so a client may name any payment in the
// chain and still get a consistent revision.
func (s *Service) effectivePayment(ctx context.Context, q *sqlcgen.Queries, paymentID int64) (sqlcgen.Payment, int64, error) {
	current, err := q.GetPayment(ctx, paymentID)
	if err != nil {
		return sqlcgen.Payment{}, 0, err
	}

	// Walk back to the chain root so the revision counts from a fixed point.
	root := current
	for root.CorrectsPaymentID.Valid {
		prev, err := q.GetPayment(ctx, root.CorrectsPaymentID.Int64)
		if err != nil {
			return sqlcgen.Payment{}, 0, err
		}
		root = prev
	}

	// Walk forward to the payment nothing has superseded.
	effective := root
	var revision int64
	for {
		correction, err := q.GetPaymentCorrectionByOriginal(ctx, effective.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return effective, revision, nil
		}
		if err != nil {
			return sqlcgen.Payment{}, 0, err
		}
		next, err := q.GetPayment(ctx, correction.ReplacementPaymentID)
		if err != nil {
			return sqlcgen.Payment{}, 0, err
		}
		effective = next
		revision++
	}
}

// CorrectPayment appends a reversal and a replacement for a posted payment.
//
// Correcting $400 to $40 leaves the $400 payment exactly where it is, adds a
// -$400 reversal and a +$40 replacement, and the batch's net ledger total falls
// by $360. Nothing is overwritten and nothing disappears.
func (s *Service) CorrectPayment(ctx context.Context, p *authz.Principal, paymentID int64, params CorrectParams, now time.Time) (Corrected, error) {
	if err := authz.Authorize(ctx, p, "payment.correct", nil); err != nil {
		return Corrected{}, err
	}
	if !params.Confirm {
		return Corrected{}, ErrConfirmationRequired
	}
	if params.IdempotencyKey == "" {
		return Corrected{}, ErrIdempotencyKeyRequired
	}
	if err := params.validate(); err != nil {
		return Corrected{}, err
	}
	if now.IsZero() {
		now = time.Now()
	}

	requestHash := idem.Hash(
		strconv.FormatInt(paymentID, 10),
		strconv.FormatInt(params.AmountCents, 10),
		params.Method, params.Reference, params.ReceivedOn,
		params.ReceivedByOfficer, params.PaidThrough,
		params.TreasurerNote, params.Reason,
	)

	var out Corrected
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		claim, err := idem.Begin(ctx, q, p.UserID, OpPaymentCorrect, params.IdempotencyKey, requestHash)
		if err != nil {
			return err
		}
		if claim.Replay {
			out, err = s.loadCorrected(ctx, q, claim.ResourceID)
			return err
		}

		target, err := q.GetPayment(ctx, paymentID)
		if err != nil {
			return err
		}
		if target.EntryKind == EntryKindReversal {
			return ErrNotAnOriginalOrReplacement
		}

		effective, revision, err := s.effectivePayment(ctx, q, paymentID)
		if err != nil {
			return err
		}
		if revision != params.ExpectedRevision {
			return db.ErrStale
		}
		if effective.ID != target.ID {
			return ErrPaymentSuperseded
		}

		correctedAt := now.UTC().Format(isoTimestamp)
		nextRevision := revision + 1

		// 1. Reverse what is currently in force, in full.
		reversal, err := q.CreatePayment(ctx, sqlcgen.CreatePaymentParams{
			MembershipID:      effective.MembershipID,
			BatchID:           effective.BatchID,
			AmountCents:       -effective.AmountCents,
			Method:            effective.Method,
			Reference:         effective.Reference,
			ReceivedOn:        effective.ReceivedOn,
			ReceivedByOfficer: effective.ReceivedByOfficer,
			EnteredBy:         p.UserID,
			EnteredAt:         correctedAt,
			ReceiptCode:       fmt.Sprintf("%s-R%d", effective.ReceiptCode, nextRevision),
			EntryKind:         EntryKindReversal,
			CorrectsPaymentID: sql.NullInt64{Int64: effective.ID, Valid: true},
			TreasurerNote:     effective.TreasurerNote,
		})
		if err != nil {
			return err
		}

		// 2. Append what it should have been.
		replacement, err := q.CreatePayment(ctx, sqlcgen.CreatePaymentParams{
			MembershipID:      effective.MembershipID,
			BatchID:           effective.BatchID,
			AmountCents:       params.AmountCents,
			Method:            params.Method,
			Reference:         nullString(params.Reference),
			ReceivedOn:        params.ReceivedOn,
			ReceivedByOfficer: nullString(params.ReceivedByOfficer),
			EnteredBy:         p.UserID,
			EnteredAt:         correctedAt,
			ReceiptCode:       fmt.Sprintf("%s-C%d", effective.ReceiptCode, nextRevision),
			EntryKind:         EntryKindReplacement,
			CorrectsPaymentID: sql.NullInt64{Int64: effective.ID, Valid: true},
			TreasurerNote:     nullString(params.TreasurerNote),
		})
		if err != nil {
			return err
		}

		// 3. Link the three, with the reason and who decided it.
		if _, err := q.CreatePaymentCorrection(ctx, sqlcgen.CreatePaymentCorrectionParams{
			OriginalPaymentID:    effective.ID,
			ReversalPaymentID:    reversal.ID,
			ReplacementPaymentID: replacement.ID,
			Reason:               params.Reason,
			CorrectedBy:          p.UserID,
			CorrectedAt:          correctedAt,
		}); err != nil {
			return err
		}

		// 4. Adjust coverage only when the paid-through actually changed.
		//    Correcting a mistyped amount says nothing about how long the
		//    member is covered, so the existing decision stands.
		if err := s.adjustCoverageForCorrection(ctx, q, p, effective, replacement, params, correctedAt); err != nil {
			return err
		}

		if err := claim.Complete(ctx, q, "payment", replacement.ID); err != nil {
			return err
		}
		out, err = s.loadCorrected(ctx, q, replacement.ID)
		return err
	})
	return out, err
}

// adjustCoverageForCorrection appends a superseding coverage event when, and
// only when, the correction states a different paid-through date.
func (s *Service) adjustCoverageForCorrection(ctx context.Context, q *sqlcgen.Queries, p *authz.Principal, effective, replacement sqlcgen.Payment, params CorrectParams, correctedAt string) error {
	priorForPayment, err := q.GetCoverageEventByPayment(ctx, sql.NullInt64{Int64: effective.ID, Valid: true})
	switch {
	case err == nil:
		if priorForPayment.PaidThrough == params.PaidThrough {
			// Money changed, coverage did not. Leave the decision alone.
			return nil
		}
	case errors.Is(err, sql.ErrNoRows):
		// This payment granted no coverage. Only state one now if the request
		// asks for a date, which it always does.
	default:
		return err
	}

	// Supersede whatever is currently effective for the member, which may be a
	// later decision than the one this payment granted.
	var supersedes sql.NullInt64
	current, err := q.GetEffectiveCoverageEvent(ctx, effective.MembershipID)
	switch {
	case err == nil:
		supersedes = sql.NullInt64{Int64: current.ID, Valid: true}
	case errors.Is(err, sql.ErrNoRows):
	default:
		return err
	}

	_, err = q.CreateCoverageEvent(ctx, sqlcgen.CreateCoverageEventParams{
		MembershipID:      effective.MembershipID,
		PaidThrough:       params.PaidThrough,
		ReasonKind:        "correction",
		Reason:            sql.NullString{String: params.Reason, Valid: true},
		PaymentID:         sql.NullInt64{Int64: replacement.ID, Valid: true},
		SupersedesEventID: supersedes,
		DecidedBy:         sql.NullInt64{Int64: p.UserID, Valid: p.UserID != 0},
		DecidedAt:         correctedAt,
	})
	return err
}

// loadCorrected assembles the response for a chain, given any payment in it.
func (s *Service) loadCorrected(ctx context.Context, q *sqlcgen.Queries, paymentID int64) (Corrected, error) {
	effective, _, err := s.effectivePayment(ctx, q, paymentID)
	if err != nil {
		return Corrected{}, err
	}

	out := Corrected{
		Effective:    paymentFromRow(effective),
		MembershipID: effective.MembershipID,
	}

	// Walk the chain from its root, collecting every payment and correction.
	root := effective
	for root.CorrectsPaymentID.Valid {
		prev, err := q.GetPayment(ctx, root.CorrectsPaymentID.Int64)
		if err != nil {
			return Corrected{}, err
		}
		root = prev
	}

	current := root
	out.Chain = append(out.Chain, paymentFromRow(current))
	for {
		correction, err := q.GetPaymentCorrectionByOriginal(ctx, current.ID)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return Corrected{}, err
		}
		reversal, err := q.GetPayment(ctx, correction.ReversalPaymentID)
		if err != nil {
			return Corrected{}, err
		}
		replacement, err := q.GetPayment(ctx, correction.ReplacementPaymentID)
		if err != nil {
			return Corrected{}, err
		}
		out.Chain = append(out.Chain, paymentFromRow(reversal), paymentFromRow(replacement))
		out.Corrections = append(out.Corrections, Correction{
			ID:                   correction.ID,
			OriginalPaymentID:    correction.OriginalPaymentID,
			ReversalPaymentID:    correction.ReversalPaymentID,
			ReplacementPaymentID: correction.ReplacementPaymentID,
			Reason:               correction.Reason,
			CorrectedByUserID:    correction.CorrectedBy,
			CorrectedAt:          correction.CorrectedAt,
		})
		current = replacement
	}

	// The coverage event this correction produced, if it produced one.
	if event, err := q.GetCoverageEventByPayment(ctx, sql.NullInt64{Int64: effective.ID, Valid: true}); err == nil {
		c := coverageFromRow(event)
		out.Coverage = &c
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Corrected{}, err
	}

	// Whatever is now in force for the member, regardless of which payment
	// granted it.
	if current, err := q.GetEffectiveCoverageEvent(ctx, effective.MembershipID); err == nil {
		out.PaidThrough = current.PaidThrough
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Corrected{}, err
	}

	if effective.BatchID.Valid {
		row, err := q.GetPaymentBatch(ctx, effective.BatchID.Int64)
		if err != nil {
			return Corrected{}, err
		}
		out.Batch, err = s.assemble(ctx, q, row, true)
		if err != nil {
			return Corrected{}, err
		}
		totals, err := q.GetBatchLedgerTotals(ctx, effective.BatchID)
		if err != nil {
			return Corrected{}, err
		}
		out.LedgerTotals = LedgerTotals{
			PaymentCount:    totals.PaymentCount,
			NetTotalCents:   totals.NetTotalCents,
			CashTotalCents:  totals.CashTotalCents,
			CheckTotalCents: totals.CheckTotalCents,
			OtherTotalCents: totals.OtherTotalCents,
		}
	}
	return out, nil
}

// GetPaymentChain returns a payment's full correction chain without changing
// anything, so a client can read the ETag before correcting.
func (s *Service) GetPaymentChain(ctx context.Context, p *authz.Principal, paymentID int64) (Corrected, error) {
	if err := authz.Authorize(ctx, p, "payment.read", nil); err != nil {
		return Corrected{}, err
	}
	return s.loadCorrected(ctx, s.Q, paymentID)
}

// LedgerTotalsFor returns the net posted totals for a batch.
func (s *Service) LedgerTotalsFor(ctx context.Context, p *authz.Principal, batchID int64) (LedgerTotals, error) {
	if err := authz.Authorize(ctx, p, "payment.read", nil); err != nil {
		return LedgerTotals{}, err
	}
	totals, err := s.Q.GetBatchLedgerTotals(ctx, sql.NullInt64{Int64: batchID, Valid: true})
	if err != nil {
		return LedgerTotals{}, err
	}
	return LedgerTotals{
		PaymentCount:    totals.PaymentCount,
		NetTotalCents:   totals.NetTotalCents,
		CashTotalCents:  totals.CashTotalCents,
		CheckTotalCents: totals.CheckTotalCents,
		OtherTotalCents: totals.OtherTotalCents,
	}, nil
}
