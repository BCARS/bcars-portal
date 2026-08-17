package web

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/domain/changerequests"
	"github.com/bcars/bcars-portal/internal/domain/memberaccess"
)

// The member self-service UI (bcars-portal-4ux.11).
//
// These drive the pages a member actually clicks. The API tests already hold
// the authorization rules at the JSON boundary; what a page can still get wrong
// is different — rendering a value the caller may not see, offering a link that
// answers 403, or letting a posted field name somebody else's row — so these
// assert on rendered HTML.

const (
	assocEmail  = "associate@bcars.example"
	memberPass2 = "membersfirstpassword1"
)

// grantTo provisions an account, gives it a password, and grants it a record.
func (e *memberTestEnv) grantTo(t *testing.T, email string, personID int64) *http.Cookie {
	t.Helper()

	acct, err := e.access.Provision(context.Background(),
		&authz.Principal{UserID: e.officerUserID},
		memberaccess.ProvisionParams{Email: email}, time.Now().UTC())
	require.NoError(t, err)

	if personID != 0 {
		_, err = e.access.GrantAccess(context.Background(),
			&authz.Principal{UserID: e.officerUserID}, acct.UserID,
			memberaccess.GrantParams{PersonID: personID, AccessKind: memberaccess.AccessSelf,
				Reason: "test setup"}, time.Now().UTC())
		require.NoError(t, err)
	}

	token := e.recoveryToken(t, email)
	w := e.post(t, RouteResetPassword, url.Values{
		"token": {token}, "password": {memberPass2}, "confirm": {memberPass2},
	}, nil)
	require.Equal(t, http.StatusSeeOther, w.Code)
	return cookieFrom(t, w)
}

// requestPath returns the path a submission redirected to, without the success
// query string a later POST must not carry.
func requestPath(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	loc, err := url.Parse(w.Header().Get("Location"))
	require.NoError(t, err)
	require.NotEmpty(t, loc.Path)
	return loc.Path
}

// seedPersonRow inserts a person with no grant to anyone under test.
func (e *memberTestEnv) seedPersonRow(t *testing.T, name, callSign string) int64 {
	t.Helper()
	res, err := e.h.db.Exec(
		`INSERT INTO persons (display_name, sort_name, call_sign) VALUES (?, ?, ?)`,
		name, name, callSign)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	_, err = e.h.db.Exec(
		`INSERT INTO memberships (person_id, base_type, lifecycle) VALUES (?, 'full', 'approved')`, id)
	require.NoError(t, err)
	return id
}

// TestMemberLandingOffersOnlyReachablePages checks the rule the officer
// dashboard broke before: a page must not advertise a link that answers 403.
func TestMemberLandingOffersOnlyReachablePages(t *testing.T) {
	e := setupMemberEnv(t)
	e.grant(t, "Dale Rutherford")
	cookie := e.signInMember(t)

	body := e.getAs(t, RouteMemberHome, cookie).Body.String()

	// Everything the member chrome offers must be a route this caller can open.
	for _, link := range []string{"/member/", "/member/requests", "/member/suggest"} {
		require.Contains(t, body, `href="`+link+`"`, "the landing must offer %s", link)
		w := e.getAs(t, link, cookie)
		assert.Less(t, w.Code, 400, "%s is offered and must be reachable, got %d", link, w.Code)
	}

	// And must not offer the officer surface at all.
	for _, forbidden := range []string{"/admin/", "/admin/members", "/admin/treasury", "/admin/imports"} {
		assert.NotContains(t, body, `href="`+forbidden+`"`,
			"the member layout must not link to %s, which answers 403", forbidden)
	}
}

// TestMemberRecordPageShowsSafeFieldsOnly is the page-level version of the
// safe read model: what a template receives is what it can leak.
func TestMemberRecordPageShowsSafeFieldsOnly(t *testing.T) {
	e := setupMemberEnv(t)
	personID := e.grant(t, "Dale Rutherford")
	cookie := e.signInMember(t)

	_, err := e.h.db.Exec(
		`INSERT INTO contact_methods (person_id, kind, label, value_raw, value_norm, is_primary)
		 VALUES (?, 'email', 'home', 'dale@example.test', 'dale@example.test', 1)`, personID)
	require.NoError(t, err)
	_, err = e.h.db.Exec(
		`INSERT INTO notes (subject_kind, subject_id, category, visibility, body, author_id, source)
		 VALUES ('person', ?, 'general', 'officer', 'OFFICER ONLY chased at the meeting', 1, 'manual')`,
		personID)
	require.NoError(t, err)

	w := e.getAs(t, fmt.Sprintf("/member/records/%d", personID), cookie)
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	assert.Contains(t, body, "Dale Rutherford")
	assert.Contains(t, body, "dale@example.test", "a member must see their own contact details to correct them")
	assert.NotContains(t, body, "OFFICER ONLY", "officer notes must never reach a member page")
	assert.Contains(t, body, "Payment details are not shown here",
		"the page says what it withholds rather than leaving a member wondering")
}

// TestMemberCannotOpenAnotherMembersRecord holds the read boundary at the page
// level, with a record that belongs to somebody so an empty result cannot pass
// for a working check.
func TestMemberCannotOpenAnotherMembersRecord(t *testing.T) {
	e := setupMemberEnv(t)
	e.grant(t, "Dale Rutherford")
	mine := e.signInMember(t)

	strangerID := e.seedPersonRow(t, "Marguerite Ashby", "W3MGA")
	theirs := e.grantTo(t, assocEmail, strangerID)

	ungranted := e.getAs(t, fmt.Sprintf("/member/records/%d", strangerID), mine)
	missing := e.getAs(t, "/member/records/999999", mine)
	assert.Equal(t, http.StatusNotFound, ungranted.Code,
		"one member's grant must not become another member's page")
	assert.Equal(t, http.StatusNotFound, missing.Code)
	assert.Equal(t, missing.Body.String(), ungranted.Body.String(),
		"a record you cannot see must render exactly like one that does not exist")
	assert.NotContains(t, ungranted.Body.String(), "Marguerite",
		"and must not name them in the refusal")

	// The account that holds the grant reads it, so the refusal above is
	// authorization working rather than the record being unreachable.
	w := e.getAs(t, fmt.Sprintf("/member/records/%d", strangerID), theirs)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Marguerite Ashby")
}

