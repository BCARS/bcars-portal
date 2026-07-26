package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// --- Member / person types ---

type MemberSummary struct {
	ID          int64  `json:"id" doc:"Person ID."`
	DisplayName string `json:"display_name"`
	SortName    string `json:"sort_name"`
	CallSign    string `json:"call_sign,omitempty"`
	Lifecycle   string `json:"lifecycle" enum:"pending,approved,rejected,inactive,resigned,deceased"`
	BaseType    string `json:"base_type,omitempty" enum:"full,associate"`
}

type MemberDetail struct {
	ID            int64  `json:"id"`
	DisplayName   string `json:"display_name"`
	SortName      string `json:"sort_name"`
	CallSign      string `json:"call_sign,omitempty"`
	Lifecycle     string `json:"lifecycle"`
	BaseType      string `json:"base_type,omitempty"`
	DeceasedAt    string `json:"deceased_at,omitempty" format:"date"`
	DeactivatedAt string `json:"deactivated_at,omitempty" format:"date-time"`
	Version       int64  `json:"version"`
	CreatedAt     string `json:"created_at" format:"date-time"`
	UpdatedAt     string `json:"updated_at" format:"date-time"`
}

type TimelineEvent struct {
	Kind      string `json:"kind" doc:"Event category (e.g. import, membership_approval, contact_change)."`
	ActorName string `json:"actor_name,omitempty"`
	Detail    string `json:"detail,omitempty"`
	OccuredAt string `json:"occurred_at" format:"date-time"`
}

// --- Input / output types ---

type MembersListInput struct {
	PageQuery
	Name      string `query:"name"       doc:"Filter by partial display or sort name."`
	CallSign  string `query:"call_sign"  doc:"Filter by exact call sign (case-insensitive)."`
	BaseType  string `query:"base_type"  doc:"Filter by base type (full, associate)."`
	Lifecycle string `query:"lifecycle"  doc:"Filter by lifecycle state."`
}
type MembersListOutput struct {
	Body Page[MemberSummary]
}

type CreateMemberBody struct {
	DisplayName string `json:"display_name" minLength:"1"`
	SortName    string `json:"sort_name"    minLength:"1"`
	CallSign    string `json:"call_sign,omitempty"`
	BaseType    string `json:"base_type" enum:"full,associate"`
}
type CreateMemberInput struct{ Body CreateMemberBody }
type CreateMemberOutput struct {
	Body MemberDetail
}

type MemberGetInput struct {
	ID int64 `path:"id"`
}
type MemberGetOutput struct {
	Body MemberDetail
}

type MemberUpdateBody struct {
	DisplayName string `json:"display_name,omitempty"`
	SortName    string `json:"sort_name,omitempty"`
	CallSign    string `json:"call_sign,omitempty"`
	Version     int64  `json:"version" doc:"Current row version for optimistic concurrency."`
}
type MemberUpdateInput struct {
	ID   int64 `path:"id"`
	Body MemberUpdateBody
}
type MemberUpdateOutput struct {
	Body MemberDetail
}

type MemberDeactivateInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Reason  string `json:"reason,omitempty"`
		Version int64  `json:"version"`
	}
}
type MemberDeactivateOutput struct{}

type MemberReactivateInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Version int64 `json:"version"`
	}
}
type MemberReactivateOutput struct{}

type MemberTimelineInput struct {
	ID     int64 `path:"id"`
	PageQuery
}
type MemberTimelineOutput struct {
	Body Page[TimelineEvent]
}

// RegisterMembers registers all member / person endpoints.
func RegisterMembers(api huma.API) {
	Register(api, huma.Operation{
		OperationID: "members-list",
		Method:      http.MethodGet,
		Path:        "/members",
		Summary:     "Search and filter members",
		Tags:        []string{"members"},
	}, OperationMeta{
		RequiredCapability: "member.read",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "read-only",
	}, func(ctx context.Context, input *MembersListInput) (*MembersListOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "member-create",
		Method:      http.MethodPost,
		Path:        "/members",
		Summary:     "Create a person and pending membership",
		Tags:        []string{"members"},
		DefaultStatus: http.StatusCreated,
	}, OperationMeta{
		RequiredCapability: "member.create",
		AuditAction:        "member.create",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "curated",
	}, func(ctx context.Context, input *CreateMemberInput) (*CreateMemberOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "member-get",
		Method:      http.MethodGet,
		Path:        "/members/{id}",
		Summary:     "Get member detail (server-filtered by caller role)",
		Tags:        []string{"members"},
	}, OperationMeta{
		RequiredCapability: "member.read",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "read-only",
	}, func(ctx context.Context, input *MemberGetInput) (*MemberGetOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "member-update",
		Method:      http.MethodPatch,
		Path:        "/members/{id}",
		Summary:     "Update person fields",
		Tags:        []string{"members"},
	}, OperationMeta{
		RequiredCapability: "member.update",
		AuditAction:        "member.update",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "curated",
	}, func(ctx context.Context, input *MemberUpdateInput) (*MemberUpdateOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "member-deactivate",
		Method:      http.MethodPost,
		Path:        "/members/{id}/deactivate",
		Summary:     "Deactivate a member",
		Tags:        []string{"members"},
		DefaultStatus: http.StatusNoContent,
	}, OperationMeta{
		RequiredCapability: "member.deactivate",
		AuditAction:        "member.deactivate",
		ConfirmationLevel:  "explicit-confirm",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *MemberDeactivateInput) (*MemberDeactivateOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "member-reactivate",
		Method:      http.MethodPost,
		Path:        "/members/{id}/reactivate",
		Summary:     "Reactivate a member",
		Tags:        []string{"members"},
		DefaultStatus: http.StatusNoContent,
	}, OperationMeta{
		RequiredCapability: "member.deactivate",
		AuditAction:        "member.reactivate",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *MemberReactivateInput) (*MemberReactivateOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "member-timeline",
		Method:      http.MethodGet,
		Path:        "/members/{id}/timeline",
		Summary:     "Get the merged event timeline for a member",
		Tags:        []string{"members"},
	}, OperationMeta{
		RequiredCapability: "member.read",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "read-only",
	}, func(ctx context.Context, input *MemberTimelineInput) (*MemberTimelineOutput, error) {
		return nil, ErrNotImplemented()
	})
}
