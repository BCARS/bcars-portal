package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/authn"
	"github.com/bcars/bcars-portal/internal/db"
	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/domain/members"
)

// --- Membership types ---

type Membership struct {
	ID                 int64  `json:"id"`
	PersonID           int64  `json:"person_id"`
	BaseType           string `json:"base_type" enum:"full,associate"`
	Lifecycle          string `json:"lifecycle" enum:"pending,approved,rejected,inactive,resigned,deceased"`
	JoinedOn           string `json:"joined_on,omitempty" format:"date"`
	EndedOn            string `json:"ended_on,omitempty" format:"date"`
	LegacyCurrentUntil string `json:"legacy_current_until,omitempty" format:"date"`
	Version            int64  `json:"version"`
	CreatedAt          string `json:"created_at" format:"date-time"`
	UpdatedAt          string `json:"updated_at" format:"date-time"`
}

type FCCVerification struct {
	ID                 int64  `json:"id"`
	MembershipID       int64  `json:"membership_id"`
	CallSign           string `json:"call_sign"`
	VerificationSource string `json:"verification_source"`
	VerifiedAt         string `json:"verified_at" format:"date-time"`
	VerifiedByUserID   int64  `json:"verified_by_user_id"`
	RevokedAt          string `json:"revoked_at,omitempty" format:"date-time"`
	RevokedByUserID    int64  `json:"revoked_by_user_id,omitempty"`
	RevokeReason       string `json:"revoke_reason,omitempty"`
}

type HonoraryGrant struct {
	ID           int64  `json:"id"`
	MembershipID int64  `json:"membership_id"`
	GrantType    string `json:"grant_type"`
	IsLifetime   bool   `json:"is_lifetime"`
	GrantedAt    string `json:"granted_at" format:"date-time"`
	EndsOn       string `json:"ends_on,omitempty" format:"date"`
	Reason       string `json:"reason,omitempty"`
	RevokedAt    string `json:"revoked_at,omitempty" format:"date-time"`
}

// --- Membership lifecycle inputs / outputs ---

type ApplyMembershipInput struct {
	MemberID int64 `path:"id"`
	Body     struct {
		BaseType string `json:"base_type" enum:"full,associate"`
	}
}
type ApplyMembershipOutput struct {
	Body Membership
}

type ApproveMembershipBody struct {
	BaseType string `json:"base_type" enum:"full,associate"`
	Reason   string `json:"reason,omitempty"`
	Version  int64  `json:"version"`
}
type ApproveMembershipInput struct {
	ID   int64 `path:"id"`
	Body ApproveMembershipBody
}
type ApproveMembershipOutput struct {
	Body Membership
}

type RejectMembershipBody struct {
	Reason  string `json:"reason" minLength:"1"`
	Version int64  `json:"version"`
}
type RejectMembershipInput struct {
	ID   int64 `path:"id"`
	Body RejectMembershipBody
}
type RejectMembershipOutput struct {
	Body Membership
}

type LifecycleMembershipBody struct {
	To      string `json:"to" enum:"inactive,resigned,deceased"`
	Reason  string `json:"reason,omitempty"`
	Version int64  `json:"version"`
}
type LifecycleMembershipInput struct {
	ID   int64 `path:"id"`
	Body LifecycleMembershipBody
}
type LifecycleMembershipOutput struct {
	Body Membership
}

// --- FCC verification inputs / outputs ---

type CreateFCCBody struct {
	CallSign           string `json:"call_sign" minLength:"1"`
	VerificationSource string `json:"verification_source" enum:"manual_officer,legacy_import"`
}
type CreateFCCInput struct {
	MembershipID int64 `path:"id"`
	Body         CreateFCCBody
}
type CreateFCCOutput struct {
	Body FCCVerification
}

type RevokeFCCBody struct {
	Reason string `json:"reason" minLength:"1"`
}
type RevokeFCCInput struct {
	ID   int64 `path:"id"`
	Body RevokeFCCBody
}
type RevokeFCCOutput struct{}

// --- Honorary grant inputs / outputs ---

