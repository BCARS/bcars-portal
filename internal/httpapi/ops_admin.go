package httpapi

import (
	"context"
	"database/sql"
	"net/http"
	"sort"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/authn"
	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/authz"
)

// --- Admin / RBAC types ---

type CapabilityCatalogEntry struct {
	Code              string `json:"code"`
	Description       string `json:"description"`
	Category          string `json:"category"`
	AIToolEligibility string `json:"ai_tool_eligibility"`
}

type AdminRole struct {
	Code         string   `json:"code"`
	Description  string   `json:"description"`
	Kind         string   `json:"kind"`
	Capabilities []string `json:"capabilities" doc:"Capability codes granted by this role."`
}

type AdminUser struct {
	ID        int64  `json:"id"`
	Email     string `json:"email"`
	IsActive  bool   `json:"is_active"`
	CreatedAt string `json:"created_at" format:"date-time"`
}

type RoleGrant struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"user_id"`
	RoleCode  string `json:"role_code"`
	GrantedBy int64  `json:"granted_by"`
	GrantedAt string `json:"granted_at" format:"date-time"`
}

// --- Admin inputs / outputs ---

type CapabilitiesListInput struct{}
type CapabilitiesListOutput struct {
	Body []CapabilityCatalogEntry
}

type RolesListInput struct{}
type RolesListOutput struct {
	Body []AdminRole
}

type UsersListInput struct {
	PageQuery
}
type UsersListOutput struct {
	Body Page[AdminUser]
}

type CreateRoleGrantBody struct {
	RoleCode string `json:"role_code" minLength:"1"`
}
type CreateRoleGrantInput struct {
	UserID int64 `path:"id"`
	Body   CreateRoleGrantBody
}
type CreateRoleGrantOutput struct {
	Body RoleGrant
}

type RevokeRoleGrantInput struct {
	ID int64 `path:"id"`
}
type RevokeRoleGrantOutput struct{}

// RegisterAdmin registers capability catalog, role, and user management endpoints.
func RegisterAdmin(api huma.API, deps Deps) {
	var q *sqlcgen.Queries
	if deps.DB != nil {
		q = sqlcgen.New(deps.DB)
	}

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
		_, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}

		entries := make([]CapabilityCatalogEntry, len(authz.All))
		for i, cap := range authz.All {
			entries[i] = CapabilityCatalogEntry{
				Code:              cap.Code,
				Description:       cap.Description,
				Category:          cap.Category,
				AIToolEligibility: cap.AIToolEligibility,
			}
		}

		return &CapabilitiesListOutput{Body: entries}, nil
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
		if q == nil {
			return nil, ErrNotImplemented()
		}

		_, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}

		roles, err := q.ListRoles(ctx)
		if err != nil {
			return nil, huma.NewError(http.StatusInternalServerError, "failed to list roles")
		}

		roleCaps, err := q.ListRoleCapabilities(ctx)
		if err != nil {
			return nil, huma.NewError(http.StatusInternalServerError, "failed to list role capabilities")
		}

		// Build a map of role → capabilities.
		capsByRole := make(map[string][]string)
		for _, rc := range roleCaps {
			capsByRole[rc.RoleCode] = append(capsByRole[rc.RoleCode], rc.CapabilityCode)
		}

		result := make([]AdminRole, len(roles))
		for i, r := range roles {
			caps := capsByRole[r.Code]
			sort.Strings(caps)
			result[i] = AdminRole{
				Code:         r.Code,
				Description:  r.Description,
				Kind:         r.Kind,
				Capabilities: caps,
			}
		}

		return &RolesListOutput{Body: result}, nil
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

		users, err := q.ListUsers(ctx, sqlcgen.ListUsersParams{
			Limit:  limit,
			Offset: 0,
		})
		if err != nil {
			return nil, huma.NewError(http.StatusInternalServerError, "failed to list users")
		}

		data := make([]AdminUser, len(users))
		for i, u := range users {
			data[i] = AdminUser{
				ID:        u.ID,
				Email:     u.Email,
				IsActive:  u.IsActive == 1,
				CreatedAt: u.CreatedAt,
			}
		}

		return &UsersListOutput{
			Body: Page[AdminUser]{Data: data},
		}, nil
	})

	Register(api, huma.Operation{
		OperationID:   "role-grant-create",
		Method:        http.MethodPost,
		Path:          "/users/{id}/role-grants",
		Summary:       "Grant a role to a user",
		Tags:          []string{"admin"},
		DefaultStatus: http.StatusCreated,
	}, OperationMeta{
		RequiredCapability: "role.grant",
		AuditAction:        "role.grant.create",
		ConfirmationLevel:  "recent-auth",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *CreateRoleGrantInput) (*CreateRoleGrantOutput, error) {
		if q == nil {
			return nil, ErrNotImplemented()
		}

		principal := authn.PrincipalFrom(ctx)
		if principal == nil {
			return nil, huma.NewError(http.StatusUnauthorized, "not authenticated")
		}

		now := time.Now().UTC().Format(time.RFC3339)
		grant, err := q.CreateRoleGrant(ctx, sqlcgen.CreateRoleGrantParams{
			UserID:    input.UserID,
			RoleCode:  input.Body.RoleCode,
			GrantedBy: principal.UserID,
			GrantedAt: now,
		})
		if err != nil {
			return nil, huma.NewError(http.StatusConflict, "failed to create role grant: "+err.Error())
		}

		return &CreateRoleGrantOutput{
			Body: RoleGrant{
				ID:        grant.ID,
				UserID:    grant.UserID,
				RoleCode:  grant.RoleCode,
				GrantedBy: grant.GrantedBy,
				GrantedAt: grant.GrantedAt,
			},
		}, nil
	})

	Register(api, huma.Operation{
		OperationID:   "role-grant-revoke",
		Method:        http.MethodPost,
		Path:          "/role-grants/{id}/revoke",
		Summary:       "Revoke a role grant",
		Tags:          []string{"admin"},
		DefaultStatus: http.StatusNoContent,
	}, OperationMeta{
		RequiredCapability: "role.grant",
		AuditAction:        "role.grant.revoke",
		ConfirmationLevel:  "recent-auth",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *RevokeRoleGrantInput) (*RevokeRoleGrantOutput, error) {
		if q == nil {
			return nil, ErrNotImplemented()
		}

		principal := authn.PrincipalFrom(ctx)
		if principal == nil {
			return nil, huma.NewError(http.StatusUnauthorized, "not authenticated")
		}

		err := q.RevokeRoleGrant(ctx, sqlcgen.RevokeRoleGrantParams{
			RevokedBy: sql.NullInt64{Int64: principal.UserID, Valid: true},
			ID:        input.ID,
		})
		if err != nil {
			return nil, huma.NewError(http.StatusNotFound, "role grant not found or already revoked")
		}

		return nil, nil
	})
}
