package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/authn"
	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/importd"
)

// maxUploadSize limits import file uploads to 10 MB.
const maxUploadSize = 10 << 20

// --- Import types ---

type ImportRunResponse struct {
	ID             int64  `json:"id"`
	Status         string `json:"status" enum:"uploaded,validated,previewed,committed,discarded"`
	SourceKind     string `json:"source_kind" enum:"csv,json"`
	SourceFilename string `json:"source_filename"`
	SourceSHA256   string `json:"source_sha256" doc:"SHA-256 of the uploaded file."`
	UploadedBy     int64  `json:"uploaded_by"`
	UploadedAt     string `json:"uploaded_at" format:"date-time"`
	CommittedAt    string `json:"committed_at,omitempty" format:"date-time"`
	Version        int64  `json:"version"`
	CreatedAt      string `json:"created_at" format:"date-time"`
	UpdatedAt      string `json:"updated_at" format:"date-time"`
}

type StagedRowResponse struct {
	ID             int64  `json:"id"`
	ImportRunID    int64  `json:"import_run_id"`
	SourceRowIndex int64  `json:"source_row_index"`
	ExternalID     string `json:"external_id,omitempty"`
	NormalizedJSON string `json:"normalized_json"`
	MatchPersonID  int64  `json:"match_person_id,omitempty"`
	MatchMethod    string `json:"match_method,omitempty"`
	ProposedAction string `json:"proposed_action" enum:"create,update,skip,manual"`
	RequiresManual bool   `json:"requires_manual"`
	ManualReason   string `json:"manual_reason,omitempty"`
}

type ImportPreviewSummary struct {
	RunID            int64 `json:"run_id"`
	TotalRows        int   `json:"total_rows"`
	Creates          int   `json:"creates"`
	Updates          int   `json:"updates"`
	Skips            int   `json:"skips"`
	Manuals          int   `json:"manuals"`
	UnresolvedManual int   `json:"unresolved_manual"`
	Ready            bool  `json:"ready" doc:"True when all manual rows are resolved and commit is allowed."`
}

type ImportCommitResult struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
	Skipped int `json:"skipped"`
}

// --- Import inputs / outputs ---

type UploadImportForm struct {
	File huma.FormFile `form:"file" required:"true" doc:"The Groups.io export file (CSV or JSON)."`
}
type UploadImportInput struct {
	IdempotencyKey string `header:"Idempotency-Key" required:"true" doc:"Client-generated key for safe retry."`
	RawBody        huma.MultipartFormFiles[UploadImportForm]
}
type UploadImportOutput struct {
	Body struct {
		Run        ImportRunResponse `json:"run"`
		TotalRows  int               `json:"total_rows"`
		AutoRows   int               `json:"auto_rows"`
		ManualRows int               `json:"manual_rows"`
	}
}

type ImportsListInput struct {
	PageQuery
}
type ImportsListOutput struct {
	Body Page[ImportRunResponse]
}

type ImportGetInput struct {
	ID int64 `path:"id"`
}
type ImportGetOutput struct {
	Body ImportRunResponse
}

type ImportRowsListInput struct {
	ID int64 `path:"id"`
	PageQuery
	RequiresManual string `query:"requires_manual" doc:"Filter: 'true' for manual rows only."`
}
type ImportRowsListOutput struct {
	Body Page[StagedRowResponse]
}

type ImportRowDecisionBody struct {
	Action      string `json:"action" enum:"approve_create,approve_update,skip" doc:"Decision action."`
	PayloadJSON string `json:"payload_json,omitempty" doc:"Optional JSON with override details."`
}
type ImportRowDecisionInput struct {
	ID    int64 `path:"id"`
	RowID int64 `path:"rowId"`
	Body  ImportRowDecisionBody
}
type ImportRowDecisionOutput struct {
	Body struct {
		DecisionID int64  `json:"decision_id"`
		Action     string `json:"action"`
	}
}

type ImportPreviewInput struct {
	ID int64 `path:"id"`
}
type ImportPreviewOutput struct {
	Body ImportPreviewSummary
}

type ImportCommitInput struct {
	ID int64 `path:"id"`
}
type ImportCommitOutput struct {
	Body ImportCommitResult
}

type ImportDiscardInput struct {
	ID int64 `path:"id"`
}
type ImportDiscardOutput struct{}

