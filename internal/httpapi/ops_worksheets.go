package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/audit"
	"github.com/bcars/bcars-portal/internal/domain/worksheets"
)

// --- Response types ---

type WorksheetRun struct {
	ID           int64  `json:"id"`
	Label        string `json:"label,omitempty"`
	AsOf         string `json:"as_of" format:"date" doc:"The date standing was judged against, which is what makes the sheet reproducible."`
	FilterKind   string `json:"filter_kind" enum:"owes,active,unpaid_since_run"`
	SourceRunID  int64  `json:"source_run_id,omitempty"`
	SortOrder    string `json:"sort_order" enum:"last_name,call_sign,longest_overdue"`
	IncludeEmail bool   `json:"include_email" doc:"Whether email was actually included, which the server decides."`
	IncludePhone bool   `json:"include_phone"`
	WarningDays  int64  `json:"warning_days"`
	GeneratedBy  int64  `json:"generated_by_user_id"`
	GeneratedAt  string `json:"generated_at" format:"date-time" doc:"Also the \"good as of\" stamp for the contact snapshot."`
	RowCount     int64  `json:"row_count"`
}

type WorksheetRow struct {
	ID           int64  `json:"id"`
	Ordinal      int64  `json:"ordinal" doc:"Stable print order. A batch created from this run reuses it."`
	MembershipID int64  `json:"membership_id"`
	DisplayName  string `json:"display_name"`
	CallSign     string `json:"call_sign,omitempty"`
	BaseType     string `json:"base_type"`
	DuesStatus   string `json:"dues_status"`
	PaidThrough  string `json:"paid_through,omitempty" format:"date"`
	Email        string `json:"email,omitempty" doc:"As it stood when the sheet was generated."`
	Phone        string `json:"phone,omitempty"`
	EnteredSince bool   `json:"entered_since" doc:"A payment has been posted for this member since the sheet ran. Computed at read time; the snapshot is never rewritten."`
}

// --- Inputs and outputs ---

type CreateWorksheetBody struct {
	Label        string `json:"label,omitempty"`
	AsOf         string `json:"as_of,omitempty" format:"date" doc:"Defaults to today (UTC)."`
	FilterKind   string `json:"filter_kind" enum:"owes,active,unpaid_since_run"`
	SourceRunID  int64  `json:"source_run_id,omitempty" doc:"Required for unpaid_since_run."`
	SortOrder    string `json:"sort_order" enum:"last_name,call_sign,longest_overdue" default:"last_name"`
	IncludeEmail bool   `json:"include_email,omitempty" doc:"A request, not a grant: contact columns are authorized server-side."`
	IncludePhone bool   `json:"include_phone,omitempty"`
	WarningDays  int    `json:"warning_days,omitempty" minimum:"1" maximum:"365"`
}
type CreateWorksheetInput struct{ Body CreateWorksheetBody }
type CreateWorksheetOutput struct {
	Body struct {
		Run  WorksheetRun   `json:"run"`
		Rows []WorksheetRow `json:"rows"`
	}
}

type WorksheetListInput struct{ PageQuery }
type WorksheetListOutput struct {
	Body Page[WorksheetRun]
}

type WorksheetGetInput struct {
	ID int64 `path:"id"`
}
type WorksheetGetOutput struct {
	Body WorksheetRun
}

type WorksheetRowsInput struct {
	PageQuery
	ID int64 `path:"id"`
}
type WorksheetRowsOutput struct {
	Body Page[WorksheetRow]
}

type LinkWorksheetBatchBody struct {
	BatchID int64 `json:"batch_id" minimum:"1"`
}
type LinkWorksheetBatchInput struct {
	ID   int64 `path:"id"`
	Body LinkWorksheetBatchBody
}
type LinkWorksheetBatchOutput struct {
	Body struct {
		RunID   int64 `json:"run_id"`
		BatchID int64 `json:"batch_id"`
	}
}

// --- Conversions ---

func worksheetRunToResponse(r worksheets.Run) WorksheetRun {
	return WorksheetRun{
		ID: r.ID, Label: r.Label, AsOf: r.AsOf, FilterKind: r.FilterKind,
		SourceRunID: r.SourceRunID, SortOrder: r.SortOrder,
		IncludeEmail: r.IncludeEmail, IncludePhone: r.IncludePhone,
		WarningDays: r.WarningDays, GeneratedBy: r.GeneratedBy,
		GeneratedAt: r.GeneratedAt, RowCount: r.RowCount,
	}
}

func worksheetRowToResponse(r worksheets.Row) WorksheetRow {
	return WorksheetRow{
		ID: r.ID, Ordinal: r.Ordinal, MembershipID: r.MembershipID,
		DisplayName: r.DisplayName, CallSign: r.CallSign, BaseType: r.BaseType,
		DuesStatus: r.DuesStatus, PaidThrough: r.PaidThrough,
		Email: r.Email, Phone: r.Phone, EnteredSince: r.EnteredSince,
	}
}

func mapWorksheetError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, worksheets.ErrUnknownFilter),
		errors.Is(err, worksheets.ErrUnknownSort),
		errors.Is(err, worksheets.ErrSourceRunRequired),
		errors.Is(err, worksheets.ErrTooManyRows):
		return huma.Error422UnprocessableEntity(err.Error())
	case errors.Is(err, worksheets.ErrBatchNotEmpty):
		return ErrConflict(err.Error())
	}
	return mapDomainError(err)
}

