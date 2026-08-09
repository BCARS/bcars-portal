package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/audit"
	"github.com/bcars/bcars-portal/internal/domain/batches"
	"github.com/bcars/bcars-portal/internal/domain/idem"
)

// --- Response types ---

type BatchTotals struct {
	EntryCount      int64 `json:"entry_count"`
	CashCount       int64 `json:"cash_count"`
	CashTotalCents  int64 `json:"cash_total_cents"`
	CheckCount      int64 `json:"check_count"`
	CheckTotalCents int64 `json:"check_total_cents"`
	OtherCount      int64 `json:"other_count"`
	OtherTotalCents int64 `json:"other_total_cents"`
	NetTotalCents   int64 `json:"net_total_cents"`
}

type BatchEntry struct {
	ID                int64  `json:"id"`
	BatchID           int64  `json:"batch_id"`
	MembershipID      int64  `json:"membership_id"`
	Sequence          int64  `json:"sequence" doc:"Server-assigned and stable; removing a row never renumbers the others."`
	AmountCents       int64  `json:"amount_cents"`
	Method            string `json:"method" enum:"cash,check,other"`
	Reference         string `json:"reference,omitempty" doc:"Check number or similar."`
	ReceivedOn        string `json:"received_on" format:"date"`
	ReceivedByOfficer string `json:"received_by_officer,omitempty"`
	PaidThrough       string `json:"paid_through" format:"date" doc:"The coverage this row will grant when the batch is posted."`
	TreasurerNote     string `json:"treasurer_note,omitempty"`
	Version           int64  `json:"version"`
	CreatedAt         string `json:"created_at" format:"date-time"`
	UpdatedAt         string `json:"updated_at" format:"date-time"`
}

type PaymentBatch struct {
	ID     int64       `json:"id"`
	Label  string      `json:"label"`
	State  string      `json:"state" enum:"open,posted,abandoned"`
	Totals BatchTotals `json:"totals" doc:"Always calculated by the server; a client never submits a total."`

	DefaultAmountCents int64  `json:"default_amount_cents,omitempty" doc:"Prefill hint for a new row. Every entry still carries explicit values."`
	DefaultPaidThrough string `json:"default_paid_through,omitempty" format:"date" doc:"Prefill hint for a new row."`

	OpenedByUserID int64  `json:"opened_by_user_id"`
	OpenedAt       string `json:"opened_at" format:"date-time"`

	PostedByUserID int64  `json:"posted_by_user_id,omitempty"`
	PostedAt       string `json:"posted_at,omitempty" format:"date-time"`

	AbandonedByUserID int64  `json:"abandoned_by_user_id,omitempty"`
	AbandonedAt       string `json:"abandoned_at,omitempty" format:"date-time"`
	AbandonReason     string `json:"abandon_reason,omitempty"`

	WorksheetRunID int64 `json:"worksheet_run_id,omitempty" doc:"The renewal sheet this batch was opened from, when it was one."`

	Version   int64  `json:"version"`
	CreatedAt string `json:"created_at" format:"date-time"`
	UpdatedAt string `json:"updated_at" format:"date-time"`

	Entries []BatchEntry `json:"entries,omitempty" doc:"Present on batch detail, absent from the list."`
}

// --- Inputs and outputs ---

type BatchListInput struct {
	PageQuery
	State string `query:"state" enum:"open,posted,abandoned" doc:"Filter by batch state."`
}
type BatchListOutput struct {
	Body Page[PaymentBatch]
}

type BatchGetInput struct {
	ID int64 `path:"id"`
}
type BatchGetOutput struct {
	ETag string `header:"ETag"`
	Body PaymentBatch
}

