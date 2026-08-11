package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/audit"
	"github.com/bcars/bcars-portal/internal/db"
	"github.com/bcars/bcars-portal/internal/domain/memberaccess"
)

// Officer provisioning of optional member accounts and record access
// (bcars-portal-4ux.4).
//
// Every operation here is an officer deciding, explicitly, that one account may
// see one record. Nothing derives access from a matching contact email or a
// family relationship, which is the whole of ADR-0010.

// --- Response types ---

type MemberAccessGrantResponse struct {
	ID          int64  `json:"id"`
	UserID      int64  `json:"user_id"`
	PersonID    int64  `json:"person_id"`
	DisplayName string `json:"display_name,omitempty"`
	AccessKind  string `json:"access_kind" enum:"self,delegate"`
	Reason      string `json:"reason,omitempty"`
	Active      bool   `json:"active"`

	GrantedByUserID int64  `json:"granted_by_user_id,omitempty"`
	GrantedAt       string `json:"granted_at" format:"date-time"`
	RevokedByUserID int64  `json:"revoked_by_user_id,omitempty"`
	RevokedAt       string `json:"revoked_at,omitempty" format:"date-time"`
	RevokeReason    string `json:"revoke_reason,omitempty"`
	Version         int64  `json:"version"`
}

// MemberAccount deliberately carries no password state, no session, and no
// token. There is nothing to expose because a member account has no password
// and its sign-in link is issued elsewhere and stored only as a hash.
type MemberAccount struct {
	UserID  int64  `json:"user_id"`
	Email   string `json:"email"`
	Created bool   `json:"created" doc:"False when an existing account for this address was reused."`

	Grants []MemberAccessGrantResponse `json:"grants"`
}

// --- Inputs and outputs ---

type ProvisionMemberAccountBody struct {
	Email string `json:"email" format:"email" minLength:"3" maxLength:"320" doc:"Normalized before lookup, so one mailbox is always one account."`
}

type ProvisionMemberAccountInput struct {
	Body ProvisionMemberAccountBody
}

type ProvisionMemberAccountOutput struct {
	Body MemberAccount
}

type GrantMemberAccessBody struct {
	PersonID   int64  `json:"person_id" minimum:"1"`
	AccessKind string `json:"access_kind,omitempty" enum:"self,delegate" doc:"Defaults to self. A delegate is someone acting for the member, granted deliberately."`
	Reason     string `json:"reason,omitempty" maxLength:"1000"`
}

type GrantMemberAccessInput struct {
	UserID int64 `path:"user_id"`
	Body   GrantMemberAccessBody
}

type GrantMemberAccessOutput struct {
	Body MemberAccessGrantResponse
}

type RevokeMemberAccessBody struct {
	PersonID int64  `json:"person_id" minimum:"1"`
	Reason   string `json:"reason,omitempty" maxLength:"1000"`
}

type RevokeMemberAccessInput struct {
	UserID int64 `path:"user_id"`
	Body   RevokeMemberAccessBody
}

type RevokeMemberAccessOutput struct {
	Body MemberAccessGrantResponse
}

type ListMemberAccessInput struct {
	UserID int64 `path:"user_id"`
}

type ListMemberAccessOutput struct {
	Body []MemberAccessGrantResponse
}

type ListPersonAccessInput struct {
	PersonID int64 `path:"person_id"`
}

type ListPersonAccessOutput struct {
	Body []MemberAccessGrantResponse
}

