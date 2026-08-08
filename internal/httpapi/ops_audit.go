package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
)

// --- Audit event types ---

type AuditEventResponse struct {
	ID           int64  `json:"id"`
	Action       string `json:"action" doc:"Dot-separated audit action name (e.g. member.create)."`
	ActorUserID  int64  `json:"actor_user_id,omitempty"`
	ResourceKind string `json:"resource_kind,omitempty"`
	ResourceID   int64  `json:"resource_id,omitempty"`
	Outcome      string `json:"outcome"`
	RequestID    string `json:"request_id,omitempty"`
	DetailJSON   string `json:"detail_json,omitempty" doc:"JSON-encoded structured detail."`
	OccurredAt   string `json:"occurred_at" format:"date-time"`
}

type AuditEventsListInput struct {
	PageQuery
	Action      string `query:"action"       doc:"Filter by action prefix (e.g. 'member.')."`
	ActorUserID int64  `query:"actor_user_id" doc:"Filter by actor."`
	SubjectKind string `query:"subject_kind"`
	SubjectID   int64  `query:"subject_id"`
}
type AuditEventsListOutput struct {
	Body Page[AuditEventResponse]
}

func auditEventToResponse(e sqlcgen.AuditEvent) AuditEventResponse {
	return AuditEventResponse{
		ID:           e.ID,
		Action:       e.Action,
		ActorUserID:  e.ActorUserID.Int64,
		ResourceKind: e.ResourceKind.String,
		ResourceID:   e.ResourceID.Int64,
		Outcome:      e.Outcome,
		RequestID:    e.RequestID.String,
		DetailJSON:   e.DetailJson.String,
		OccurredAt:   e.OccurredAt,
	}
}

// RegisterAudit registers audit event endpoints.
func RegisterAudit(api huma.API, deps Deps) {
	var q *sqlcgen.Queries
	if deps.DB != nil {
		q = sqlcgen.New(deps.DB)
	}

	Register(api, huma.Operation{
		OperationID: "audit-events-list",
		Method:      http.MethodGet,
		Path:        "/audit-events",
		Summary:     "Search audit events",
		Tags:        []string{"audit"},
	}, OperationMeta{
		RequiredCapability: "audit.read",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "read-only",
	}, func(ctx context.Context, input *AuditEventsListInput) (*AuditEventsListOutput, error) {
		if q == nil {
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

		events, err := q.ListAuditEvents(ctx, sqlcgen.ListAuditEventsParams{
			Limit:  limit,
			Offset: 0,
		})
		if err != nil {
			return nil, huma.NewError(http.StatusInternalServerError, "failed to list audit events")
		}

		data := make([]AuditEventResponse, len(events))
		for i, e := range events {
			data[i] = auditEventToResponse(e)
		}

		return &AuditEventsListOutput{
			Body: Page[AuditEventResponse]{Data: data},
		}, nil
	})
}
