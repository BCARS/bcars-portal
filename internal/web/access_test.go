package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/domain/relationships"
)

// The officer access-management UI (bcars-portal-4ux.10).
//
// The page has four controls because there are four decisions, and most of what
// follows is checking that each one does its own job and nobody else's:
// provisioning grants nothing, granting creates no account, a relationship
// creates neither, and revoking takes effect at once.

func accessPage(personID int64) string {
	return fmt.Sprintf("/admin/members/%d/access", personID)
}

// TestProvisioningCreatesAnAccountAndGrantsNothing is the separation that
// matters most, because the opposite is the convenient-seeming default: an
// officer who typed an address into a page about Dale's record has not thereby
// decided that address may read it.
func TestProvisioningCreatesAnAccountAndGrantsNothing(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.seedPersonRow(t, "Dale Rutherford", "W3DLR")

	w := e.post(t, accessPage(personID)+"/accounts",
		url.Values{"email": {"  NewMember@Example.Test  "}}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code)
	assert.Contains(t, w.Header().Get("Location"), "no+access+until+you+grant+one")

	var userID int64
	var passwordHash any
	require.NoError(t, e.h.db.QueryRow(
		`SELECT id, password_hash FROM users WHERE email = 'newmember@example.test'`).
		Scan(&userID, &passwordHash))
	assert.Nil(t, passwordHash, "an officer must not choose somebody else's password")

	var grants int
	require.NoError(t, e.h.db.QueryRow(
		`SELECT count(*) FROM member_access_grants WHERE user_id = ?`, userID).Scan(&grants))
	assert.Zero(t, grants, "provisioning must grant access to nothing")

	body := e.getAs(t, accessPage(personID), officer).Body.String()
	assert.Contains(t, body, "No account reaches this record")
}

// TestGrantThenRevokeKeepsTheHistory covers the everyday pair of controls and
// the record they leave.
func TestGrantThenRevokeKeepsTheHistory(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.seedPersonRow(t, "Dale Rutherford", "W3DLR")

	require.Equal(t, http.StatusSeeOther, e.post(t, accessPage(personID)+"/accounts",
		url.Values{"email": {"dale@example.test"}}, officer).Code)

	w := e.post(t, accessPage(personID)+"/grants", url.Values{
		"email": {"dale@example.test"}, "access_kind": {"self"}, "reason": {"His own record"},
	}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code)

	body := e.getAs(t, accessPage(personID), officer).Body.String()
	assert.Contains(t, body, "dale@example.test")
	assert.Contains(t, body, "Their own record")
	assert.Contains(t, body, "His own record")
	assert.Contains(t, body, "No password set yet",
		"an officer needs to see that a granted account still cannot sign in")

	var userID int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT id FROM users WHERE email = 'dale@example.test'`).Scan(&userID))

	w = e.post(t, accessPage(personID)+"/revoke", url.Values{
		"user_id": {fmt.Sprint(userID)}, "reason": {"Left the household"},
	}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code)
	assert.Contains(t, w.Header().Get("Location"), "session+already+open")

	body = e.getAs(t, accessPage(personID), officer).Body.String()
	assert.Contains(t, body, "Revoked", "the revoked grant stays visible as history")

	var active, total int
	require.NoError(t, e.h.db.QueryRow(
		`SELECT count(*) FROM member_access_grants WHERE user_id = ? AND revoked_at IS NULL`,
		userID).Scan(&active))
	require.NoError(t, e.h.db.QueryRow(
		`SELECT count(*) FROM member_access_grants WHERE user_id = ?`, userID).Scan(&total))
	assert.Zero(t, active)
	assert.Equal(t, 1, total, "revoking archives the grant rather than deleting it")
}

// TestGrantingNeedsAnExistingAccount keeps the two controls apart from the
// other direction: a typo in the grant form must not quietly create a login.
func TestGrantingNeedsAnExistingAccount(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.seedPersonRow(t, "Dale Rutherford", "W3DLR")

	w := e.post(t, accessPage(personID)+"/grants",
		url.Values{"email": {"nobody@example.test"}}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code)
	assert.Contains(t, w.Header().Get("Location"), "Create+the+account+first")

	var users, grants int
	require.NoError(t, e.h.db.QueryRow(
		`SELECT count(*) FROM users WHERE email = 'nobody@example.test'`).Scan(&users))
	require.NoError(t, e.h.db.QueryRow(
		`SELECT count(*) FROM member_access_grants`).Scan(&grants))
	assert.Zero(t, users, "a grant form must not create an account")
	assert.Zero(t, grants)
}

// TestRevocationTakesEffectOnTheNextPageLoad is the promise the page makes to
// an officer, tested through the member's own pages rather than the grant row.
func TestRevocationTakesEffectOnTheNextPageLoad(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.grant(t, "Dale Rutherford")
	member := e.signInMember(t) // the session is established once, here

	before := e.getAs(t, fmt.Sprintf("/member/records/%d", personID), member)
	require.Equal(t, http.StatusOK, before.Code, "the member can read it while granted")

	w := e.post(t, accessPage(personID)+"/revoke", url.Values{
		"user_id": {fmt.Sprint(e.memberUserID)}, "reason": {"No longer appropriate"},
	}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code)

	after := e.getAs(t, fmt.Sprintf("/member/records/%d", personID), member)
	assert.Equal(t, http.StatusNotFound, after.Code,
		"the very next request in the same session must be refused")
}

// TestRecoveryTellsTheSameStoryForEveryAddress keeps the officer page from
// becoming the enumeration oracle the public form refuses to be.
func TestRecoveryTellsTheSameStoryForEveryAddress(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.seedPersonRow(t, "Dale Rutherford", "W3DLR")

	known := e.post(t, accessPage(personID)+"/recovery",
		url.Values{"email": {memberEmail}}, officer)
	unknown := e.post(t, accessPage(personID)+"/recovery",
		url.Values{"email": {"nobody-at-all@example.test"}}, officer)

	require.Equal(t, http.StatusSeeOther, known.Code)
	require.Equal(t, http.StatusSeeOther, unknown.Code)
	assert.Equal(t, known.Header().Get("Location"), unknown.Header().Get("Location"),
		"a registered and an unregistered address must be indistinguishable here")

	// The real account did receive a link, so the sameness is not achieved by
	// sending nothing at all.
	assert.NotEmpty(t, e.recoveryToken(t, memberEmail))
}

// TestEachAccessControlIsSeparatelyAudited is the reason the page has four
// forms rather than one. "Who gave this account access to that record" must be
// answerable without inferring intent from a compound event.
func TestEachAccessControlIsSeparatelyAudited(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.seedPersonRow(t, "Dale Rutherford", "W3DLR")

	require.Equal(t, http.StatusSeeOther, e.post(t, accessPage(personID)+"/accounts",
		url.Values{"email": {"dale@example.test"}}, officer).Code)
	require.Equal(t, http.StatusSeeOther, e.post(t, accessPage(personID)+"/grants",
		url.Values{"email": {"dale@example.test"}}, officer).Code)

	var userID int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT id FROM users WHERE email = 'dale@example.test'`).Scan(&userID))
	require.Equal(t, http.StatusSeeOther, e.post(t, accessPage(personID)+"/revoke",
		url.Values{"user_id": {fmt.Sprint(userID)}, "reason": {"done"}}, officer).Code)
	require.Equal(t, http.StatusSeeOther, e.post(t, accessPage(personID)+"/recovery",
		url.Values{"email": {"dale@example.test"}}, officer).Code)

	for _, action := range []string{
		"member_access.provision", "member_access.grant",
		"member_access.revoke", "auth.recovery.request",
	} {
		var n int
		var actor int64
		require.NoError(t, e.h.db.QueryRow(`
			SELECT count(*), coalesce(max(actor_user_id), 0) FROM audit_events
			 WHERE action = ? AND outcome = 'success'`, action).Scan(&n, &actor))
		assert.Equal(t, 1, n, "%s must be audited as its own action", action)
		assert.Equal(t, e.officerUserID, actor, "%s must record which officer did it", action)
	}
}

