package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
)

// --- Export types ---

type ExportMembersBody struct {
	Format string `json:"format" enum:"csv,json" default:"csv"`
}

type ExportResult struct {
	RowCount int    `json:"row_count"`
	Format   string `json:"format"`
	Data     string `json:"data" doc:"Base64-encoded export content."`
}

type ExportMembersInput struct{ Body ExportMembersBody }
type ExportMembersOutput struct {
	Body ExportResult
}

type exportRow struct {
	ID          int64  `json:"id"`
	DisplayName string `json:"display_name"`
	SortName    string `json:"sort_name"`
	CallSign    string `json:"call_sign,omitempty"`
	Email       string `json:"email,omitempty"`
	Phone       string `json:"phone,omitempty"`
	BaseType    string `json:"base_type,omitempty"`
	Lifecycle   string `json:"lifecycle,omitempty"`
}

// RegisterExports registers export endpoints.
func RegisterExports(api huma.API, deps Deps) {
	var q *sqlcgen.Queries
	if deps.DB != nil {
		q = sqlcgen.New(deps.DB)
	}

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
		if q == nil {
			return nil, ErrNotImplemented()
		}

		_, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}

		// Fetch all persons.
		persons, err := q.ListPersons(ctx, sqlcgen.ListPersonsParams{Limit: 10000, Offset: 0})
		if err != nil {
			return nil, huma.NewError(http.StatusInternalServerError, "failed to list persons")
		}

		// Build export rows with primary email/phone from contact methods.
		rows := make([]exportRow, 0, len(persons))
		for _, p := range persons {
			row := exportRow{
				ID:          p.ID,
				DisplayName: p.DisplayName,
				SortName:    p.SortName,
				CallSign:    p.CallSign.String,
			}

			// Get primary contact methods.
			cms, _ := q.ListContactMethods(ctx, p.ID)
			for _, cm := range cms {
				if cm.ArchivedAt.Valid {
					continue
				}
				switch cm.Kind {
				case "email":
					if cm.IsPrimary == 1 || row.Email == "" {
						row.Email = cm.ValueNorm
					}
				case "phone":
					if row.Phone == "" {
						row.Phone = cm.ValueNorm
					}
				}
			}

			// Get membership info.
			memberships, _ := q.ListMembershipsByPerson(ctx, p.ID)
			for _, m := range memberships {
				if m.Lifecycle == "approved" || row.Lifecycle == "" {
					row.BaseType = m.BaseType
					row.Lifecycle = m.Lifecycle
				}
			}

			rows = append(rows, row)
		}

		format := input.Body.Format
		if format == "" {
			format = "csv"
		}

		var encoded string
		switch format {
		case "json":
			b, err := json.Marshal(rows)
			if err != nil {
				return nil, huma.NewError(http.StatusInternalServerError, "failed to encode JSON")
			}
			encoded = base64.StdEncoding.EncodeToString(b)
		default: // csv
			var buf bytes.Buffer
			w := csv.NewWriter(&buf)
			_ = w.Write([]string{"ID", "Display Name", "Sort Name", "Call Sign", "Email", "Phone", "Base Type", "Lifecycle"})
			for _, r := range rows {
				_ = w.Write([]string{
					strconv.FormatInt(r.ID, 10), r.DisplayName, r.SortName, r.CallSign,
					r.Email, r.Phone, r.BaseType, r.Lifecycle,
				})
			}
			w.Flush()
			encoded = base64.StdEncoding.EncodeToString(buf.Bytes())
		}

		return &ExportMembersOutput{
			Body: ExportResult{
				RowCount: len(rows),
				Format:   format,
				Data:     encoded,
			},
		}, nil
	})
}
