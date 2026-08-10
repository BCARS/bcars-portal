package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/audit"
	"github.com/bcars/bcars-portal/internal/db"
	"github.com/bcars/bcars-portal/internal/domain/changerequests"
	"github.com/bcars/bcars-portal/internal/domain/idem"
)

// Officer-entered change-request intake and triage (bcars-portal-4ux.2).
//
// Every operation here captures or reads a PROPOSAL. None of them writes a
// person, contact method, membership, coverage row, payment, or preference
// event. Applying an approved item is bcars-portal-4ux.3's job and goes through
// the domain service that owns the field, which is what keeps intake safe to
// expose to a wider set of officers than canonical editing.

// --- Response types ---

type ChangeRequestItem struct {
	ID            int64  `json:"id"`
	Ordinal       int64  `json:"ordinal" doc:"Position as submitted. Stable, so a reviewer and a submitter refer to the same item."`
	Operation     string `json:"operation" doc:"One of the allowlisted operations. 'other' is a reviewable note that can never be approved."`
	ProposedValue string `json:"proposed_value,omitempty"`
	TargetKind    string `json:"target_kind,omitempty" enum:"person,contact_method,membership,relationship"`
	TargetID      int64  `json:"target_id,omitempty"`
	TargetVersion int64  `json:"target_version,omitempty" doc:"Version the submitter saw, when known. Review uses it to detect a change made since."`
	Sensitivity   string `json:"sensitivity" enum:"ordinary,sensitive"`
	Status        string `json:"status" enum:"pending,approved,rejected,needs_verification"`

	ReviewedByUserID int64  `json:"reviewed_by_user_id,omitempty"`
	ReviewedAt       string `json:"reviewed_at,omitempty" format:"date-time"`
	DecisionReason   string `json:"decision_reason,omitempty"`
	VerificationNote string `json:"verification_note,omitempty"`

	AppliedAt              string `json:"applied_at,omitempty" format:"date-time"`
	AppliedResourceKind    string `json:"applied_resource_kind,omitempty"`
	AppliedResourceID      int64  `json:"applied_resource_id,omitempty"`
	AppliedResourceVersion int64  `json:"applied_resource_version,omitempty"`

	Version int64 `json:"version"`
}

type ChangeRequest struct {
	ID     int64  `json:"id"`
	Source string `json:"source" enum:"officer_phone,officer_email,officer_mail,officer_meeting,member,public"`
	Status string `json:"status" enum:"draft,submitted,in_review,resolved,withdrawn"`

	TargetPersonID    int64  `json:"target_person_id,omitempty" doc:"Empty until an officer links an unresolved submission to a person."`
	TargetDisplayName string `json:"target_display_name,omitempty"`

	SuppliedName       string `json:"supplied_name,omitempty" doc:"What the submitter said, never rewritten by triage."`
	SuppliedCallSign   string `json:"supplied_call_sign,omitempty"`
	SuppliedContact    string `json:"supplied_contact,omitempty"`
	StatedRelationship string `json:"stated_relationship,omitempty" doc:"How the submitter says they know the member. Informational; it confers nothing."`

	Summary           string `json:"summary"`
	RequesterUserID   int64  `json:"requester_user_id,omitempty" doc:"Set only for an authenticated member's own request."`
	ReceivedByUserID  int64  `json:"received_by_user_id,omitempty" doc:"The officer who recorded it."`
	SubmittedAt       string `json:"submitted_at" format:"date-time"`
	TriagedByUserID   int64  `json:"triaged_by_user_id,omitempty"`
	TriagedAt         string `json:"triaged_at,omitempty" format:"date-time"`
	ResolvedAt        string `json:"resolved_at,omitempty" format:"date-time"`
	WithdrawnAt       string `json:"withdrawn_at,omitempty" format:"date-time"`
	PendingItemsCount int64  `json:"pending_items_count" doc:"The request resolves only when this reaches zero."`

	Items   []ChangeRequestItem `json:"items"`
	Version int64               `json:"version"`

	CreatedAt string `json:"created_at" format:"date-time"`
	UpdatedAt string `json:"updated_at" format:"date-time"`
}