type OpenBatchBody struct {
	Label              string `json:"label" minLength:"1" doc:"How the treasurer will recognise this batch later."`
	DefaultAmountCents int64  `json:"default_amount_cents,omitempty" minimum:"0"`
	DefaultPaidThrough string `json:"default_paid_through,omitempty" format:"date"`
}
type OpenBatchInput struct {
	IdempotencyKey string `header:"Idempotency-Key" doc:"Optional client-generated key so a retry cannot open a second batch."`
	Body           OpenBatchBody
}
type OpenBatchOutput struct {
	ETag string `header:"ETag"`
	Body PaymentBatch
}

type UpdateBatchBody struct {
	Label              string `json:"label" minLength:"1"`
	DefaultAmountCents int64  `json:"default_amount_cents,omitempty" minimum:"0"`
	DefaultPaidThrough string `json:"default_paid_through,omitempty" format:"date"`
}
type UpdateBatchInput struct {
	ID      int64  `path:"id"`
	IfMatch string `header:"If-Match" doc:"Batch version you last read. Required: a missing header is a 428."`
	Body    UpdateBatchBody
}
type UpdateBatchOutput struct {
	ETag string `header:"ETag"`
	Body PaymentBatch
}

type AbandonBatchBody struct {
	Reason string `json:"reason" minLength:"1" doc:"Why this batch was abandoned. Required, and permanent."`
}
type AbandonBatchInput struct {
	ID      int64  `path:"id"`
	IfMatch string `header:"If-Match" doc:"Batch version you last read. Required: a missing header is a 428."`
	Body    AbandonBatchBody
}
type AbandonBatchOutput struct {
	ETag string `header:"ETag"`
	Body PaymentBatch
}

// BatchEntryBody carries the explicit values for a draft row. Batch defaults
// prefill the client's form; they are never substituted server-side, so a value
// the treasurer typed is never silently replaced.
type BatchEntryBody struct {
	MembershipID      int64  `json:"membership_id" minimum:"1"`
	AmountCents       int64  `json:"amount_cents" minimum:"1"`
	Method            string `json:"method" enum:"cash,check,other"`
	Reference         string `json:"reference,omitempty"`
	ReceivedOn        string `json:"received_on" format:"date"`
	ReceivedByOfficer string `json:"received_by_officer,omitempty"`
	PaidThrough       string `json:"paid_through" format:"date"`
	TreasurerNote     string `json:"treasurer_note,omitempty"`
}

type CreateBatchEntryInput struct {
	ID             int64  `path:"id"`
	IdempotencyKey string `header:"Idempotency-Key" doc:"Optional client-generated key so a retry cannot add the row twice."`
	Body           BatchEntryBody
}

// BatchEntryOutput returns the row plus the batch it belongs to, because every
// entry mutation moves the batch version and totals. The ETag is the batch
// version, which is what a later post is checked against.
type BatchEntryOutput struct {
	ETag string `header:"ETag"`
	Body struct {
		Entry BatchEntry   `json:"entry"`
		Batch PaymentBatch `json:"batch"`
	}
}

type UpdateBatchEntryInput struct {
	ID      int64  `path:"id"`
	EntryID int64  `path:"entry_id"`
	IfMatch string `header:"If-Match" doc:"Entry version you last read. Required: a missing header is a 428."`
	Body    BatchEntryBody
}

type DeleteBatchEntryInput struct {
	ID      int64  `path:"id"`
	EntryID int64  `path:"entry_id"`
	IfMatch string `header:"If-Match" doc:"Entry version you last read. Required: a missing header is a 428."`
}
type DeleteBatchEntryOutput struct {
	ETag string `header:"ETag"`
	Body PaymentBatch
}

// --- Conversions ---

func batchEntryToResponse(e batches.Entry) BatchEntry {
	return BatchEntry{
		ID:                e.ID,
		BatchID:           e.BatchID,
		MembershipID:      e.MembershipID,
		Sequence:          e.Sequence,
		AmountCents:       e.AmountCents,
		Method:            e.Method,
		Reference:         e.Reference,
		ReceivedOn:        e.ReceivedOn,
		ReceivedByOfficer: e.ReceivedByOfficer,
		PaidThrough:       e.PaidThrough,
		TreasurerNote:     e.TreasurerNote,
		Version:           e.Version,
		CreatedAt:         e.CreatedAt,
		UpdatedAt:         e.UpdatedAt,
	}
}

