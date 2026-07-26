package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// --- Import types ---

type ImportRun struct {
	ID             int64  `json:"id"`
	Status         string `json:"status" enum:"uploaded,validated,previewed,committed,discarded"`
	SourceFormat   string `json:"source_format" enum:"groupsio_json,groupsio_csv"`
	FileHash       string `json:"file_hash" doc:"SHA-256 of the uploaded file."`
	TotalRows      int    `json:"total_rows"`
	RequiresManual int    `json:"requires_manual" doc:"Rows that need a reconciliation decision."`
	CreatedByID    int64  `json:"created_by_id"`
	CreatedAt      string `json:"created_at" format:"date-time"`
	CommittedAt    string `json:"committed_at,omitempty" format:"date-time"`
}

type StagedImportRow struct {
	ID               int64    `json:"id"`
	ImportID         int64    `json:"import_id"`
	ExternalID       string   `json:"external_id,omitempty"`
	DisplayName      string   `json:"display_name,omitempty"`
	Email            string   `json:"email,omitempty"`
	CallSign         string   `json:"call_sign,omitempty"`
	MatchMethod      string   `json:"match_method,omitempty" enum:"external_id,call_sign,email,manual,none"`
	MatchedPersonID  int64    `json:"matched_person_id,omitempty"`
	RequiresManual   bool     `json:"requires_manual"`
	ProposedAction   string   `json:"proposed_action,omitempty" enum:"create,update,skip,conflict"`
	ValidationErrors []string `json:"validation_errors,omitempty"`
}

type ReconciliationDecision struct {
	Decision string `json:"decision" enum:"create_new,link_to,skip"`
	LinkToID int64  `json:"link_to_id,omitempty" doc:"Person ID when decision is link_to."`
}

type ImportPreviewSummary struct {
	Creates   int `json:"creates"`
	Updates   int `json:"updates"`
	Skips     int `json:"skips"`
	Conflicts int `json:"conflicts"`
	Manuals   int `json:"requires_manual"`
}

// --- Import inputs / outputs ---

type UploadImportInput struct {
	RawBody []byte `doc:"Multipart form upload of the import file." contentType:"multipart/form-data"`
}
type UploadImportOutput struct {
	Body ImportRun
}

type ImportsListInput struct {
	PageQuery
}
type ImportsListOutput struct {
	Body Page[ImportRun]
}

type ImportGetInput struct {
	ID int64 `path:"id"`
}
type ImportGetOutput struct {
	Body ImportRun
}

type ImportRowsListInput struct {
	ID int64 `path:"id"`
	PageQuery
	RequiresManual string `query:"requires_manual" doc:"Filter to rows needing a decision: 'true' or 'false'."`
}
type ImportRowsListOutput struct {
	Body Page[StagedImportRow]
}

type ImportRowDecisionInput struct {
	ID    int64 `path:"id"`
	RowID int64 `path:"rowId"`
	Body  ReconciliationDecision
}
type ImportRowDecisionOutput struct {
	Body StagedImportRow
}

type ImportPreviewInput struct {
	ID int64 `path:"id"`
}
type ImportPreviewOutput struct {
	Body ImportPreviewSummary
}

type ImportCommitInput struct {
	ID             int64  `path:"id"`
	IdempotencyKey string `header:"Idempotency-Key" required:"true" doc:"Client-generated UUID for safe retry."`
}
type ImportCommitOutput struct {
	Body ImportPreviewSummary
}

type ImportDiscardInput struct {
	ID int64 `path:"id"`
}
type ImportDiscardOutput struct{}

// RegisterImports registers all import workflow endpoints.
func RegisterImports(api huma.API) {
	Register(api, huma.Operation{
		OperationID:   "import-upload",
		Method:        http.MethodPost,
		Path:          "/imports",
		Summary:       "Upload a Groups.io export file for staging",
		Tags:          []string{"imports"},
		DefaultStatus: http.StatusCreated,
	}, OperationMeta{
		RequiredCapability: "import.upload",
		AuditAction:        "import.upload",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *UploadImportInput) (*UploadImportOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "imports-list",
		Method:      http.MethodGet,
		Path:        "/imports",
		Summary:     "List import runs",
		Tags:        []string{"imports"},
	}, OperationMeta{
		RequiredCapability: "import.upload",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "read-only",
	}, func(ctx context.Context, input *ImportsListInput) (*ImportsListOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "import-get",
		Method:      http.MethodGet,
		Path:        "/imports/{id}",
		Summary:     "Get an import run",
		Tags:        []string{"imports"},
	}, OperationMeta{
		RequiredCapability: "import.upload",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "read-only",
	}, func(ctx context.Context, input *ImportGetInput) (*ImportGetOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "import-rows-list",
		Method:      http.MethodGet,
		Path:        "/imports/{id}/rows",
		Summary:     "List staged rows for an import run",
		Tags:        []string{"imports"},
	}, OperationMeta{
		RequiredCapability: "import.upload",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *ImportRowsListInput) (*ImportRowsListOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "import-row-decision",
		Method:      http.MethodPost,
		Path:        "/imports/{id}/rows/{rowId}/decisions",
		Summary:     "Record a reconciliation decision for a staged row",
		Tags:        []string{"imports"},
	}, OperationMeta{
		RequiredCapability: "import.upload",
		AuditAction:        "import.row.decision",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *ImportRowDecisionInput) (*ImportRowDecisionOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "import-preview",
		Method:      http.MethodPost,
		Path:        "/imports/{id}/preview",
		Summary:     "Recompute proposed actions after decisions",
		Tags:        []string{"imports"},
	}, OperationMeta{
		RequiredCapability: "import.upload",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *ImportPreviewInput) (*ImportPreviewOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "import-commit",
		Method:      http.MethodPost,
		Path:        "/imports/{id}/commit",
		Summary:     "Commit a reconciled import to canonical data (idempotent)",
		Tags:        []string{"imports"},
	}, OperationMeta{
		RequiredCapability: "import.commit",
		AuditAction:        "import.commit",
		ConfirmationLevel:  "explicit-confirm",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *ImportCommitInput) (*ImportCommitOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID:   "import-discard",
		Method:        http.MethodPost,
		Path:          "/imports/{id}/discard",
		Summary:       "Discard a staged import run",
		Tags:          []string{"imports"},
		DefaultStatus: http.StatusNoContent,
	}, OperationMeta{
		RequiredCapability: "import.upload",
		AuditAction:        "import.discard",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *ImportDiscardInput) (*ImportDiscardOutput, error) {
		return nil, ErrNotImplemented()
	})
}
