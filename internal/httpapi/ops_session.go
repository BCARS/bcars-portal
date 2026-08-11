package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/audit"
	"github.com/bcars/bcars-portal/internal/authn"
)

// --- Session / auth types ---

type signInBody struct {
	Email    string `json:"email" format:"email" doc:"Account email address. Members and officers use the same sign-in."`
	Password string `json:"password" minLength:"1" doc:"Account password."`
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
	SetCookie string `header:"Set-Cookie"`
	Body      sessionInfo
}

type SignOutInput struct{}
type SignOutOutput struct {
	SetCookie string `header:"Set-Cookie"`
}

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
	SetCookie string `header:"Set-Cookie"`
	Body      sessionInfo
}

type InvitationConsumeBody struct {
	Token       string `json:"token" minLength:"1"`
	NewPassword string `json:"new_password" minLength:"12"`
}
type InvitationConsumeInput struct{ Body InvitationConsumeBody }
type InvitationConsumeOutput struct {
	SetCookie string `header:"Set-Cookie"`
	Body      sessionInfo
}

// cookieConfig is the shared source of session-cookie attributes for every
// endpoint that creates or clears a session, so sign-in, recovery consumption,
// invitation consumption and sign-out cannot disagree — and so that an
// endpoint which creates a session cannot forget to hand one back, which is
// how recovery and invitation left the caller signed in on the server but
// anonymous in the browser. The same configuration type backs the admin UI.
func (d Deps) cookieConfig() authn.SessionCookieConfig {
	return authn.SessionCookieConfig{
		Name:          d.CookieName,
		AllowInsecure: d.AllowInsecureCookies,
	}
}

// sessionCookie builds the cookie handed back by a session-creating endpoint.
func (d Deps) sessionCookie(sessionID string, expiresAt time.Time) *http.Cookie {
	return d.cookieConfig().Set(sessionID, expiresAt)
}