// --- Inputs and outputs ---

type ChangeRequestItemBody struct {
	Operation     string `json:"operation" minLength:"1" doc:"Must be an allowlisted operation. There is no arbitrary field path."`
	ProposedValue string `json:"proposed_value,omitempty" maxLength:"2000" doc:"Required for every operation except 'other'."`
	TargetKind    string `json:"target_kind,omitempty" enum:"person,contact_method,membership,relationship"`
	TargetID      int64  `json:"target_id,omitempty"`
	TargetVersion int64  `json:"target_version,omitempty"`
	Sensitivity   string `json:"sensitivity,omitempty" enum:"ordinary,sensitive" doc:"Defaults to ordinary. A sensitive approval later requires a verification note."`
}

type CreateChangeRequestBody struct {
	Source             string                  `json:"source" enum:"officer_phone,officer_email,officer_mail,officer_meeting" doc:"How the correction reached the officer. Member and public intake use their own endpoints."`
	TargetPersonID     int64                   `json:"target_person_id,omitempty" doc:"The member this concerns, when known. Otherwise supply a hint."`
	SuppliedName       string                  `json:"supplied_name,omitempty" maxLength:"200"`
	SuppliedCallSign   string                  `json:"supplied_call_sign,omitempty" maxLength:"200"`
	SuppliedContact    string                  `json:"supplied_contact,omitempty" maxLength:"200"`
	StatedRelationship string                  `json:"stated_relationship,omitempty" maxLength:"200"`
	Summary            string                  `json:"summary" minLength:"1" maxLength:"4000" doc:"What was reported, in plain language."`
	Items              []ChangeRequestItemBody `json:"items" minItems:"1" maxItems:"25"`
}

type CreateChangeRequestInput struct {
	IdempotencyKey string `header:"Idempotency-Key" doc:"Required. A retry with the same key returns the original request rather than recording a second one."`
	Body           CreateChangeRequestBody
}

type CreateChangeRequestOutput struct {
	ETag string `header:"ETag"`
	Body ChangeRequest
}

type ListChangeRequestsInput struct {
	Status         string `query:"status" enum:"draft,submitted,in_review,resolved,withdrawn" doc:"Optional exact filter."`
	Source         string `query:"source" enum:"officer_phone,officer_email,officer_mail,officer_meeting,member,public" doc:"Optional exact filter."`
	UnresolvedOnly bool   `query:"unresolved_target_only" doc:"Only submissions with no linked person and no terminal status: the triage queue."`
	Limit          int64  `query:"limit" minimum:"1" maximum:"200" doc:"Defaults to 50."`
	Offset         int64  `query:"offset" minimum:"0"`
}

type ListChangeRequestsOutput struct {
	Body []ChangeRequest
}

type GetChangeRequestInput struct {
	ID int64 `path:"id"`
}

type GetChangeRequestOutput struct {
	ETag string `header:"ETag"`
	Body ChangeRequest
}

type TriageChangeRequestBody struct {
	TargetPersonID int64 `json:"target_person_id" minimum:"1" doc:"The member this submission turned out to concern."`
}

type TriageChangeRequestInput struct {
	ID      int64  `path:"id"`
	IfMatch string `header:"If-Match" doc:"Request version you last read. Required: a missing header is a 428. Two officers triaging one public submission must not silently overwrite each other."`
	Body    TriageChangeRequestBody
}

type TriageChangeRequestOutput struct {
	ETag string `header:"ETag"`
	Body ChangeRequest
}