// TestMemberSuggestsCorrectionToOwnRecord covers the ordinary flow end to end
// through the form.
func TestMemberSuggestsCorrectionToOwnRecord(t *testing.T) {
	e := setupMemberEnv(t)
	personID := e.grant(t, "Dale Rutherford")
	cookie := e.signInMember(t)

	res, err := e.h.db.Exec(
		`INSERT INTO contact_methods (person_id, kind, value_raw, value_norm, is_primary)
		 VALUES (?, 'email', 'dale.old@example.test', 'dale.old@example.test', 1)`, personID)
	require.NoError(t, err)
	contactID, err := res.LastInsertId()
	require.NoError(t, err)

	form := e.getAs(t, fmt.Sprintf("/member/records/%d/suggest", personID), cookie)
	require.Equal(t, http.StatusOK, form.Code)
	assert.Contains(t, form.Body.String(), "An officer checks every change",
		"the form must say review is required before anything changes")
	assert.Contains(t, form.Body.String(), "dale.old@example.test",
		"the form holds what the club currently has, so the member edits it")

	w := e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), url.Values{
		"display_name":                       {"Dale Rutherford"},
		fmt.Sprintf("contact_%d", contactID): {"dale.new@example.test"},
		"note":                               {"My email address changed last month."},
	}, cookie)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())
	assert.Contains(t, w.Header().Get("Location"), "/member/requests/")

	// Canonical data is untouched: that is the premise of the whole model.
	var stored string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT value_raw FROM contact_methods WHERE id = ?`, contactID).Scan(&stored))
	assert.Equal(t, "dale.old@example.test", stored,
		"a suggestion must not change the record it is about")

	// The item is aimed at the right row, with the version the member saw.
	var kind string
	var targetID, targetVersion int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT target_kind, target_id, COALESCE(target_version, 0)
		   FROM member_change_request_items ORDER BY id DESC LIMIT 1`).
		Scan(&kind, &targetID, &targetVersion))
	assert.Equal(t, "contact_method", kind)
	assert.Equal(t, contactID, targetID)
	assert.NotZero(t, targetVersion, "the version the member saw travels with the proposal")
}

// A correction to the name carries the PERSON's version, the same way a contact
// correction carries the contact row's. Without it an approval weeks later
// silently overwrites whatever an officer changed in the meantime
// (bcars-portal-ssz.2).
func TestANameCorrectionCarriesTheVersionTheMemberSaw(t *testing.T) {
	e := setupMemberEnv(t)
	personID := e.grant(t, "Dale Rutherford")
	cookie := e.signInMember(t)

	w := e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), url.Values{
		"display_name": {"Dale Rutherforde"},
	}, cookie)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	var targetKind string
	var targetVersion int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT target_kind, COALESCE(target_version, 0)
		   FROM member_change_request_items ORDER BY id DESC LIMIT 1`).
		Scan(&targetKind, &targetVersion))
	assert.Equal(t, "person", targetKind)
	assert.NotZero(t, targetVersion,
		"a person correction must record which version of the record it was written against")
}

// TestMemberCannotAimOwnFormAtAnotherRecord stops the posted contact id from
// being trusted. The form offers only the caller's own details; a hand-built
// POST must not get further.
func TestMemberCannotAimOwnFormAtAnotherRecord(t *testing.T) {
	e := setupMemberEnv(t)
	personID := e.grant(t, "Dale Rutherford")
	cookie := e.signInMember(t)

	strangerID := e.seedPersonRow(t, "Marguerite Ashby", "W3MGA")
	res, err := e.h.db.Exec(
		`INSERT INTO contact_methods (person_id, kind, value_raw, value_norm, is_primary)
		 VALUES (?, 'phone', '+15555550101', '+15555550101', 1)`, strangerID)
	require.NoError(t, err)
	strangerContact, err := res.LastInsertId()
	require.NoError(t, err)

	// A hand-built POST naming a contact row the record does not hold. The
	// browser never sends this: the form has an input per contact the record
	// actually has, and this is not one of them.
	w := e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), url.Values{
		"display_name": {"Dale Rutherford"},
		fmt.Sprintf("contact_%d", strangerContact): {"+15555550199"},
		"note": {"Fix this number."},
	}, cookie)
	require.Equal(t, http.StatusSeeOther, w.Code,
		"the note is a legitimate submission on its own: %s", w.Body.String())

	// The stranger's row is untouched and unmentioned. The form reads only the
	// contacts the RECORD holds, so an unknown field name is not refused, it is
	// simply not a field -- and nothing can be stored pointing at it.
	var items int
	require.NoError(t, e.h.db.QueryRow(
		`SELECT count(*) FROM member_change_request_items WHERE target_id = ?`, strangerContact).Scan(&items))
	assert.Zero(t, items, "nothing may be stored pointing at a contact the record does not hold")

	var stored string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT value_raw FROM contact_methods WHERE id = ?`, strangerContact).Scan(&stored))
	assert.Equal(t, "+15555550101", stored)
}

