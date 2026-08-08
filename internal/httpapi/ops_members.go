package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/members"
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
	ID int64 `path:"id"`
	PageQuery
}
type MemberTimelineOutput struct {
	Body Page[TimelineEvent]
}

// personToDetail converts a sqlcgen.Person to the API MemberDetail type.
func personToDetail(p sqlcgen.Person) MemberDetail {
	return MemberDetail{
		ID:            p.ID,
		DisplayName:   p.DisplayName,
		SortName:      p.SortName,
		CallSign:      p.CallSign.String,
		DeceasedAt:    p.DeceasedAt.String,
		DeactivatedAt: p.DeactivatedAt.String,
		Version:       p.Version,
		CreatedAt:     p.CreatedAt,
		UpdatedAt:     p.UpdatedAt,
	}
}

// RegisterMembers registers all member / person endpoints.
func RegisterMembers(api huma.API, deps Deps) {
	var memberSvc *members.Service
	if deps.DB != nil {
		memberSvc = members.NewService(deps.DB)
	}

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
		if memberSvc == nil {
			return nil, ErrNotImplemented()
		}

		principal, err := requirePrincipal(ctx)
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

		results, err := memberSvc.ListPersons(ctx, principal, members.ListPersonsParams{
			Query:  input.Name,
			Limit:  limit + 1, // fetch one extra to detect next page
			Offset: offset,
		})
		if err != nil {
			return nil, mapDomainError(err)
		}

		data := make([]MemberSummary, 0, len(results))
		for i, r := range results {
			if int64(i) >= limit {
				break
			}
			data = append(data, MemberSummary{
				ID:          r.ID,
				DisplayName: r.DisplayName,
				SortName:    r.SortName,
				CallSign:    r.CallSign.String,
			})
		}

		var nextCursor string
		if int64(len(results)) > limit {
			nextCursor = EncodeCursor(fmt.Sprintf("%d", offset+limit))
		}

		return &MembersListOutput{
			Body: Page[MemberSummary]{
				Data:       data,
				NextCursor: nextCursor,
			},
		}, nil
	})

	Register(api, huma.Operation{
		OperationID:   "member-create",
		Method:        http.MethodPost,
		Path:          "/members",
		Summary:       "Create a person and pending membership",
		Tags:          []string{"members"},
		DefaultStatus: http.StatusCreated,
	}, OperationMeta{
		RequiredCapability: "member.create",
		AuditAction:        "member.create",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "curated",
	}, func(ctx context.Context, input *CreateMemberInput) (*CreateMemberOutput, error) {
		if memberSvc == nil {
			return nil, ErrNotImplemented()
		}

		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}

		person, err := memberSvc.CreatePerson(ctx, principal, members.CreatePersonParams{
			DisplayName: input.Body.DisplayName,
			SortName:    input.Body.SortName,
			CallSign:    input.Body.CallSign,
			BaseType:    input.Body.BaseType,
		})
		if err != nil {
			return nil, mapDomainError(err)
		}

		return &CreateMemberOutput{
			Body: personToDetail(person),
		}, nil
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
		if memberSvc == nil {
			return nil, ErrNotImplemented()
		}

		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}

		person, err := memberSvc.GetPerson(ctx, principal, input.ID)
		if err != nil {
			return nil, mapDomainError(err)
		}

		return &MemberGetOutput{
			Body: personToDetail(person),
		}, nil
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
		if memberSvc == nil {
			return nil, ErrNotImplemented()
		}

		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}

		person, err := memberSvc.UpdatePerson(ctx, principal, members.UpdatePersonParams{
			ID:          input.ID,
			DisplayName: input.Body.DisplayName,
			SortName:    input.Body.SortName,
			CallSign:    input.Body.CallSign,
			Version:     input.Body.Version,
		})
		if err != nil {
			return nil, mapDomainError(err)
		}

		return &MemberUpdateOutput{
			Body: personToDetail(person),
		}, nil
	})

	Register(api, huma.Operation{
		OperationID:   "member-deactivate",
		Method:        http.MethodPost,
		Path:          "/members/{id}/deactivate",
		Summary:       "Deactivate a member",
		Tags:          []string{"members"},
		DefaultStatus: http.StatusNoContent,
	}, OperationMeta{
		RequiredCapability: "member.deactivate",
		AuditAction:        "member.deactivate",
		ConfirmationLevel:  "explicit-confirm",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *MemberDeactivateInput) (*MemberDeactivateOutput, error) {
		if memberSvc == nil {
			return nil, ErrNotImplemented()
		}

		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}

		if err := memberSvc.DeactivatePerson(ctx, principal, input.ID, input.Body.Version); err != nil {
			return nil, mapDomainError(err)
		}

		return nil, nil
	})

	Register(api, huma.Operation{
		OperationID:   "member-reactivate",
		Method:        http.MethodPost,
		Path:          "/members/{id}/reactivate",
		Summary:       "Reactivate a member",
		Tags:          []string{"members"},
		DefaultStatus: http.StatusNoContent,
	}, OperationMeta{
		RequiredCapability: "member.deactivate",
		AuditAction:        "member.reactivate",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *MemberReactivateInput) (*MemberReactivateOutput, error) {
		if memberSvc == nil {
			return nil, ErrNotImplemented()
		}

		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}

		if err := memberSvc.ReactivatePerson(ctx, principal, input.ID, input.Body.Version); err != nil {
			return nil, mapDomainError(err)
		}

		return nil, nil
	})

	// Timeline remains a stub — separate bead bcars-portal-exo.
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