// RegisterChangeRequests registers officer intake and triage.
func RegisterChangeRequests(api huma.API, deps Deps) {
	var svc *changerequests.Service
	if deps.DB != nil {
		svc = changerequests.NewService(deps.DB)
	}

	Register(api, huma.Operation{
		OperationID: "change-request-create",
		Method:      http.MethodPost,
		Path:        "/change-requests",
		Summary:     "Record a member correction reported to an officer",
		Description: "Captures a proposal only. No canonical member data changes until an " +
			"officer reviews the individual items.",
		Tags: []string{"change-requests"},
	}, OperationMeta{
		RequiredCapability: "change_request.manage",
		AuditAction:        "change_request.create",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *CreateChangeRequestInput) (*CreateChangeRequestOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		if input.IdempotencyKey == "" {
			return nil, huma.Error422UnprocessableEntity("an Idempotency-Key header is required")
		}

		params := changerequests.CreateParams{
			Source:             input.Body.Source,
			TargetPersonID:     input.Body.TargetPersonID,
			SuppliedName:       input.Body.SuppliedName,
			SuppliedCallSign:   input.Body.SuppliedCallSign,
			SuppliedContact:    input.Body.SuppliedContact,
			StatedRelationship: input.Body.StatedRelationship,
			Summary:            input.Body.Summary,
			SourceIPHash:       ClientIPHashFrom(ctx),
		}
		for _, it := range input.Body.Items {
			params.Items = append(params.Items, changerequests.ItemInput{
				Operation:     it.Operation,
				ProposedValue: it.ProposedValue,
				TargetKind:    it.TargetKind,
				TargetID:      it.TargetID,
				TargetVersion: it.TargetVersion,
				Sensitivity:   it.Sensitivity,
			})
		}

		r, err := svc.Create(ctx, principal, params, input.IdempotencyKey, time.Now())
		if err != nil {
			return nil, mapChangeRequestError(err)
		}
		audit.StampResource(ctx, "change_request", r.ID)
		return &CreateChangeRequestOutput{
			ETag: FormatETag(r.Version),
			Body: changeRequestToResponse(r),
		}, nil
	})

	Register(api, huma.Operation{
		OperationID: "change-request-list",
		Method:      http.MethodGet,
		Path:        "/change-requests",
		Summary:     "List change requests",
		Description: "Ordered by submission time, newest first, with an id tie-breaker so " +
			"paging is deterministic.",
		Tags: []string{"change-requests"},
	}, OperationMeta{
		RequiredCapability: "change_request.manage",
		AuditAction:        "change_request.list",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *ListChangeRequestsInput) (*ListChangeRequestsOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		list, err := svc.List(ctx, principal, changerequests.ListFilter{
			Status:               input.Status,
			Source:               input.Source,
			UnresolvedTargetOnly: input.UnresolvedOnly,
			Limit:                input.Limit,
			Offset:               input.Offset,
		})
		if err != nil {
			return nil, mapChangeRequestError(err)
		}
		out := make([]ChangeRequest, 0, len(list))
		for _, r := range list {
			out = append(out, changeRequestToResponse(r))
		}
		return &ListChangeRequestsOutput{Body: out}, nil
	})

	Register(api, huma.Operation{
		OperationID: "change-request-get",
		Method:      http.MethodGet,
		Path:        "/change-requests/{id}",
		Summary:     "Read one change request and its items",
		Tags:        []string{"change-requests"},
	}, OperationMeta{
		RequiredCapability: "change_request.manage",
		AuditAction:        "change_request.read",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *GetChangeRequestInput) (*GetChangeRequestOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		r, err := svc.Get(ctx, principal, input.ID)
		if err != nil {
			return nil, mapChangeRequestError(err)
		}
		return &GetChangeRequestOutput{
			ETag: FormatETag(r.Version),
			Body: changeRequestToResponse(r),
		}, nil
	})

	Register(api, huma.Operation{
		OperationID: "change-request-triage",
		Method:      http.MethodPost,
		Path:        "/change-requests/{id}/target",
		Summary:     "Link a submission to the member it concerns",
		Description: "Records the officer's conclusion. What the submitter supplied is kept " +
			"unchanged beside it.",
		Tags: []string{"change-requests"},
	}, OperationMeta{
		RequiredCapability: "change_request.manage",
		AuditAction:        "change_request.triage",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *TriageChangeRequestInput) (*TriageChangeRequestOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		version, err := requireIfMatch(input.IfMatch)
		if err != nil {
			return nil, err
		}
		r, err := svc.Triage(ctx, principal, input.ID, changerequests.TriageParams{
			TargetPersonID:  input.Body.TargetPersonID,
			ExpectedVersion: version,
		}, time.Now())
		if err != nil {
			return nil, mapChangeRequestError(err)
		}
		audit.StampResource(ctx, "change_request", r.ID)
		return &TriageChangeRequestOutput{
			ETag: FormatETag(r.Version),
			Body: changeRequestToResponse(r),
		}, nil
	})
}

