package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/audit"
	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/domain/batches"
	"github.com/bcars/bcars-portal/internal/domain/dues"
)

// --- Response types ---

type PaymentCorrection struct {
	ID                   int64  `json:"id"`
	OriginalPaymentID    int64  `json:"original_payment_id"`
	ReversalPaymentID    int64  `json:"reversal_payment_id"`
	ReplacementPaymentID int64  `json:"replacement_payment_id"`
	Reason               string `json:"reason"`
	CorrectedByUserID    int64  `json:"corrected_by_user_id"`
	CorrectedAt          string `json:"corrected_at" format:"date-time"`
}

// LedgerTotals are the net sums over a batch's posted payments. They diverge
// from the batch's draft `totals` once a correction lands, and that divergence
// is deliberate: the entries record what the treasurer typed on the night, the
// ledger records what the club actually holds.
type LedgerTotals struct {
	PaymentCount    int64 `json:"payment_count"`
	NetTotalCents   int64 `json:"net_total_cents"`
	CashTotalCents  int64 `json:"cash_total_cents"`
	CheckTotalCents int64 `json:"check_total_cents"`
	OtherTotalCents int64 `json:"other_total_cents"`
}

// PaymentChain is what both the correction and the read return: the payment now
// in force, everything it replaced, and why.
type PaymentChain struct {
	Effective    Payment             `json:"effective_payment" doc:"The payment now in force."`
	Chain        []Payment           `json:"chain" doc:"Every payment in this chain, oldest first, including reversals."`
	Corrections  []PaymentCorrection `json:"corrections" doc:"The correction records, oldest first."`
	Coverage     *PostedCoverage     `json:"coverage_event,omitempty" doc:"Present only when a correction changed paid-through."`
	PaidThrough  string              `json:"paid_through,omitempty" format:"date" doc:"The coverage date in force for this member."`
	Standing     *DuesStanding       `json:"standing,omitempty" doc:"Present when the caller also holds dues.read."`
	Batch        PaymentBatch        `json:"batch"`
	LedgerTotals LedgerTotals        `json:"ledger_totals"`
	Revision     int64               `json:"revision" doc:"Correction-chain revision; send as If-Match to correct."`
}

// --- Inputs and outputs ---

type CorrectPaymentBody struct {
	AmountCents       int64  `json:"amount_cents" minimum:"1" doc:"What the payment should have been, stated in full."`
	Method            string `json:"method" enum:"cash,check,other"`
	Reference         string `json:"reference,omitempty"`
	ReceivedOn        string `json:"received_on" format:"date"`
	ReceivedByOfficer string `json:"received_by_officer,omitempty"`
	PaidThrough       string `json:"paid_through" format:"date" doc:"Pass the date the payment already grants to leave coverage untouched; pass a different one to supersede it."`
	TreasurerNote     string `json:"treasurer_note,omitempty"`
	Reason            string `json:"reason" minLength:"1" doc:"Required. It is what makes the chain readable a year later."`
}
type CorrectPaymentInput struct {
	ID             int64  `path:"id"`
	IfMatch        string `header:"If-Match" doc:"Correction-chain revision you last read. Send \"0\" for a payment that has never been corrected. Required: a missing header is a 428."`
	IdempotencyKey string `header:"Idempotency-Key" doc:"Required."`
	Body           CorrectPaymentBody
}
type CorrectPaymentOutput struct {
	ETag string `header:"ETag"`
	Body PaymentChain
}

type GetPaymentInput struct {
	ID int64 `path:"id"`
}
type GetPaymentOutput struct {
	ETag string `header:"ETag"`
	Body PaymentChain
}

// --- Conversions ---

func correctionToResponse(c batches.Correction) PaymentCorrection {
	return PaymentCorrection{
		ID:                   c.ID,
		OriginalPaymentID:    c.OriginalPaymentID,
		ReversalPaymentID:    c.ReversalPaymentID,
		ReplacementPaymentID: c.ReplacementPaymentID,
		Reason:               c.Reason,
		CorrectedByUserID:    c.CorrectedByUserID,
		CorrectedAt:          c.CorrectedAt,
	}
}

func chainToResponse(c batches.Corrected, revision int64) PaymentChain {
	out := PaymentChain{
		Effective:   paymentToResponse(c.Effective),
		PaidThrough: c.PaidThrough,
		Batch:       batchToResponse(c.Batch),
		Revision:    revision,
		LedgerTotals: LedgerTotals{
			PaymentCount:    c.LedgerTotals.PaymentCount,
			NetTotalCents:   c.LedgerTotals.NetTotalCents,
			CashTotalCents:  c.LedgerTotals.CashTotalCents,
			CheckTotalCents: c.LedgerTotals.CheckTotalCents,
			OtherTotalCents: c.LedgerTotals.OtherTotalCents,
		},
	}
	for _, p := range c.Chain {
		out.Chain = append(out.Chain, paymentToResponse(p))
	}
	for _, corr := range c.Corrections {
		out.Corrections = append(out.Corrections, correctionToResponse(corr))
	}
	if c.Coverage != nil {
		cov := postedCoverageToResponse(*c.Coverage)
		out.Coverage = &cov
	}
	return out
}

