package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/httpapi"
)

// Officer provisioning of member accounts and record access
// (bcars-portal-4ux.4).
//
// The rule under test is ADR-0010's: access comes from an explicit officer
// decision, never from a matching contact email or a family relationship.

type apiAccount struct {
	UserID  int64  `json:"user_id"`
	Email   string `json:"email"`
	Created bool   `json:"created"`
	Grants  []apiGrant
}

type apiGrant struct {
	ID              int64  `json:"id"`
	UserID          int64  `json:"user_id"`
	PersonID        int64  `json:"person_id"`
	DisplayName     string `json:"display_name"`
	AccessKind      string `json:"access_kind"`
	Reason          string `json:"reason"`
	Active          bool   `json:"active"`
	GrantedByUserID int64  `json:"granted_by_user_id"`
	RevokedByUserID int64  `json:"revoked_by_user_id"`
	RevokeReason    string `json:"revoke_reason"`
	Version         int64  `json:"version"`
}

func provision(t *testing.T, env *authzEnv, cookie *http.Cookie, email string) *http.Response {
	t.Helper()
	return doWithHeaders(t, env, http.MethodPost, "/api/v1/member-accounts", cookie,
		fmt.Sprintf(`{"email":%q}`, email), nil)
}

func grantRecord(t *testing.T, env *authzEnv, cookie *http.Cookie, userID, personID int64, kind string) *http.Response {
	t.Helper()
	body := fmt.Sprintf(`{"person_id":%d}`, personID)
	if kind != "" {
		body = fmt.Sprintf(`{"person_id":%d,"access_kind":%q}`, personID, kind)
	}
	return doWithHeaders(t, env, http.MethodPost,
		fmt.Sprintf("/api/v1/member-accounts/%d/records", userID), cookie, body, nil)
}

func revokeRecord(t *testing.T, env *authzEnv, cookie *http.Cookie, userID, personID int64, reason string) *http.Response {
	t.Helper()
	return doWithHeaders(t, env, http.MethodPost,
		fmt.Sprintf("/api/v1/member-accounts/%d/records/revoke", userID), cookie,
		fmt.Sprintf(`{"person_id":%d,"reason":%q}`, personID, reason), nil)
}

func decodeAccount(t *testing.T, resp *http.Response) apiAccount {
	t.Helper()
	var a apiAccount
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&a))
	return a
}

func decodeGrant(t *testing.T, resp *http.Response) apiGrant {
	t.Helper()
	var g apiGrant
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&g))
	return g
}

// TestProvisionCreatesAnAccountWithNoPassword proves what provisioning does
// and, more importantly, what it does not. An officer must not choose someone
// else's credential, so the account arrives with none and its member sets the
// first one through recovery (ADR-0012).
func TestProvisionCreatesAnAccountWithNoPassword(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)

	resp := provision(t, env, cookie, "  NewMember@Example.Test  ")
	require.Equal(t, http.StatusOK, resp.StatusCode, readAll(t, resp))
	acct := decodeAccount(t, resp)

	assert.True(t, acct.Created)
	assert.Equal(t, "newmember@example.test", acct.Email, "the address is normalized")
	assert.Empty(t, acct.Grants, "a new account reaches nothing until an officer grants it")

	var passwordHash, algoParams, personID any
	require.NoError(t, env.db.QueryRow(
		`SELECT password_hash, password_algo_params, person_id FROM users WHERE id = ?`,
		acct.UserID).Scan(&passwordHash, &algoParams, &personID))
	assert.Nil(t, passwordHash, "a member account stores no password")
	assert.Nil(t, algoParams)
	assert.Nil(t, personID,
		"users.person_id is the legacy officer link and must not become an access authority")

	rows, err := env.db.Query(
		`SELECT role_code FROM user_role_grants WHERE user_id = ? AND revoked_at IS NULL`, acct.UserID)
	require.NoError(t, err)
	defer rows.Close()
	var roles []string
	for rows.Next() {
		var r string
		require.NoError(t, rows.Scan(&r))
		roles = append(roles, r)
	}
	assert.Equal(t, []string{"member"}, roles, "provisioning assigns only the member role")
}

