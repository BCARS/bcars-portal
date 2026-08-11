package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/authn"
	"github.com/bcars/bcars-portal/internal/db"
	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/domain/memberaccess"
)

// These tests drive member onboarding through the surfaces a member actually
// touches: the forgot-password form, the emailed link, the reset form, and the
// ordinary login form. Nothing here calls SetPassword or Provision through a
// shortcut on the way in, because the property under test is that the shipped
// pages reach each other — asserting that each service works in isolation is
// how a flow can pass everywhere and still be unreachable.

const memberEmail = "member@bcars.example"

// memberTestEnv is a handler with a real officer and a real member account
// provisioned the way an officer provisions one: active, member role, and NO
// password (ADR-0012).
type memberTestEnv struct {
	*testEnv
	officerUserID int64
	memberUserID  int64
	access        *memberaccess.Service
}

func setupMemberEnv(t *testing.T) *memberTestEnv {
	t.Helper()

	d, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	require.NoError(t, db.Migrate(d))

	// The officer who does the provisioning. Ordinary password account.
	hash, err := authn.HashPassword("officerpassword12345", nil, authn.DefaultParams())
	require.NoError(t, err)
	res, err := d.Exec(`INSERT INTO users (email, password_hash, password_algo_params, is_active)
		VALUES ('secretary@bcars.example', ?, 'argon2id', 1)`, hash)
	require.NoError(t, err)
	officerID, err := res.LastInsertId()
	require.NoError(t, err)
	_, err = d.Exec(`INSERT INTO user_role_grants (user_id, role_code, granted_by, granted_at, reason)
		VALUES (?, 'secretary', ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'), 'test setup')`, officerID, officerID)
	require.NoError(t, err)

	mailer := testMailer(t)
	h, err := NewHandler(d, HandlerConfig{Mailer: mailer, BaseURL: "http://portal.example"})
	require.NoError(t, err)
	h.testMailer = mailer

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	env := &memberTestEnv{
		testEnv:       &testEnv{h: h, mux: mux},
		officerUserID: officerID,
		access:        memberaccess.NewService(d),
	}

	// Provisioning runs through the real domain service, so the account under
	// test is the one officer provisioning produces rather than a hand-built
	// approximation of it.
	acct, err := env.access.Provision(context.Background(),
		&authz.Principal{UserID: officerID},
		memberaccess.ProvisionParams{Email: memberEmail},
		time.Now().UTC())
	require.NoError(t, err)
	require.True(t, acct.Created)
	env.memberUserID = acct.UserID

	return env
}

// grant gives the member account access to a person and returns the person id.
func (e *memberTestEnv) grant(t *testing.T, name string) int64 {
	t.Helper()
	res, err := e.h.db.Exec(
		`INSERT INTO persons (display_name, sort_name) VALUES (?, ?)`, name, name)
	require.NoError(t, err)
	personID, err := res.LastInsertId()
	require.NoError(t, err)

	_, err = e.access.GrantAccess(context.Background(),
		&authz.Principal{UserID: e.officerUserID}, e.memberUserID,
		memberaccess.GrantParams{PersonID: personID, AccessKind: memberaccess.AccessSelf,
			Reason: "test setup"},
		time.Now().UTC())
	require.NoError(t, err)
	return personID
}

// post submits a form without a session, the way an anonymous browser does.
func (e *memberTestEnv) post(t *testing.T, target string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	return w
}

func (e *memberTestEnv) getAs(t *testing.T, target string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", target, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	return w
}

func cookieFrom(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == authn.DefaultSessionCookieName {
			return c
		}
	}
	t.Fatalf("no session cookie in response")
	return nil
}

// recoveryToken requests recovery through the form and returns the emailed
// token. It reads the mail the handler's own configured sender wrote, so a
// server that never sends fails here.
func (e *memberTestEnv) recoveryToken(t *testing.T, email string) string {
	t.Helper()
	before, err := e.h.mailerForTest().ReadAll()
	require.NoError(t, err)

	w := e.post(t, RouteForgotPassword, url.Values{"email": {email}}, nil)
	require.Equal(t, http.StatusOK, w.Code)

	sent, err := e.h.mailerForTest().ReadAll()
	require.NoError(t, err)
	require.Greater(t, len(sent), len(before), "a recovery mail must be sent for %s", email)

	token := sent[len(sent)-1].Message.Payload["token"]
	require.NotEmpty(t, token)
	return token
}