func mapCorrectionError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, batches.ErrPaymentSuperseded):
		return ErrConflict("this payment has already been corrected; correct the replacement instead")
	case errors.Is(err, batches.ErrNotAnOriginalOrReplacement):
		return ErrConflict("a reversal cannot be corrected")
	}
	return mapPostError(err)
}

// RegisterCorrections registers the posted-payment read and correction
// endpoints. There is deliberately no PATCH and no DELETE for a payment.
func RegisterCorrections(api huma.API, deps Deps) {
	var (
		svc     *batches.Service
		duesSvc *dues.Service
	)
	if deps.DB != nil {
		svc = batches.NewService(deps.DB)
		duesSvc = dues.NewService(deps.DB)
	}

	// standingFor adds the member's derived standing when the caller also holds
	// dues.read. A treasurer holds both by seed; a caller who somehow holds
	// only payment.correct still gets the correction, just without this
	// convenience field.
	standingFor := func(ctx context.Context, principal *authz.Principal, membershipID int64) *DuesStanding {
		if duesSvc == nil || membershipID == 0 {
			return nil
		}
		st, err := duesSvc.GetStanding(ctx, principal, membershipID, time.Time{}, 0)
		if err != nil {
			return nil
		}
		out := duesStandingToResponse(st)
		return &out
	}

	Register(api, huma.Operation{
		OperationID: "payment-get",
		Method:      http.MethodGet,
		Path:        "/payments/{id}",
		Summary:     "Read a payment with its full correction chain",
		Description: "The ETag is the correction-chain revision. Send it as If-Match to correct " +
			"the payment, so a client working from a stale view of the chain is refused.",
		Tags: []string{"payments"},
	}, OperationMeta{
		RequiredCapability: "payment.read",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *GetPaymentInput) (*GetPaymentOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		chain, err := svc.GetPaymentChain(ctx, principal, input.ID)
		if err != nil {
			return nil, mapCorrectionError(err)
		}
		revision, err := svc.ChainRevision(ctx, principal, input.ID)
		if err != nil {
			return nil, mapCorrectionError(err)
		}
		body := chainToResponse(chain, revision)
		body.Standing = standingFor(ctx, principal, chain.MembershipID)
		return &GetPaymentOutput{ETag: FormatETag(revision), Body: body}, nil
	})

	Register(api, huma.Operation{
		OperationID:   "payment-correct",
		Method:        http.MethodPost,
		Path:          "/payments/{id}/corrections",
		Summary:       "Correct a posted payment",
		DefaultStatus: http.StatusCreated,
		Description: "Appends a signed reversal of what was recorded and a positive replacement of " +
			"what it should have been, and links the three with a required reason. The original is " +
			"never overwritten or deleted. Coverage moves only when the request states a different " +
			"paid-through date: correcting a mistyped amount says nothing about how long the member " +
			"is covered.",
		Tags: []string{"payments"},
	}, OperationMeta{
		RequiredCapability: "payment.correct",
		AuditAction:        "payment.correct",
		ConfirmationLevel:  ConfirmExplicit,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *CorrectPaymentInput) (*CorrectPaymentOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		revision, err := parseRequiredRevision(input.IfMatch)
		if err != nil {
			return nil, err
		}

		result, err := svc.CorrectPayment(ctx, principal, input.ID, batches.CorrectParams{
			AmountCents:       input.Body.AmountCents,
			Method:            input.Body.Method,
			Reference:         input.Body.Reference,
			ReceivedOn:        input.Body.ReceivedOn,
			ReceivedByOfficer: input.Body.ReceivedByOfficer,
			TreasurerNote:     input.Body.TreasurerNote,
			PaidThrough:       input.Body.PaidThrough,
			Reason:            input.Body.Reason,
			ExpectedRevision:  revision,
			IdempotencyKey:    input.IdempotencyKey,
			Confirm:           ConfirmedFrom(ctx),
		}, time.Now())
		if err != nil {
			return nil, mapCorrectionError(err)
		}
		audit.StampResource(ctx, "payment", result.Effective.ID)

		body := chainToResponse(result, revision+1)
		body.Standing = standingFor(ctx, principal, result.MembershipID)
		return &CorrectPaymentOutput{ETag: FormatETag(revision + 1), Body: body}, nil
	})
}

// parseRequiredRevision reads the correction-chain revision from If-Match.
//
// Unlike a row version, revision 0 is meaningful here — it is an uncorrected
// chain — so a missing header cannot be inferred from a zero value and needs
// its own check.
func parseRequiredRevision(raw string) (int64, error) {
	raw = strings.Trim(strings.TrimSpace(raw), `"`)
	if raw == "" {
		return 0, huma.Error428PreconditionRequired(
			`If-Match is required: send the chain revision you last read ("0" if never corrected)`)
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return 0, huma.Error422UnprocessableEntity(
			"If-Match must be a quoted non-negative chain revision, for example \"0\"")
	}
	return v, nil
}
