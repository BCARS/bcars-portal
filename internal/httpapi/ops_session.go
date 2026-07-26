package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// --- Session / auth types ---

type signInBody struct {
	Email    string `json:"email" format:"email" doc:"Officer email address."`
	Password string `json:"password" minLength:"1" doc:"Officer password."`
}

type sessionInfo struct {
	UserID       int64    `json:"user_id"`
	Email        string   `json:"email"`
	Capabilities []string `json:"capabilities" doc:"Effective capability codes for this session."`
	ExpiresAt    string   `json:"expires_at" format:"date-time"`
}

type SignInInput struct {
	Body signInBody
}
type SignInOutput struct {
	Body sessionInfo
}

type SignOutInput struct{}
type SignOutOutput struct{}

type CurrentSessionInput struct{}
type CurrentSessionOutput struct {
	Body sessionInfo
}

type RecoveryRequestBody struct {
	Email string `json:"email" format:"email"`
}
type RecoveryRequestInput struct{ Body RecoveryRequestBody }
type RecoveryRequestOutput struct{}

type RecoveryConsumeBody struct {
	Token       string `json:"token" minLength:"1"`
	NewPassword string `json:"new_password" minLength:"12"`
}
type RecoveryConsumeInput struct{ Body RecoveryConsumeBody }
type RecoveryConsumeOutput struct {
	Body sessionInfo
}

type InvitationConsumeBody struct {
	Token       string `json:"token" minLength:"1"`
	NewPassword string `json:"new_password" minLength:"12"`
}
type InvitationConsumeInput struct{ Body InvitationConsumeBody }
type InvitationConsumeOutput struct {
	Body sessionInfo
}

// RegisterSessions registers all session and auth endpoints.
func RegisterSessions(api huma.API) {
	Register(api, huma.Operation{
		OperationID: "session-signin",
		Method:      http.MethodPost,
		Path:        "/sessions",
		Summary:     "Sign in with email and password",
		Tags:        []string{"session"},
	}, OperationMeta{
		RequiredCapability: PublicCapability,
		AuditAction:        "session.signin",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *SignInInput) (*SignInOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "session-signout",
		Method:      http.MethodDelete,
		Path:        "/sessions/current",
		Summary:     "Sign out the current session",
		Tags:        []string{"session"},
	}, OperationMeta{
		RequiredCapability: "session.self.read",
		AuditAction:        "session.signout",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *SignOutInput) (*SignOutOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "session-current",
		Method:      http.MethodGet,
		Path:        "/sessions/current",
		Summary:     "Get the current session and effective capabilities",
		Tags:        []string{"session"},
	}, OperationMeta{
		RequiredCapability: "session.self.read",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "read-only",
	}, func(ctx context.Context, input *CurrentSessionInput) (*CurrentSessionOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "auth-recovery-request",
		Method:      http.MethodPost,
		Path:        "/auth/recovery/request",
		Summary:     "Request a password-recovery email",
		Tags:        []string{"auth"},
	}, OperationMeta{
		RequiredCapability: PublicCapability,
		AuditAction:        "auth.recovery.request",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *RecoveryRequestInput) (*RecoveryRequestOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "auth-recovery-consume",
		Method:      http.MethodPost,
		Path:        "/auth/recovery/consume",
		Summary:     "Complete recovery and set a new password",
		Tags:        []string{"auth"},
	}, OperationMeta{
		RequiredCapability: PublicCapability,
		AuditAction:        "auth.recovery.consume",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *RecoveryConsumeInput) (*RecoveryConsumeOutput, error) {
		return nil, ErrNotImplemented()
	})

	Register(api, huma.Operation{
		OperationID: "auth-invitation-consume",
		Method:      http.MethodPost,
		Path:        "/auth/invitations/consume",
		Summary:     "Accept an invitation, set initial password, and sign in",
		Tags:        []string{"auth"},
	}, OperationMeta{
		RequiredCapability: PublicCapability,
		AuditAction:        "auth.invitation.consume",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *InvitationConsumeInput) (*InvitationConsumeOutput, error) {
		return nil, ErrNotImplemented()
	})
}
