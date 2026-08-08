package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/authn"
	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/members"
)

// --- Contact method types ---

type ContactMethod struct {
	ID         int64  `json:"id"`
	PersonID   int64  `json:"person_id"`
	Kind       string `json:"kind" enum:"email,phone,postal"`
	Label      string `json:"label,omitempty"`
	ValueRaw   string `json:"value_raw"`
	ValueNorm  string `json:"value_norm,omitempty"`
	IsPrimary  bool   `json:"is_primary"`
	ArchivedAt string `json:"archived_at,omitempty" format:"date-time"`
	Version    int64  `json:"version"`
	CreatedAt  string `json:"created_at" format:"date-time"`
}

type VisibilityEvent struct {
	ID              int64  `json:"id"`
	ContactMethodID int64  `json:"contact_method_id"`
	Audience        string `json:"audience" enum:"hidden,full_members,officers_only"`
	Source          string `json:"source"`
	EffectiveAt     string `json:"effective_at" format:"date-time"`
}

type ACSARESEvent struct {
	ID           int64  `json:"id"`
	PersonID     int64  `json:"person_id"`
	Participates bool   `json:"participates"`
	Source       string `json:"source"`
	EffectiveAt  string `json:"effective_at" format:"date-time"`
	Reason       string `json:"reason,omitempty"`
}

// --- Inputs / outputs ---

type ContactMethodsListInput struct {
	MemberID int64 `path:"id"`
}
type ContactMethodsListOutput struct {
	Body []ContactMethod
}

type CreateContactMethodBody struct {
	Kind  string `json:"kind" enum:"email,phone,postal"`
	Label string `json:"label,omitempty"`
	Value string `json:"value_raw" minLength:"1"`
}
type CreateContactMethodInput struct {
	MemberID int64 `path:"id"`
	Body     CreateContactMethodBody
}
type CreateContactMethodOutput struct {
	Body ContactMethod
}

type ArchiveContactMethodInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Version int64 `json:"version"`
	}
}
type ArchiveContactMethodOutput struct{}

type MakePrimaryInput struct {
	ID int64 `path:"id"`
}
type MakePrimaryOutput struct{}

type PostVisibilityBody struct {
	Audience string `json:"audience" enum:"hidden,full_members,officers_only"`
}
type PostVisibilityInput struct {
	ID   int64 `path:"id"`
	Body PostVisibilityBody
}
type PostVisibilityOutput struct {
	Body VisibilityEvent
}

type GetACSARESInput struct {
	MemberID int64 `path:"id"`
}
type GetACSARESOutput struct {
	Body ACSARESEvent
}

type PostACSARESBody struct {
	Participates bool   `json:"participates"`
	Reason       string `json:"reason,omitempty"`
}
type PostACSARESInput struct {
	MemberID int64 `path:"id"`
	Body     PostACSARESBody
}
type PostACSARESOutput struct {
	Body ACSARESEvent
}

func contactToResponse(c sqlcgen.ContactMethod) ContactMethod {
	return ContactMethod{
		ID:        c.ID,
		PersonID:  c.PersonID,
		Kind:      c.Kind,
		ValueRaw:  c.ValueRaw,
		ValueNorm: c.ValueNorm,
		IsPrimary: c.IsPrimary == 1,
		Version:   c.Version,
		CreatedAt: c.CreatedAt,
	}
}