func batchToResponse(b batches.Batch) PaymentBatch {
	out := PaymentBatch{
		ID:    b.ID,
		Label: b.Label,
		State: b.State,
		Totals: BatchTotals{
			EntryCount:      b.Totals.EntryCount,
			CashCount:       b.Totals.CashCount,
			CashTotalCents:  b.Totals.CashTotalCents,
			CheckCount:      b.Totals.CheckCount,
			CheckTotalCents: b.Totals.CheckTotalCents,
			OtherCount:      b.Totals.OtherCount,
			OtherTotalCents: b.Totals.OtherTotalCents,
			NetTotalCents:   b.Totals.NetTotalCents,
		},
		DefaultAmountCents: b.DefaultAmountCents,
		DefaultPaidThrough: b.DefaultPaidThrough,
		OpenedByUserID:     b.OpenedByUserID,
		OpenedAt:           b.OpenedAt,
		PostedByUserID:     b.PostedByUserID,
		PostedAt:           b.PostedAt,
		AbandonedByUserID:  b.AbandonedByUserID,
		AbandonedAt:        b.AbandonedAt,
		AbandonReason:      b.AbandonReason,
		WorksheetRunID:     b.WorksheetRunID,
		Version:            b.Version,
		CreatedAt:          b.CreatedAt,
		UpdatedAt:          b.UpdatedAt,
	}
	for _, e := range b.Entries {
		out.Entries = append(out.Entries, batchEntryToResponse(e))
	}
	return out
}

func entryInputFromBody(b BatchEntryBody) batches.EntryInput {
	return batches.EntryInput{
		MembershipID:      b.MembershipID,
		AmountCents:       b.AmountCents,
		Method:            b.Method,
		Reference:         b.Reference,
		ReceivedOn:        b.ReceivedOn,
		ReceivedByOfficer: b.ReceivedByOfficer,
		PaidThrough:       b.PaidThrough,
		TreasurerNote:     b.TreasurerNote,
	}
}

// mapBatchError translates batch-domain errors to HTTP status codes.
func mapBatchError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, batches.ErrBatchNotOpen):
		return ErrConflict("this batch is posted or abandoned and accepts no further changes")
	case errors.Is(err, batches.ErrReasonRequired):
		return huma.Error422UnprocessableEntity("a reason is required")
	case errors.Is(err, batches.ErrLabelRequired):
		return huma.Error422UnprocessableEntity("a label is required")
	case errors.Is(err, batches.ErrInvalidAmount),
		errors.Is(err, batches.ErrInvalidMethod),
		errors.Is(err, batches.ErrInvalidDate):
		return huma.Error422UnprocessableEntity(err.Error())
	case errors.Is(err, batches.ErrEntryNotInBatch):
		return huma.Error404NotFound("entry not found in that batch")
	case errors.Is(err, idem.ErrKeyReused):
		return ErrIdempotencyMismatch("this Idempotency-Key was already used with a different request")
	}
	return mapDomainError(err)
}

