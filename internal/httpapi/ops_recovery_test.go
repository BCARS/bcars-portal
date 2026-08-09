package httpapi_test

import (
	"context"
	"net/http"
	"strings"
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

// TestRecoveryRequestRecordsClientIPHash is the regression for
// bcars-portal-fmc.9: requested_ip_hash used to be sha256 of a timestamp, so
// every recovery request recorded a distinct value and nothing that groups by
// source — rate limiting, audit correlation — could ever work.
func TestRecoveryRequestRecordsClientIPHash(t *testing.T) {
	env := setupAuthzTest(t, "member")

	for range 2 {
		resp := env.do(t, http.MethodPost, "/api/v1/auth/recovery/request", nil,
			`{"email":"user@bcars.org"}`)
		require.Equal(t, http.StatusNoContent, resp.StatusCode)
	}

	hashes := scanStrings(t, env, `SELECT COALESCE(requested_ip_hash, '') FROM email_links
		WHERE purpose = 'password_recovery' ORDER BY id`)
	require.Len(t, hashes, 2, "both recovery requests must be recorded")

	assert.NotEmpty(t, hashes[0], "a request from a real address must record a hash")
	assert.Equal(t, hashes[0], hashes[1],
		"two requests from the same client must record the same hash")
	assert.NotContains(t, hashes[0], "127.0.0.1", "the address must not be stored in the clear")
}

// TestSignInRecordsClientIPHash covers the other consumer of the value: the
// session row. A forged X-Forwarded-For must not change it, because no trusted
// proxy header is configured in this assembly.
func TestSignInRecordsClientIPHash(t *testing.T) {
	env := setupAuthzTest(t, "member")

	env.signIn(t)

	req, err := http.NewRequest(http.MethodPost, env.ts.URL+"/api/v1/sessions",
		strings.NewReader(`{"email":"user@bcars.org","password":"correcthorsebatterystaple"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "198.51.100.1")
	resp, err := env.ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	hashes := scanStrings(t, env, `SELECT COALESCE(ip_hash, '') FROM sessions ORDER BY rowid`)
	require.Len(t, hashes, 2)

	assert.NotEmpty(t, hashes[0], "sign-in must record the client address hash")
	assert.Equal(t, hashes[0], hashes[1],
		"a forged X-Forwarded-For must not let one client look like two sources")
}

// scanStrings runs a single-column query against the test database.
func scanStrings(t *testing.T, env *authzEnv, query string) []string {
	t.Helper()
	rows, err := env.db.Query(query)
	require.NoError(t, err)
	defer rows.Close()

	var out []string
	for rows.Next() {
		var v string
		require.NoError(t, rows.Scan(&v))
		out = append(out, v)
	}
	require.NoError(t, rows.Err())
	return out
}