// TestProvisionReusesOneAccountForASharedMailbox is the household case.
func TestProvisionReusesOneAccountForASharedMailbox(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)

	first := decodeAccount(t, provision(t, env, cookie, "household@example.test"))
	second := decodeAccount(t, provision(t, env, cookie, "HOUSEHOLD@example.test"))

	assert.True(t, first.Created)
	assert.False(t, second.Created, "an existing account for the same mailbox is reused")
	assert.Equal(t, first.UserID, second.UserID,
		"two spellings of one address must not become two accounts")

	var users int
	require.NoError(t, env.db.QueryRow(
		`SELECT count(*) FROM users WHERE email = 'household@example.test'`).Scan(&users))
	assert.Equal(t, 1, users)

	var roleGrants int
	require.NoError(t, env.db.QueryRow(
		`SELECT count(*) FROM user_role_grants WHERE user_id = ? AND role_code = 'member'`,
		first.UserID).Scan(&roleGrants))
	assert.Equal(t, 1, roleGrants, "reuse must not stack a second member role grant")
}

// TestOneAccountReachesSeveralRecords is the household mailbox working.
func TestOneAccountReachesSeveralRecords(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)

	acct := decodeAccount(t, provision(t, env, cookie, "household@example.test"))
	one := seedPersonForRequest(t, env, "Household One")
	two := seedPersonForRequest(t, env, "Household Two")
	stranger := seedPersonForRequest(t, env, "Not In This House")

	require.Equal(t, http.StatusOK, grantRecord(t, env, cookie, acct.UserID, one, "self").StatusCode)
	require.Equal(t, http.StatusOK, grantRecord(t, env, cookie, acct.UserID, two, "delegate").StatusCode)

	resp := env.do(t, http.MethodGet,
		fmt.Sprintf("/api/v1/member-accounts/%d/records", acct.UserID), cookie, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var grants []apiGrant
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&grants))

	reachable := map[int64]string{}
	for _, g := range grants {
		if g.Active {
			reachable[g.PersonID] = g.AccessKind
		}
	}
	assert.Equal(t, "self", reachable[one])
	assert.Equal(t, "delegate", reachable[two])
	assert.NotContains(t, reachable, stranger, "an ungranted record stays unreachable")
}

// TestDuplicateActiveGrantIsRefused proves the pair is unique while active.
func TestDuplicateActiveGrantIsRefused(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	acct := decodeAccount(t, provision(t, env, cookie, "dupe@example.test"))
	person := seedPersonForRequest(t, env, "Only Once")

	require.Equal(t, http.StatusOK, grantRecord(t, env, cookie, acct.UserID, person, "").StatusCode)
	resp := grantRecord(t, env, cookie, acct.UserID, person, "")
	assert.Equal(t, http.StatusConflict, resp.StatusCode, "a second active grant must be refused")

	var active int
	require.NoError(t, env.db.QueryRow(
		`SELECT count(*) FROM member_access_grants WHERE user_id = ? AND revoked_at IS NULL`,
		acct.UserID).Scan(&active))
	assert.Equal(t, 1, active)
}

// TestRevokeThenRegrantKeepsTheHistory proves re-granting is a new row rather
// than an undelete, so who could reach a record and when stays answerable.
func TestRevokeThenRegrantKeepsTheHistory(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	acct := decodeAccount(t, provision(t, env, cookie, "regrant@example.test"))
	person := seedPersonForRequest(t, env, "Comes And Goes")

	first := decodeGrant(t, grantRecord(t, env, cookie, acct.UserID, person, ""))

	revoked := decodeGrant(t, revokeRecord(t, env, cookie, acct.UserID, person, "Left the household."))
	assert.False(t, revoked.Active)
	assert.NotZero(t, revoked.RevokedByUserID, "a revocation records who did it")
	assert.Equal(t, "Left the household.", revoked.RevokeReason)

	// Revoking again has nothing to revoke.
	assert.Equal(t, http.StatusNotFound,
		revokeRecord(t, env, cookie, acct.UserID, person, "again").StatusCode)

	second := decodeGrant(t, grantRecord(t, env, cookie, acct.UserID, person, ""))
	assert.NotEqual(t, first.ID, second.ID, "re-granting creates a new row")
	assert.True(t, second.Active)

	var total int
	require.NoError(t, env.db.QueryRow(
		`SELECT count(*) FROM member_access_grants WHERE user_id = ? AND person_id = ?`,
		acct.UserID, person).Scan(&total))
	assert.Equal(t, 2, total, "the revoked grant stays as history")
}