// RegisterWorksheets registers renewal worksheet endpoints.
func RegisterWorksheets(api huma.API, deps Deps) {
	var svc *worksheets.Service
	if deps.DB != nil {
		svc = worksheets.NewService(deps.DB)
	}

	Register(api, huma.Operation{
		OperationID:   "worksheet-create",
		Method:        http.MethodPost,
		Path:          "/dues-worksheets",
		Summary:       "Generate and save a renewal worksheet",
		DefaultStatus: http.StatusCreated,
		Description: "Snapshots the rows as printed, in print order, so the sheet is reproducible " +
			"months later even as the underlying data moves.",
		Tags: []string{"worksheets"},
	}, OperationMeta{
		RequiredCapability: "dues.worksheet.manage",
		AuditAction:        "dues.worksheet.create",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *CreateWorksheetInput) (*CreateWorksheetOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		asOf, err := parseAsOf(input.Body.AsOf)
		if err != nil {
			return nil, err
		}
		sortOrder := input.Body.SortOrder
		if sortOrder == "" {
			sortOrder = worksheets.SortLastName
		}

		run, rows, err := svc.Create(ctx, principal, worksheets.CreateParams{
			Label:        input.Body.Label,
			AsOf:         asOf,
			FilterKind:   input.Body.FilterKind,
			SourceRunID:  input.Body.SourceRunID,
			SortOrder:    sortOrder,
			IncludeEmail: input.Body.IncludeEmail,
			IncludePhone: input.Body.IncludePhone,
			WarningDays:  input.Body.WarningDays,
		}, time.Now())
		if err != nil {
			return nil, mapWorksheetError(err)
		}
		audit.StampResource(ctx, "dues_worksheet_run", run.ID)

		out := &CreateWorksheetOutput{}
		out.Body.Run = worksheetRunToResponse(run)
		for _, r := range rows {
			out.Body.Rows = append(out.Body.Rows, worksheetRowToResponse(r))
		}
		return out, nil
	})

	Register(api, huma.Operation{
		OperationID: "worksheet-list",
		Method:      http.MethodGet,
		Path:        "/dues-worksheets",
		Summary:     "List saved worksheet runs",
		Tags:        []string{"worksheets"},
	}, OperationMeta{
		RequiredCapability: "dues.worksheet.manage",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *WorksheetListInput) (*WorksheetListOutput, error) {
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
			limit = worksheets.DefaultLimit
		}
		runs, err := svc.List(ctx, principal, limit, offset)
		if err != nil {
			return nil, mapWorksheetError(err)
		}
		data := make([]WorksheetRun, len(runs))
		for i, r := range runs {
			data[i] = worksheetRunToResponse(r)
		}
		return &WorksheetListOutput{Body: Page[WorksheetRun]{
			Data: data, NextCursor: nextCursor(len(runs), limit, offset),
		}}, nil
	})

	Register(api, huma.Operation{
		OperationID: "worksheet-get",
		Method:      http.MethodGet,
		Path:        "/dues-worksheets/{id}",
		Summary:     "Read a worksheet run",
		Tags:        []string{"worksheets"},
	}, OperationMeta{
		RequiredCapability: "dues.worksheet.manage",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *WorksheetGetInput) (*WorksheetGetOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		run, err := svc.Get(ctx, principal, input.ID)
		if err != nil {
			return nil, mapWorksheetError(err)
		}
		return &WorksheetGetOutput{Body: worksheetRunToResponse(run)}, nil
	})

	Register(api, huma.Operation{
		OperationID: "worksheet-rows",
		Method:      http.MethodGet,
		Path:        "/dues-worksheets/{id}/rows",
		Summary:     "Read a worksheet's rows in print order",
		Description: "Rows are the snapshot as printed. entered_since is computed at read time, so " +
			"a reprint marks the lines already done without rewriting what the sheet said.",
		Tags: []string{"worksheets"},
	}, OperationMeta{
		RequiredCapability: "dues.worksheet.manage",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *WorksheetRowsInput) (*WorksheetRowsOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		if _, err := svc.Get(ctx, principal, input.ID); err != nil {
			return nil, mapWorksheetError(err)
		}
		offset, err := offsetFromCursor(input.Cursor)
		if err != nil {
			return nil, err
		}
		limit := int64(input.Limit)
		if limit <= 0 {
			limit = worksheets.DefaultLimit
		}
		rows, err := svc.Rows(ctx, principal, input.ID, limit, offset)
		if err != nil {
			return nil, mapWorksheetError(err)
		}
		data := make([]WorksheetRow, len(rows))
		for i, r := range rows {
			data[i] = worksheetRowToResponse(r)
		}
		return &WorksheetRowsOutput{Body: Page[WorksheetRow]{
			Data: data, NextCursor: nextCursor(len(rows), limit, offset),
		}}, nil
	})

	Register(api, huma.Operation{
		OperationID: "worksheet-link-batch",
		Method:      http.MethodPost,
		Path:        "/dues-worksheets/{id}/batch",
		Summary:     "Record that a batch was created from this worksheet",
		Description: "Links an existing open, empty batch to the run so the client can present " +
			"entry in worksheet order and a later print can tell which lines are done. It " +
			"deliberately creates no entries: inventing rows from a worksheet would be inventing " +
			"payments, and the treasurer types what was actually written on the paper.",
		Tags: []string{"worksheets"},
	}, OperationMeta{
		RequiredCapability: "payment.batch.manage",
		AuditAction:        "dues.worksheet.batch.link",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *LinkWorksheetBatchInput) (*LinkWorksheetBatchOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		if err := svc.LinkBatch(ctx, principal, input.ID, input.Body.BatchID); err != nil {
			return nil, mapWorksheetError(err)
		}
		audit.StampResource(ctx, "payment_batch", input.Body.BatchID)

		out := &LinkWorksheetBatchOutput{}
		out.Body.RunID = input.ID
		out.Body.BatchID = input.Body.BatchID
		return out, nil
	})
}
