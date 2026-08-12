package smoke

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Member onboarding, driven against the shipped binaries.
//
// The package tests build the web handler directly. That is exactly how the
// portal once shipped a login form that answered 401 for every correct password
// (bcars-portal-pma.12): the assembly, not the handler, was wrong. So the whole
// member journey is repeated here through the real server process — the API to
// provision, the HTML forms to set a password and sign in, and one cookie that
// has to work on both.
const (
	memberSmokeEmail = "household@bcars.example"
	memberSmokePass  = "membersfirstpassword1"
	memberSelfName   = "Dale Rutherford"
	memberOtherName  = "Marguerite Rutherford"
	memberNeverName  = "Unrelated Stranger"
)

func TestMemberOnboardingSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("smoke test builds binaries and starts a server; skipped in -short")
	}

	e := start(t)
	e.requireReady()

	admin := e.consumeInvitation(e.bootstrapAdmin(), adminEmail, adminPass)

	// An officer provisions the account. It gets the member role and no
	// password: an officer must not choose someone else's credential.
	memberUserID := e.provisionMember(admin, memberSmokeEmail)

	self := e.createPerson(admin, memberSelfName, "full")
	other := e.createPerson(admin, memberOtherName, "full")
	e.createPerson(admin, memberNeverName, "full")

	// One shared mailbox, two explicit grants. This is the household case.
	e.grantRecord(admin, memberUserID, self, "self")
	e.grantRecord(admin, memberUserID, other, "delegate")

	// Nothing can sign into the account yet.
	resp := e.postForm("/login", nil, url.Values{
		"email": {memberSmokeEmail}, "password": {memberSmokePass}})
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"an account with no password must refuse sign-in")

	// Initial password setup is the ordinary recovery flow: same form, same
	// mail, same single-use token.
	before := e.mailCount()
	resp = e.postForm("/forgot-password", nil, url.Values{"email": {memberSmokeEmail}})
	require.Equal(t, http.StatusOK, resp.StatusCode, "the forgot-password form must render")
	token := e.waitForMailToken(before, "password_recovery")

	resp = e.do(http.MethodGet, "/reset-password?token="+url.QueryEscape(token), nil, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, "the emailed link must resolve to the reset form")

	resp = e.postForm("/reset-password", nil, url.Values{
		"token": {token}, "password": {memberSmokePass}, "confirm": {memberSmokePass}})
	require.Equal(t, http.StatusSeeOther, resp.StatusCode, "setting the first password must succeed")
	require.Equal(t, "/member/", resp.Header.Get("Location"),
		"a member must land on a member surface, not the officer dashboard")
	require.NotNil(t, sessionCookie(resp), "setting a password must hand back a session")

	// A spent token is spent, even for an account that has just started using
	// passwords.
	resp = e.postForm("/reset-password", nil, url.Values{
		"token": {token}, "password": {"anotherpassword12345"}, "confirm": {"anotherpassword12345"}})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "a consumed token must not work twice")

	// Sign in the ordinary way, with the password the member chose.
	resp = e.postForm("/login", nil, url.Values{
		"email": {memberSmokeEmail}, "password": {memberSmokePass}})
	require.Equal(t, http.StatusSeeOther, resp.StatusCode, "the member's password must authenticate")
	require.Equal(t, "/member/", resp.Header.Get("Location"))
	member := sessionCookie(resp)
	require.NotNil(t, member)

	// The landing shows exactly the granted records and nothing else.
	resp = e.do(http.MethodGet, "/member/", member, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, "the member landing must be reachable")
	body := readBody(resp)
	assert.Contains(t, body, memberSelfName)
	assert.Contains(t, body, memberOtherName)
	assert.NotContains(t, body, memberNeverName,
		"a record nobody granted must not appear on the member landing")

	// The officer surfaces stay shut, on both transports, with the same cookie.
	resp = e.do(http.MethodGet, "/admin/", member, "")
	assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
	assert.Equal(t, "/member/", resp.Header.Get("Location"),
		"a member opening the dashboard is sent to their own landing, not shown club data")

	resp = e.do(http.MethodGet, "/admin/members", member, "")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	resp = e.do(http.MethodGet, "/api/v1/audit-events", member, "")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"one cookie authenticates both surfaces, and the member holds no officer capability on either")

	// The member API agrees with the landing about which records exist, and
	// carries the safe dues summary rather than anything a treasurer sees.
	resp = e.do(http.MethodGet, "/api/v1/me/records", member, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, "member records: %s", readBody(resp))
	records := readBody(resp)
	assert.Contains(t, records, memberSelfName)
	assert.Contains(t, records, memberOtherName)
	assert.NotContains(t, records, memberNeverName)
	for _, officerOnly := range []string{"amount_cents", "treasurer", "audit"} {
		assert.NotContains(t, records, officerOnly,
			"the member read model must not carry %q", officerOnly)
	}

	// A suggestion about someone the member cannot see is accepted, changes
	// nothing, and confers no access.
	resp = e.doWithKey(http.MethodPost, "/api/v1/me/change-requests", member,
		`{"about_name":"Someone Not On My List","summary":"Their call sign is printed wrong.",`+
			`"items":[{"operation":"other"}]}`, "smoke-suggestion-1")
	require.Equal(t, http.StatusCreated, resp.StatusCode, "member suggestion: %s", readBody(resp))
	assert.NotContains(t, readBody(resp), "about_person_id",
		"submission must not name a canonical record back to the submitter")

	resp = e.do(http.MethodGet, "/api/v1/me/change-requests", member, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, readBody(resp), "Someone Not On My List",
		"the member can track their own suggestion")

	// The member UI renders the same record, and its suggestion form files a
	// request through the shipped binary rather than a hand-built handler.
	resp = e.do(http.MethodGet, fmt.Sprintf("/member/records/%d", self), member, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, "member record page: %s", readBody(resp))
	assert.Contains(t, readBody(resp), memberSelfName)

	resp = e.do(http.MethodGet, "/member/suggest", member, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, "the hint form must render")
	assert.NotContains(t, readBody(resp), memberNeverName,
		"the hint form must not list anyone")

	resp = e.postForm("/member/suggest", member, url.Values{
		"kind":       {"other"},
		"about_name": {"Someone Not On My List"},
		"summary":    {"Their call sign is printed wrong."}})
	require.Equal(t, http.StatusSeeOther, resp.StatusCode, "suggestion form: %s", readBody(resp))

	resp = e.do(http.MethodGet, "/member/requests", member, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, readBody(resp), "Their call sign is printed wrong",
		"a member can track the suggestion they just sent")

	// Revoking a grant takes effect inside the session that is already open.
	e.revokeRecord(admin, memberUserID, self)

	resp = e.do(http.MethodGet, "/api/v1/me/records", member, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotContains(t, readBody(resp), memberSelfName,
		"a revoked grant must leave the member API too, not only the landing page")
	resp = e.do(http.MethodGet, "/member/", member, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body = readBody(resp)
	assert.NotContains(t, body, memberSelfName,
		"a revoked grant must disappear from an open session, not at next sign-in")
	assert.Contains(t, body, memberOtherName, "revoking one grant must not withdraw the others")

	// Provisioning an identity that already signs in adds a role. It does not
	// create a second account, and it does not disturb the password.
	e.provisionMember(admin, adminEmail)
	resp = e.postForm("/login", nil, url.Values{"email": {adminEmail}, "password": {adminPass}})
	require.Equal(t, http.StatusSeeOther, resp.StatusCode,
		"provisioning must leave an existing password working")
	assert.Equal(t, "/admin/", resp.Header.Get("Location"),
		"an officer who is also a member still lands on the officer surface")

	adminAfter := sessionCookie(resp)
	require.NotNil(t, adminAfter)
	resp = e.do(http.MethodGet, "/api/v1/audit-events", adminAfter, "")
	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"gaining the member role must not cost an officer their capabilities")

	// No account was duplicated for that address.
	ids := e.queryStrings(fmt.Sprintf(`SELECT id FROM users WHERE email = '%s'`, adminEmail))
	assert.Len(t, ids, 1, "one mailbox is one identity")
}

