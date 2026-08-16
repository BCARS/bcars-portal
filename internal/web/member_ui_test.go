package web

import (
	"context"
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
	assert.Contains(t, form.Body.String(), "An officer reviews every suggestion",
		"the form must say review is required before anything changes")
	assert.Contains(t, form.Body.String(), "dale.old@example.test",
		"the member picks which of their own details is wrong")

	w := e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), url.Values{
		"target":         {fmt.Sprintf("contact:%d", contactID)},
		"proposed_value": {"dale.new@example.test"},
		"summary":        {"My email address changed last month."},
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

	w := e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), url.Values{
		"target":         {"contact_method.update"},
		"contact_id":     {fmt.Sprint(strangerContact)},
		"proposed_value": {"+15555550199"},
		"summary":        {"Fix this number."},
	}, cookie)
	assert.Equal(t, http.StatusBadRequest, w.Code,
		"an item may only name a contact on the record the suggestion is about")

	var items int
	require.NoError(t, e.h.db.QueryRow(
		`SELECT count(*) FROM member_change_request_items WHERE target_id = ?`, strangerContact).Scan(&items))
	assert.Zero(t, items, "and nothing may be stored pointing at it")
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
	assert.Contains(t, body, "Their name is wrong",
		"a delegate corrects someone else's record, and the form must say so")
	assert.NotContains(t, body, "My name is wrong")
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
		"target":         {"person.call_sign.set"},
		"proposed_value": {"W3NEW"},
		"summary":        {"My call sign changed."},
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
		"target":         {"person.display_name.set"},
		"proposed_value": {"Dale Rutherforde"},
		"summary":        {"My name is spelled wrong."},
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
		"target":         {"person.display_name.set"},
		"proposed_value": {"Dale Rutherforde"},
		"summary":        {"My name is spelled wrong."},
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

// The correction form asks one question (bcars-portal-245). It used to ask two
// and only mean one: radios chose what needs correcting, and a separate
// "which contact detail?" dropdown sat below them, always visible and governed
// by nothing — so a member choosing "My name is wrong" was shown a live list of
// their own telephone numbers to pick from.

func TestTheCorrectionFormAsksOneQuestion(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, personID := e.eligibleMember(t)
	contactID := e.seedContact(t, personID, "email", "dale.old@example.test")

	body := e.getAs(t, fmt.Sprintf("/member/records/%d/suggest", personID), cookie).Body.String()

	// Each contact detail is one of the choices, named and shown.
	assert.Contains(t, body, fmt.Sprintf(`value="contact:%d"`, contactID),
		"the member's email should be one of the things they can say is wrong")
	assert.Contains(t, body, "dale.old@example.test",
		"a choice about a contact detail should show which detail it means")

	// And there is no second, ungoverned chooser.
	assert.NotContains(t, body, `name="contact_id"`,
		"the contact dropdown should be gone; the radio carries which detail it is")
	assert.NotContains(t, body, "only for a contact correction",
		"a hint apologising for a control that does not apply means the control is wrong")
}

func TestANoteIsOptionalOnAnOrdinaryCorrection(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, personID := e.eligibleMember(t)

	w := e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), url.Values{
		"target":         {"person.call_sign.set"},
		"proposed_value": {"W3NEW"},
		// No summary: "my call sign should be W3NEW" says everything.
	}, cookie)

	require.Equal(t, http.StatusSeeOther, w.Code,
		"a correction with a value and no note must be accepted: %s", w.Body.String())

	// And it reaches an officer intact.
	var value, summary string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT i.proposed_value, COALESCE(r.summary, '')
		   FROM member_change_request_items i
		   JOIN member_change_requests r ON r.id = i.request_id
		  ORDER BY i.id DESC LIMIT 1`).Scan(&value, &summary))
	assert.Equal(t, "W3NEW", value)
	// The member wrote no note, so the form composed the line an officer
	// triages from rather than storing a blank one.
	assert.Contains(t, summary, "W3NEW")
	assert.Contains(t, strings.ToLower(summary), "call sign")
}

// TestSomethingElseStillNeedsItsNote is the other half. The catch-all carries no
// value, so the note is the whole of what an officer would read.
func TestSomethingElseStillNeedsItsNote(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, personID := e.eligibleMember(t)

	w := e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), url.Values{
		"target": {changerequests.OpOther},
	}, cookie)

	require.Equal(t, http.StatusBadRequest, w.Code,
		"a submission saying nothing at all must be refused")
	assert.Contains(t, w.Body.String(), "Tell the officers what should change",
		"the refusal must name what to supply")
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

	w := e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), url.Values{
		"target":         {fmt.Sprintf("contact:%d", strangerContact)},
		"proposed_value": {"540-555-0000"},
	}, cookie)

	require.Equal(t, http.StatusBadRequest, w.Code,
		"a contact the member does not hold is not theirs to correct here")
	assert.NotContains(t, w.Body.String(), "540-555-0199",
		"the refusal must not echo a stranger's telephone number back")
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
		"target":         {fmt.Sprintf("contact:%d", contactID)},
		"proposed_value": {"814-555-0199"},
		"summary":        {"New mobile number since June."},
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
		"target":         {fmt.Sprintf("contact:%d", contactID)},
		"proposed_value": {"814-555-0199"},
		"summary":        {"New mobile number since June."},
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
