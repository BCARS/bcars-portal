package httpapi

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/audit"
	"github.com/bcars/bcars-portal/internal/domain/treasury"
)

// --- Response types ---
//
// Everything in this file is treasury-only. These responses carry amounts,
// references, receipt codes, correction reasons, and treasurer notes, which is
// exactly why they sit behind payment.read and payment.export rather than
// dues.read.

type LedgerEntry struct {
	PaymentID         int64  `json:"payment_id"`
	MembershipID      int64  `json:"membership_id"`
	BatchID           int64  `json:"batch_id,omitempty"`
	DisplayName       string `json:"display_name"`
	CallSign          string `json:"call_sign,omitempty"`
	AmountCents       int64  `json:"amount_cents"`
	Method            string `json:"method" enum:"cash,check,other"`
	Reference         string `json:"reference,omitempty"`
	ReceivedOn        string `json:"received_on" format:"date"`
	ReceivedByOfficer string `json:"received_by_officer,omitempty"`
	EnteredByUserID   int64  `json:"entered_by_user_id"`
	EnteredAt         string `json:"entered_at" format:"date-time"`
	ReceiptCode       string `json:"receipt_code"`
	EntryKind         string `json:"entry_kind" enum:"original,reversal,replacement"`
	CorrectsPaymentID int64  `json:"corrects_payment_id,omitempty"`
	TreasurerNote     string `json:"treasurer_note,omitempty"`
	Superseded        bool   `json:"superseded" doc:"True when a correction has already replaced this row."`
}

type Receipt struct {
	ReceiptCode       string `json:"receipt_code" doc:"Stable and non-secret: it identifies the payment but grants no access to it."`
	PaymentID         int64  `json:"payment_id"`
	MembershipID      int64  `json:"membership_id"`
	DisplayName       string `json:"display_name"`
	CallSign          string `json:"call_sign,omitempty"`
	BaseType          string `json:"base_type"`
	AmountCents       int64  `json:"amount_cents"`
	Method            string `json:"method"`
	Reference         string `json:"reference,omitempty"`
	ReceivedOn        string `json:"received_on" format:"date"`
	ReceivedByOfficer string `json:"received_by_officer,omitempty"`
	EnteredAt         string `json:"entered_at" format:"date-time"`
	EntryKind         string `json:"entry_kind"`
	BatchID           int64  `json:"batch_id,omitempty"`
	PaidThrough       string `json:"paid_through,omitempty" format:"date"`
	Superseded        bool   `json:"superseded" doc:"True when a correction replaced this payment; a reprint must not look current."`
}

type BatchActivityEntry struct {
	Kind        string `json:"kind" enum:"opened,posted,abandoned,corrected"`
	At          string `json:"at" format:"date-time"`
	ActorUserID int64  `json:"actor_user_id,omitempty"`
	Summary     string `json:"summary" doc:"Plain language, for an officer asking what happened to this batch."`
	Reason      string `json:"reason,omitempty" doc:"The officer's own words, when they gave any."`
}

type AppliedFilter struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type TreasuryExport struct {
	Filename       string          `json:"filename"`
	GeneratedAt    string          `json:"generated_at" format:"date-time"`
	AppliedFilters []AppliedFilter `json:"applied_filters" doc:"What produced these rows. An export that does not say what it excluded is a liability at audit time."`
	RowCount       int             `json:"row_count"`
	Format         string          `json:"format" enum:"csv"`
	Data           string          `json:"data" doc:"Base64-encoded CSV."`
}

// --- Inputs and outputs ---

type LedgerListInput struct {
	PageQuery
	MembershipID  int64  `query:"membership_id" doc:"Filter to one member."`
	BatchID       int64  `query:"batch_id" doc:"Filter to one batch."`
	Method        string `query:"method" enum:"cash,check,other"`
	ReceiptCode   string `query:"receipt_code"`
	ReceivedFrom  string `query:"received_from" doc:"ISO date, inclusive."`
	ReceivedTo    string `query:"received_to" doc:"ISO date, inclusive."`
	EffectiveOnly bool   `query:"effective_only" doc:"Hide reversals and superseded originals, leaving what the club currently holds."`
}
type LedgerListOutput struct {
	Body Page[LedgerEntry]
}

type ReceiptInput struct {
	ID int64 `path:"id"`
}
type ReceiptOutput struct {
	Body Receipt
}