// mapImportError converts import domain errors to Huma HTTP errors.
func mapImportError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, importd.ErrRunNotFound) {
		return huma.NewError(http.StatusNotFound, "import run not found")
	}
	if errors.Is(err, importd.ErrRowNotFound) {
		return huma.NewError(http.StatusNotFound, "staged row not found")
	}
	if errors.Is(err, importd.ErrInvalidTransition) {
		return huma.NewError(http.StatusConflict, err.Error())
	}
	if errors.Is(err, importd.ErrUnresolvedManual) {
		return huma.NewError(http.StatusConflict, err.Error())
	}
	if errors.Is(err, importd.ErrRowNotManual) {
		return huma.NewError(http.StatusBadRequest, err.Error())
	}
	if errors.Is(err, importd.ErrRowNotInRun) {
		return huma.NewError(http.StatusBadRequest, err.Error())
	}
	if strings.Contains(err.Error(), "duplicate idempotency key") {
		return huma.NewError(http.StatusConflict, err.Error())
	}
	return huma.NewError(http.StatusInternalServerError, err.Error())
}

// detectSourceKind guesses the format from filename extension.
func detectSourceKind(filename string) string {
	lower := strings.ToLower(filename)
	if strings.HasSuffix(lower, ".json") {
		return "json"
	}
	return "csv"
}