// mapChangeRequestError translates domain errors to HTTP.
//
// Validation refusals are 422 with the domain's own wording, which names the
// offending field. A stale triage is 412, matching every other optimistic
// concurrency failure in this API.
func mapChangeRequestError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, changerequests.ErrNotFound):
		return huma.Error404NotFound("change request not found")
	case errors.Is(err, db.ErrStale):
		return huma.Error412PreconditionFailed("this request changed since you read it; re-read and retry")
	case errors.Is(err, idem.ErrKeyReused):
		return huma.Error409Conflict("that Idempotency-Key was already used for a different request")
	case errors.Is(err, changerequests.ErrUnknownPerson):
		return huma.Error422UnprocessableEntity("target_person_id does not name an existing person")
	case errors.Is(err, changerequests.ErrAlreadyResolved):
		return huma.Error409Conflict("this request is already resolved or withdrawn")
	case errors.Is(err, changerequests.ErrSourceRequired),
		errors.Is(err, changerequests.ErrSummaryRequired),
		errors.Is(err, changerequests.ErrTooLong),
		errors.Is(err, changerequests.ErrNoTarget),
		errors.Is(err, changerequests.ErrUnknownOperation),
		errors.Is(err, changerequests.ErrUnknownTargetKind),
		errors.Is(err, changerequests.ErrTargetIncomplete),
		errors.Is(err, changerequests.ErrValueRequired),
		errors.Is(err, changerequests.ErrTooManyItems),
		errors.Is(err, changerequests.ErrNoItems):
		return huma.Error422UnprocessableEntity(err.Error())
	}
	return huma.Error500InternalServerError("change request operation failed")
}

func changeRequestToResponse(r changerequests.Request) ChangeRequest {
	items := make([]ChangeRequestItem, 0, len(r.Items))
	for _, it := range r.Items {
		items = append(items, ChangeRequestItem{
			ID:                     it.ID,
			Ordinal:                it.Ordinal,
			Operation:              it.Operation,
			ProposedValue:          it.ProposedValue,
			TargetKind:             it.TargetKind,
			TargetID:               it.TargetID,
			TargetVersion:          it.TargetVersion,
			Sensitivity:            it.Sensitivity,
			Status:                 it.Status,
			ReviewedByUserID:       it.ReviewedBy,
			ReviewedAt:             it.ReviewedAt,
			DecisionReason:         it.DecisionReason,
			VerificationNote:       it.VerificationNote,
			AppliedAt:              it.AppliedAt,
			AppliedResourceKind:    it.AppliedResourceKind,
			AppliedResourceID:      it.AppliedResourceID,
			AppliedResourceVersion: it.AppliedResourceVersion,
			Version:                it.Version,
		})
	}
	return ChangeRequest{
		ID:                 r.ID,
		Source:             r.Source,
		Status:             r.Status,
		TargetPersonID:     r.TargetPersonID,
		TargetDisplayName:  r.TargetDisplayName,
		SuppliedName:       r.SuppliedName,
		SuppliedCallSign:   r.SuppliedCallSign,
		SuppliedContact:    r.SuppliedContact,
		StatedRelationship: r.StatedRelationship,
		Summary:            r.Summary,
		RequesterUserID:    r.RequesterUserID,
		ReceivedByUserID:   r.ReceivedBy,
		SubmittedAt:        r.SubmittedAt,
		TriagedByUserID:    r.TriagedBy,
		TriagedAt:          r.TriagedAt,
		ResolvedAt:         r.ResolvedAt,
		WithdrawnAt:        r.WithdrawnAt,
		PendingItemsCount:  r.PendingItems,
		Items:              items,
		Version:            r.Version,
		CreatedAt:          r.CreatedAt,
		UpdatedAt:          r.UpdatedAt,
	}
}
