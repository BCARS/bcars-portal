package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// --- Export types ---

type ExportMembersBody struct {
	Format   string   `json:"format" enum:"csv,json" default:"csv"`
	Fields   []string `json:"fields,omitempty"  doc:"Subset of fields to include; omit for caller's default view."`
	Filter   struct {
		Lifecycle []string `json:"lifecycle,omitempty"`
		BaseType  []string `json:"base_type,omitempty"`
	} `json:"filter,omitempty"`
}

type ExportResult struct {
	RowCount int    `json:"row_count"`
	Format   string `json:"format"`
	// Body content is returned inline as base64 when format is json,
	// or as an attachment header for csv. Both are represented in the
	// OpenAPI schema; the implementation handles content negotiation.
	Data string `json:"data" doc:"Base64-encoded export content."`
}

type ExportMembersInput struct{ Body ExportMembersBody }
type ExportMembersOutput struct {
	Body ExportResult
}

// RegisterExports registers export endpoints.
func RegisterExports(api huma.API) {
	Register(api, huma.Operation{
		OperationID: "export-members",
		Method:      http.MethodPost,
		Path:        "/exports/members",
		Summary:     "Export member data (audited; respects caller field visibility)",
		Tags:        []string{"exports"},
	}, OperationMeta{
		RequiredCapability: "member.export",
		AuditAction:        "member.export",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "curated",
	}, func(ctx context.Context, input *ExportMembersInput) (*ExportMembersOutput, error) {
		return nil, ErrNotImplemented()
	})
}