// RegisterMemberAccess registers officer provisioning and access management.
func RegisterMemberAccess(api huma.API, deps Deps) {
	var svc *memberaccess.Service
	if deps.DB != nil {
		svc = memberaccess.NewService(deps.DB)
	}

	Register(api, huma.Operation{
		OperationID: "member-account-provision",
		Method:      http.MethodPost,
		Path:        "/member-accounts",
		Summary:     "Create or reuse a member account",
		Description: "Assigns only the member role and never chooses a password: a new account has none " +
			"until its member sets one through password recovery, and an existing account keeps the " +
			"password and roles it already had. An existing account for the same normalized address " +
			"is reused, which is how a shared household mailbox reaches several records.",
		Tags: []string{"member-access"},
	}, OperationMeta{
		RequiredCapability: "member_access.manage",
		AuditAction:        "member_access.provision",
		ConfirmationLevel:  ConfirmExplicit,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *ProvisionMemberAccountInput) (*ProvisionMemberAccountOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		acct, err := svc.Provision(ctx, principal, memberaccess.ProvisionParams{
			Email: input.Body.Email,
		}, time.Now())
		if err != nil {
			return nil, mapMemberAccessError(err)
		}
		audit.StampResource(ctx, "user", acct.UserID)
		return &ProvisionMemberAccountOutput{Body: accountToResponse(acct)}, nil
	})

	Register(api, huma.Operation{
		OperationID: "member-access-grant",
		Method:      http.MethodPost,
		Path:        "/member-accounts/{user_id}/records",
		Summary:     "Grant an account access to one member record",
		Tags:        []string{"member-access"},
	}, OperationMeta{
		RequiredCapability: "member_access.manage",
		AuditAction:        "member_access.grant",
		ConfirmationLevel:  ConfirmExplicit,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *GrantMemberAccessInput) (*GrantMemberAccessOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		g, err := svc.GrantAccess(ctx, principal, input.UserID, memberaccess.GrantParams{
			PersonID:   input.Body.PersonID,
			AccessKind: input.Body.AccessKind,
			Reason:     input.Body.Reason,
		}, time.Now())
		if err != nil {
			return nil, mapMemberAccessError(err)
		}
		audit.StampResource(ctx, "member_access_grant", g.ID)
		return &GrantMemberAccessOutput{Body: grantToResponse(g)}, nil
	})

	Register(api, huma.Operation{
		OperationID: "member-access-revoke",
		Method:      http.MethodPost,
		Path:        "/member-accounts/{user_id}/records/revoke",
		Summary:     "Revoke an account's access to one member record",
		Description: "Takes effect on the account's next request, including inside a session " +
			"that is already open.",
		Tags: []string{"member-access"},
	}, OperationMeta{
		RequiredCapability: "member_access.manage",
		AuditAction:        "member_access.revoke",
		ConfirmationLevel:  ConfirmExplicit,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *RevokeMemberAccessInput) (*RevokeMemberAccessOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		g, err := svc.RevokeAccess(ctx, principal, input.UserID, memberaccess.RevokeParams{
			PersonID: input.Body.PersonID,
			Reason:   input.Body.Reason,
		}, time.Now())
		if err != nil {
			return nil, mapMemberAccessError(err)
		}
		audit.StampResource(ctx, "member_access_grant", g.ID)
		return &RevokeMemberAccessOutput{Body: grantToResponse(g)}, nil
	})

	Register(api, huma.Operation{
		OperationID: "member-access-list",
		Method:      http.MethodGet,
		Path:        "/member-accounts/{user_id}/records",
		Summary:     "List every record an account can or could reach",
		Description: "Includes revoked grants, so who could reach a record and when stays " +
			"answerable.",
		Tags: []string{"member-access"},
	}, OperationMeta{
		RequiredCapability: "member_access.manage",
		AuditAction:        "member_access.list",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *ListMemberAccessInput) (*ListMemberAccessOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		grants, err := svc.ListGrants(ctx, principal, input.UserID)
		if err != nil {
			return nil, mapMemberAccessError(err)
		}
		return &ListMemberAccessOutput{Body: grantsToResponse(grants)}, nil
	})

	Register(api, huma.Operation{
		OperationID: "person-access-list",
		Method:      http.MethodGet,
		Path:        "/members/{person_id}/access",
		Summary:     "List every account that can reach one member record",
		Tags:        []string{"member-access"},
	}, OperationMeta{
		RequiredCapability: "member_access.manage",
		AuditAction:        "member_access.list_person",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *ListPersonAccessInput) (*ListPersonAccessOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		grants, err := svc.ListGrantsForPerson(ctx, principal, input.PersonID)
		if err != nil {
			return nil, mapMemberAccessError(err)
		}
		return &ListPersonAccessOutput{Body: grantsToResponse(grants)}, nil
	})
}

func mapMemberAccessError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, memberaccess.ErrUnknownUser):
		return huma.Error404NotFound("member account not found")
	case errors.Is(err, memberaccess.ErrGrantNotFound):
		return huma.Error404NotFound("no active grant to revoke")
	case errors.Is(err, memberaccess.ErrAlreadyGranted):
		return huma.Error409Conflict("that account already has access to this record")
	case errors.Is(err, db.ErrStale):
		return huma.Error412PreconditionFailed("that grant changed since you read it; re-read and retry")
	case errors.Is(err, memberaccess.ErrEmailRequired),
		errors.Is(err, memberaccess.ErrUnknownPerson),
		errors.Is(err, memberaccess.ErrUnknownAccessKind):
		return huma.Error422UnprocessableEntity(err.Error())
	}
	return huma.Error500InternalServerError("member access operation failed")
}

func accountToResponse(a memberaccess.Account) MemberAccount {
	return MemberAccount{
		UserID:  a.UserID,
		Email:   a.Email,
		Created: a.Created,
		Grants:  grantsToResponse(a.Grants),
	}
}

func grantsToResponse(in []memberaccess.Grant) []MemberAccessGrantResponse {
	out := make([]MemberAccessGrantResponse, 0, len(in))
	for _, g := range in {
		out = append(out, grantToResponse(g))
	}
	return out
}

func grantToResponse(g memberaccess.Grant) MemberAccessGrantResponse {
	return MemberAccessGrantResponse{
		ID:              g.ID,
		UserID:          g.UserID,
		PersonID:        g.PersonID,
		DisplayName:     g.DisplayName,
		AccessKind:      g.AccessKind,
		Reason:          g.Reason,
		Active:          g.Active(),
		GrantedByUserID: g.GrantedBy,
		GrantedAt:       g.GrantedAt,
		RevokedByUserID: g.RevokedBy,
		RevokedAt:       g.RevokedAt,
		RevokeReason:    g.RevokeReason,
		Version:         g.Version,
	}
}
