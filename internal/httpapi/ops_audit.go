package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

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
	ID          int64  `json:"id"`
	Action      string `json:"action" doc:"Dot-separated audit action name (e.g. member.create)."`
	ActorUserID int64  `json:"actor_user_id,omitempty"`
	// ActorRoleCodes are the roles the actor held WHEN THE EVENT HAPPENED,
	// recorded at write time rather than resolved on read. Roles change; an
	// officer reviewing a denial from last spring needs what the actor was
	// then. Empty for an unauthenticated actor, and for events written before
	// this field was populated.
	ActorRoleCodes []string `json:"actor_role_codes,omitempty" doc:"Roles the actor held when the event happened, recorded at write time. Empty for an unauthenticated actor and for events written before this was populated."`
	ResourceKind   string   `json:"resource_kind,omitempty"`
	ResourceID     int64    `json:"resource_id,omitempty"`
	Outcome        string   `json:"outcome" enum:"success,failure,denied"`
	// ReasonCode explains a denial or failure. It is the field that separates
	// "not signed in" from "signed in without permission", which is the
	// question an officer reviewing denials actually has.
	ReasonCode string `json:"reason_code,omitempty" doc:"Why a denial or failure occurred, e.g. unauthenticated, missing_capability, missing_confirmation, rate_limited."`
	RequestID  string `json:"request_id,omitempty"`
	DetailJSON string `json:"detail_json,omitempty" doc:"JSON-encoded structured detail."`
	OccurredAt string `json:"occurred_at" format:"date-time"`
}

type AuditEventsListInput struct {
	PageQuery
	Action      string `query:"action"       doc:"Filter by action prefix (e.g. 'member.' matches member.create). Prefix, not equality."`
	ActorUserID int64  `query:"actor_user_id" doc:"Filter by actor user id (exact). Omit or 0 for any actor." minimum:"0"`
	SubjectKind string `query:"subject_kind"  doc:"Filter by the audited resource kind (exact), e.g. 'member'."`
	SubjectID   int64  `query:"subject_id"    doc:"Filter by the audited resource id (exact). Omit or 0 for any subject." minimum:"0"`
	Outcome     string `query:"outcome"       enum:"success,failure,denied" doc:"Filter by outcome (exact). 'denied' returns every refusal, whatever action it was recorded under."`
}
type AuditEventsListOutput struct {
	Body Page[AuditEventResponse]
}

func auditEventToResponse(e sqlcgen.AuditEvent) AuditEventResponse {
	return AuditEventResponse{
		ID:             e.ID,
		Action:         e.Action,
		ActorUserID:    e.ActorUserID.Int64,
		ActorRoleCodes: splitRoleCodes(e.ActorRoleCodes.String),
		ResourceKind:   e.ResourceKind.String,
		ResourceID:     e.ResourceID.Int64,
		Outcome:        e.Outcome,
		ReasonCode:     e.ReasonCode.String,
		RequestID:      e.RequestID.String,
		DetailJSON:     e.DetailJson.String,
		OccurredAt:     e.OccurredAt,
	}
}

// splitRoleCodes turns the stored comma-separated column into a list.
//
// The column predates this field being populated, so most historical rows are
// empty; those become an absent JSON field rather than a list containing one
// empty string, which a client would have to know to filter.
func splitRoleCodes(stored string) []string {
	if strings.TrimSpace(stored) == "" {
		return nil
	}
	parts := strings.Split(stored, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
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
		ConfirmationLevel:  ConfirmNone,
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
		if input.Outcome != "" {
			// The enum tag already refuses anything else, so this cannot
			// become a way to probe the column with arbitrary strings.
			params.Outcome = input.Outcome
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
