package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// --- Admin / RBAC types ---

type CapabilityCatalogEntry struct {
	Code              string `json:"code"`
	Description       string `json:"description"`
	Category          string `json:"category"`
	AIToolEligibility string `json:"ai_tool_eligibility"`
}

type Role struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities" doc:"Capability codes granted by this role."`
}

type User struct {
	ID          int64  `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Roles       []string `json:"roles" doc:"Role names currently granted."`
	IsActive    bool   `json:"is_active"`
	CreatedAt   string `json:"created_at" format:"date-time"`
}

type RoleGrant struct {
	ID          int64  `json:"id"`
	UserID      int64  `json:"user_id"`
	RoleName    string `json:"role_name"`
	GrantedByID int64  `json:"granted_by_id"`
	GrantedAt   string `json:"granted_at" format:"date-time"`
	RevokedAt   string `json:"revoked_at,omitempty" format:"date-time"`
}

// --- Admin inputs / outputs ---

type CapabilitiesListInput struct{}
type CapabilitiesListOutput struct {
	Body []CapabilityCatalogEntry
}

type RolesListInput struct{}
type RolesListOutput struct {
	Body []Role
}

type UsersListInput struct {
	PageQuery
}
type UsersListOutput struct {
	Body Page[User]
}

type CreateRoleGrantBody struct {
	RoleName string `json:"role_name" minLength:"1"`
}
type CreateRoleGrantInput struct {
	UserID int64 `path:"id"`
	Body   CreateRoleGrantBody
}
type CreateRoleGrantOutput struct {
	Body RoleGrant
}

type RevokeRoleGrantInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Reason string `json:"reason,omitempty"`
	}
}
type RevokeRoleGrantOutput struct{}

// RegisterAdmin registers capability catalog, role, and user management endpoints.
func RegisterAdmin(api huma.API) {
	Register(api, huma.Operation{
		OperationID: "capabilities-list",
		Method:      http.MethodGet,
		Path:        "/capabilities",
		Summary:     "List all capabilities in the catalog",
		Tags:        []string{"admin"},
	}, OperationMeta{
		RequiredCapability: "session.self.read",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "read-only",
	}, func(ctx context.Context, input *CapabilitiesListInput) (*CapabilitiesListOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "roles-list",
		Method:      http.MethodGet,
		Path:        "/roles",
		Summary:     "List all roles and their capability assignments",
		Tags:        []string{"admin"},
	}, OperationMeta{
		RequiredCapability: "session.self.read",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "read-only",
	}, func(ctx context.Context, input *RolesListInput) (*RolesListOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "users-list",
		Method:      http.MethodGet,
		Path:        "/users",
		Summary:     "List users (admin only)",
		Tags:        []string{"admin"},
	}, OperationMeta{
		RequiredCapability: "role.grant",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "read-only",
	}, func(ctx context.Context, input *UsersListInput) (*UsersListOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "role-grant-create",
		Method:      http.MethodPost,
		Path:        "/users/{id}/role-grants",
		Summary:     "Grant a role to a user",
		Tags:        []string{"admin"},
		DefaultStatus: http.StatusCreated,
	}, OperationMeta{
		RequiredCapability: "role.grant",
		AuditAction:        "role.grant.create",
		ConfirmationLevel:  "recent-auth",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *CreateRoleGrantInput) (*CreateRoleGrantOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "role-grant-revoke",
		Method:      http.MethodPost,
		Path:        "/role-grants/{id}/revoke",
		Summary:     "Revoke a role grant",
		Tags:        []string{"admin"},
		DefaultStatus: http.StatusNoContent,
	}, OperationMeta{
		RequiredCapability: "role.grant",
		AuditAction:        "role.grant.revoke",
		ConfirmationLevel:  "recent-auth",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *RevokeRoleGrantInput) (*RevokeRoleGrantOutput, error) {
		return nil, ErrNotImplemented()
	})
}