// TestRevocationTakesEffectInsideAnOpenSession is the property the design
// promises: authorization loads grants per request, so revoking does not wait
// for the member to sign out.
//
// The directory is the observable surface: eligibility is recomputed on every
// call, so a revoked member loses it immediately.
func TestRevocationTakesEffectInsideAnOpenSession(t *testing.T) {
	env := setupAuthzTest(t, "member")
	cookie := env.signIn(t) // the session is established once, here

	person := dirMember(t, env, "Session Member", "W3SESS", "full", "approved")
	grantAccess(t, env, person)
	dirMember(t, env, "Someone Else", "W3ELSE2", "full", "approved")

	before, _ := readDirectory(t, env, cookie, "")
	require.Equal(t, http.StatusOK, before.StatusCode,
		"the member can browse while the grant is active")

	// An officer revokes, without touching the member's session.
	_, err := env.db.Exec(`
		UPDATE member_access_grants
		   SET revoked_at = '2026-08-10T12:00:00.000Z', version = version + 1
		 WHERE user_id = 1 AND person_id = ?`, person)
	require.NoError(t, err)

	after, _ := readDirectory(t, env, cookie, "")
	assert.Equal(t, http.StatusNotFound, after.StatusCode,
		"the very next request in the same session must be refused")
}

// TestProvisioningAddsMemberSelfServiceToAnOfficer is the owner's model: an
// individual is a member, and some members are also officers. A role adds
// permissions to one identity rather than defining a separate kind of login, so
// an officer's address must not need a second mailbox to get member
// self-service.
func TestProvisioningAddsMemberSelfServiceToAnOfficer(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)

	// user 1 is the signed-in treasurer.
	var officerEmail string
	require.NoError(t, env.db.QueryRow(`SELECT email FROM users WHERE id = 1`).Scan(&officerEmail))
	var passwordBefore any
	require.NoError(t, env.db.QueryRow(`SELECT password_hash FROM users WHERE id = 1`).Scan(&passwordBefore))
	require.NotNil(t, passwordBefore, "the officer signs in with a password")

	resp := provision(t, env, cookie, officerEmail)
	require.Equal(t, http.StatusOK, resp.StatusCode, readAll(t, resp))
	acct := decodeAccount(t, resp)

	assert.False(t, acct.Created, "the officer's existing identity is reused")
	assert.Equal(t, int64(1), acct.UserID, "one person is one identity")

	rows, err := env.db.Query(
		`SELECT role_code FROM user_role_grants WHERE user_id = 1 AND revoked_at IS NULL ORDER BY role_code`)
	require.NoError(t, err)
	defer rows.Close()
	var roles []string
	for rows.Next() {
		var r string
		require.NoError(t, rows.Scan(&r))
		roles = append(roles, r)
	}
	assert.Equal(t, []string{"member", "treasurer"}, roles,
		"the member role is added; the treasurer role is not removed")

	var passwordAfter any
	require.NoError(t, env.db.QueryRow(`SELECT password_hash FROM users WHERE id = 1`).Scan(&passwordAfter))
	assert.Equal(t, passwordBefore, passwordAfter,
		"provisioning must never touch an existing password")
}

// TestProvisioningAnOfficerTwiceIsIdempotent proves reuse does not stack roles.
func TestProvisioningAnOfficerTwiceIsIdempotent(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)

	var officerEmail string
	require.NoError(t, env.db.QueryRow(`SELECT email FROM users WHERE id = 1`).Scan(&officerEmail))

	require.Equal(t, http.StatusOK, provision(t, env, cookie, officerEmail).StatusCode)
	require.Equal(t, http.StatusOK, provision(t, env, cookie, officerEmail).StatusCode)

	var memberGrants int
	require.NoError(t, env.db.QueryRow(
		`SELECT count(*) FROM user_role_grants WHERE user_id = 1 AND role_code = 'member'`).Scan(&memberGrants))
	assert.Equal(t, 1, memberGrants, "provisioning twice must not stack the member role")
}

// TestContactEmailAloneCreatesNoAccess is ADR-0010's central rule, asserted
// rather than assumed.
func TestContactEmailAloneCreatesNoAccess(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)

	person := seedPersonForRequest(t, env, "Has An Email")
	_, err := env.db.Exec(`
		INSERT INTO contact_methods (person_id, kind, value_raw, value_norm)
		VALUES (?, 'email', 'shared@example.test', 'shared@example.test')`, person)
	require.NoError(t, err)

	// Provisioning an account at that very address grants nothing.
	acct := decodeAccount(t, provision(t, env, cookie, "shared@example.test"))
	assert.Empty(t, acct.Grants,
		"matching a contact email must not confer access to that person's record")

	var grants int
	require.NoError(t, env.db.QueryRow(
		`SELECT count(*) FROM member_access_grants WHERE user_id = ?`, acct.UserID).Scan(&grants))
	assert.Zero(t, grants)
}