// RegisterBatches registers draft payment-batch endpoints.
func RegisterBatches(api huma.API, deps Deps) {
	var svc *batches.Service
	if deps.DB != nil {
		svc = batches.NewService(deps.DB)
	}

	Register(api, huma.Operation{
		OperationID: "payment-batches-list",
		Method:      http.MethodGet,
		Path:        "/payment-batches",
		Summary:     "List payment batches",
		Tags:        []string{"payments"},
	}, OperationMeta{
		RequiredCapability: "payment.read",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *BatchListInput) (*BatchListOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		offset, err := offsetFromCursor(input.Cursor)
		if err != nil {
			return nil, err
		}
		limit := int64(input.Limit)
		if limit <= 0 {
			limit = batches.DefaultLimit
		}
		rows, err := svc.List(ctx, principal, input.State, limit, offset)
		if err != nil {
			return nil, mapBatchError(err)
		}
		data := make([]PaymentBatch, len(rows))
		for i, b := range rows {
			data[i] = batchToResponse(b)
		}
		return &BatchListOutput{Body: Page[PaymentBatch]{
			Data:       data,
			NextCursor: nextCursor(len(rows), limit, offset),
		}}, nil
	})

	Register(api, huma.Operation{
		OperationID: "payment-batch-get",
		Method:      http.MethodGet,
		Path:        "/payment-batches/{id}",
		Summary:     "Read a batch with its entries and server-calculated totals",
		Tags:        []string{"payments"},
	}, OperationMeta{
		RequiredCapability: "payment.read",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *BatchGetInput) (*BatchGetOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		b, err := svc.Get(ctx, principal, input.ID)
		if err != nil {
			return nil, mapBatchError(err)
		}
		return &BatchGetOutput{ETag: FormatETag(b.Version), Body: batchToResponse(b)}, nil
	})

	Register(api, huma.Operation{
		OperationID:   "payment-batch-open",
		Method:        http.MethodPost,
		Path:          "/payment-batches",
		Summary:       "Open a draft batch",
		Description:   "A draft batch creates no payment and no coverage event, and changes no member's dues standing until it is posted.",
		DefaultStatus: http.StatusCreated,
		Tags:          []string{"payments"},
	}, OperationMeta{
		RequiredCapability: "payment.batch.manage",
		AuditAction:        "payment.batch.open",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *OpenBatchInput) (*OpenBatchOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		b, err := svc.Open(ctx, principal, batches.OpenParams{
			Label:              input.Body.Label,
			DefaultAmountCents: input.Body.DefaultAmountCents,
			DefaultPaidThrough: input.Body.DefaultPaidThrough,
			IdempotencyKey:     input.IdempotencyKey,
		}, time.Now())
		if err != nil {
			return nil, mapBatchError(err)
		}
		audit.StampResource(ctx, "payment_batch", b.ID)
		return &OpenBatchOutput{ETag: FormatETag(b.Version), Body: batchToResponse(b)}, nil
	})

	Register(api, huma.Operation{
		OperationID: "payment-batch-update",
		Method:      http.MethodPatch,
		Path:        "/payment-batches/{id}",
		Summary:     "Change an open batch's label and new-row defaults",
		Tags:        []string{"payments"},
	}, OperationMeta{
		RequiredCapability: "payment.batch.manage",
		AuditAction:        "payment.batch.update",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *UpdateBatchInput) (*UpdateBatchOutput, error) {
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
		b, err := svc.Update(ctx, principal, input.ID, batches.UpdateParams{
			Label:              input.Body.Label,
			DefaultAmountCents: input.Body.DefaultAmountCents,
			DefaultPaidThrough: input.Body.DefaultPaidThrough,
			ExpectedVersion:    version,
		})
		if err != nil {
			return nil, mapBatchError(err)
		}
		audit.StampResource(ctx, "payment_batch", b.ID)
		return &UpdateBatchOutput{ETag: FormatETag(b.Version), Body: batchToResponse(b)}, nil
	})

	Register(api, huma.Operation{
		OperationID: "payment-batch-abandon",
		Method:      http.MethodPost,
		Path:        "/payment-batches/{id}/abandon",
		Summary:     "Abandon an open batch",
		Description: "Terminal and audited. The draft rows stay readable as the record of what was abandoned.",
		Tags:        []string{"payments"},
	}, OperationMeta{
		RequiredCapability: "payment.batch.manage",
		AuditAction:        "payment.batch.abandon",
		ConfirmationLevel:  "explicit-confirm",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *AbandonBatchInput) (*AbandonBatchOutput, error) {
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
		b, err := svc.Abandon(ctx, principal, input.ID, input.Body.Reason, version, time.Now())
		if err != nil {
			return nil, mapBatchError(err)
		}
		audit.StampResource(ctx, "payment_batch", b.ID)
		return &AbandonBatchOutput{ETag: FormatETag(b.Version), Body: batchToResponse(b)}, nil
	})

	Register(api, huma.Operation{
		OperationID:   "payment-batch-entry-create",
		Method:        http.MethodPost,
		Path:          "/payment-batches/{id}/entries",
		Summary:       "Add a draft row",
		Description:   "Returns the row and the batch, whose version and totals both move on every entry mutation.",
		DefaultStatus: http.StatusCreated,
		Tags:          []string{"payments"},
	}, OperationMeta{
		RequiredCapability: "payment.batch.manage",
		AuditAction:        "payment.batch.entry.create",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *CreateBatchEntryInput) (*BatchEntryOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		entry, batch, err := svc.AddEntry(ctx, principal, input.ID,
			entryInputFromBody(input.Body), input.IdempotencyKey)
		if err != nil {
			return nil, mapBatchError(err)
		}
		audit.StampResource(ctx, "payment_batch_entry", entry.ID)

		out := &BatchEntryOutput{ETag: FormatETag(batch.Version)}
		out.Body.Entry = batchEntryToResponse(entry)
		out.Body.Batch = batchToResponse(batch)
		return out, nil
	})

	Register(api, huma.Operation{
		OperationID: "payment-batch-entry-update",
		Method:      http.MethodPut,
		Path:        "/payment-batches/{id}/entries/{entry_id}",
		Summary:     "Edit a draft row",
		Description: "Only open batches allow this. A posted payment is corrected, never edited.",
		Tags:        []string{"payments"},
	}, OperationMeta{
		RequiredCapability: "payment.batch.manage",
		AuditAction:        "payment.batch.entry.update",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *UpdateBatchEntryInput) (*BatchEntryOutput, error) {
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
		entry, batch, err := svc.UpdateEntry(ctx, principal, input.ID, input.EntryID,
			entryInputFromBody(input.Body), version)
		if err != nil {
			return nil, mapBatchError(err)
		}
		audit.StampResource(ctx, "payment_batch_entry", entry.ID)

		out := &BatchEntryOutput{ETag: FormatETag(batch.Version)}
		out.Body.Entry = batchEntryToResponse(entry)
		out.Body.Batch = batchToResponse(batch)
		return out, nil
	})

	Register(api, huma.Operation{
		OperationID: "payment-batch-entry-delete",
		Method:      http.MethodDelete,
		Path:        "/payment-batches/{id}/entries/{entry_id}",
		Summary:     "Remove a draft row",
		Description: "Remaining sequence numbers are left alone so an order already read off a printed sheet does not shift.",
		Tags:        []string{"payments"},
	}, OperationMeta{
		RequiredCapability: "payment.batch.manage",
		AuditAction:        "payment.batch.entry.delete",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *DeleteBatchEntryInput) (*DeleteBatchEntryOutput, error) {
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
		batch, err := svc.DeleteEntry(ctx, principal, input.ID, input.EntryID, version)
		if err != nil {
			return nil, mapBatchError(err)
		}
		audit.StampResource(ctx, "payment_batch_entry", input.EntryID)
		return &DeleteBatchEntryOutput{
			ETag: FormatETag(batch.Version),
			Body: batchToResponse(batch),
		}, nil
	})
}

// requireIfMatch parses a mandatory If-Match header.
func requireIfMatch(raw string) (int64, error) {
	version, err := parseIfMatchValue(raw)
	if err != nil {
		return 0, err
	}
	if version == 0 {
		return 0, huma.Error428PreconditionRequired(
			"If-Match is required: send the version you last read")
	}
	return version, nil
}