// RegisterImports registers all import workflow endpoints.
func RegisterImports(api huma.API, deps Deps) {
	var importSvc *importd.Service
	if deps.DB != nil {
		importSvc = importd.NewService(deps.DB)
	}

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
		if importSvc == nil {
			return nil, ErrNotImplemented()
		}

		principal := authn.PrincipalFrom(ctx)
		if principal == nil {
			return nil, huma.NewError(http.StatusUnauthorized, "not authenticated")
		}

		formData := input.RawBody.Data()
		file := formData.File

		// Read file with size limit.
		data, err := io.ReadAll(io.LimitReader(file, maxUploadSize+1))
		if err != nil {
			return nil, huma.NewError(http.StatusBadRequest, "failed to read upload")
		}
		if len(data) > maxUploadSize {
			return nil, huma.NewError(http.StatusRequestEntityTooLarge,
				fmt.Sprintf("file exceeds %d byte limit", maxUploadSize))
		}

		filename := file.Filename
		if filename == "" {
			filename = "upload"
		}
		sourceKind := detectSourceKind(filename)

		idemKey := input.IdempotencyKey
		if idemKey == "" {
			return nil, huma.NewError(http.StatusBadRequest, "Idempotency-Key header is required")
		}

		result, err := importSvc.Upload(ctx, bytes.NewReader(data), sourceKind, filename, principal.UserID, idemKey)
		if err != nil {
			return nil, mapImportError(err)
		}

		run, err := importSvc.GetRun(ctx, result.RunID)
		if err != nil {
			return nil, mapImportError(err)
		}

		out := &UploadImportOutput{}
		out.Body.Run = importRunToResponse(run)
		out.Body.TotalRows = result.TotalRows
		out.Body.AutoRows = result.AutoRows
		out.Body.ManualRows = result.ManualRows
		return out, nil
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
		if importSvc == nil {
			return nil, ErrNotImplemented()
		}

		_, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}

		limit := int64(input.Limit)
		if limit <= 0 {
			limit = 50
		}
		var offset int64
		if input.Cursor != "" {
			raw, err := DecodeCursor(input.Cursor)
			if err != nil {
				return nil, huma.NewError(http.StatusBadRequest, "invalid cursor")
			}
			offset, err = strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return nil, huma.NewError(http.StatusBadRequest, "invalid cursor")
			}
		}

		runs, err := importSvc.ListRuns(ctx, limit+1, offset)
		if err != nil {
			return nil, mapImportError(err)
		}

		data := make([]ImportRunResponse, 0, len(runs))
		for i, r := range runs {
			if int64(i) >= limit {
				break
			}
			data = append(data, importRunToResponse(r))
		}

		var nextCursor string
		if int64(len(runs)) > limit {
			nextCursor = EncodeCursor(fmt.Sprintf("%d", offset+limit))
		}

		return &ImportsListOutput{
			Body: Page[ImportRunResponse]{Data: data, NextCursor: nextCursor},
		}, nil
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
		if importSvc == nil {
			return nil, ErrNotImplemented()
		}

		_, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}

		run, err := importSvc.GetRun(ctx, input.ID)
		if err != nil {
			return nil, mapImportError(err)
		}

		return &ImportGetOutput{Body: importRunToResponse(run)}, nil
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
		if importSvc == nil {
			return nil, ErrNotImplemented()
		}

		_, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}

		limit := int64(input.Limit)
		if limit <= 0 {
			limit = 50
		}
		var offset int64
		if input.Cursor != "" {
			raw, err := DecodeCursor(input.Cursor)
			if err != nil {
				return nil, huma.NewError(http.StatusBadRequest, "invalid cursor")
			}
			offset, err = strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return nil, huma.NewError(http.StatusBadRequest, "invalid cursor")
			}
		}

		rows, err := importSvc.ListRows(ctx, input.ID, limit+1, offset)
		if err != nil {
			return nil, mapImportError(err)
		}

		data := make([]StagedRowResponse, 0, len(rows))
		for i, r := range rows {
			if int64(i) >= limit {
				break
			}
			// Optionally filter by requires_manual.
			if input.RequiresManual == "true" && r.RequiresManual == 0 {
				continue
			}
			if input.RequiresManual == "false" && r.RequiresManual == 1 {
				continue
			}

			var normDisplay, normEmail, normCallSign string
			var norm struct {
				DisplayName string `json:"DisplayName"`
				Email       string `json:"Email"`
				CallSign    string `json:"CallSign"`
			}
			if err := json.Unmarshal([]byte(r.NormalizedJson), &norm); err == nil {
				normDisplay = norm.DisplayName
				normEmail = norm.Email
				normCallSign = norm.CallSign
			}
			_ = normDisplay
			_ = normEmail
			_ = normCallSign

			data = append(data, StagedRowResponse{
				ID:             r.ID,
				ImportRunID:    r.ImportRunID,
				SourceRowIndex: r.SourceRowIndex,
				ExternalID:     r.SourceExternalID.String,
				NormalizedJSON: r.NormalizedJson,
				MatchPersonID:  r.MatchPersonID.Int64,
				MatchMethod:    r.MatchMethod.String,
				ProposedAction: r.ProposedAction,
				RequiresManual: r.RequiresManual == 1,
				ManualReason:   r.ManualReason.String,
			})
		}

		var nextCursor string
		if int64(len(rows)) > limit {
			nextCursor = EncodeCursor(fmt.Sprintf("%d", offset+limit))
		}

		return &ImportRowsListOutput{
			Body: Page[StagedRowResponse]{Data: data, NextCursor: nextCursor},
		}, nil
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
		if importSvc == nil {
			return nil, ErrNotImplemented()
		}

		principal := authn.PrincipalFrom(ctx)
		if principal == nil {
			return nil, huma.NewError(http.StatusUnauthorized, "not authenticated")
		}

		decision, err := importSvc.RecordDecision(ctx, input.ID, importd.DecisionInput{
			RowID:       input.RowID,
			DecidedBy:   principal.UserID,
			Action:      input.Body.Action,
			PayloadJSON: input.Body.PayloadJSON,
		})
		if err != nil {
			return nil, mapImportError(err)
		}

		return &ImportRowDecisionOutput{
			Body: struct {
				DecisionID int64  `json:"decision_id"`
				Action     string `json:"action"`
			}{
				DecisionID: decision.ID,
				Action:     decision.Action,
			},
		}, nil
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
		if importSvc == nil {
			return nil, ErrNotImplemented()
		}

		_, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}

		preview, err := importSvc.Preview(ctx, input.ID)
		if err != nil {
			return nil, mapImportError(err)
		}

		return &ImportPreviewOutput{
			Body: ImportPreviewSummary{
				RunID:            preview.RunID,
				TotalRows:        preview.TotalRows,
				Creates:          preview.CreateCount,
				Updates:          preview.UpdateCount,
				Skips:            preview.SkipCount,
				Manuals:          preview.ManualCount,
				UnresolvedManual: preview.UnresolvedManual,
				Ready:            preview.Ready,
			},
		}, nil
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
		if importSvc == nil {
			return nil, ErrNotImplemented()
		}

		principal := authn.PrincipalFrom(ctx)
		if principal == nil {
			return nil, huma.NewError(http.StatusUnauthorized, "not authenticated")
		}

		result, err := importSvc.Commit(ctx, input.ID, principal.UserID)
		if err != nil {
			return nil, mapImportError(err)
		}

		return &ImportCommitOutput{
			Body: ImportCommitResult{
				Created: result.Created,
				Updated: result.Updated,
				Skipped: result.Skipped,
			},
		}, nil
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
		if importSvc == nil {
			return nil, ErrNotImplemented()
		}

		_, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}

		if err := importSvc.Discard(ctx, input.ID); err != nil {
			return nil, mapImportError(err)
		}

		return nil, nil
	})
}

// importRunToResponse converts a sqlcgen.ImportRun to the API response type.
func importRunToResponse(r sqlcgen.ImportRun) ImportRunResponse {
	return ImportRunResponse{
		ID:             r.ID,
		Status:         r.Status,
		SourceKind:     r.SourceKind,
		SourceFilename: r.SourceFilename,
		SourceSHA256:   r.SourceSha256,
		UploadedBy:     r.UploadedBy,
		UploadedAt:     r.UploadedAt,
		CommittedAt:    r.CommittedAt.String,
		Version:        r.Version,
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
	}
}
