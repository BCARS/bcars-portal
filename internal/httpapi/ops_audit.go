package httpapi

import (
	"context"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
)

// defaultAuditPageSize is used when the caller omits limit=.
const defaultAuditPageSize = 50

// decodeOffsetCursor turns the opaque cursor returned by a previous audit-event
// page back into a row offset.
//
// Cursor convention: the raw value is the decimal offset of the first row of
// the next page, wrapped by EncodeCursor. Offset paging (rather than a keyset
// cursor on occurred_at) is what the audit table's filters allow without a
// composite index per filter combination; the ORDER BY carries an id tiebreak
// so the ordering is total and pages do not overlap for rows written in the
// same millisecond. Newly written events can still shift a walk in progress,
// which is acceptable for an append-mostly audit log read newest-first.
func decodeOffsetCursor(cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	raw, err := DecodeCursor(cursor)
	if err != nil {
		return 0, huma.NewError(http.StatusBadRequest, "invalid cursor")
	}
	offset, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || offset < 0 {
		return 0, huma.NewError(http.StatusBadRequest, "invalid cursor")
	}
	return offset, nil
}

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
	Action      string `query:"action"       doc:"Filter by action prefix (e.g. 'member.' matches member.create). Prefix, not equality."`
	ActorUserID int64  `query:"actor_user_id" doc:"Filter by actor user id (exact). Omit or 0 for any actor." minimum:"0"`
	SubjectKind string `query:"subject_kind"  doc:"Filter by the audited resource kind (exact), e.g. 'member'."`
	SubjectID   int64  `query:"subject_id"    doc:"Filter by the audited resource id (exact). Omit or 0 for any subject." minimum:"0"`
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
			limit = defaultAuditPageSize
		}

		offset, err := decodeOffsetCursor(input.Cursor)
		if err != nil {
			return nil, err
		}

		params := sqlcgen.SearchAuditEventsParams{
			// Fetch one extra row: if it comes back there is another page.
			Limit:  limit + 1,
			Offset: offset,
		}
		if input.Action != "" {
			params.ActionPrefix = input.Action
		}
		if input.ActorUserID != 0 {
			params.ActorUserID = input.ActorUserID
		}
		if input.SubjectKind != "" {
			params.ResourceKind = input.SubjectKind
		}
		if input.SubjectID != 0 {
			params.ResourceID = input.SubjectID
		}

		events, err := q.SearchAuditEvents(ctx, params)
		if err != nil {
			return nil, huma.NewError(http.StatusInternalServerError, "failed to list audit events")
		}

		hasMore := int64(len(events)) > limit
		if hasMore {
			events = events[:limit]
		}

		data := make([]AuditEventResponse, len(events))
		for i, e := range events {
			data[i] = auditEventToResponse(e)
		}

		page := Page[AuditEventResponse]{Data: data}
		if hasMore {
			page.NextCursor = EncodeCursor(strconv.FormatInt(offset+limit, 10))
		}

		return &AuditEventsListOutput{Body: page}, nil
	})
}