type BatchActivityInput struct {
	ID int64 `path:"id"`
}
type BatchActivityOutput struct {
	Body struct {
		BatchID  int64                `json:"batch_id"`
		Activity []BatchActivityEntry `json:"activity"`
	}
}

type ExportLedgerBody struct {
	MembershipID  int64  `json:"membership_id,omitempty"`
	BatchID       int64  `json:"batch_id,omitempty"`
	Method        string `json:"method,omitempty" enum:"cash,check,other"`
	ReceiptCode   string `json:"receipt_code,omitempty"`
	ReceivedFrom  string `json:"received_from,omitempty" format:"date"`
	ReceivedTo    string `json:"received_to,omitempty" format:"date"`
	EffectiveOnly bool   `json:"effective_only,omitempty"`
}
type ExportLedgerInput struct{ Body ExportLedgerBody }
type ExportLedgerOutput struct {
	Body TreasuryExport
}

type ExportBatchInput struct {
	ID int64 `path:"id"`
}
type ExportBatchOutput struct {
	Body TreasuryExport
}

// --- Conversions ---

func ledgerEntryToResponse(e treasury.LedgerEntry) LedgerEntry {
	return LedgerEntry{
		PaymentID:         e.PaymentID,
		MembershipID:      e.MembershipID,
		BatchID:           e.BatchID,
		DisplayName:       e.DisplayName,
		CallSign:          e.CallSign,
		AmountCents:       e.AmountCents,
		Method:            e.Method,
		Reference:         e.Reference,
		ReceivedOn:        e.ReceivedOn,
		ReceivedByOfficer: e.ReceivedByOfficer,
		EnteredByUserID:   e.EnteredByUserID,
		EnteredAt:         e.EnteredAt,
		ReceiptCode:       e.ReceiptCode,
		EntryKind:         e.EntryKind,
		CorrectsPaymentID: e.CorrectsPaymentID,
		TreasurerNote:     e.TreasurerNote,
		Superseded:        e.Superseded,
	}
}

func exportToResponse(e treasury.Export) TreasuryExport {
	out := TreasuryExport{
		Filename:    e.Filename,
		GeneratedAt: e.GeneratedAt,
		RowCount:    e.RowCount,
		Format:      "csv",
		Data:        base64.StdEncoding.EncodeToString([]byte(e.CSV)),
	}
	for _, f := range e.AppliedFilters {
		out.AppliedFilters = append(out.AppliedFilters, AppliedFilter{Name: f.Name, Value: f.Value})
	}
	return out
}

func mapTreasuryError(err error) error {
	if errors.Is(err, treasury.ErrExportTooLarge) {
		return huma.Error422UnprocessableEntity(err.Error())
	}
	return mapDomainError(err)
}

