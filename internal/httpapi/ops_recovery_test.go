package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sessionCookieFrom returns the session cookie a response set, or nil.
func sessionCookieFrom(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == "bcars_session" {
			return c
		}
	}
	return nil
}

// TestRecoveryConsumeSetsSessionCookie is the regression for the defect where
// recovery and invitation consumption created a session server-side and
// returned only a JSON body — leaving the caller signed in on the server but
// anonymous in the browser.
func TestRecoveryConsumeSetsSessionCookie(t *testing.T) {
	env := setupAuthzTest(t, "administrator")

	require.NoError(t, env.links.RequestRecovery(context.Background(), "user@bcars.org", ""))
	sent, err := env.mailer.ReadAll()
	require.NoError(t, err)
	require.Len(t, sent, 1, "recovery mail must be sent")
	token := sent[0].Message.Payload["token"]
	require.NotEmpty(t, token)

	resp := env.do(t, http.MethodPost, "/api/v1/auth/recovery/consume", nil,
		`{"token":"`+token+`","new_password":"brandnewpassword123"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	cookie := sessionCookieFrom(resp)
	require.NotNil(t, cookie, "recovery consumption must return a session cookie")
	assert.True(t, cookie.HttpOnly)
	assert.True(t, cookie.Secure)
	assert.Positive(t, cookie.MaxAge, "a non-positive MaxAge deletes the cookie immediately")

	// The cookie must actually authenticate.
	guarded := env.do(t, http.MethodGet, "/api/v1/audit-events", cookie, "")
	assert.Equal(t, http.StatusOK, guarded.StatusCode,
		"the cookie returned by recovery must resolve to the recovered principal")
}

func TestInvitationConsumeSetsSessionCookie(t *testing.T) {
	env := setupAuthzTest(t)

	token, err := env.links.CreateInvitation(context.Background(), "invited@bcars.org", "administrator", false)
	require.NoError(t, err)

	resp := env.do(t, http.MethodPost, "/api/v1/auth/invitations/consume", nil,
		`{"token":"`+token+`","new_password":"brandnewpassword123"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	cookie := sessionCookieFrom(resp)
	require.NotNil(t, cookie, "invitation consumption must return a session cookie")
	assert.True(t, cookie.HttpOnly)
	assert.Positive(t, cookie.MaxAge)

	guarded := env.do(t, http.MethodGet, "/api/v1/audit-events", cookie, "")
	assert.Equal(t, http.StatusOK, guarded.StatusCode)
}

// TestRecoveryConsumeRejectsWrongPurpose guards the purpose check itself: an
// invitation token must not be usable on the recovery endpoint.
func TestRecoveryConsumeRejectsWrongPurpose(t *testing.T) {
	env := setupAuthzTest(t)

	token, err := env.links.CreateInvitation(context.Background(), "invited@bcars.org", "", false)
	require.NoError(t, err)

	resp := env.do(t, http.MethodPost, "/api/v1/auth/recovery/consume", nil,
		`{"token":"`+token+`","new_password":"brandnewpassword123"}`)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"an invitation token must not be accepted as a recovery token")
}

// TestRecoveryPasswordActuallyChanges proves the flow does the thing it claims
// to: the new password authenticates and the old one stops working.
func TestRecoveryPasswordActuallyChanges(t *testing.T) {
	env := setupAuthzTest(t, "member")

	require.NoError(t, env.links.RequestRecovery(context.Background(), "user@bcars.org", ""))
	sent, err := env.mailer.ReadAll()
	require.NoError(t, err)
	require.Len(t, sent, 1)

	resp := env.do(t, http.MethodPost, "/api/v1/auth/recovery/consume", nil,
		`{"token":"`+sent[0].Message.Payload["token"]+`","new_password":"brandnewpassword123"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	newPw := env.do(t, http.MethodPost, "/api/v1/sessions", nil,
		`{"email":"user@bcars.org","password":"brandnewpassword123"}`)
	assert.Equal(t, http.StatusOK, newPw.StatusCode, "the new password must authenticate")

	oldPw := env.do(t, http.MethodPost, "/api/v1/sessions", nil,
		`{"email":"user@bcars.org","password":"correcthorsebatterystaple"}`)
	assert.Equal(t, http.StatusUnauthorized, oldPw.StatusCode, "the old password must stop working")
}