// TestAssociateUsesHintFormAndLearnsNothing is the Associate case: no grant to
// anyone, still able to report a mistake, still told nothing.
func TestAssociateUsesHintFormAndLearnsNothing(t *testing.T) {
	e := setupMemberEnv(t)
	strangerID := e.seedPersonRow(t, "Marguerite Ashby", "W3MGA")
	associate := e.grantTo(t, assocEmail, 0)

	// They can reach the hint form and their own landing, and see no records.
	landing := e.getAs(t, RouteMemberHome, associate)
	require.Equal(t, http.StatusOK, landing.Code)
	assert.Contains(t, landing.Body.String(), "not linked to any member record")
	assert.Contains(t, landing.Body.String(), `href="/member/suggest"`,
		"a member with no records may still report a mistake")

	form := e.getAs(t, RouteMemberSuggest, associate)
	require.Equal(t, http.StatusOK, form.Code)
	formBody := form.Body.String()
	assert.Contains(t, formBody, "This form does not look anyone up",
		"the form says plainly that it confirms nothing")
	assert.Contains(t, formBody, "does not give you access to anyone",
		"and that submitting grants nothing")
	assert.Contains(t, formBody, `autocomplete="off"`,
		"the hint fields must not autocomplete against anything")
	assert.NotContains(t, formBody, "Marguerite",
		"no directory, no candidates, no names the caller did not type")

	w := e.post(t, RouteMemberSuggest, url.Values{
		"kind":            {"person.call_sign.set"},
		"proposed_value":  {"W3MGB"},
		"about_name":      {"Marguerite Ashby"},
		"about_call_sign": {"W3MGA"},
		"relationship":    {"We operate the same net"},
		"summary":         {"Her call sign is printed wrong in the newsletter."},
	}, associate)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	// The stored request names no canonical record: no lookup happened.
	var target any
	var source string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT target_person_id, source FROM member_change_requests ORDER BY id DESC LIMIT 1`).
		Scan(&target, &source))
	assert.Nil(t, target, "the hint form must not resolve who it is about")
	assert.Equal(t, "member", source)

	// And the submitter still cannot see that record, or any record.
	assert.Equal(t, http.StatusNotFound,
		e.getAs(t, fmt.Sprintf("/member/records/%d", strangerID), associate).Code,
		"suggesting a correction about someone must not grant access to them")
	assert.Contains(t, e.getAs(t, RouteMemberHome, associate).Body.String(),
		"not linked to any member record")
}

// TestDelegateFormDoesNotClaimTheRecordIsTheirs checks the copy a delegate
// sees. An officer may grant someone access on another member's behalf, and a
// form telling them "My name is wrong" would be asking them to confirm
// something untrue about themselves.
func TestDelegateFormDoesNotClaimTheRecordIsTheirs(t *testing.T) {
	e := setupMemberEnv(t)
	personID := e.seedPersonRow(t, "Marguerite Ashby", "W3MGA")

	_, err := e.access.GrantAccess(context.Background(),
		&authz.Principal{UserID: e.officerUserID}, e.memberUserID,
		memberaccess.GrantParams{PersonID: personID, AccessKind: memberaccess.AccessDelegate,
			Reason: "acts for her"}, time.Now().UTC())
	require.NoError(t, err)
	cookie := e.signInMember(t)

	body := e.getAs(t, fmt.Sprintf("/member/records/%d/suggest", personID), cookie).Body.String()
	assert.Contains(t, body, "Correct the details for Marguerite Ashby",
		"a delegate corrects someone else's record, and the form must name whose it is")
	assert.NotContains(t, body, "your details",
		"the form must not tell a delegate that somebody else's record is theirs")
	assert.NotContains(t, body, "My name",
		"nor phrase a field as the caller's own")
}

// TestMemberPagesSpeakToAPerson checks the small things a member actually
// reads. Each of these shipped wrong once, and none of them is visible in a
// status code: a raw RFC 3339 timestamp, the database's word for the ordinary
// membership state, and "1 suggestion(s)".
func TestMemberPagesSpeakToAPerson(t *testing.T) {
	e := setupMemberEnv(t)
	personID := e.grant(t, "Dale Rutherford")
	cookie := e.signInMember(t)

	var membershipID int64
	require.NoError(t, e.h.db.QueryRow(
		`INSERT INTO memberships (person_id, base_type, lifecycle) VALUES (?, 'full', 'approved') RETURNING id`,
		personID).Scan(&membershipID))
	_, err := e.h.db.Exec(
		`INSERT INTO coverage_events (membership_id, paid_through, reason_kind, decided_at)
		 VALUES (?, '2026-12-31', 'adjustment', '2026-01-01T00:00:00.000Z')`, membershipID)
	require.NoError(t, err)

	record := e.getAs(t, fmt.Sprintf("/member/records/%d", personID), cookie).Body.String()
	assert.Contains(t, record, "31 December 2026", "a member reads dates, not storage format")
	assert.NotContains(t, record, "2026-12-31")
	assert.NotContains(t, record, "approved",
		"'approved' is database vocabulary; the ordinary state needs no announcement")

	w := e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), url.Values{
		"display_name": {"Dale Rutherford"},
		"call_sign":    {"W3NEW"},
		"note":         {"My call sign changed."},
	}, cookie)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	landing := e.getAs(t, RouteMemberHome, cookie).Body.String()
	assert.Contains(t, landing, "You have one suggestion waiting")
	assert.NotContains(t, landing, "suggestion(s)")

	list := e.getAs(t, RouteMemberRequests, cookie).Body.String()
	assert.NotContains(t, list, "T00:00:00", "no timestamps in the member's own list")
	assert.NotRegexp(t, `\d{4}-\d{2}-\d{2}T`, list)
}

// TestMemberTracksAndWithdrawsOwnSuggestions covers the tracking pages.
func TestMemberTracksAndWithdrawsOwnSuggestions(t *testing.T) {
	e := setupMemberEnv(t)
	personID := e.grant(t, "Dale Rutherford")
	mine := e.signInMember(t)
	theirs := e.grantTo(t, assocEmail, 0)

	w := e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), url.Values{
		"display_name": {"Dale Rutherforde"},
		"note":         {"My name is spelled wrong."},
	}, mine)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())
	detail := requestPath(t, w)

	require.Equal(t, http.StatusSeeOther, e.post(t, RouteMemberSuggest, url.Values{
		"kind":       {changerequests.OpOther},
		"about_name": {"Someone Else"},
		"summary":    {"Their address is out of date."},
	}, theirs).Code)

	// Each member's list holds only their own.
	list := e.getAs(t, RouteMemberRequests, mine)
	require.Equal(t, http.StatusOK, list.Code)
	assert.Contains(t, list.Body.String(), "My name is spelled wrong")
	assert.NotContains(t, list.Body.String(), "Their address is out of date",
		"another member's suggestion must not appear in this list")

	// And another member's suggestion is not readable by id either.
	var otherID int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT id FROM member_change_requests WHERE summary LIKE 'Their address%'`).Scan(&otherID))
	notYours := e.getAs(t, fmt.Sprintf("%s/%d", RouteMemberRequests, otherID), mine)
	neverExisted := e.getAs(t, RouteMemberRequests+"/999999", mine)
	assert.Equal(t, http.StatusNotFound, notYours.Code)
	assert.Equal(t, neverExisted.Body.String(), notYours.Body.String(),
		"another member's suggestion must render like one that does not exist")

	// Withdrawing your own works and keeps what was asked for.
	w = e.post(t, detail+"/withdrawal", url.Values{}, mine)
	require.Equal(t, http.StatusSeeOther, w.Code)
	page := e.getAs(t, detail, mine)
	require.Equal(t, http.StatusOK, page.Code)
	assert.Contains(t, page.Body.String(), "Withdrawn")
	assert.Contains(t, page.Body.String(), "Dale Rutherforde",
		"withdrawal retracts the suggestion; it does not erase it")
}