// RegisterContactMethods registers contact method and sharing preference endpoints.
func RegisterContactMethods(api huma.API, deps Deps) {
	var memberSvc *members.Service
	if deps.DB != nil {
		memberSvc = members.NewService(deps.DB)
	}

	Register(api, huma.Operation{
		OperationID: "contact-methods-list",
		Method:      http.MethodGet,
		Path:        "/members/{id}/contact-methods",
		Summary:     "List contact methods for a member",
		Tags:        []string{"contact-methods"},
	}, OperationMeta{
		RequiredCapability: "member.read",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "read-only",
	}, func(ctx context.Context, input *ContactMethodsListInput) (*ContactMethodsListOutput, error) {
		if memberSvc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		methods, err := memberSvc.ListContactMethods(ctx, principal, input.MemberID)
		if err != nil {
			return nil, mapDomainError(err)
		}
		data := make([]ContactMethod, len(methods))
		for i, m := range methods {
			data[i] = contactToResponse(m)
		}
		return &ContactMethodsListOutput{Body: data}, nil
	})

	Register(api, huma.Operation{
		OperationID:   "contact-method-create",
		Method:        http.MethodPost,
		Path:          "/members/{id}/contact-methods",
		Summary:       "Add a contact method to a member",
		Tags:          []string{"contact-methods"},
		DefaultStatus: http.StatusCreated,
	}, OperationMeta{
		RequiredCapability: "contact_method.write",
		AuditAction:        "contact_method.create",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "curated",
	}, func(ctx context.Context, input *CreateContactMethodInput) (*CreateContactMethodOutput, error) {
		if memberSvc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		cm, err := memberSvc.CreateContactMethod(ctx, principal, members.CreateContactMethodParams{
			PersonID: input.MemberID,
			Kind:     input.Body.Kind,
			ValueRaw: input.Body.Value,
		})
		if err != nil {
			return nil, mapDomainError(err)
		}
		resp := contactToResponse(cm)
		return &CreateContactMethodOutput{Body: resp}, nil
	})

	Register(api, huma.Operation{
		OperationID:   "contact-method-archive",
		Method:        http.MethodPost,
		Path:          "/contact-methods/{id}/archive",
		Summary:       "Archive a contact method",
		Tags:          []string{"contact-methods"},
		DefaultStatus: http.StatusNoContent,
	}, OperationMeta{
		RequiredCapability: "contact_method.write",
		AuditAction:        "contact_method.archive",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *ArchiveContactMethodInput) (*ArchiveContactMethodOutput, error) {
		if memberSvc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		if err := memberSvc.ArchiveContactMethod(ctx, principal, input.ID, input.Body.Version); err != nil {
			return nil, mapDomainError(err)
		}
		return nil, nil
	})

	Register(api, huma.Operation{
		OperationID:   "contact-method-make-primary",
		Method:        http.MethodPost,
		Path:          "/contact-methods/{id}/make-primary",
		Summary:       "Make a contact method the primary for its kind",
		Tags:          []string{"contact-methods"},
		DefaultStatus: http.StatusNoContent,
	}, OperationMeta{
		RequiredCapability: "contact_method.write",
		AuditAction:        "contact_method.make_primary",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *MakePrimaryInput) (*MakePrimaryOutput, error) {
		if memberSvc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		if err := memberSvc.MakePrimary(ctx, principal, input.ID); err != nil {
			return nil, mapDomainError(err)
		}
		return nil, nil
	})

	Register(api, huma.Operation{
		OperationID:   "contact-method-visibility-post",
		Method:        http.MethodPost,
		Path:          "/contact-methods/{id}/visibility",
		Summary:       "Record a new visibility preference event",
		Tags:          []string{"sharing"},
		DefaultStatus: http.StatusCreated,
	}, OperationMeta{
		RequiredCapability: "sharing_pref.write.officer",
		AuditAction:        "contact_method.visibility.set",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *PostVisibilityInput) (*PostVisibilityOutput, error) {
		if memberSvc == nil {
			return nil, ErrNotImplemented()
		}
		principal := authn.PrincipalFrom(ctx)
		if principal == nil {
			return nil, huma.NewError(http.StatusUnauthorized, "not authenticated")
		}
		ev, err := memberSvc.SetDirectoryVisibility(ctx, toAuthzPrincipal(principal), input.ID, input.Body.Audience)
		if err != nil {
			return nil, mapDomainError(err)
		}
		return &PostVisibilityOutput{Body: VisibilityEvent{
			ID:              ev.ID,
			ContactMethodID: ev.ContactMethodID,
			Audience:        ev.Audience,
			Source:          ev.Source,
			EffectiveAt:     ev.EffectiveAt,
		}}, nil
	})

	Register(api, huma.Operation{
		OperationID: "member-acs-ares-get",
		Method:      http.MethodGet,
		Path:        "/members/{id}/acs-ares-sharing",
		Summary:     "Get current ACS/ARES sharing preference",
		Tags:        []string{"sharing"},
	}, OperationMeta{
		RequiredCapability: "member.read",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "read-only",
	}, func(ctx context.Context, input *GetACSARESInput) (*GetACSARESOutput, error) {
		// Stub — reading sharing history requires a new query not yet in scope.
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID:   "member-acs-ares-post",
		Method:        http.MethodPost,
		Path:          "/members/{id}/acs-ares-sharing",
		Summary:       "Record a new ACS/ARES sharing preference event",
		Tags:          []string{"sharing"},
		DefaultStatus: http.StatusCreated,
	}, OperationMeta{
		RequiredCapability: "sharing_pref.write.officer",
		AuditAction:        "acs_ares.sharing.set",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *PostACSARESInput) (*PostACSARESOutput, error) {
		if memberSvc == nil {
			return nil, ErrNotImplemented()
		}
		principal := authn.PrincipalFrom(ctx)
		if principal == nil {
			return nil, huma.NewError(http.StatusUnauthorized, "not authenticated")
		}
		ev, err := memberSvc.SetAcsAresSharing(ctx, toAuthzPrincipal(principal), input.MemberID, input.Body.Participates, input.Body.Reason)
		if err != nil {
			return nil, mapDomainError(err)
		}
		return &PostACSARESOutput{Body: ACSARESEvent{
			ID:           ev.ID,
			PersonID:     ev.PersonID,
			Participates: ev.Participates == 1,
			Source:       ev.Source,
			EffectiveAt:  ev.EffectiveAt,
			Reason:       ev.Reason.String,
		}}, nil
	})
}
