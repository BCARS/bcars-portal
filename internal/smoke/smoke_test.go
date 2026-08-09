// Package smoke contains the production-assembly smoke test.
//
// Every other test in this repository exercises a handler, a domain service,
// or a hand-assembled router. None of them start what actually ships. That is
// how the portal reached a state where all seven quality gates passed while
// the production API could not resolve a signed-in principal at all
// (bcars-portal-fmc.2) and a fresh installation had no route to a working
// administrator (bcars-portal-fmc.3).
//
// This test builds both real binaries and drives them as processes over HTTP,
// so the thing under test is the deployed artifact rather than a reconstruction
// of it.
package smoke

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/mail"
)

const (
	adminEmail   = "admin@bcars.example"
	adminPass    = "correcthorsebatterystaple"
	officerEmail = "officer@bcars.example"
	officerPass  = "officerpassword12345"
	newPass      = "brandnewpassword12345"
)

type env struct {
	t       *testing.T
	baseURL string
	dbPath  string
	mailDir string
	client  *http.Client
	mailer  *mail.FilelogSender
}

func TestProductionAssemblySmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("smoke test builds binaries and starts a server; skipped in -short")
	}

	e := start(t)

	// 1. Readiness. This also asserts the binary's ExpectedMigrationVersion
	//    matches the schema the migrations actually produce — a mismatch here
	//    means a deployment would never report healthy.
	e.requireReady()

	// 2. Bootstrap produces a usable administrator.
	adminCookie := e.consumeInvitation(e.bootstrapAdmin(), adminEmail, adminPass)

	caps := e.currentCapabilities(adminCookie)
	for _, code := range []string{"role.grant", "audit.read", "member.read", "import.commit"} {
		require.Contains(t, caps, code,
			"the bootstrapped administrator must hold %s; a fresh install has no other route to one", code)
	}

	// 3. A capability-guarded read succeeds for that administrator.
	resp := e.do(http.MethodGet, "/api/v1/audit-events", adminCookie, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, "administrator must reach a guarded read")

	// 4. The same read is denied for an under-privileged principal.
	officerCookie := e.consumeInvitation(e.inviteWithoutRole(adminCookie, officerEmail), officerEmail, officerPass)
	resp = e.do(http.MethodGet, "/api/v1/audit-events", officerCookie, "")
	require.Equal(t, http.StatusForbidden, resp.StatusCode,
		"a principal without audit.read must be denied, not merely unauthenticated")

	// Anonymous is a distinct outcome from under-privileged.
	resp = e.do(http.MethodGet, "/api/v1/audit-events", nil, "")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// 5. Password recovery round trip, end to end through the running server.
	e.recoveryRoundTrip(officerEmail)
}

// --- flow steps ---

// bootstrapAdmin runs the real portalctl binary and returns the invitation token.
func (e *env) bootstrapAdmin() string {
	e.t.Helper()
	out := e.run(binPath(e.t, "portalctl"),
		"bootstrap-admin",
		"--db", e.dbPath,
		"--email", adminEmail,
		"--base-url", e.baseURL,
	)
	require.Contains(e.t, out, "administrator", "bootstrap must state the role it confers")
	return tokenFromURL(e.t, out)
}

// inviteWithoutRole invites an ordinary user through the API, using the
// administrator's own session. Every step of onboarding a second officer now
// runs through supported routes — the smoke test previously had to write an
// email_links row directly because no endpoint existed (bcars-portal-fmc.17).
func (e *env) inviteWithoutRole(adminCookie *http.Cookie, email string) string {
	e.t.Helper()

	before := e.mailCount()
	resp := e.do(http.MethodPost, "/api/v1/invitations", adminCookie,
		fmt.Sprintf(`{"email":%q}`, email))
	require.Equal(e.t, http.StatusCreated, resp.StatusCode,
		"inviting a second user must be possible through the API: %s", readBody(resp))

	return e.waitForMailToken(before, "invitation")
}

// consumeInvitation accepts an invitation and returns the session cookie it
// hands back. A missing cookie is a failure: an endpoint that creates a
// session and does not return one leaves the caller anonymous in the browser.
func (e *env) consumeInvitation(token, email, password string) *http.Cookie {
	e.t.Helper()

	resp := e.do(http.MethodPost, "/api/v1/auth/invitations/consume", nil,
		fmt.Sprintf(`{"token":%q,"new_password":%q}`, token, password))
	require.Equal(e.t, http.StatusOK, resp.StatusCode,
		"invitation consumption failed for %s: %s", email, readBody(resp))

	cookie := sessionCookie(resp)
	require.NotNil(e.t, cookie, "consumption must return a session cookie")
	return cookie
}

func (e *env) recoveryRoundTrip(email string) {
	e.t.Helper()

	before := e.mailCount()
	resp := e.do(http.MethodPost, "/api/v1/auth/recovery/request", nil,
		fmt.Sprintf(`{"email":%q}`, email))
	require.Equal(e.t, http.StatusNoContent, resp.StatusCode, "recovery request: %s", readBody(resp))

	token := e.waitForMailToken(before, "password_recovery")
	resp = e.do(http.MethodPost, "/api/v1/auth/recovery/consume", nil,
		fmt.Sprintf(`{"token":%q,"new_password":%q}`, token, newPass))
	require.Equal(e.t, http.StatusOK, resp.StatusCode, "recovery consume: %s", readBody(resp))
	require.NotNil(e.t, sessionCookie(resp), "recovery must return a session cookie")

	// The new password authenticates and the old one does not.
	resp = e.do(http.MethodPost, "/api/v1/sessions", nil,
		fmt.Sprintf(`{"email":%q,"password":%q}`, email, newPass))
	assert.Equal(e.t, http.StatusOK, resp.StatusCode, "the recovered password must work")

	resp = e.do(http.MethodPost, "/api/v1/sessions", nil,
		fmt.Sprintf(`{"email":%q,"password":%q}`, email, officerPass))
	assert.Equal(e.t, http.StatusUnauthorized, resp.StatusCode, "the old password must stop working")
}

// waitForMailToken polls the filelog mail directory for the newest message of
// a given template. Mail is written by the server process, so it is not
// visible the instant the response returns.
func (e *env) waitForMailToken(before int, templateID string) string {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		sent, err := e.mailer.ReadAll()
		if err == nil && len(sent) > before {
			for i := len(sent) - 1; i >= 0; i-- {
				if sent[i].Message.TemplateID != templateID {
					continue
				}
				if tok := sent[i].Message.Payload["token"]; tok != "" {
					return tok
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.t.Fatalf("no %s mail was delivered; the mail sender is not wired", templateID)
	return ""
}

func (e *env) currentCapabilities(cookie *http.Cookie) []string {
	e.t.Helper()
	resp := e.do(http.MethodGet, "/api/v1/sessions/current", cookie, "")
	require.Equal(e.t, http.StatusOK, resp.StatusCode, "session lookup: %s", readBody(resp))

	var body struct {
		Capabilities []string `json:"capabilities"`
	}
	require.NoError(e.t, json.NewDecoder(resp.Body).Decode(&body))
	return body.Capabilities
}