// TestWithdrawalPageHidesTheButtonOnceDecided keeps the UI from offering an
// action the domain service would refuse.
func TestWithdrawalPageHidesTheButtonOnceDecided(t *testing.T) {
	e := setupMemberEnv(t)
	personID := e.grant(t, "Dale Rutherford")
	cookie := e.signInMember(t)

	w := e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), url.Values{
		"display_name": {"Dale Rutherforde"},
		"note":         {"My name is spelled wrong."},
	}, cookie)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())
	detail := requestPath(t, w)

	assert.Contains(t, e.getAs(t, detail, cookie).Body.String(), "Withdraw this suggestion")

	_, err := e.h.db.Exec(
		`UPDATE member_change_request_items
		    SET status = 'rejected', reviewed_by = ?, reviewed_at = datetime('now'),
		        decision_reason = 'Spelling confirmed correct at the meeting'
		  WHERE id = (SELECT id FROM member_change_request_items ORDER BY id DESC LIMIT 1)`,
		e.officerUserID)
	require.NoError(t, err)

	page := e.getAs(t, detail, cookie)
	require.Equal(t, http.StatusOK, page.Code)
	assert.NotContains(t, page.Body.String(), "Withdraw this suggestion",
		"the button must not be offered once an officer has decided")
	assert.Contains(t, page.Body.String(), "Spelling confirmed correct",
		"a member is entitled to know why their suggestion was refused")

	// Posting anyway is refused rather than accepted because the button was
	// merely hidden.
	w = e.post(t, detail+"/withdrawal", url.Values{}, cookie)
	require.Equal(t, http.StatusSeeOther, w.Code)
	assert.Contains(t, w.Header().Get("Location"), "error=",
		"hiding the button is presentation; the refusal is the domain service's")
}

// TestMemberPagesEscapeWhatAMemberTyped guards the obvious injection surface:
// a member's own words are echoed back on several pages.
func TestMemberPagesEscapeWhatAMemberTyped(t *testing.T) {
	e := setupMemberEnv(t)
	cookie := e.grantTo(t, assocEmail, 0)

	const payload = `<script>alert("xss")</script>`
	w := e.post(t, RouteMemberSuggest, url.Values{
		"kind":       {changerequests.OpOther},
		"about_name": {payload},
		"summary":    {"Something is wrong " + payload},
	}, cookie)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())
	detail := requestPath(t, w)

	for _, page := range []string{RouteMemberRequests, detail} {
		body := e.getAs(t, page, cookie).Body.String()
		assert.NotContains(t, body, "<script>alert",
			"%s must escape what the member typed", page)
		assert.Contains(t, body, "&lt;script&gt;",
			"%s must still show it, escaped", page)
	}
}

// TestMemberPagesRefuseAnonymousAndUnderprivileged covers the two negative
// authorization cases for every member route.
func TestMemberPagesRefuseAnonymousAndUnderprivileged(t *testing.T) {
	e := setupMemberEnv(t)
	e.grant(t, "Dale Rutherford")

	routes := []struct{ method, path string }{
		{"GET", RouteMemberHome},
		{"GET", "/member/records/1"},
		{"GET", "/member/records/1/suggest"},
		{"POST", "/member/records/1/suggest"},
		{"GET", RouteMemberSuggest},
		{"POST", RouteMemberSuggest},
		{"GET", RouteMemberRequests},
		{"GET", RouteMemberRequests + "/1"},
		{"POST", RouteMemberRequests + "/1/withdrawal"},
	}

	for _, rt := range routes {
		t.Run("anonymous "+rt.method+" "+rt.path, func(t *testing.T) {
			var w *httptest.ResponseRecorder
			if rt.method == "GET" {
				w = e.getAs(t, rt.path, nil)
			} else {
				w = e.post(t, rt.path, url.Values{}, nil)
			}
			require.Equal(t, http.StatusSeeOther, w.Code,
				"an anonymous caller must be sent to sign-in, never served %s", rt.path)
			assert.Equal(t, "/login", w.Header().Get("Location"))
		})
	}

	// An officer identity never provisioned as a member holds neither member
	// capability and is refused rather than quietly served an empty page.
	officer := e.officerCookie(t)
	for _, rt := range routes {
		t.Run("officer "+rt.method+" "+rt.path, func(t *testing.T) {
			var w *httptest.ResponseRecorder
			if rt.method == "GET" {
				w = e.getAs(t, rt.path, officer)
			} else {
				w = e.post(t, rt.path, url.Values{}, officer)
			}
			assert.Equal(t, http.StatusForbidden, w.Code,
				"%s must require a member capability", rt.path)
		})
	}
}

