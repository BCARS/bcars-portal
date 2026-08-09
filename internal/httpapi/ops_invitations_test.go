package httpapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/authn"
)

// grantCapability gives the signed-in user a single direct capability, for
// building principals that hold an exact set rather than a whole role.
func (e *authzEnv) grantCapability(t *testing.T, codes ...string) {
	t.Helper()
	for _, code := range codes {
		_, err := e.db.Exec(
			`INSERT INTO user_capability_grants (user_id, capability_code, granted_by, granted_at)
			 VALUES (1, ?, 1, datetime('now'))`, code)
		require.NoError(t, err)
	}
}

func TestInvitationCreateRequiresCapability(t *testing.T) {
	env := setupAuthzTest(t, "member") // holds only session.self.read
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodPost, "/api/v1/invitations", cookie,
		`{"email":"new@bcars.org"}`)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"inviting must require user.invite")
}

func TestInvitationCreateWithoutRole(t *testing.T) {
	env := setupAuthzTest(t)
	env.grantCapability(t, "user.invite")
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodPost, "/api/v1/invitations", cookie,
		`{"email":"new@bcars.org"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode, readAll(t, resp))

	var body struct {
		Email    string `json:"email"`
		RoleCode string `json:"role_code"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "new@bcars.org", body.Email)
	assert.Empty(t, body.RoleCode)

	// The invitation is delivered, and the token is not in the response.
	sent, err := env.mailer.ReadAll()
	require.NoError(t, err)
	require.Len(t, sent, 1)
	assert.Equal(t, "invitation", sent[0].Message.TemplateID)
	assert.NotEmpty(t, sent[0].Message.Payload["token"])
}

// TestInvitationDoesNotReturnTheToken guards a deliberate omission: the token
// is a bearer credential for account creation and must travel only by email.
func TestInvitationDoesNotReturnTheToken(t *testing.T) {
	env := setupAuthzTest(t)
	env.grantCapability(t, "user.invite")
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodPost, "/api/v1/invitations", cookie,
		`{"email":"new@bcars.org"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	sent, err := env.mailer.ReadAll()
	require.NoError(t, err)
	require.Len(t, sent, 1)

	assert.NotContains(t, readAll(t, resp), sent[0].Message.Payload["token"],
		"the invitation token must not appear in the API response")
}

// --- escalation guard ---

// TestInvitationRoleRequiresRoleGrant covers the necessary half of the guard.
func TestInvitationRoleRequiresRoleGrant(t *testing.T) {
	env := setupAuthzTest(t)
	env.grantCapability(t, "user.invite")
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodPost, "/api/v1/invitations", cookie,
		`{"email":"new@bcars.org","role_code":"member"}`)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"conferring a role must require role.grant in addition to user.invite")
}

// TestInvitationCannotEscalateBeyondInviter is the sufficient half, and the
// reason this endpoint is not simply "user.invite + role.grant": otherwise
// anyone who could invite could mint an administrator and use that account,
// routing around the entire capability model.
func TestInvitationCannotEscalateBeyondInviter(t *testing.T) {
	env := setupAuthzTest(t)
	// Holds the two invite-related capabilities but nothing an administrator
	// holds beyond them.
	env.grantCapability(t, "user.invite", "role.grant", "session.self.read")
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodPost, "/api/v1/invitations", cookie,
		`{"email":"new@bcars.org","role_code":"administrator"}`)
	require.Equal(t, http.StatusForbidden, resp.StatusCode,
		"an inviter must not confer capabilities they do not hold")
	assert.Contains(t, readAll(t, resp), "capabilities you do not hold")
}

// TestInvitationAllowsConferringHeldRole is the other side: an administrator
// holds everything, so they may confer anything.
func TestInvitationAllowsConferringHeldRole(t *testing.T) {
	env := setupAuthzTest(t, "administrator")
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodPost, "/api/v1/invitations", cookie,
		`{"email":"new@bcars.org","role_code":"treasurer"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode, readAll(t, resp))
}

func TestInvitationRejectsUnknownRole(t *testing.T) {
	env := setupAuthzTest(t, "administrator")
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodPost, "/api/v1/invitations", cookie,
		`{"email":"new@bcars.org","role_code":"supreme-leader"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
}

// TestInvitationRoundTrip closes the loop: an invitation created through the
// API produces a working account with the intended role.
func TestInvitationRoundTrip(t *testing.T) {
	env := setupAuthzTest(t, "administrator")
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodPost, "/api/v1/invitations", cookie,
		`{"email":"treasurer@bcars.org","role_code":"treasurer"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	sent, err := env.mailer.ReadAll()
	require.NoError(t, err)
	require.Len(t, sent, 1)
	token := sent[0].Message.Payload["token"]
	require.NotEmpty(t, token)

	consume := env.do(t, http.MethodPost, "/api/v1/auth/invitations/consume", nil,
		`{"token":"`+token+`","new_password":"brandnewpassword12345"}`)
	require.Equal(t, http.StatusOK, consume.StatusCode, readAll(t, consume))

	var body struct {
		UserID       int64    `json:"user_id"`
		Capabilities []string `json:"capabilities"`
	}
	require.NoError(t, json.NewDecoder(consume.Body).Decode(&body))
	require.NotZero(t, body.UserID)

	caps, err := effectiveCaps(env, body.UserID)
	require.NoError(t, err)
	assert.Contains(t, caps, "notes.read.treasurer", "the invited role must be granted")
}

// readAll returns the response body without consuming it for later readers.
func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body = io.NopCloser(bytes.NewReader(b))
	return string(b)
}

func effectiveCaps(env *authzEnv, userID int64) (map[string]struct{}, error) {
	return (&authn.SQLCapabilityLoader{DB: env.db}).EffectiveCapabilities(userID)
}
