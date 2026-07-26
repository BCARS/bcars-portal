package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// --- Audit event types ---

type AuditEvent struct {
	ID          int64  `json:"id"`
	Action      string `json:"action" doc:"Dot-separated audit action name (e.g. member.create)."`
	ActorUserID int64  `json:"actor_user_id,omitempty"`
	SubjectKind string `json:"subject_kind,omitempty"`
	SubjectID   int64  `json:"subject_id,omitempty"`
	RequestID   string `json:"request_id,omitempty"`
	Detail      string `json:"detail,omitempty" doc:"JSON-encoded structured detail."`
	OccurredAt  string `json:"occurred_at" format:"date-time"`
}

type AuditEventsListInput struct {
	PageQuery
	Action      string `query:"action"       doc:"Filter by action prefix (e.g. 'member.')."`
	ActorUserID int64  `query:"actor_user_id" doc:"Filter by actor."`
	SubjectKind string `query:"subject_kind"`
	SubjectID   int64  `query:"subject_id"`
	Since       string `query:"since" format:"date-time" doc:"Return events on or after this timestamp."`
	Until       string `query:"until" format:"date-time" doc:"Return events before this timestamp."`
}
type AuditEventsListOutput struct {
	Body Page[AuditEvent]
}

// RegisterAudit registers audit event endpoints.
func RegisterAudit(api huma.API) {
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
		return nil, ErrNotImplemented()
	})
}