// TestNoAnonymousSuggestionPageExists holds the line the corrected plan drew.
func TestNoAnonymousSuggestionPageExists(t *testing.T) {
	e := setupMemberEnv(t)

	for _, path := range []string{
		"/suggest", "/corrections", "/public/corrections", "/report-a-correction",
	} {
		w := e.getAs(t, path, nil)
		assert.Equal(t, http.StatusNotFound, w.Code,
			"%s must not exist; correction suggestions are authenticated", path)
	}

	// The signed-out entry point is sign-in, and it offers no correction form.
	login := e.getAs(t, RouteLogin, nil)
	require.Equal(t, http.StatusOK, login.Code)
	assert.NotContains(t, strings.ToLower(login.Body.String()), "suggest a correction")
}

// TestMemberFormsRelyOnSameSiteCookie records the CSRF defence these forms use,
// matching the admin UI: there is no token, so the cookie attribute is the
// control and a test has to hold it.
func TestMemberFormsRelyOnSameSiteCookie(t *testing.T) {
	e := setupMemberEnv(t)
	personID := e.grant(t, "Dale Rutherford")
	cookie := e.signInMember(t)

	assert.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
	assert.True(t, cookie.HttpOnly)

	// The same POST without the cookie, which is what a cross-site submission
	// looks like at the server: unauthenticated, and redirected rather than
	// acted on.
	w := e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), url.Values{
		"kind":           {"person.display_name.set"},
		"proposed_value": {"Attacker Chosen Name"},
		"summary":        {"cross-site"},
	}, nil)
	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/login", w.Header().Get("Location"))

	var count int
	require.NoError(t, e.h.db.QueryRow(
		`SELECT count(*) FROM member_change_requests`).Scan(&count))
	assert.Zero(t, count, "a cross-site post must file nothing")
}

// TestMemberPagesCarryAccessibilityBasics checks the MVP floor: a page a member
// cannot navigate is not delivered, however correct its authorization.
func TestMemberPagesCarryAccessibilityBasics(t *testing.T) {
	e := setupMemberEnv(t)
	personID := e.grant(t, "Dale Rutherford")
	cookie := e.signInMember(t)

	pages := []string{
		RouteMemberHome,
		fmt.Sprintf("/member/records/%d", personID),
		fmt.Sprintf("/member/records/%d/suggest", personID),
		RouteMemberSuggest,
		RouteMemberRequests,
	}

	for _, page := range pages {
		t.Run(page, func(t *testing.T) {
			w := e.getAs(t, page, cookie)
			require.Equal(t, http.StatusOK, w.Code)
			body := w.Body.String()

			assert.Contains(t, body, `<html lang="en"`, "a screen reader needs the language")
			assert.Contains(t, body, `class="skip-link"`, "keyboard users need to skip the header")
			assert.Contains(t, body, `<main id="main"`, "the skip link needs its target")
			assert.Contains(t, body, "<title>", "a tab needs a name")
			assert.NotContains(t, body, "<title></title>")

			// Every form control the page renders is labelled.
			for _, id := range formControlIDs(body) {
				assert.Contains(t, body, `for="`+id+`"`,
					"control %q on %s has no label", id, page)
			}
		})
	}
}

// formControlIDs pulls the id of every labelable form control out of rendered
// HTML, so the label assertion covers whatever the page actually rendered
// rather than a list kept by hand.
func formControlIDs(body string) []string {
	var ids []string
	for _, tag := range []string{"<input ", "<select ", "<textarea "} {
		rest := body
		for {
			i := strings.Index(rest, tag)
			if i < 0 {
				break
			}
			rest = rest[i+len(tag):]
			end := strings.Index(rest, ">")
			if end < 0 {
				break
			}
			attrs := rest[:end]
			// Radios share one name and are labelled by wrapping, and hidden
			// inputs are not user-facing.
			if strings.Contains(attrs, `type="radio"`) || strings.Contains(attrs, `type="hidden"`) {
				continue
			}
			idAt := strings.Index(attrs, `id="`)
			if idAt < 0 {
				continue
			}
			idRest := attrs[idAt+len(`id="`):]
			q := strings.Index(idRest, `"`)
			if q < 0 {
				continue
			}
			ids = append(ids, idRest[:q])
		}
	}
	return ids
}

