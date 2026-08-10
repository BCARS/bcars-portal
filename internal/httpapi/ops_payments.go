package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/audit"
	"github.com/bcars/bcars-portal/internal/domain/batches"
)

// --- Response types ---

// Payment is an immutable posted ledger row. There is deliberately no PATCH or
// DELETE for one: a posted payment is corrected by appending a reversal and a
// replacement (pma.5), never edited.
type Payment struct {
	ID                int64  `json:"id"`
	MembershipID      int64  `json:"membership_id"`
	BatchID           int64  `json:"batch_id"`
	AmountCents       int64  `json:"amount_cents"`
	Method            string `json:"method" enum:"cash,check,other"`
	Reference         string `json:"reference,omitempty"`
	ReceivedOn        string `json:"received_on" format:"date"`
	ReceivedByOfficer string `json:"received_by_officer,omitempty"`
	EnteredByUserID   int64  `json:"entered_by_user_id"`
	EnteredAt         string `json:"entered_at" format:"date-time"`
	ReceiptCode       string `json:"receipt_code" doc:"Stable printable identifier; a reprint is not a new number."`
	EntryKind         string `json:"entry_kind" enum:"original,reversal,replacement"`
	TreasurerNote     string `json:"treasurer_note,omitempty"`
	CreatedAt         string `json:"created_at" format:"date-time"`
}

// PostedCoverage is the paid-through decision a posted payment granted. It is a
// separate row from the payment so either can be corrected without disturbing
// the other.
type PostedCoverage struct {
	ID                int64  `json:"id"`
	MembershipID      int64  `json:"membership_id"`
	PaidThrough       string `json:"paid_through" format:"date"`
	ReasonKind        string `json:"reason_kind"`
	PaymentID         int64  `json:"payment_id"`
	SupersedesEventID int64  `json:"supersedes_event_id,omitempty"`
	DecidedByUserID   int64  `json:"decided_by_user_id"`
	DecidedAt         string `json:"decided_at" format:"date-time"`
}

// PostResult is the shape both posting endpoints return, so a single payment
// and a batched one are indistinguishable to a client afterwards.
type PostResult struct {
	Batch    PaymentBatch     `json:"batch"`
	Payments []Payment        `json:"payments"`
	Coverage []PostedCoverage `json:"coverage_events"`
}

// --- Inputs and outputs ---

// PostBatchBody is empty: posting is fully described by the batch, the
// If-Match version, and the Idempotency-Key. Intent is stated with the
// X-Confirm header, which AuthzMiddleware enforces from the declared
// ConfirmationLevel (bcars-portal-6q6.1).
type PostBatchBody struct{}
type PostBatchInput struct {
	ID             int64  `path:"id"`
	IfMatch        string `header:"If-Match" doc:"Batch version you last read. Required: a missing header is a 428. Because every entry mutation moves it, a stale value means a row changed since you looked."`
	IdempotencyKey string `header:"Idempotency-Key" doc:"Required. A retry with the same key returns the original result and cannot post the money twice."`
	Body           PostBatchBody
}
type PostBatchOutput struct {
	ETag string `header:"ETag"`
	Body PostResult
}

type CreatePaymentBody struct {
	MembershipID      int64  `json:"membership_id" minimum:"1"`
	AmountCents       int64  `json:"amount_cents" minimum:"1"`
	Method            string `json:"method" enum:"cash,check,other"`
	Reference         string `json:"reference,omitempty"`
	ReceivedOn        string `json:"received_on" format:"date"`
	ReceivedByOfficer string `json:"received_by_officer,omitempty"`
	PaidThrough       string `json:"paid_through" format:"date" doc:"Stated explicitly. The server never derives coverage from the amount."`
	TreasurerNote     string `json:"treasurer_note,omitempty"`
	Label             string `json:"label,omitempty" doc:"Names the one-row batch created for this payment. Defaults to a dated label."`
}
type CreatePaymentInput struct {
	IdempotencyKey string `header:"Idempotency-Key" doc:"Required."`
	Body           CreatePaymentBody
}
type CreatePaymentOutput struct {
	ETag string `header:"ETag"`
	Body PostResult
}

// --- Conversions ---

func paymentToResponse(p batches.Payment) Payment {
	return Payment{
		ID:                p.ID,
		MembershipID:      p.MembershipID,
		BatchID:           p.BatchID,
		AmountCents:       p.AmountCents,
		Method:            p.Method,
		Reference:         p.Reference,
		ReceivedOn:        p.ReceivedOn,
		ReceivedByOfficer: p.ReceivedByOfficer,
		EnteredByUserID:   p.EnteredByUserID,
		EnteredAt:         p.EnteredAt,
		ReceiptCode:       p.ReceiptCode,
		EntryKind:         p.EntryKind,
		TreasurerNote:     p.TreasurerNote,
		CreatedAt:         p.CreatedAt,
	}
}