// TestRelationshipsAreShownWithoutBecomingAccess is ADR-0010 on the one page
// where the two facts sit next to each other and could most easily be confused.
func TestRelationshipsAreShownWithoutBecomingAccess(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.seedPersonRow(t, "Dale Rutherford", "W3DLR")
	spouse := e.seedPersonRow(t, "Marguerite Ashby", "W3MGA")

	rels := relationships.NewService(e.h.db)
	_, err := rels.Create(context.Background(), e.officerPrincipal(), relationships.CreateParams{
		FromPersonID: personID, ToPersonID: spouse,
		Kind: relationships.KindSpousePartner, Context: "Married thirty years.",
	})
	require.NoError(t, err)

	body := e.getAs(t, accessPage(personID), officer).Body.String()

	assert.Contains(t, body, "Marguerite Ashby", "the household is shown to an officer")
	assert.Contains(t, body, "Recorded relationships")
	assert.Contains(t, body, "Informational only")
	assert.Contains(t, body, "No account reaches this record",
		"a recorded spouse must not appear as access")

	var grants int
	require.NoError(t, e.h.db.QueryRow(`SELECT count(*) FROM member_access_grants`).Scan(&grants))
	assert.Zero(t, grants)
}

// TestAccessPageRefusesWithoutTheCapability covers authorization on every
// control, and the anonymous case that stands in for a cross-site POST.
func TestAccessPageRefusesWithoutTheCapability(t *testing.T) {
	e := setupMemberEnv(t)
	personID := e.grant(t, "Dale Rutherford")
	member := e.signInMember(t)

	routes := []struct {
		method, path string
		form         url.Values
	}{
		{"GET", accessPage(personID), nil},
		{"POST", accessPage(personID) + "/accounts", url.Values{"email": {"x@example.test"}}},
		{"POST", accessPage(personID) + "/grants", url.Values{"email": {"x@example.test"}}},
		{"POST", accessPage(personID) + "/revoke", url.Values{"user_id": {"1"}}},
		{"POST", accessPage(personID) + "/recovery", url.Values{"email": {"x@example.test"}}},
	}

	for _, rt := range routes {
		var res *http.Response
		if rt.method == "GET" {
			res = e.getAs(t, rt.path, member).Result()
		} else {
			res = e.post(t, rt.path, rt.form, member).Result()
		}
		assert.Equal(t, http.StatusForbidden, res.StatusCode,
			"%s %s must require member_access.manage", rt.method, rt.path)

		var anon *http.Response
		if rt.method == "GET" {
			anon = e.getAs(t, rt.path, nil).Result()
		} else {
			anon = e.post(t, rt.path, rt.form, nil).Result()
		}
		assert.Equal(t, http.StatusSeeOther, anon.StatusCode,
			"%s %s must send an unauthenticated caller to sign-in", rt.method, rt.path)
		assert.Equal(t, RouteLogin, anon.Header.Get("Location"))
	}
}

// TestAccessPageForAnUnknownRecordIsNotFound covers the missing-row path.
func TestAccessPageForAnUnknownRecordIsNotFound(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	assert.Equal(t, http.StatusNotFound, e.getAs(t, accessPage(999999), officer).Code)
}