type CreateHonoraryBody struct {
	GrantType  string `json:"grant_type" minLength:"1"`
	IsLifetime bool   `json:"is_lifetime"`
	EndsOn     string `json:"ends_on,omitempty" format:"date"`
	Reason     string `json:"reason,omitempty"`
}
type CreateHonoraryInput struct {
	MembershipID int64 `path:"id"`
	Body         CreateHonoraryBody
}
type CreateHonoraryOutput struct {
	Body HonoraryGrant
}

type UpdateHonoraryBody struct {
	Reason  string `json:"reason,omitempty"`
	EndsOn  string `json:"ends_on,omitempty" format:"date"`
	Version int64  `json:"version"`
}
type UpdateHonoraryInput struct {
	ID   int64 `path:"id"`
	Body UpdateHonoraryBody
}
type UpdateHonoraryOutput struct {
	Body HonoraryGrant
}

type ExpireHonoraryBody struct {
	Reason  string `json:"reason,omitempty"`
	Version int64  `json:"version"`
}
type ExpireHonoraryInput struct {
	ID   int64 `path:"id"`
	Body ExpireHonoraryBody
}
type ExpireHonoraryOutput struct{}

type RevokeHonoraryBody struct {
	Reason  string `json:"reason" minLength:"1"`
	Version int64  `json:"version"`
}
type RevokeHonoraryInput struct {
	ID   int64 `path:"id"`
	Body RevokeHonoraryBody
}
type RevokeHonoraryOutput struct{}

// honoraryFromDB converts a sqlcgen.HonoraryGrant to the API type.
func honoraryFromDB(g sqlcgen.HonoraryGrant) HonoraryGrant {
	return HonoraryGrant{
		ID:           g.ID,
		MembershipID: g.MembershipID,
		IsLifetime:   g.IsLifetime == 1,
		GrantedAt:    g.ApprovedAt,
		EndsOn:       g.EndsOn.String,
		Reason:       g.Reason,
		RevokedAt:    g.RevokedAt.String,
	}
}

// membershipFromDB converts a sqlcgen.Membership to the API type.
func membershipFromDB(m sqlcgen.Membership) Membership {
	return Membership{
		ID:                 m.ID,
		PersonID:           m.PersonID,
		BaseType:           m.BaseType,
		Lifecycle:          m.Lifecycle,
		JoinedOn:           m.JoinedOn.String,
		EndedOn:            m.EndedOn.String,
		LegacyCurrentUntil: m.LegacyCurrentUntil.String,
		Version:            m.Version,
		CreatedAt:          m.CreatedAt,
		UpdatedAt:          m.UpdatedAt,
	}
}

// mapMembershipError converts domain errors to HTTP status codes.
func mapMembershipError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, authz.ErrUnauthenticated) {
		return huma.NewError(http.StatusUnauthorized, "not authenticated")
	}
	if errors.Is(err, authz.ErrDenied) {
		return huma.NewError(http.StatusForbidden, "insufficient capabilities")
	}
	if errors.Is(err, db.ErrStale) {
		return ErrStale("resource was modified by another request; re-fetch and retry")
	}
	if errors.Is(err, sql.ErrNoRows) {
		return huma.NewError(http.StatusNotFound, "membership not found")
	}
	return huma.NewError(http.StatusInternalServerError, err.Error())
}

// requireAuthnPrincipal gets the authn principal and converts to authz.
func requireAuthnPrincipal(ctx context.Context) (*authz.Principal, error) {
	p := authn.PrincipalFrom(ctx)
	if p == nil {
		return nil, huma.NewError(http.StatusUnauthorized, "not authenticated")
	}
	return &authz.Principal{
		UserID:       p.UserID,
		Capabilities: p.Capabilities,
	}, nil
}