// seedContact gives a person one contact detail and returns its id.
func (e *memberTestEnv) seedContact(t *testing.T, personID int64, kind, value string) int64 {
	t.Helper()
	res, err := e.h.db.Exec(
		`INSERT INTO contact_methods (person_id, kind, value_raw, value_norm, is_primary)
		 VALUES (?, ?, ?, ?, 1)`, personID, kind, value, value)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

// The form is a mirror of the record, not a question about it
// (bcars-portal-ssz.2, ADR-0014).
//
// bcars-portal-245 fixed a real defect here: radios chose WHICH field was wrong
// beside a separate "which contact detail?" dropdown that no radio governed, so
// a member choosing "My name is wrong" was shown a live list of their telephone
// numbers to pick from. The fix then was to ask one question.
//
// An edit form asks none. Every field is on screen holding its own current
// value, so there is nothing to choose between and nothing for a chooser to
// disagree with. This asserts that property in the new shape: the ambiguity 245
// removed must not come back as a field the member has to aim.
func TestTheFormShowsEveryFieldHoldingItsOwnValue(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, personID := e.eligibleMember(t)
	contactID := e.seedContact(t, personID, "email", "dale.old@example.test")

	body := e.getAs(t, fmt.Sprintf("/member/records/%d/suggest", personID), cookie).Body.String()

	assert.Contains(t, body, fmt.Sprintf(`name="contact_%d"`, contactID),
		"each contact detail is its own field")
	assert.Contains(t, body, `value="dale.old@example.test"`,
		"and it is filled with what the club currently holds, so a member edits rather than retypes")
	assert.Contains(t, body, `name="display_name"`)
	assert.Contains(t, body, `name="call_sign"`)

	assert.NotContains(t, body, `name="target"`,
		"nothing asks the member which field they mean")
	assert.NotContains(t, body, `name="contact_id"`,
		"and no separate chooser governs the contact fields")
}

func TestANoteIsOptionalOnAnOrdinaryCorrection(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, personID := e.eligibleMember(t)

	w := e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), url.Values{
		"display_name": {"Dale Rutherford"},
		"call_sign":    {"W3NEW"},
		// No note: "my call sign should be W3NEW" says everything.
	}, cookie)

	require.Equal(t, http.StatusSeeOther, w.Code,
		"a correction with a changed value and no note must be accepted: %s", w.Body.String())

	var value, summary string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT i.proposed_value, COALESCE(r.summary, '')
		   FROM member_change_request_items i
		   JOIN member_change_requests r ON r.id = i.request_id
		  ORDER BY i.id DESC LIMIT 1`).Scan(&value, &summary))
	assert.Equal(t, "W3NEW", value)
	assert.NotEmpty(t, summary,
		"the member wrote no note, so the form composes the line an officer triages from")
	assert.Contains(t, summary, "call sign",
		"and that line names what changed")
}

// Only the fields the member actually changed become proposals. The form posts
// all of them, and asking an officer to approve a record's current values back
// onto itself is both noise and a stale-version conflict waiting to happen.
func TestUnchangedFieldsProposeNothing(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, personID := e.eligibleMember(t)
	contactID := e.seedContact(t, personID, "phone", "814-555-0113")

	form := e.getAs(t, fmt.Sprintf("/member/records/%d/suggest", personID), cookie)
	require.Equal(t, http.StatusOK, form.Code)

	// Everything posted back as it stands, except one telephone number.
	w := e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), url.Values{
		"display_name":                       {"Dale Rutherford"},
		"call_sign":                          {"W3DLR"},
		fmt.Sprintf("contact_%d", contactID): {"814-555-0199"},
	}, cookie)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	var operations []string
	rows, err := e.h.db.Query(
		`SELECT operation FROM member_change_request_items ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var op string
		require.NoError(t, rows.Scan(&op))
		operations = append(operations, op)
	}
	require.NoError(t, rows.Err())

	assert.Equal(t, []string{"contact_method.update"}, operations,
		"only the telephone number changed, so it is the only thing proposed")
}

// A form that changes nothing and says nothing is refused. A form that changes
// nothing but carries a note is the "please add my new work number" case, which
// the form deliberately cannot do itself, so it travels as words instead.
func TestAnEmptySubmissionIsRefusedButANoteAloneIsNot(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, personID := e.eligibleMember(t)

	unchanged := url.Values{"display_name": {"Dale Rutherford"}, "call_sign": {"W3DLR"}}
	w := e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), unchanged, cookie)
	require.Equal(t, http.StatusBadRequest, w.Code,
		"a submission proposing nothing and saying nothing must be refused")
	assert.Contains(t, w.Body.String(), "Change something on the form",
		"the refusal must name what to supply")

	withNote := url.Values{
		"display_name": {"Dale Rutherford"},
		"call_sign":    {"W3DLR"},
		"note":         {"Please add my new work number, 814-555-0177."},
	}
	w = e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), withNote, cookie)
	require.Equal(t, http.StatusSeeOther, w.Code,
		"a note with no field edits is a real request: %s", w.Body.String())

	var operation, summary string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT i.operation, r.summary
		   FROM member_change_request_items i
		   JOIN member_change_requests r ON r.id = i.request_id
		  ORDER BY i.id DESC LIMIT 1`).Scan(&operation, &summary))
	assert.Equal(t, changerequests.OpOther, operation,
		"it reaches the queue as a note rather than as a change nobody can apply")
	assert.Contains(t, summary, "new work number",
		"and the member's own words are what an officer reads")
}

// TestAMemberCannotCorrectSomeoneElsesContactThroughTheirOwnForm asserts the
// outcome, and deliberately not which layer produces it.
//
// Two independent checks refuse this: the form maps the posted target against
// the contacts the member actually holds, and the domain refuses an item naming
// a contact that is not the target person's. Removing the form's check leaves
// this test passing, which is worth knowing — the form's check exists to give
// an ordinary "choose what needs correcting" rather than a domain error, not to
// be the thing standing between a member and someone else's telephone number.
func TestAMemberCannotCorrectSomeoneElsesContactThroughTheirOwnForm(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, personID := e.eligibleMember(t)

	stranger := e.dirPerson(t, "Someone Else", "W3SEL", "full")
	strangerContact := e.seedContact(t, stranger, "phone", "540-555-0199")

	form := e.getAs(t, fmt.Sprintf("/member/records/%d/suggest", personID), cookie).Body.String()
	assert.NotContains(t, form, "540-555-0199",
		"the form shows only the contacts of the record it is for")
	assert.NotContains(t, form, fmt.Sprintf(`name="contact_%d"`, strangerContact),
		"and offers no field aimed at anyone else's")

	w := e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), url.Values{
		"display_name": {"Dale Rutherford"},
		"call_sign":    {"W3DLR"},
		fmt.Sprintf("contact_%d", strangerContact): {"540-555-0000"},
	}, cookie)

	require.Equal(t, http.StatusBadRequest, w.Code,
		"the posted field names no contact this record holds, so the submission proposes nothing")
	assert.NotContains(t, w.Body.String(), "540-555-0199",
		"and the refusal must not echo a stranger's telephone number back")

	var items int
	require.NoError(t, e.h.db.QueryRow(
		`SELECT count(*) FROM member_change_request_items WHERE target_id = ?`, strangerContact).Scan(&items))
	assert.Zero(t, items)
}

// seedPerson inserts a bare person for an officer-side form test.
func seedPerson(t *testing.T, e *testEnv, name string) int64 {
	t.Helper()
	res, err := e.h.db.Exec(
		`INSERT INTO persons (display_name, sort_name) VALUES (?, ?)`, name, name)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

// A mailing address is recorded in parts (bcars-portal-a9w). The columns and
// the Groups.io import have always written them; the officer UI offered one
// free-text box, so an address typed by an officer was a single line while an
// imported one was structured.

func TestAnAddressIsStoredInParts(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")
	personID := seedPerson(t, e, "Ada Lovelace")

	w := e.postForm(t, fmt.Sprintf("/admin/members/%d/address/new", personID), url.Values{
		"label":       {"Home"},
		"line1":       {"1234 Any Street"},
		"city":        {"Everett"},
		"state":       {"PA"},
		"postal_code": {"15537"},
		"country":     {"United States"},
		"is_primary":  {"1"},
	})
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	var line1, city, state, postal, country, raw string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT COALESCE(postal_line1,''), COALESCE(postal_city,''), COALESCE(postal_state,''),
		        COALESCE(postal_postal_code,''), COALESCE(postal_country,''), value_raw
		   FROM contact_methods WHERE person_id = ? AND kind = 'postal'`, personID).
		Scan(&line1, &city, &state, &postal, &country, &raw))

	assert.Equal(t, "1234 Any Street", line1)
	assert.Equal(t, "Everett", city)
	assert.Equal(t, "PA", state)
	assert.Equal(t, "15537", postal)
	assert.Equal(t, "United States", country)

	// The one-line reading is what existing surfaces render, and it omits the
	// default country rather than ending every Pennsylvania address with it.
	assert.Equal(t, "1234 Any Street, Everett, PA, 15537", raw)
}