// RegisterSessions registers all session and auth endpoints.
func RegisterSessions(api huma.API, deps Deps) {
	Register(api, huma.Operation{
		OperationID: "session-signin",
		Method:      http.MethodPost,
		Path:        "/sessions",
		Summary:     "Sign in with email and password",
		Tags:        []string{"session"},
	}, OperationMeta{
		RequiredCapability: PublicCapability,
		AuditAction:        "session.signin",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *SignInInput) (*SignInOutput, error) {
		if deps.AuthService == nil {
			return nil, ErrNotImplemented()
		}

		// Keyed hash of the client address, resolved by ClientIPMiddleware.
		// Empty when no address is available; stored as NULL rather than as a
		// value that would group nothing.
		ipHash := ClientIPHashFrom(ctx)

		sessionID, err := deps.AuthService.SignIn(
			input.Body.Email, input.Body.Password, ipHash, "api",
		)
		if err != nil {
			if errors.Is(err, authn.ErrInvalidCredentials) ||
				errors.Is(err, authn.ErrAccountInactive) {
				return nil, huma.NewError(http.StatusUnauthorized, "invalid email or password")
			}
			if errors.Is(err, authn.ErrAccountLocked) {
				return nil, huma.NewError(http.StatusTooManyRequests, "account temporarily locked")
			}
			return nil, huma.NewError(http.StatusInternalServerError, "sign-in failed")
		}

		// Get session details for the response.
		sess, err := deps.SessionStore.Get(sessionID)
		if err != nil {
			return nil, huma.NewError(http.StatusInternalServerError, "session created but retrieval failed")
		}

		// Load capabilities for the session user.
		capLoader := &authn.SQLCapabilityLoader{DB: deps.DB}
		caps, err := capLoader.EffectiveCapabilities(sess.UserID)
		if err != nil {
			return nil, huma.NewError(http.StatusInternalServerError, "failed to load capabilities")
		}

		return &SignInOutput{
			SetCookie: deps.sessionCookie(sessionID, sess.ExpiresAt).String(),
			Body: sessionInfo{
				UserID:       sess.UserID,
				Email:        input.Body.Email,
				Capabilities: sortedCaps(caps),
				ExpiresAt:    sess.ExpiresAt.Format(time.RFC3339),
			},
		}, nil
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
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *SignOutInput) (*SignOutOutput, error) {
		if deps.AuthService == nil {
			return nil, ErrNotImplemented()
		}

		principal := authn.PrincipalFrom(ctx)
		if principal == nil {
			return nil, huma.NewError(http.StatusUnauthorized, "not authenticated")
		}

		if err := deps.AuthService.SignOut(principal.SessionID); err != nil {
			return nil, huma.NewError(http.StatusInternalServerError, "sign-out failed")
		}

		// Clear the session cookie.
		cookie := deps.cookieConfig().Clear()

		return &SignOutOutput{
			SetCookie: cookie.String(),
		}, nil
	})

	Register(api, huma.Operation{
		OperationID: "session-current",
		Method:      http.MethodGet,
		Path:        "/sessions/current",
		Summary:     "Get the current session and effective capabilities",
		Tags:        []string{"session"},
	}, OperationMeta{
		RequiredCapability: "session.self.read",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "read-only",
	}, func(ctx context.Context, input *CurrentSessionInput) (*CurrentSessionOutput, error) {
		if deps.SessionStore == nil {
			return nil, ErrNotImplemented()
		}

		principal := authn.PrincipalFrom(ctx)
		if principal == nil {
			return nil, huma.NewError(http.StatusUnauthorized, "not authenticated")
		}

		sess, err := deps.SessionStore.Get(principal.SessionID)
		if err != nil {
			if errors.Is(err, authn.ErrSessionNotFound) || errors.Is(err, authn.ErrSessionRevoked) {
				return nil, huma.NewError(http.StatusUnauthorized, "session expired or revoked")
			}
			return nil, huma.NewError(http.StatusInternalServerError, "failed to retrieve session")
		}

		return &CurrentSessionOutput{
			Body: sessionInfo{
				UserID:       principal.UserID,
				Email:        principal.Email,
				Capabilities: sortedCaps(principal.Capabilities),
				ExpiresAt:    sess.ExpiresAt.Format(time.RFC3339),
			},
		}, nil
	})

	// --- Recovery and invitation endpoints ---

	Register(api, huma.Operation{
		OperationID: "auth-recovery-request",
		Method:      http.MethodPost,
		Path:        "/auth/recovery/request",
		Summary:     "Request a password-recovery email",
		Tags:        []string{"auth"},
	}, OperationMeta{
		RequiredCapability: PublicCapability,
		AuditAction:        "auth.recovery.request",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *RecoveryRequestInput) (*RecoveryRequestOutput, error) {
		if deps.EmailLinkService == nil {
			return nil, ErrNotImplemented()
		}
		// Always return success to prevent email enumeration.
		// The IP hash is what per-source recovery-abuse limiting groups on, so
		// it has to be the real client address rather than a placeholder.
		err := deps.EmailLinkService.RequestRecovery(ctx, input.Body.Email, ClientIPHashFrom(ctx))
		if errors.Is(err, authn.ErrRateLimited) {
			// 429 for everyone at the bound, known address or not. The limiter
			// decided without looking the address up, and this response must
			// not undo that: a 429 here and a 204 there would answer the
			// question the uniform success response exists to hide.
			return nil, huma.Error429TooManyRequests("too many requests; try again later")
		}
		return &RecoveryRequestOutput{}, nil
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
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *RecoveryConsumeInput) (*RecoveryConsumeOutput, error) {
		if deps.EmailLinkService == nil || deps.AuthService == nil || deps.SessionStore == nil {
			return nil, ErrNotImplemented()
		}

		link, err := deps.EmailLinkService.ConsumeLink(input.Body.Token)
		if err != nil {
			return nil, huma.NewError(http.StatusBadRequest, "invalid or expired recovery link")
		}
		if link.Purpose != authn.PurposeRecovery || link.UserID == nil {
			return nil, huma.NewError(http.StatusBadRequest, "invalid recovery link")
		}

		if err := deps.AuthService.SetPassword(ctx, *link.UserID, input.Body.NewPassword); err != nil {
			return nil, huma.NewError(http.StatusInternalServerError, "failed to set password")
		}

		sessionID, err := deps.AuthService.LoginByUserID(ctx, *link.UserID, deps.SessionStore)
		if err != nil {
			return nil, huma.NewError(http.StatusInternalServerError, "password reset but login failed")
		}
		// Not ignorable: a zero session yields a cookie with a negative
		// MaxAge, which deletes itself the moment the browser stores it.
		sess, err := deps.SessionStore.Get(sessionID)
		if err != nil {
			return nil, huma.NewError(http.StatusInternalServerError, "session created but retrieval failed")
		}

		return &RecoveryConsumeOutput{
			SetCookie: deps.sessionCookie(sessionID, sess.ExpiresAt).String(),
			Body: sessionInfo{
				UserID:    *link.UserID,
				Email:     link.Email,
				ExpiresAt: sess.ExpiresAt.Format(time.RFC3339),
			},
		}, nil
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
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *InvitationConsumeInput) (*InvitationConsumeOutput, error) {
		if deps.EmailLinkService == nil || deps.AuthService == nil || deps.SessionStore == nil {
			return nil, ErrNotImplemented()
		}

		link, err := deps.EmailLinkService.ConsumeLink(input.Body.Token)
		if err != nil {
			return nil, huma.NewError(http.StatusBadRequest, "invalid or expired invitation link")
		}
		if link.Purpose != authn.PurposeInvitation {
			return nil, huma.NewError(http.StatusBadRequest, "invalid invitation link")
		}

		// Create the account and grant the role the invitation carried, in
		// one transaction. An invitation that confers no role produces an
		// ordinary account.
		userID, err := deps.AuthService.CreateUserFromInvitation(ctx, link, input.Body.NewPassword)
		if err != nil {
			return nil, huma.NewError(http.StatusConflict, "account already exists for this email")
		}
		if link.IntendedRoleCode != "" {
			audit.StampResource(ctx, "user", userID)
		}

		sessionID, err := deps.AuthService.LoginByUserID(ctx, userID, deps.SessionStore)
		if err != nil {
			return nil, huma.NewError(http.StatusInternalServerError, "account created but login failed")
		}
		// Not ignorable: a zero session yields a cookie with a negative
		// MaxAge, which deletes itself the moment the browser stores it.
		sess, err := deps.SessionStore.Get(sessionID)
		if err != nil {
			return nil, huma.NewError(http.StatusInternalServerError, "session created but retrieval failed")
		}

		return &InvitationConsumeOutput{
			SetCookie: deps.sessionCookie(sessionID, sess.ExpiresAt).String(),
			Body: sessionInfo{
				UserID:    userID,
				Email:     link.Email,
				ExpiresAt: sess.ExpiresAt.Format(time.RFC3339),
			},
		}, nil
	})
}

// sortedCaps converts a capability set to a sorted slice.
func sortedCaps(caps map[string]struct{}) []string {
	out := make([]string, 0, len(caps))
	for k := range caps {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