// RegisterTreasury registers treasury history, receipt, and export endpoints.
func RegisterTreasury(api huma.API, deps Deps) {
	var svc *treasury.Service
	if deps.DB != nil {
		svc = treasury.NewService(deps.DB)
	}

	queryFrom := func(in *LedgerListInput) treasury.LedgerQuery {
		return treasury.LedgerQuery{
			MembershipID:  in.MembershipID,
			BatchID:       in.BatchID,
			Method:        in.Method,
			ReceiptCode:   in.ReceiptCode,
			ReceivedFrom:  in.ReceivedFrom,
			ReceivedTo:    in.ReceivedTo,
			EffectiveOnly: in.EffectiveOnly,
		}
	}

	Register(api, huma.Operation{
		OperationID: "ledger-list",
		Method:      http.MethodGet,
		Path:        "/ledger-entries",
		Summary:     "Page and filter posted ledger entries",
		Description: "Ordered newest received first, with a stable tie break on id so paging " +
			"cannot repeat or skip a row.",
		Tags: []string{"treasury"},
	}, OperationMeta{
		RequiredCapability: "payment.read",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *LedgerListInput) (*LedgerListOutput, error) {
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
			limit = treasury.DefaultLimit
		}
		q := queryFrom(input)
		q.Limit = limit
		q.Offset = offset

		rows, err := svc.ListLedger(ctx, principal, q)
		if err != nil {
			return nil, mapTreasuryError(err)
		}
		data := make([]LedgerEntry, len(rows))
		for i, r := range rows {
			data[i] = ledgerEntryToResponse(r)
		}
		return &LedgerListOutput{Body: Page[LedgerEntry]{
			Data:       data,
			NextCursor: nextCursor(len(rows), limit, offset),
		}}, nil
	})

	Register(api, huma.Operation{
		OperationID: "payment-receipt",
		Method:      http.MethodGet,
		Path:        "/payments/{id}/receipt",
		Summary:     "Read the printable receipt for a payment",
		Tags:        []string{"treasury"},
	}, OperationMeta{
		RequiredCapability: "payment.read",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *ReceiptInput) (*ReceiptOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		r, err := svc.GetReceipt(ctx, principal, input.ID)
		if err != nil {
			return nil, mapTreasuryError(err)
		}
		return &ReceiptOutput{Body: Receipt{
			ReceiptCode:       r.ReceiptCode,
			PaymentID:         r.PaymentID,
			MembershipID:      r.MembershipID,
			DisplayName:       r.DisplayName,
			CallSign:          r.CallSign,
			BaseType:          r.BaseType,
			AmountCents:       r.AmountCents,
			Method:            r.Method,
			Reference:         r.Reference,
			ReceivedOn:        r.ReceivedOn,
			ReceivedByOfficer: r.ReceivedByOfficer,
			EnteredAt:         r.EnteredAt,
			EntryKind:         r.EntryKind,
			BatchID:           r.BatchID,
			PaidThrough:       r.PaidThrough,
			Superseded:        r.Superseded,
		}}, nil
	})

	Register(api, huma.Operation{
		OperationID: "payment-batch-activity",
		Method:      http.MethodGet,
		Path:        "/payment-batches/{id}/activity",
		Summary:     "What happened to this batch, in plain language",
		Tags:        []string{"treasury"},
	}, OperationMeta{
		RequiredCapability: "payment.read",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *BatchActivityInput) (*BatchActivityOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		entries, err := svc.BatchActivity(ctx, principal, input.ID)
		if err != nil {
			return nil, mapTreasuryError(err)
		}
		out := &BatchActivityOutput{}
		out.Body.BatchID = input.ID
		for _, e := range entries {
			out.Body.Activity = append(out.Body.Activity, BatchActivityEntry{
				Kind: e.Kind, At: e.At, ActorUserID: e.ActorUserID,
				Summary: e.Summary, Reason: e.Reason,
			})
		}
		return out, nil
	})

	Register(api, huma.Operation{
		OperationID: "export-treasury",
		Method:      http.MethodPost,
		Path:        "/exports/treasury",
		Summary:     "Export filtered ledger entries to CSV for the books",
		Description: "Deterministic given the same filters and data. Cells that a spreadsheet " +
			"would evaluate as a formula are neutralized, and amounts are rendered from integer " +
			"cents without float arithmetic.",
		Tags: []string{"treasury", "exports"},
	}, OperationMeta{
		RequiredCapability: "payment.export",
		AuditAction:        "payment.export",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *ExportLedgerInput) (*ExportLedgerOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		export, err := svc.ExportLedger(ctx, principal, treasury.LedgerQuery{
			MembershipID:  input.Body.MembershipID,
			BatchID:       input.Body.BatchID,
			Method:        input.Body.Method,
			ReceiptCode:   input.Body.ReceiptCode,
			ReceivedFrom:  input.Body.ReceivedFrom,
			ReceivedTo:    input.Body.ReceivedTo,
			EffectiveOnly: input.Body.EffectiveOnly,
		}, time.Now())
		if err != nil {
			return nil, mapTreasuryError(err)
		}
		return &ExportLedgerOutput{Body: exportToResponse(export)}, nil
	})

	Register(api, huma.Operation{
		OperationID: "export-payment-batch",
		Method:      http.MethodPost,
		Path:        "/payment-batches/{id}/export",
		Summary:     "Export one batch's posted rows to CSV",
		Tags:        []string{"treasury", "exports"},
	}, OperationMeta{
		RequiredCapability: "payment.export",
		AuditAction:        "payment.export",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *ExportBatchInput) (*ExportBatchOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		export, err := svc.ExportBatch(ctx, principal, input.ID, time.Now())
		if err != nil {
			return nil, mapTreasuryError(err)
		}
		audit.StampResource(ctx, "payment_batch", input.ID)
		return &ExportBatchOutput{Body: exportToResponse(export)}, nil
	})
}