func TestTheCountryBoxStartsOnTheClubsOwn(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")
	personID := seedPerson(t, e, "Ada Lovelace")

	body := e.body(t, "GET", fmt.Sprintf("/admin/members/%d/address/new", personID), "")

	assert.Contains(t, body, `value="United States"`,
		"the country should arrive filled in, since almost every member is here")
}

// TestAPartialAddressIsAccepted covers the case the club's own export contains:
// a member with a town and no street. A form that refused those could not
// record what the club knows.
func TestAPartialAddressIsAccepted(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")
	personID := seedPerson(t, e, "Ada Lovelace")

	w := e.postForm(t, fmt.Sprintf("/admin/members/%d/address/new", personID), url.Values{
		"city":    {"Bedford"},
		"state":   {"PA"},
		"country": {"United States"},
	})
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	var raw string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT value_raw FROM contact_methods WHERE person_id = ? AND kind = 'postal'`, personID).Scan(&raw))
	assert.Equal(t, "Bedford, PA", raw,
		"a town with no street must not read as a leading comma")
}

// TestAnEmptyAddressIsRefused: the country arrives pre-filled, so a form
// submitted untouched would otherwise store "United States" as an address.
func TestAnEmptyAddressIsRefused(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")
	personID := seedPerson(t, e, "Ada Lovelace")

	w := e.postForm(t, fmt.Sprintf("/admin/members/%d/address/new", personID), url.Values{
		"country": {"United States"},
	})
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Contains(t, w.Body.String(), "Give at least a street")

	var count int
	require.NoError(t, e.h.db.QueryRow(
		`SELECT COUNT(*) FROM contact_methods WHERE person_id = ?`, personID).Scan(&count))
	assert.Zero(t, count, "nothing should have been stored")
}

// TestTheContactFormNoLongerOffersPostal: an address typed into a single value
// box is the state this bead exists to end, so the box must stop offering it.
func TestTheContactFormNoLongerOffersPostal(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")
	personID := seedPerson(t, e, "Ada Lovelace")

	body := e.body(t, "GET", fmt.Sprintf("/admin/members/%d/contacts/new", personID), "")

	assert.NotContains(t, body, `value="postal"`,
		"a postal address has its own form; offering it here records one unstructured line")
}

// The member surface and the officer surface have to agree on how a proposed
// contact value is written down (bcars-portal-b4d).
//
// They did not. The member form stored the bare value the member typed; the
// review path reads a contact value as "kind:value" and refused anything else,
// so EVERY contact correction a member sent was rejected at approval time with
// "the proposed value is not valid for this kind of change" -- and the officer
// could not repair it, because the review screen has no editable value.
//
// Every existing test missed it because they seed the request through the
// domain service and approve it there. The defect lived in the gap between the
// two surfaces, so this test crosses that gap: it posts the member's form and
// then posts the officer's decision, and asserts on the contact row.
func TestAMemberContactCorrectionCanBeApprovedByAnOfficer(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, personID := e.eligibleMember(t)
	contactID := e.seedContact(t, personID, "phone", "814-555-0113")

	w := e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), url.Values{
		"display_name":                       {"Dale Rutherford"},
		"call_sign":                          {"W3DLR"},
		fmt.Sprintf("contact_%d", contactID): {"814-555-0199"},
		"note":                               {"New mobile number since June."},
	}, cookie)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	var requestID, itemID int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT request_id, id FROM member_change_request_items ORDER BY id DESC LIMIT 1`).
		Scan(&requestID, &itemID))

	officer := e.officerCookie(t)
	path := fmt.Sprintf("%s/%d/items/%d/decision", RouteAdminRequests, requestID, itemID)
	w = e.post(t, path, url.Values{"decision": {"approved"}}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())
	assert.NotContains(t, w.Header().Get("Location"), "error=",
		"an officer approving a correction the portal's own form produced must not be refused")

	var stored string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT value_raw FROM contact_methods WHERE id = ?`, contactID).Scan(&stored))
	assert.Equal(t, "814-555-0199", stored,
		"the approved value reaches the contact the member named")
}

// The encoding the applier needs is not something either reader should see.
func TestTheContactKindEncodingNeverReachesAScreen(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, personID := e.eligibleMember(t)
	contactID := e.seedContact(t, personID, "phone", "814-555-0113")

	w := e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), url.Values{
		"display_name":                       {"Dale Rutherford"},
		"call_sign":                          {"W3DLR"},
		fmt.Sprintf("contact_%d", contactID): {"814-555-0199"},
		"note":                               {"New mobile number since June."},
	}, cookie)
	require.Equal(t, http.StatusSeeOther, w.Code)
	detail := w.Header().Get("Location")

	var requestID int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT request_id FROM member_change_request_items ORDER BY id DESC LIMIT 1`).Scan(&requestID))

	member := e.getAs(t, detail, cookie).Body.String()
	assert.Contains(t, member, "814-555-0199", "the member sees the value they proposed")
	assert.NotContains(t, member, "phone:814-555-0199",
		"and never the storage encoding")

	officer := e.getAs(t, fmt.Sprintf("%s/%d", RouteAdminRequests, requestID), e.officerCookie(t)).Body.String()
	assert.Contains(t, officer, "814-555-0199", "the officer sees the value under review")
	assert.NotContains(t, officer, "phone:814-555-0199",
		"and never the storage encoding")
	assert.Contains(t, officer, "phone",
		"but is told which kind of detail the correction is for")
}