// --- flow steps ---

func (e *env) provisionMember(admin *http.Cookie, email string) int64 {
	e.t.Helper()
	resp := e.do(http.MethodPost, "/api/v1/member-accounts", admin,
		fmt.Sprintf(`{"email":%q}`, email))
	require.Equal(e.t, http.StatusOK, resp.StatusCode, "provision %s: %s", email, readBody(resp))

	var body struct {
		UserID int64 `json:"user_id"`
	}
	require.NoError(e.t, json.NewDecoder(resp.Body).Decode(&body))
	require.NotZero(e.t, body.UserID)
	return body.UserID
}

func (e *env) createPerson(admin *http.Cookie, name, baseType string) int64 {
	e.t.Helper()
	resp := e.do(http.MethodPost, "/api/v1/members", admin,
		fmt.Sprintf(`{"display_name":%q,"sort_name":%q,"base_type":%q}`, name, name, baseType))
	require.Equal(e.t, http.StatusCreated, resp.StatusCode, "create %s: %s", name, readBody(resp))

	var body struct {
		ID int64 `json:"id"`
	}
	require.NoError(e.t, json.NewDecoder(resp.Body).Decode(&body))
	require.NotZero(e.t, body.ID)
	return body.ID
}

func (e *env) grantRecord(admin *http.Cookie, userID, personID int64, kind string) {
	e.t.Helper()
	resp := e.do(http.MethodPost,
		fmt.Sprintf("/api/v1/member-accounts/%d/records", userID), admin,
		fmt.Sprintf(`{"person_id":%d,"access_kind":%q,"reason":"smoke test"}`, personID, kind))
	require.Equal(e.t, http.StatusOK, resp.StatusCode, "grant record: %s", readBody(resp))
}

func (e *env) revokeRecord(admin *http.Cookie, userID, personID int64) {
	e.t.Helper()
	resp := e.do(http.MethodPost,
		fmt.Sprintf("/api/v1/member-accounts/%d/records/revoke", userID), admin,
		fmt.Sprintf(`{"person_id":%d,"reason":"smoke test"}`, personID))
	require.Equal(e.t, http.StatusOK, resp.StatusCode, "revoke record: %s", readBody(resp))
}

// doWithKey issues a JSON request carrying an Idempotency-Key, which the
// submission endpoint requires.
func (e *env) doWithKey(method, path string, cookie *http.Cookie, body, key string) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest(method, e.baseURL+path, strings.NewReader(body))
	require.NoError(e.t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("X-Confirm", "true")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := e.client.Do(req)
	require.NoError(e.t, err)

	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.NoError(e.t, err)
	resp.Body = &replayBody{data: raw}
	return resp
}

// postForm submits an HTML form the way a browser does. The API helper sends
// JSON, and a member never touches the API: the forms are the surface under
// test here.
func (e *env) postForm(path string, cookie *http.Cookie, form url.Values) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.baseURL+path, strings.NewReader(form.Encode()))
	require.NoError(e.t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := e.client.Do(req)
	require.NoError(e.t, err)

	// Buffered and closed the same way e.do does it, so a body can be read
	// more than once and no connection is left hanging.
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.NoError(e.t, err)
	resp.Body = &replayBody{data: raw}
	return resp
}