func postedCoverageToResponse(c batches.Coverage) PostedCoverage {
	return PostedCoverage{
		ID:                c.ID,
		MembershipID:      c.MembershipID,
		PaidThrough:       c.PaidThrough,
		ReasonKind:        c.ReasonKind,
		PaymentID:         c.PaymentID,
		SupersedesEventID: c.SupersedesEventID,
		DecidedByUserID:   c.DecidedByUserID,
		DecidedAt:         c.DecidedAt,
	}
}

func postResultToResponse(p batches.Posted) PostResult {
	out := PostResult{Batch: batchToResponse(p.Batch)}
	for _, pay := range p.Payments {
		out.Payments = append(out.Payments, paymentToResponse(pay))
	}
	for _, c := range p.Coverage {
		out.Coverage = append(out.Coverage, postedCoverageToResponse(c))
	}
	return out
}

// mapPostError adds the posting-specific failures to the batch mapper.
func mapPostError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, batches.ErrEmptyBatch):
		return huma.Error422UnprocessableEntity("this batch has no entries, so there is nothing to post")
	case errors.Is(err, batches.ErrConfirmationRequired):
		// Unreachable while the middleware enforces the declared level; kept
		// so the domain guard still reports honestly if it ever is not.
		return huma.NewError(confirmationStatus,
			"posting requires explicit confirmation; resend with "+ConfirmHeader+": true")
	case errors.Is(err, batches.ErrIdempotencyKeyRequired):
		return huma.Error422UnprocessableEntity("posting requires an Idempotency-Key header")
	}
	return mapBatchError(err)
}

// RegisterPayments registers the atomic posting and single-payment endpoints.
func RegisterPayments(api huma.API, deps Deps) {
	var svc *batches.Service
	if deps.DB != nil {
		svc = batches.NewService(deps.DB)
	}

	Register(api, huma.Operation{
		OperationID: "payment-batch-post",
		Method:      http.MethodPost,
		Path:        "/payment-batches/{id}/post",
		Summary:     "Post a batch atomically",
		Description: "Either every entry becomes an immutable payment plus its explicit coverage " +
			"event and the batch becomes posted, or nothing is written at all. Retrying with the " +
			"same Idempotency-Key returns the original result and cannot post the money twice.",
		Tags: []string{"payments"},
	}, OperationMeta{
		RequiredCapability: "payment.post",
		AuditAction:        "payment.batch.post",
		ConfirmationLevel:  ConfirmExplicit,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *PostBatchInput) (*PostBatchOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		version, err := requireIfMatch(input.IfMatch)
		if err != nil {
			return nil, err
		}
		result, err := svc.Post(ctx, principal, input.ID, batches.PostParams{
			ExpectedVersion: version,
			IdempotencyKey:  input.IdempotencyKey,
			Confirm:         ConfirmedFrom(ctx),
		}, time.Now())
		if err != nil {
			return nil, mapPostError(err)
		}
		audit.StampResource(ctx, "payment_batch", result.Batch.ID)
		return &PostBatchOutput{
			ETag: FormatETag(result.Batch.Version),
			Body: postResultToResponse(result),
		}, nil
	})

	Register(api, huma.Operation{
		OperationID:   "payment-create",
		Method:        http.MethodPost,
		Path:          "/payments",
		Summary:       "Record and post a single payment",
		Description:   "A convenience contract over the same posting primitive: the server creates a one-row batch and posts it, so a single payment and a batched one are indistinguishable in the ledger afterwards.",
		DefaultStatus: http.StatusCreated,
		Tags:          []string{"payments"},
	}, OperationMeta{
		RequiredCapability: "payment.post",
		AuditAction:        "payment.create",
		ConfirmationLevel:  ConfirmExplicit,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *CreatePaymentInput) (*CreatePaymentOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		result, err := svc.PostSinglePayment(ctx, principal, batches.SingleParams{
			Entry: batches.EntryInput{
				MembershipID:      input.Body.MembershipID,
				AmountCents:       input.Body.AmountCents,
				Method:            input.Body.Method,
				Reference:         input.Body.Reference,
				ReceivedOn:        input.Body.ReceivedOn,
				ReceivedByOfficer: input.Body.ReceivedByOfficer,
				PaidThrough:       input.Body.PaidThrough,
				TreasurerNote:     input.Body.TreasurerNote,
			},
			Label:          input.Body.Label,
			IdempotencyKey: input.IdempotencyKey,
			Confirm:        ConfirmedFrom(ctx),
		}, time.Now())
		if err != nil {
			return nil, mapPostError(err)
		}
		if len(result.Payments) > 0 {
			audit.StampResource(ctx, "payment", result.Payments[0].ID)
		}
		return &CreatePaymentOutput{
			ETag: FormatETag(result.Batch.Version),
			Body: postResultToResponse(result),
		}, nil
	})
}