// A report about a record the member cannot see is a NOTE (ADR-0014.4,
// bcars-portal-ssz.4).
//
// It used to offer the same field list as the own-record form and produce an
// item with no target, which nothing could ever apply: an officer who linked
// the request was told to link the request (bcars-portal-3la). The words were
// always the useful part, so the words are the whole of it.
func TestAReportAboutSomeoneElseIsANote(t *testing.T) {
	e := setupMemberEnv(t)
	cookie := e.signInMember(t)

	form := e.getAs(t, RouteMemberSuggest, cookie).Body.String()
	assert.NotContains(t, form, `name="kind"`,
		"there is no field to choose: the note is the submission")
	assert.NotContains(t, form, `name="proposed_value"`,
		"and nothing structured to propose")
	assert.Contains(t, form, `name="about_name"`,
		"who it is about is still the member's own words")
	assert.Contains(t, form, `name="summary"`)

	w := e.post(t, RouteMemberSuggest, url.Values{
		"about_name": {"Marguerite Ashby"},
		"summary":    {"Her mobile number has changed to 814-555-0177."},
	}, cookie)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	var operation, proposed string
	var targetKind sql.NullString
	require.NoError(t, e.h.db.QueryRow(
		`SELECT operation, COALESCE(proposed_value, ''), target_kind
		   FROM member_change_request_items ORDER BY id DESC LIMIT 1`).
		Scan(&operation, &proposed, &targetKind))
	assert.Equal(t, changerequests.OpOther, operation,
		"a note carries nothing an adapter could apply")
	assert.Empty(t, proposed)
	assert.False(t, targetKind.Valid, "and names no record, which is the whole point")
}

// The note is the submission, so an empty one is refused.
func TestANoteAboutSomeoneElseNeedsWords(t *testing.T) {
	e := setupMemberEnv(t)
	cookie := e.signInMember(t)

	w := e.post(t, RouteMemberSuggest, url.Values{
		"about_name": {"Marguerite Ashby"},
	}, cookie)
	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "Tell the officers what they should know")

	var items int
	require.NoError(t, e.h.db.QueryRow(
		`SELECT count(*) FROM member_change_request_items`).Scan(&items))
	assert.Zero(t, items)
}

// A note has no items to count down, so the rule that resolves a request when
// its pending items reach zero must not fire on arrival. Nothing calls that
// helper for notes today; this fails if anything starts to (bcars-portal-ssz.6).
func TestANewNoteIsNotResolvedOnArrival(t *testing.T) {
	e := setupMemberEnv(t)
	cookie := e.signInMember(t)

	w := e.post(t, RouteMemberSuggest, url.Values{
		"about_name": {"Marguerite Ashby"},
		"summary":    {"Her mobile number has changed."},
	}, cookie)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	var status string
	var resolvedAt sql.NullString
	require.NoError(t, e.h.db.QueryRow(
		`SELECT status, resolved_at FROM member_change_requests ORDER BY id DESC LIMIT 1`).
		Scan(&status, &resolvedAt))
	assert.Equal(t, "submitted", status,
		"a note an officer has not read is not done")
	assert.False(t, resolvedAt.Valid)
}

// Submitting a note still tells the sender nothing about the person named.
func TestSendingANoteStillRevealsNothing(t *testing.T) {
	e := setupMemberEnv(t)
	e.seedPersonRow(t, "Marguerite Ashby", "W3MGA")
	cookie := e.signInMember(t)

	w := e.post(t, RouteMemberSuggest, url.Values{
		"about_name":      {"Marguerite Ashby"},
		"about_call_sign": {"W3MGA"},
		"summary":         {"Her mobile number has changed."},
	}, cookie)
	require.Equal(t, http.StatusSeeOther, w.Code)

	body := e.getAs(t, w.Header().Get("Location"), cookie).Body.String()
	assert.Contains(t, body, "Marguerite Ashby", "their own words read back to them")
	assert.NotContains(t, body, "W3MGA - ",
		"but nothing canonical about her is echoed")
	assert.NotContains(t, body, "member record",
		"and no hint that a record exists")
}
