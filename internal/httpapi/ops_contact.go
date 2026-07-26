package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// --- Contact method types ---

type ContactMethod struct {
	ID         int64  `json:"id"`
	PersonID   int64  `json:"person_id"`
	Kind       string `json:"kind" enum:"email,phone,postal"`
	Label      string `json:"label,omitempty"`
	Value      string `json:"value_raw"`
	IsPrimary  bool   `json:"is_primary"`
	ArchivedAt string `json:"archived_at,omitempty" format:"date-time"`
	Version    int64  `json:"version"`
	CreatedAt  string `json:"created_at" format:"date-time"`
	UpdatedAt  string `json:"updated_at" format:"date-time"`
}

type VisibilityEvent struct {
	ID              int64  `json:"id"`
	ContactMethodID int64  `json:"contact_method_id"`
	Audience        string `json:"audience" enum:"hidden,full_members,officers_only"`
	Source          string `json:"source" enum:"import_default,officer_ui,member_request"`
	EffectiveAt     string `json:"effective_at" format:"date-time"`
	ActorUserID     int64  `json:"actor_user_id,omitempty"`
	Note            string `json:"note,omitempty"`
}

type VisibilityHistory struct {
	Current string          `json:"current" enum:"hidden,full_members,officers_only"`
	History []VisibilityEvent `json:"history"`
}

type ACSARESEvent struct {
	ID          int64  `json:"id"`
	PersonID    int64  `json:"person_id"`
	Participates bool  `json:"participates"`
	Source      string `json:"source" enum:"import_default,officer_ui,member_request"`
	EffectiveAt string `json:"effective_at" format:"date-time"`
	ActorUserID int64  `json:"actor_user_id,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

type ACSARESHistory struct {
	Current  bool           `json:"current"`
	History  []ACSARESEvent `json:"history"`
}

// --- Contact method inputs / outputs ---

type ContactMethodsListInput struct {
	MemberID int64 `path:"id"`
}
type ContactMethodsListOutput struct {
	Body []ContactMethod
}

type CreateContactMethodBody struct {
	Kind    string `json:"kind" enum:"email,phone,postal"`
	Label   string `json:"label,omitempty"`
	Value   string `json:"value_raw" minLength:"1"`
}
type CreateContactMethodInput struct {
	MemberID int64 `path:"id"`
	Body     CreateContactMethodBody
}
type CreateContactMethodOutput struct {
	Body ContactMethod
}

type UpdateContactMethodBody struct {
	Label   string `json:"label,omitempty"`
	Value   string `json:"value_raw,omitempty"`
	Version int64  `json:"version"`
}
type UpdateContactMethodInput struct {
	ID   int64 `path:"id"`
	Body UpdateContactMethodBody
}
type UpdateContactMethodOutput struct {
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
	ID   int64 `path:"id"`
	Body struct {
		Version int64 `json:"version"`
	}
}
type MakePrimaryOutput struct{}

type GetVisibilityInput struct {
	ID int64 `path:"id"`
}
type GetVisibilityOutput struct {
	Body VisibilityHistory
}

type PostVisibilityBody struct {
	Audience string `json:"audience" enum:"hidden,full_members,officers_only"`
	Note     string `json:"note,omitempty"`
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
	Body ACSARESHistory
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

// RegisterContactMethods registers contact method and sharing preference endpoints.
func RegisterContactMethods(api huma.API) {
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
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "contact-method-create",
		Method:      http.MethodPost,
		Path:        "/members/{id}/contact-methods",
		Summary:     "Add a contact method to a member",
		Tags:        []string{"contact-methods"},
		DefaultStatus: http.StatusCreated,
	}, OperationMeta{
		RequiredCapability: "contact_method.write",
		AuditAction:        "contact_method.create",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "curated",
	}, func(ctx context.Context, input *CreateContactMethodInput) (*CreateContactMethodOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "contact-method-update",
		Method:      http.MethodPatch,
		Path:        "/contact-methods/{id}",
		Summary:     "Update a contact method",
		Tags:        []string{"contact-methods"},
	}, OperationMeta{
		RequiredCapability: "contact_method.write",
		AuditAction:        "contact_method.update",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "curated",
	}, func(ctx context.Context, input *UpdateContactMethodInput) (*UpdateContactMethodOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "contact-method-archive",
		Method:      http.MethodPost,
		Path:        "/contact-methods/{id}/archive",
		Summary:     "Archive a contact method",
		Tags:        []string{"contact-methods"},
		DefaultStatus: http.StatusNoContent,
	}, OperationMeta{
		RequiredCapability: "contact_method.write",
		AuditAction:        "contact_method.archive",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *ArchiveContactMethodInput) (*ArchiveContactMethodOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "contact-method-make-primary",
		Method:      http.MethodPost,
		Path:        "/contact-methods/{id}/make-primary",
		Summary:     "Make a contact method the primary for its kind",
		Tags:        []string{"contact-methods"},
		DefaultStatus: http.StatusNoContent,
	}, OperationMeta{
		RequiredCapability: "contact_method.write",
		AuditAction:        "contact_method.make_primary",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *MakePrimaryInput) (*MakePrimaryOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "contact-method-visibility-get",
		Method:      http.MethodGet,
		Path:        "/contact-methods/{id}/visibility",
		Summary:     "Get current audience and visibility history for a contact method",
		Tags:        []string{"sharing"},
	}, OperationMeta{
		RequiredCapability: "member.read",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "read-only",
	}, func(ctx context.Context, input *GetVisibilityInput) (*GetVisibilityOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "contact-method-visibility-post",
		Method:      http.MethodPost,
		Path:        "/contact-methods/{id}/visibility",
		Summary:     "Record a new visibility preference event",
		Tags:        []string{"sharing"},
		DefaultStatus: http.StatusCreated,
	}, OperationMeta{
		RequiredCapability: "sharing_pref.write.officer",
		AuditAction:        "contact_method.visibility.set",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *PostVisibilityInput) (*PostVisibilityOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "member-acs-ares-get",
		Method:      http.MethodGet,
		Path:        "/members/{id}/acs-ares-sharing",
		Summary:     "Get ACS/ARES sharing preference and history",
		Tags:        []string{"sharing"},
	}, OperationMeta{
		RequiredCapability: "member.read",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "read-only",
	}, func(ctx context.Context, input *GetACSARESInput) (*GetACSARESOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "member-acs-ares-post",
		Method:      http.MethodPost,
		Path:        "/members/{id}/acs-ares-sharing",
		Summary:     "Record a new ACS/ARES sharing preference event",
		Tags:        []string{"sharing"},
		DefaultStatus: http.StatusCreated,
	}, OperationMeta{
		RequiredCapability: "sharing_pref.write.officer",
		AuditAction:        "acs_ares.sharing.set",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *PostACSARESInput) (*PostACSARESOutput, error) {
		return nil, ErrNotImplemented()
	})
}