// TestRelationshipAloneCreatesNoAccess is the other half of ADR-0010.
func TestRelationshipAloneCreatesNoAccess(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)

	granted := seedPersonForRequest(t, env, "Granted Spouse")
	other := seedPersonForRequest(t, env, "Other Spouse")
	acct := decodeAccount(t, provision(t, env, cookie, "spouse@example.test"))
	require.Equal(t, http.StatusOK, grantRecord(t, env, cookie, acct.UserID, granted, "").StatusCode)

	_, err := env.db.Exec(`
		INSERT INTO person_relationships (from_person_id, to_person_id, kind)
		VALUES (?, ?, 'spouse_partner')`, granted, other)
	require.NoError(t, err)

	var reachable int
	require.NoError(t, env.db.QueryRow(
		`SELECT count(*) FROM member_access_grants
		  WHERE user_id = ? AND person_id = ? AND revoked_at IS NULL`,
		acct.UserID, other).Scan(&reachable))
	assert.Zero(t, reachable, "being someone's spouse must not confer access to their record")
}

// TestMemberAccessDeniedWithoutTheCapability covers authorization.
func TestMemberAccessDeniedWithoutTheCapability(t *testing.T) {
	env := setupAuthzTest(t, "acs_coordinator")
	cookie := env.signIn(t)

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/member-accounts", `{"email":"x@example.test"}`},
		{http.MethodPost, "/api/v1/member-accounts/1/records", `{"person_id":1}`},
		{http.MethodPost, "/api/v1/member-accounts/1/records/revoke", `{"person_id":1}`},
		{http.MethodGet, "/api/v1/member-accounts/1/records", ""},
		{http.MethodGet, "/api/v1/members/1/access", ""},
	} {
		resp := doWithHeaders(t, env, tc.method, tc.path, cookie, tc.body, nil)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"%s %s must require member_access.manage", tc.method, tc.path)
	}

	anon := doWithHeaders(t, env, http.MethodGet, "/api/v1/member-accounts/1/records", nil, "", nil)
	assert.Equal(t, http.StatusUnauthorized, anon.StatusCode)
}

// TestProvisioningRequiresConfirmation proves the consequential operations use
// the generic control rather than a new convention.
func TestProvisioningRequiresConfirmation(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)

	resp := doWithHeaders(t, env, http.MethodPost, "/api/v1/member-accounts", cookie,
		`{"email":"unconfirmed@example.test"}`,
		map[string]string{httpapi.ConfirmHeader: "false"})
	assert.Equal(t, http.StatusPreconditionRequired, resp.StatusCode)

	var users int
	require.NoError(t, env.db.QueryRow(
		`SELECT count(*) FROM users WHERE email = 'unconfirmed@example.test'`).Scan(&users))
	assert.Zero(t, users, "a refused confirmation must create no account")
}

// TestPersonAccessListShowsWhoCanReachARecord covers the reverse view.
func TestPersonAccessListShowsWhoCanReachARecord(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)

	person := seedPersonForRequest(t, env, "Watched Record")
	one := decodeAccount(t, provision(t, env, cookie, "one@example.test"))
	two := decodeAccount(t, provision(t, env, cookie, "two@example.test"))
	require.Equal(t, http.StatusOK, grantRecord(t, env, cookie, one.UserID, person, "").StatusCode)
	require.Equal(t, http.StatusOK, grantRecord(t, env, cookie, two.UserID, person, "delegate").StatusCode)

	resp := env.do(t, http.MethodGet, fmt.Sprintf("/api/v1/members/%d/access", person), cookie, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var grants []apiGrant
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&grants))

	accounts := map[int64]bool{}
	for _, g := range grants {
		accounts[g.UserID] = g.Active
	}
	assert.True(t, accounts[one.UserID])
	assert.True(t, accounts[two.UserID])
}

// TestUnknownTargetsAreRefused keeps a typo from creating a dangling grant.
func TestUnknownTargetsAreRefused(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	acct := decodeAccount(t, provision(t, env, cookie, "targets@example.test"))

	assert.Equal(t, http.StatusUnprocessableEntity,
		grantRecord(t, env, cookie, acct.UserID, 99999, "").StatusCode,
		"an unknown person must be refused")
	assert.Equal(t, http.StatusNotFound,
		grantRecord(t, env, cookie, 99999, 1, "").StatusCode,
		"an unknown account must be refused")
}
