package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/audit"
	"github.com/bcars/bcars-portal/internal/authn"
	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
)

type CreateInvitationBody struct {
	Email string `json:"email" format:"email" doc:"Address the invitation is sent to."`
	// RoleCode is optional. An invitation with no role produces an ordinary
	// account holding no capabilities.
	RoleCode string `json:"role_code,omitempty" doc:"Role granted when the invitation is accepted. Requires role.grant, and the inviter must already hold every capability the role confers."`
}

type CreateInvitationInput struct {
	Body CreateInvitationBody
}

type InvitationSummary struct {
	Email     string `json:"email"`
	RoleCode  string `json:"role_code,omitempty"`
	ExpiresAt string `json:"expires_at" format:"date-time"`
}

type CreateInvitationOutput struct {
	Body InvitationSummary
}

// RegisterInvitations registers invitation creation.
//
// Without it a fresh installation can bootstrap exactly one administrator via
// portalctl and has no supported route to onboard anyone else — the remaining
// options were direct database access, a second --force bootstrap that confers
// administrator, or seed-demo's hardcoded credentials.
func RegisterInvitations(api huma.API, deps Deps) {
	var q *sqlcgen.Queries
	if deps.DB != nil {
		q = sqlcgen.New(deps.DB)
	}

	Register(api, huma.Operation{
		OperationID:   "invitation-create",
		Method:        http.MethodPost,
		Path:          "/invitations",
		Summary:       "Invite a new officer or user",
		Tags:          []string{"admin"},
		DefaultStatus: http.StatusCreated,
	}, OperationMeta{
		RequiredCapability: "user.invite",
		AuditAction:        "auth.invitation.create",
		ConfirmationLevel:  "recent-auth",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *CreateInvitationInput) (*CreateInvitationOutput, error) {
		if q == nil || deps.EmailLinkService == nil {
			return nil, ErrNotImplemented()
		}

		principal := authn.PrincipalFrom(ctx)
		if principal == nil {
			return nil, huma.NewError(http.StatusUnauthorized, "not authenticated")
		}

		email := strings.TrimSpace(input.Body.Email)
		if email == "" {
			return nil, huma.NewError(http.StatusUnprocessableEntity, "email is required")
		}

		roleCode := strings.TrimSpace(input.Body.RoleCode)
		if roleCode != "" {
			if err := checkMayConferRole(ctx, q, principal, roleCode); err != nil {
				return nil, err
			}
		}

		if _, err := deps.EmailLinkService.CreateInvitation(ctx, email, roleCode, true); err != nil {
			return nil, huma.NewError(http.StatusInternalServerError,
				"failed to create invitation: "+err.Error())
		}

		// The token is deliberately not returned. It is a bearer credential
		// that grants account creation; it travels by email only, so it does
		// not end up in a response log or a client's history.
		audit.StampResource(ctx, "email_link", 0)

		return &CreateInvitationOutput{
			Body: InvitationSummary{
				Email:     email,
				RoleCode:  roleCode,
				ExpiresAt: time.Now().UTC().Add(deps.InvitationTTL()).Format(time.RFC3339),
			},
		}, nil
	})
}

// checkMayConferRole enforces that an invitation cannot grant authority the
// inviter does not already hold.
//
// Without this, user.invite alone would be a privilege-escalation path around
// the entire capability model: anyone who could invite could invite an
// administrator, then use that account. Requiring role.grant is necessary but
// not sufficient — a treasurer holding role.grant must still not be able to
// mint a webmaster.
func checkMayConferRole(ctx context.Context, q *sqlcgen.Queries, principal *authn.Principal, roleCode string) error {
	exists, err := q.RoleExists(ctx, roleCode)
	if err != nil {
		return huma.NewError(http.StatusInternalServerError, "failed to check role")
	}
	if !exists {
		return huma.NewError(http.StatusUnprocessableEntity, fmt.Sprintf("unknown role %q", roleCode))
	}

	if !principal.HasCapability("role.grant") {
		return huma.NewError(http.StatusForbidden,
			"granting a role by invitation requires the role.grant capability")
	}

	conferred, err := q.CapabilitiesForRole(ctx, roleCode)
	if err != nil {
		return huma.NewError(http.StatusInternalServerError, "failed to load role capabilities")
	}

	var missing []string
	for _, code := range conferred {
		if !principal.HasCapability(code) {
			missing = append(missing, code)
		}
	}
	if len(missing) > 0 {
		// Names the capabilities so the caller can tell this apart from a
		// missing role.grant, without revealing anything they cannot already
		// read from the capability catalog.
		return huma.NewError(http.StatusForbidden, fmt.Sprintf(
			"cannot invite to role %q: it confers capabilities you do not hold (%s)",
			roleCode, strings.Join(missing, ", ")))
	}
	return nil
}