// RegisterMemberships registers all membership lifecycle, FCC, and honorary grant endpoints.
func RegisterMemberships(api huma.API, deps Deps) {
	var memberSvc *members.Service
	if deps.DB != nil {
		memberSvc = members.NewService(deps.DB)
	}

	Register(api, huma.Operation{
		OperationID:   "membership-apply",
		Method:        http.MethodPost,
		Path:          "/members/{id}/memberships/apply",
		Summary:       "Create a pending membership application for a member",
		Tags:          []string{"memberships"},
		DefaultStatus: http.StatusCreated,
	}, OperationMeta{
		RequiredCapability: "member.create",
		AuditAction:        "membership.apply",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "curated",
	}, func(ctx context.Context, input *ApplyMembershipInput) (*ApplyMembershipOutput, error) {
		if memberSvc == nil {
			return nil, ErrNotImplemented()
		}

		principal, err := requireAuthnPrincipal(ctx)
		if err != nil {
			return nil, err
		}

		// CreatePerson already creates a pending membership; for standalone
		// apply we use the lower-level query.
		q := sqlcgen.New(deps.DB)
		if err := authz.Authorize(ctx, principal, "member.create", nil); err != nil {
			return nil, mapMembershipError(err)
		}

		m, err := q.CreateMembership(ctx, sqlcgen.CreateMembershipParams{
			PersonID: input.MemberID,
			BaseType: input.Body.BaseType,
		})
		if err != nil {
			return nil, huma.NewError(http.StatusConflict, "failed to create membership: "+err.Error())
		}

		return &ApplyMembershipOutput{Body: membershipFromDB(m)}, nil
	})

	Register(api, huma.Operation{
		OperationID: "membership-approve",
		Method:      http.MethodPost,
		Path:        "/memberships/{id}/approve",
		Summary:     "Approve a pending membership and set base type",
		Tags:        []string{"memberships"},
	}, OperationMeta{
		RequiredCapability: "membership.approve",
		AuditAction:        "membership.approve",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *ApproveMembershipInput) (*ApproveMembershipOutput, error) {
		if memberSvc == nil {
			return nil, ErrNotImplemented()
		}

		principal, err := requireAuthnPrincipal(ctx)
		if err != nil {
			return nil, err
		}

		m, err := memberSvc.ApproveMembership(ctx, principal,
			input.ID, input.Body.Version, input.Body.BaseType, input.Body.Reason)
		if err != nil {
			return nil, mapMembershipError(err)
		}

		return &ApproveMembershipOutput{Body: membershipFromDB(m)}, nil
	})

	Register(api, huma.Operation{
		OperationID: "membership-reject",
		Method:      http.MethodPost,
		Path:        "/memberships/{id}/reject",
		Summary:     "Reject a pending membership",
		Tags:        []string{"memberships"},
	}, OperationMeta{
		RequiredCapability: "membership.approve",
		AuditAction:        "membership.reject",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *RejectMembershipInput) (*RejectMembershipOutput, error) {
		if memberSvc == nil {
			return nil, ErrNotImplemented()
		}

		principal, err := requireAuthnPrincipal(ctx)
		if err != nil {
			return nil, err
		}

		m, err := memberSvc.RejectMembership(ctx, principal,
			input.ID, input.Body.Version, input.Body.Reason)
		if err != nil {
			return nil, mapMembershipError(err)
		}

		return &RejectMembershipOutput{Body: membershipFromDB(m)}, nil
	})

	Register(api, huma.Operation{
		OperationID: "membership-lifecycle",
		Method:      http.MethodPost,
		Path:        "/memberships/{id}/lifecycle",
		Summary:     "Transition membership lifecycle (inactive, resigned, deceased)",
		Tags:        []string{"memberships"},
	}, OperationMeta{
		RequiredCapability: "membership.lifecycle",
		AuditAction:        "membership.lifecycle",
		ConfirmationLevel:  "explicit-confirm",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *LifecycleMembershipInput) (*LifecycleMembershipOutput, error) {
		if memberSvc == nil {
			return nil, ErrNotImplemented()
		}

		principal, err := requireAuthnPrincipal(ctx)
		if err != nil {
			return nil, err
		}

		m, err := memberSvc.TransitionLifecycle(ctx, principal,
			input.ID, input.Body.Version, input.Body.To)
		if err != nil {
			return nil, mapMembershipError(err)
		}

		return &LifecycleMembershipOutput{Body: membershipFromDB(m)}, nil
	})

	// --- FCC verification endpoints ---

	Register(api, huma.Operation{
		OperationID:   "fcc-verification-create",
		Method:        http.MethodPost,
		Path:          "/memberships/{id}/fcc-verifications",
		Summary:       "Record an FCC license verification for a membership",
		Tags:          []string{"fcc"},
		DefaultStatus: http.StatusCreated,
	}, OperationMeta{
		RequiredCapability: "fcc.verify",
		AuditAction:        "fcc.verification.create",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "curated",
	}, func(ctx context.Context, input *CreateFCCInput) (*CreateFCCOutput, error) {
		if memberSvc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requireAuthnPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		v, err := memberSvc.VerifyFCC(ctx, principal,
			input.MembershipID, input.Body.CallSign, "", input.Body.VerificationSource)
		if err != nil {
			return nil, mapMembershipError(err)
		}
		return &CreateFCCOutput{Body: FCCVerification{
			ID: v.ID, MembershipID: v.MembershipID,
			CallSign: v.CallSign, VerificationSource: v.VerificationSource,
			VerifiedAt: v.VerifiedAt, VerifiedByUserID: v.VerifiedBy,
		}}, nil
	})

	Register(api, huma.Operation{
		OperationID:   "fcc-verification-revoke",
		Method:        http.MethodPost,
		Path:          "/fcc-verifications/{id}/revoke",
		Summary:       "Revoke an FCC verification",
		Tags:          []string{"fcc"},
		DefaultStatus: http.StatusNoContent,
	}, OperationMeta{
		RequiredCapability: "fcc.verify",
		AuditAction:        "fcc.verification.revoke",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *RevokeFCCInput) (*RevokeFCCOutput, error) {
		if memberSvc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requireAuthnPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		if err := memberSvc.RevokeFCCVerification(ctx, principal, input.ID, input.Body.Reason); err != nil {
			return nil, mapMembershipError(err)
		}
		return nil, nil
	})

	// --- Honorary grant endpoints ---

	Register(api, huma.Operation{
		OperationID:   "honorary-grant-create",
		Method:        http.MethodPost,
		Path:          "/memberships/{id}/honorary-grants",
		Summary:       "Create an honorary grant for a membership",
		Tags:          []string{"honorary"},
		DefaultStatus: http.StatusCreated,
	}, OperationMeta{
		RequiredCapability: "honorary.grant",
		AuditAction:        "honorary.grant.create",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *CreateHonoraryInput) (*CreateHonoraryOutput, error) {
		if memberSvc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requireAuthnPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		g, err := memberSvc.CreateHonoraryGrant(ctx, principal, members.CreateHonoraryGrantParams{
			MembershipID: input.MembershipID,
			IsLifetime:   input.Body.IsLifetime,
			EndsOn:       input.Body.EndsOn,
			Reason:       input.Body.Reason,
		})
		if err != nil {
			return nil, mapMembershipError(err)
		}
		return &CreateHonoraryOutput{Body: honoraryFromDB(g)}, nil
	})

	Register(api, huma.Operation{
		OperationID: "honorary-grant-update",
		Method:      http.MethodPatch,
		Path:        "/honorary-grants/{id}",
		Summary:     "Update an honorary grant",
		Tags:        []string{"honorary"},
	}, OperationMeta{
		RequiredCapability: "honorary.grant",
		AuditAction:        "honorary.grant.update",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *UpdateHonoraryInput) (*UpdateHonoraryOutput, error) {
		// Update requires a domain method not yet implemented.
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID:   "honorary-grant-expire",
		Method:        http.MethodPost,
		Path:          "/honorary-grants/{id}/expire",
		Summary:       "Mark an honorary grant as expired",
		Tags:          []string{"honorary"},
		DefaultStatus: http.StatusNoContent,
	}, OperationMeta{
		RequiredCapability: "honorary.grant",
		AuditAction:        "honorary.grant.expire",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *ExpireHonoraryInput) (*ExpireHonoraryOutput, error) {
		// Expire requires a domain method not yet implemented.
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID:   "honorary-grant-revoke",
		Method:        http.MethodPost,
		Path:          "/honorary-grants/{id}/revoke",
		Summary:       "Revoke an honorary grant",
		Tags:          []string{"honorary"},
		DefaultStatus: http.StatusNoContent,
	}, OperationMeta{
		RequiredCapability: "honorary.grant",
		AuditAction:        "honorary.grant.revoke",
		ConfirmationLevel:  "explicit-confirm",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *RevokeHonoraryInput) (*RevokeHonoraryOutput, error) {
		if memberSvc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requireAuthnPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		if err := memberSvc.RevokeHonoraryGrant(ctx, principal, input.ID, input.Body.Version, input.Body.Reason); err != nil {
			return nil, mapMembershipError(err)
		}
		return nil, nil
	})
}