// TestMemberSetsFirstPasswordThroughRecoveryAndSignsIn is the acceptance
// criterion of bcars-portal-4ux.5, driven end to end: an account an officer
// provisioned with no password becomes an account its member signs into.
func TestMemberSetsFirstPasswordThroughRecoveryAndSignsIn(t *testing.T) {
	e := setupMemberEnv(t)
	e.grant(t, "Dale Rutherford")

	const firstPassword = "membersfirstpassword1"

	// The account exists but nothing can sign into it yet.
	w := e.post(t, RouteLogin, url.Values{"email": {memberEmail}, "password": {firstPassword}}, nil)
	require.Equal(t, http.StatusUnauthorized, w.Code,
		"an account with no password must refuse sign-in, not accept anything")

	// Initial setup IS recovery: the same form, the same mail, the same token.
	token := e.recoveryToken(t, memberEmail)

	w = e.getAs(t, RouteResetPassword+"?token="+url.QueryEscape(token), nil)
	require.Equal(t, http.StatusOK, w.Code, "the emailed link must render the reset form")

	w = e.post(t, RouteResetPassword, url.Values{
		"token": {token}, "password": {firstPassword}, "confirm": {firstPassword},
	}, nil)
	require.Equal(t, http.StatusSeeOther, w.Code, "setting the first password must succeed")
	assert.Equal(t, RouteMemberHome, w.Header().Get("Location"),
		"a member must land somewhere they can actually use, not on the admin dashboard")

	setupCookie := cookieFrom(t, w)
	w = e.getAs(t, RouteMemberHome, setupCookie)
	require.Equal(t, http.StatusOK, w.Code, "the landing must be reachable with the session it just issued")
	assert.Contains(t, w.Body.String(), "Dale Rutherford")

	// Sign out, then back in with the password just set. This is the step that
	// distinguishes a real password account from a link that happened to work
	// once: the token is spent and only the password remains.
	w = e.post(t, "/logout", url.Values{}, setupCookie)
	require.Equal(t, http.StatusSeeOther, w.Code)

	w = e.post(t, RouteResetPassword, url.Values{
		"token": {token}, "password": {firstPassword}, "confirm": {firstPassword},
	}, nil)
	assert.Equal(t, http.StatusBadRequest, w.Code, "a consumed recovery token must not work twice")

	w = e.post(t, RouteLogin, url.Values{"email": {memberEmail}, "password": {firstPassword}}, nil)
	require.Equal(t, http.StatusSeeOther, w.Code, "the password the member set must authenticate")
	assert.Equal(t, RouteMemberHome, w.Header().Get("Location"))

	memberCookie := cookieFrom(t, w)
	w = e.getAs(t, RouteMemberHome, memberCookie)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Dale Rutherford")
	assert.Contains(t, w.Body.String(), memberEmail)

	// A wrong password still fails afterwards, so the account is not simply open.
	w = e.post(t, RouteLogin, url.Values{"email": {memberEmail}, "password": {"not-the-password"}}, nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// TestMemberIsNotSentToTheAdminDashboard covers the disclosure that made a
// member landing necessary: the member role holds session.self.read, so before
// this the dashboard rendered club-wide counts and recent audit events to a
// signed-in member.
func TestMemberIsNotSentToTheAdminDashboard(t *testing.T) {
	e := setupMemberEnv(t)
	e.grant(t, "Dale Rutherford")
	cookie := e.signInMember(t)

	w := e.getAs(t, "/admin/", cookie)
	require.Equal(t, http.StatusSeeOther, w.Code,
		"a member must not be served the officer dashboard")
	assert.Equal(t, RouteMemberHome, w.Header().Get("Location"))

	// And the officer pages themselves stay closed.
	for _, target := range []string{"/admin/members", "/admin/imports", "/admin/treasury"} {
		w := e.getAs(t, target, cookie)
		assert.Equal(t, http.StatusForbidden, w.Code, "%s must refuse a member", target)
	}
}

// TestMemberHomeShowsOnlyActiveGrants proves the landing is grant-bound and
// that revocation lands inside a session that is already open.
func TestMemberHomeShowsOnlyActiveGrants(t *testing.T) {
	e := setupMemberEnv(t)
	dalePersonID := e.grant(t, "Dale Rutherford")
	e.grant(t, "Marguerite Rutherford")

	// A record nobody granted this account.
	_, err := e.h.db.Exec(`INSERT INTO persons (display_name, sort_name) VALUES (?, ?)`,
		"Stranger Nobody", "Stranger Nobody")
	require.NoError(t, err)

	cookie := e.signInMember(t)

	body := e.getAs(t, RouteMemberHome, cookie).Body.String()
	assert.Contains(t, body, "Dale Rutherford")
	assert.Contains(t, body, "Marguerite Rutherford")
	assert.NotContains(t, body, "Stranger Nobody",
		"a record with no grant must not appear, however many other records exist")

	// The officer revokes one grant. The member's session is untouched.
	_, err = e.access.RevokeAccess(context.Background(),
		&authz.Principal{UserID: e.officerUserID}, e.memberUserID,
		memberaccess.RevokeParams{PersonID: dalePersonID, Reason: "no longer authorised"},
		time.Now().UTC())
	require.NoError(t, err)

	body = e.getAs(t, RouteMemberHome, cookie).Body.String()
	assert.NotContains(t, body, "Dale Rutherford",
		"revoking a grant must take effect in an existing session, not at next sign-in")
	assert.Contains(t, body, "Marguerite Rutherford",
		"revoking one grant must not withdraw the others")
}

// TestProvisioningAnOfficerKeepsTheirPasswordAndRoles is the identity rule from
// ADR-0012: one person is one identity, and adding member self-service adds a
// role rather than a second account or a second way to sign in.
func TestProvisioningAnOfficerKeepsTheirPasswordAndRoles(t *testing.T) {
	e := setupMemberEnv(t)

	acct, err := e.access.Provision(context.Background(),
		&authz.Principal{UserID: e.officerUserID},
		memberaccess.ProvisionParams{Email: "secretary@bcars.example"},
		time.Now().UTC())
	require.NoError(t, err)
	assert.False(t, acct.Created, "an existing identity is reused, never duplicated")
	assert.Equal(t, e.officerUserID, acct.UserID)

	var accounts int
	require.NoError(t, e.h.db.QueryRow(
		`SELECT count(*) FROM users WHERE email = 'secretary@bcars.example'`).Scan(&accounts))
	assert.Equal(t, 1, accounts, "one mailbox, one identity")

	// The password they already had still signs them in, with no second flow.
	w := e.post(t, RouteLogin, url.Values{
		"email": {"secretary@bcars.example"}, "password": {"officerpassword12345"},
	}, nil)
	require.Equal(t, http.StatusSeeOther, w.Code, "provisioning must not disturb an existing password")
	assert.Equal(t, "/admin/", w.Header().Get("Location"),
		"an officer who is also a member still lands on the officer surface")

	cookie := cookieFrom(t, w)
	assert.Equal(t, http.StatusOK, e.getAs(t, "/admin/members", cookie).Code,
		"officer capabilities survive gaining the member role")
	assert.Equal(t, http.StatusOK, e.getAs(t, RouteMemberHome, cookie).Code,
		"and the same session reaches the member surface")
}

// TestDashboardWithholdsWhatTheCallerCannotRead checks the other half of the
// disclosure fix: the dashboard summarises only the pages this caller may open.
// acs_coordinator holds member.read but neither audit.read nor import.upload.
func TestDashboardWithholdsWhatTheCallerCannotRead(t *testing.T) {
	e := setupHandlerWithRoles(t, "acs_coordinator")

	w := e.get(t, "/admin/")
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	assert.Contains(t, body, "Total Members", "member.read earns the member counts")
	assert.NotContains(t, body, "Recent Activity",
		"a caller without audit.read must not be shown the audit trail")
	assert.NotContains(t, body, "Import Runs",
		"a caller without import.upload must not be told how many imports exist")
}

// signInMember sets a password through recovery and signs in, returning the
// member's session cookie.
func (e *memberTestEnv) signInMember(t *testing.T) *http.Cookie {
	t.Helper()
	const pw = "membersfirstpassword1"
	token := e.recoveryToken(t, memberEmail)
	w := e.post(t, RouteResetPassword, url.Values{
		"token": {token}, "password": {pw}, "confirm": {pw},
	}, nil)
	require.Equal(t, http.StatusSeeOther, w.Code)
	return cookieFrom(t, w)
}
