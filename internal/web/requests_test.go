package web

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/domain/changerequests"
	"github.com/bcars/bcars-portal/internal/domain/memberaccess"
	"github.com/bcars/bcars-portal/internal/domain/relationships"
)

// The officer request-review UI (bcars-portal-4ux.10).
//
// These drive the pages an officer clicks. The API tests already hold the
// review rules at the JSON boundary; what a page can still get wrong is
// different — offering a control the caller may not use, rendering a member's
// typed text as markup, or letting a stale form silently overwrite a colleague's
// decision — so these assert on rendered HTML and on what the database holds
// afterwards.

// officerPrincipal is the signed-in secretary, for setup that goes through the
// domain services rather than the pages under test.
func (e *memberTestEnv) officerPrincipal() *authz.Principal {
	return &authz.Principal{UserID: e.officerUserID}
}

// seedOfficerRequest records an officer-entered request the way the intake API
// does, so the queue has something that did not come from a member.
func (e *memberTestEnv) seedOfficerRequest(t *testing.T, summary string, personID int64) changerequests.Request {
	t.Helper()
	req, err := e.h.changeRequests.Create(context.Background(), e.officerPrincipal(),
		changerequests.CreateParams{
			Source:         changerequests.SourceOfficerPhone,
			TargetPersonID: personID,
			Summary:        summary,
			Items: []changerequests.ItemInput{{
				Operation: changerequests.OpOther, ProposedValue: "noted at the meeting",
			}},
		}, fmt.Sprintf("officer-%s", summary), time.Now().UTC())
	require.NoError(t, err)
	return req
}

// seedMemberHint records the cross-member case: a member describing somebody in
// their own words, with nothing confirmed and no target attached.
func (e *memberTestEnv) seedMemberHint(t *testing.T, name, summary string) changerequests.Request {
	t.Helper()
	req, err := e.h.changeRequests.Create(context.Background(),
		&authz.Principal{UserID: e.memberUserID},
		changerequests.CreateParams{
			Source:          changerequests.SourceMember,
			RequesterUserID: e.memberUserID,
			SuppliedName:    name,
			Summary:         summary,
			Items: []changerequests.ItemInput{{
				Operation: "person.call_sign.set", ProposedValue: "W3NEW",
			}},
		}, fmt.Sprintf("hint-%s", name), time.Now().UTC())
	require.NoError(t, err)
	return req
}

// seedOwnRequest records the self-correction case: a member working on a record
// they were granted, so the request arrives already carrying its target.
func (e *memberTestEnv) seedOwnRequest(t *testing.T, personID int64, value string) changerequests.Request {
	t.Helper()
	req, err := e.h.changeRequests.Create(context.Background(),
		&authz.Principal{UserID: e.memberUserID},
		changerequests.CreateParams{
			Source:          changerequests.SourceMember,
			RequesterUserID: e.memberUserID,
			TargetPersonID:  personID,
			Summary:         "My name is spelled wrong",
			Items: []changerequests.ItemInput{{
				Operation:     "person.display_name.set",
				ProposedValue: value,
				TargetKind:    "person",
				TargetID:      personID,
			}},
		}, fmt.Sprintf("own-%d-%s", personID, value), time.Now().UTC())
	require.NoError(t, err)
	return req
}

// TestQueueHoldsEveryChannelAndTellsThemApart is the queue's whole job: one
// list, several origins, and an officer able to see which is which.
func TestQueueHoldsEveryChannelAndTellsThemApart(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.grant(t, "Dale Rutherford")

	e.seedOwnRequest(t, personID, "Dale Rutherford Jr")
	e.seedMemberHint(t, "Marguerite from the Tuesday net", "Her call sign is printed wrong")
	e.seedOfficerRequest(t, "Called in a new address", personID)

	body := e.getAs(t, RouteAdminRequests, officer).Body.String()

	assert.Contains(t, body, "Own record",
		"a member correcting a record they hold must be distinguishable")
	assert.Contains(t, body, "About another person",
		"a cross-member hint must be distinguishable")
	assert.Contains(t, body, "Officer-entered")
	assert.Contains(t, body, "Marguerite from the Tuesday net",
		"the member's own words identify an unlinked request")
	assert.Contains(t, body, "As described by the sender; not yet linked",
		"an unlinked description must not read as a fact about the club's records")
}

// TestQueueFiltersNarrowTheList covers the controls an officer works the queue
// with, including the triage filter that is the reason the filter exists.
func TestQueueFiltersNarrowTheList(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.grant(t, "Dale Rutherford")

	e.seedOfficerRequest(t, "Called in a new address", personID)
	e.seedMemberHint(t, "Someone At The Hamfest", "Their call sign is wrong")

	memberOnly := e.getAs(t, RouteAdminRequests+"?source=member", officer).Body.String()
	assert.Contains(t, memberOnly, "Someone At The Hamfest")
	assert.NotContains(t, memberOnly, "Called in a new address")

	unlinked := e.getAs(t, RouteAdminRequests+"?unlinked=1", officer).Body.String()
	assert.Contains(t, unlinked, "Someone At The Hamfest",
		"the triage filter shows what still needs linking")
	assert.NotContains(t, unlinked, "Called in a new address",
		"a request that already names a record is not awaiting triage")
}

// TestQueueNeverOffersAnAnonymousChannel guards the vocabulary. The anonymous
// public channel was planned and withdrawn (ADR-0013), and a UI that still
// spoke of one would be describing a queue the club does not have.
func TestQueueNeverOffersAnAnonymousChannel(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.grant(t, "Dale Rutherford")
	e.seedOfficerRequest(t, "Called in a new address", personID)

	body := e.getAs(t, RouteAdminRequests, officer).Body.String()
	for _, word := range []string{"anonymous", "Anonymous", "public", "Public"} {
		assert.NotContains(t, body, word,
			"the queue must not describe an intake channel that does not exist")
	}
}

// TestOfficerLinksAHintWithoutRewritingIt is triage. The officer resolves who
// the request is about; what the member wrote stays on the record beside it.
func TestOfficerLinksAHintWithoutRewritingIt(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	target := e.seedPersonRow(t, "Marguerite Ashby", "W3MGA")
	req := e.seedMemberHint(t, "Marguerite from the Tuesday net", "Her call sign is printed wrong")

	w := e.post(t, fmt.Sprintf("%s/%d/target", RouteAdminRequests, req.ID), url.Values{
		"target_person_id": {fmt.Sprint(target)},
		"version":          {fmt.Sprint(req.Version)},
	}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code)

	body := e.getAs(t, fmt.Sprintf("%s/%d", RouteAdminRequests, req.ID), officer).Body.String()
	assert.Contains(t, body, "Marguerite Ashby", "the linked record is named")
	assert.Contains(t, body, "Marguerite from the Tuesday net",
		"what the member supplied must survive the officer's conclusion")

	var supplied string
	var targetID int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT supplied_name, target_person_id FROM member_change_requests WHERE id = ?`,
		req.ID).Scan(&supplied, &targetID))
	assert.Equal(t, "Marguerite from the Tuesday net", supplied)
	assert.Equal(t, target, targetID)
}

// TestTriageRefusesAStaleForm is the conflict path. Two officers working the
// same queue at one meeting is ordinary; the second must be told rather than
// silently win.
func TestTriageRefusesAStaleForm(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	first := e.seedPersonRow(t, "Marguerite Ashby", "W3MGA")
	second := e.seedPersonRow(t, "Someone Else Entirely", "W3ELS")
	req := e.seedMemberHint(t, "Marguerite from the net", "Call sign wrong")

	staleVersion := req.Version

	w := e.post(t, fmt.Sprintf("%s/%d/target", RouteAdminRequests, req.ID), url.Values{
		"target_person_id": {fmt.Sprint(first)}, "version": {fmt.Sprint(staleVersion)},
	}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code)

	// The second officer's page was rendered before the first saved.
	w = e.post(t, fmt.Sprintf("%s/%d/target", RouteAdminRequests, req.ID), url.Values{
		"target_person_id": {fmt.Sprint(second)}, "version": {fmt.Sprint(staleVersion)},
	}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code)
	assert.Contains(t, w.Header().Get("Location"), "Another+officer+changed+this")

	var targetID int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT target_person_id FROM member_change_requests WHERE id = ?`, req.ID).Scan(&targetID))
	assert.Equal(t, first, targetID, "the stale write must not have landed")
}

// TestApprovalAppliesOnceAndARepeatIsSafe covers the acceptance criterion that
// an eligible request applies exactly once. A browser back-and-resubmit is the
// ordinary way an officer sends the same decision twice.
func TestApprovalAppliesOnceAndARepeatIsSafe(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.grant(t, "Dale Rutherford")
	req := e.seedOwnRequest(t, personID, "Dale Rutherford Jr")
	item := req.Items[0]

	path := fmt.Sprintf("%s/%d/items/%d/decision", RouteAdminRequests, req.ID, item.ID)
	w := e.post(t, path, url.Values{"decision": {"approved"}}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code)
	assert.Contains(t, w.Header().Get("Location"), "applied")

	var name string
	var version int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT display_name, version FROM persons WHERE id = ?`, personID).Scan(&name, &version))
	assert.Equal(t, "Dale Rutherford Jr", name, "an approved correction reaches the record")

	// Resubmitting the identical decision returns the recorded outcome rather
	// than applying it a second time.
	w = e.post(t, path, url.Values{"decision": {"approved"}}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code)
	assert.Contains(t, w.Header().Get("Location"), "already+decided")

	var after int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT version FROM persons WHERE id = ?`, personID).Scan(&after))
	assert.Equal(t, version, after, "a repeated approval must not write the record again")
}

// TestChangingADecisionIsRefused keeps a decided item decided.
func TestChangingADecisionIsRefused(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.grant(t, "Dale Rutherford")
	req := e.seedOwnRequest(t, personID, "Dale Rutherford Jr")
	path := fmt.Sprintf("%s/%d/items/%d/decision", RouteAdminRequests, req.ID, req.Items[0].ID)

	require.Equal(t, http.StatusSeeOther,
		e.post(t, path, url.Values{"decision": {"approved"}}, officer).Code)

	w := e.post(t, path, url.Values{"decision": {"rejected"}, "reason": {"changed my mind"}}, officer)
	assert.Contains(t, w.Header().Get("Location"), "already+decided")

	var status string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT status FROM member_change_request_items WHERE id = ?`, req.Items[0].ID).Scan(&status))
	assert.Equal(t, changerequests.ItemApproved, status)
}

// TestRejectionNeedsAReason keeps the record of why something was refused.
func TestRejectionNeedsAReason(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.grant(t, "Dale Rutherford")
	req := e.seedOwnRequest(t, personID, "Dale Rutherford Jr")
	path := fmt.Sprintf("%s/%d/items/%d/decision", RouteAdminRequests, req.ID, req.Items[0].ID)

	w := e.post(t, path, url.Values{"decision": {"rejected"}}, officer)
	assert.Contains(t, w.Header().Get("Location"), "Give+a+reason")

	var status string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT status FROM member_change_request_items WHERE id = ?`, req.Items[0].ID).Scan(&status))
	assert.Equal(t, changerequests.ItemPending, status, "nothing was decided")

	w = e.post(t, path, url.Values{"decision": {"rejected"}, "reason": {"Name is right as recorded"}}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.NoError(t, e.h.db.QueryRow(
		`SELECT status FROM member_change_request_items WHERE id = ?`, req.Items[0].ID).Scan(&status))
	assert.Equal(t, changerequests.ItemRejected, status)
}

// TestOfficerCannotApproveTheirOwnSensitiveRequest is the self-review rule at
// the page level. An officer who is also a member may submit a suggestion; a
// sensitive one still needs somebody else's judgement.
func TestOfficerCannotApproveTheirOwnSensitiveRequest(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.grant(t, "Dale Rutherford")

	_, err := e.h.db.Exec(
		`INSERT INTO contact_methods (person_id, kind, value_raw, value_norm)
		 VALUES (?, 'email', 'dale@example.test', 'dale@example.test')`, personID)
	require.NoError(t, err)
	var contactID, contactVersion int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT id, version FROM contact_methods WHERE person_id = ?`, personID).
		Scan(&contactID, &contactVersion))

	// The officer submits it themselves, which is what makes it self-review.
	req, err := e.h.changeRequests.Create(context.Background(), e.officerPrincipal(),
		changerequests.CreateParams{
			Source:          changerequests.SourceMember,
			RequesterUserID: e.officerUserID,
			TargetPersonID:  personID,
			Summary:         "Hide my email from the directory",
			Items: []changerequests.ItemInput{{
				Operation:     "contact_method.visibility.set",
				ProposedValue: "officers",
				TargetKind:    "contact_method",
				TargetID:      contactID,
				TargetVersion: contactVersion,
			}},
		}, "self-review-case", time.Now().UTC())
	require.NoError(t, err)

	w := e.post(t, fmt.Sprintf("%s/%d/items/%d/decision", RouteAdminRequests, req.ID, req.Items[0].ID),
		url.Values{"decision": {"approved"}, "verification_note": {"it is me"}}, officer)
	assert.Contains(t, w.Header().Get("Location"), "another+officer+must+approve")

	var status string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT status FROM member_change_request_items WHERE id = ?`, req.Items[0].ID).Scan(&status))
	assert.Equal(t, changerequests.ItemPending, status)
}

// TestSensitiveApprovalNeedsAVerificationNote covers the other sensitive-path
// guard from the review page.
func TestSensitiveApprovalNeedsAVerificationNote(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.grant(t, "Dale Rutherford")

	_, err := e.h.db.Exec(
		`INSERT INTO contact_methods (person_id, kind, value_raw, value_norm)
		 VALUES (?, 'email', 'dale@example.test', 'dale@example.test')`, personID)
	require.NoError(t, err)
	var contactID, contactVersion int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT id, version FROM contact_methods WHERE person_id = ?`, personID).
		Scan(&contactID, &contactVersion))

	req, err := e.h.changeRequests.Create(context.Background(),
		&authz.Principal{UserID: e.memberUserID},
		changerequests.CreateParams{
			Source:          changerequests.SourceMember,
			RequesterUserID: e.memberUserID,
			TargetPersonID:  personID,
			Summary:         "Hide my email from the directory",
			Items: []changerequests.ItemInput{{
				Operation:     "contact_method.visibility.set",
				ProposedValue: "officers",
				TargetKind:    "contact_method",
				TargetID:      contactID,
				TargetVersion: contactVersion,
			}},
		}, "verification-case", time.Now().UTC())
	require.NoError(t, err)
	path := fmt.Sprintf("%s/%d/items/%d/decision", RouteAdminRequests, req.ID, req.Items[0].ID)

	w := e.post(t, path, url.Values{"decision": {"approved"}}, officer)
	assert.Contains(t, w.Header().Get("Location"), "Say+how+you+verified")

	w = e.post(t, path, url.Values{
		"decision": {"approved"}, "verification_note": {"called the published number back"},
	}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code)

	var note string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT coalesce(verification_note, '') FROM member_change_request_items WHERE id = ?`,
		req.Items[0].ID).Scan(&note))
	assert.Equal(t, "called the published number back", note)
}

// TestSuppliedTextIsRenderedAsText is the escaping case. Everything on the
// review page came from somebody typing into a form, including the parts that
// look like fields.
func TestSuppliedTextIsRenderedAsText(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)

	const nasty = `<script>alert('x')</script>`
	req := e.seedMemberHint(t, nasty, "Summary "+nasty)

	w := e.getAs(t, fmt.Sprintf("%s/%d", RouteAdminRequests, req.ID), officer)
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	assert.NotContains(t, body, "<script>alert",
		"a member's typing must never reach the officer's page as markup")
	assert.Contains(t, body, "&lt;script&gt;",
		"it is shown, escaped, so an officer can see what was actually sent")
}

// TestReviewControlsNeedTheReviewCapability separates triage from deciding. A
// club may let somebody work the queue without letting them change records.
func TestReviewControlsNeedTheReviewCapability(t *testing.T) {
	e := setupHandlerWithRoles(t)
	_, err := e.h.db.Exec(
		`INSERT INTO user_capability_grants (user_id, capability_code, granted_by, granted_at)
		 VALUES (1, 'change_request.manage', 1, datetime('now'))`)
	require.NoError(t, err)

	res, err := e.h.db.Exec(`INSERT INTO persons (display_name, sort_name) VALUES ('Dale', 'dale')`)
	require.NoError(t, err)
	personID, err := res.LastInsertId()
	require.NoError(t, err)

	req, err := e.h.changeRequests.Create(context.Background(), &authz.Principal{UserID: 1},
		changerequests.CreateParams{
			Source: changerequests.SourceOfficerPhone, TargetPersonID: personID,
			Summary: "Called in a correction",
			Items: []changerequests.ItemInput{{
				Operation: "person.display_name.set", ProposedValue: "Dale Rutherford",
				TargetKind: "person", TargetID: personID,
			}},
		}, "no-review-cap", time.Now().UTC())
	require.NoError(t, err)

	w := e.get(t, fmt.Sprintf("%s/%d", RouteAdminRequests, req.ID))
	require.Equal(t, http.StatusOK, w.Code, "triage access is enough to read the request")
	body := w.Body.String()
	assert.NotContains(t, body, "Record decision",
		"a page must not offer a control that would answer 403")
	assert.Contains(t, body, "Awaiting an officer who may review")

	// And the route refuses it even when posted directly.
	w = e.postForm(t, fmt.Sprintf("%s/%d/items/%d/decision", RouteAdminRequests, req.ID, req.Items[0].ID),
		url.Values{"decision": {"approved"}})
	assert.Equal(t, http.StatusForbidden, w.Code,
		"change_request.manage must not imply change_request.review")
}

// TestRequestPagesRefuseMembersAndStrangers covers authorization and, with it,
// the cross-site case: these forms carry no token because a cross-site POST
// arrives without the SameSite=Lax session cookie and is redirected to sign-in
// rather than acted on.
func TestRequestPagesRefuseMembersAndStrangers(t *testing.T) {
	e := setupMemberEnv(t)
	e.grant(t, "Dale Rutherford")
	member := e.signInMember(t)

	routes := []struct {
		method, path string
		form         url.Values
	}{
		{"GET", RouteAdminRequests, nil},
		{"GET", RouteAdminRequests + "/1", nil},
		{"POST", RouteAdminRequests + "/1/target", url.Values{"target_person_id": {"1"}, "version": {"1"}}},
		{"POST", RouteAdminRequests + "/1/items/1/decision", url.Values{"decision": {"approved"}}},
	}

	for _, rt := range routes {
		var w *http.Response
		if rt.method == "GET" {
			w = e.getAs(t, rt.path, member).Result()
		} else {
			w = e.post(t, rt.path, rt.form, member).Result()
		}
		assert.Equal(t, http.StatusForbidden, w.StatusCode,
			"%s %s must refuse a member", rt.method, rt.path)

		// No session at all: the cross-site POST case.
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

// TestMemberPagesOfferNoReviewControls is the other half: a member must not be
// shown the officer surface even as a link.
func TestMemberPagesOfferNoReviewControls(t *testing.T) {
	e := setupMemberEnv(t)
	personID := e.grant(t, "Dale Rutherford")
	member := e.signInMember(t)
	e.seedOwnRequest(t, personID, "Dale Rutherford Jr")

	for _, page := range []string{RouteMemberHome, RouteMemberRequests} {
		body := e.getAs(t, page, member).Body.String()
		assert.NotContains(t, body, RouteAdminRequests,
			"%s must not link a member to the officer queue", page)
		assert.NotContains(t, body, "Record decision", "%s must offer no review control", page)
	}
}

// TestRelationshipContextIsShownAndGrantsNothing is the bead's standing rule
// rendered: an officer may see the household, and seeing it changes nothing
// about who can read the record.
func TestRelationshipContextIsShownAndGrantsNothing(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.grant(t, "Dale Rutherford")
	spouse := e.seedPersonRow(t, "Marguerite Ashby", "W3MGA")

	rels := relationships.NewService(e.h.db)
	_, err := rels.Create(context.Background(), e.officerPrincipal(), relationships.CreateParams{
		FromPersonID: personID, ToPersonID: spouse,
		Kind: relationships.KindSpousePartner, Context: "Married thirty years.",
	})
	require.NoError(t, err)

	req := e.seedOwnRequest(t, personID, "Dale Rutherford Jr")
	body := e.getAs(t, fmt.Sprintf("%s/%d", RouteAdminRequests, req.ID), officer).Body.String()

	assert.Contains(t, body, "Marguerite Ashby", "the officer sees who is related")
	assert.Contains(t, body, "Household context")
	assert.Contains(t, body, "informational only",
		"the page must say what the context is worth")

	// The relationship created no access, and the page offering it did not either.
	var grants int
	require.NoError(t, e.h.db.QueryRow(
		`SELECT count(*) FROM member_access_grants WHERE person_id = ? AND revoked_at IS NULL`,
		spouse).Scan(&grants))
	assert.Zero(t, grants, "being recorded as a spouse must grant access to nothing")
}

// TestUnknownRequestIsNotFound covers the missing-row path.
func TestUnknownRequestIsNotFound(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	assert.Equal(t, http.StatusNotFound,
		e.getAs(t, RouteAdminRequests+"/999999", officer).Code)
}

// What an officer applied is recorded next to what the member proposed
// (bcars-portal-ssz.1, ADR-0014).
//
// Until ADR-0014 the two could not differ, so proposed_value answered "what
// changed". Once a reviewer may amend a value while approving it, that stops
// being true, and a record of the amendment has to exist before the screen
// that makes them is built.
func TestApplyingRecordsWhatReachedTheRecord(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.grant(t, "Dale Rutherford")
	req := e.seedOwnRequest(t, personID, "Dale Rutherford Jr")

	path := fmt.Sprintf("%s/%d/items/%d/decision", RouteAdminRequests, req.ID, req.Items[0].ID)
	w := e.post(t, path, url.Values{"decision": {"approved"}}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	var applied sql.NullString
	require.NoError(t, e.h.db.QueryRow(
		`SELECT applied_value FROM member_change_request_items WHERE id = ?`,
		req.Items[0].ID).Scan(&applied))
	assert.True(t, applied.Valid,
		"an item applied now records a value, so that NULL keeps meaning 'applied before this was recorded'")
	assert.Equal(t, "Dale Rutherford Jr", applied.String,
		"and the value recorded is the one that reached the record")

	var proposed string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT proposed_value FROM member_change_request_items WHERE id = ?`,
		req.Items[0].ID).Scan(&proposed))
	assert.Equal(t, "Dale Rutherford Jr", proposed,
		"and what the member asked for is not rewritten by applying it")
}

// A call sign is upper-cased on the way in, so the value that reached the
// record is not the string the member typed. That difference is exactly what
// this column exists to carry, and a test asserting the proposal back would
// have passed while recording the wrong thing.
func TestAppliedValueIsWhatWasWrittenNotWhatWasTyped(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.grant(t, "Dale Rutherford")

	req, err := e.h.changeRequests.Create(context.Background(),
		&authz.Principal{UserID: e.memberUserID},
		changerequests.CreateParams{
			Source:          changerequests.SourceMember,
			RequesterUserID: e.memberUserID,
			TargetPersonID:  personID,
			Summary:         "My call sign is wrong",
			Items: []changerequests.ItemInput{{
				Operation:     "person.call_sign.set",
				ProposedValue: "w3xyz",
				TargetKind:    "person",
				TargetID:      personID,
			}},
		}, "callsign-case", time.Now().UTC())
	require.NoError(t, err)

	// A call sign is a sensitive operation, so its approval carries the
	// verification note the policy requires.
	path := fmt.Sprintf("%s/%d/items/%d/decision", RouteAdminRequests, req.ID, req.Items[0].ID)
	w := e.post(t, path, url.Values{
		"decision":          {"approved"},
		"verification_note": {"Checked against the FCC record."},
	}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	var applied, callSign string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT applied_value FROM member_change_request_items WHERE id = ?`,
		req.Items[0].ID).Scan(&applied))
	require.NoError(t, e.h.db.QueryRow(
		`SELECT call_sign FROM persons WHERE id = ?`, personID).Scan(&callSign))

	assert.Equal(t, "W3XYZ", callSign)
	assert.Equal(t, callSign, applied,
		"the recorded value is the one on the record, not the lower-case string the member sent")
}

// A rejected item wrote nothing, so it has nothing to record.
func TestARejectedItemRecordsNoAppliedValue(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.grant(t, "Dale Rutherford")
	req := e.seedOwnRequest(t, personID, "Dale Rutherford Jr")

	path := fmt.Sprintf("%s/%d/items/%d/decision", RouteAdminRequests, req.ID, req.Items[0].ID)
	w := e.post(t, path, url.Values{
		"decision": {"rejected"}, "reason": {"Spoke to Dale; the record is right."},
	}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	var applied sql.NullString
	require.NoError(t, e.h.db.QueryRow(
		`SELECT applied_value FROM member_change_request_items WHERE id = ?`,
		req.Items[0].ID).Scan(&applied))
	assert.False(t, applied.Valid, "nothing was written, so nothing is recorded as written")
}

// The review screen ADR-0014.5 describes: tick what you accept, correct what is
// nearly right, apply once (bcars-portal-ssz.3).
//
// The case that drove it: a member sends an address with one character mistyped.
// The officer can see what was meant. Before this the only exit was to reject it
// and ask them to send the whole thing again.
func TestAnOfficerAmendsAValueAndAppliesItInOneGo(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.grant(t, "Dale Rutherford")
	contactID := e.seedContact(t, personID, "email", "dale.old@example.test")

	cookie := e.signInMember(t)
	w := e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), url.Values{
		"display_name":                       {"Dale Rutherforde"},
		fmt.Sprintf("contact_%d", contactID): {"dale.new@example.testt"},
	}, cookie)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	var requestID int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT MAX(request_id) FROM member_change_request_items`).Scan(&requestID))
	items := map[string]int64{}
	rows, err := e.h.db.Query(
		`SELECT operation, id FROM member_change_request_items WHERE request_id = ?`, requestID)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var op string
		var id int64
		require.NoError(t, rows.Scan(&op, &id))
		items[op] = id
	}
	require.NoError(t, rows.Err())
	require.Len(t, items, 2)

	// The officer sees what the record holds now, beside what was suggested.
	detail := e.getAs(t, fmt.Sprintf("%s/%d", RouteAdminRequests, requestID), officer).Body.String()
	assert.Contains(t, detail, "dale.old@example.test",
		"the reviewer must see the value they are changing away from")
	assert.Contains(t, detail, "On the record now")

	// Tick both, drop the stray character from the email on the way past.
	emailItem := items["contact_method.update"]
	nameItem := items["person.display_name.set"]
	w = e.post(t, fmt.Sprintf("%s/%d/apply", RouteAdminRequests, requestID), url.Values{
		"include":                          {fmt.Sprint(nameItem), fmt.Sprint(emailItem)},
		fmt.Sprintf("value_%d", nameItem):  {"Dale Rutherforde"},
		fmt.Sprintf("value_%d", emailItem): {"dale.new@example.test"},
	}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())
	assert.Contains(t, w.Header().Get("Location"), "success=", w.Header().Get("Location"))

	var name, email string
	require.NoError(t, e.h.db.QueryRow(`SELECT display_name FROM persons WHERE id = ?`, personID).Scan(&name))
	require.NoError(t, e.h.db.QueryRow(`SELECT value_raw FROM contact_methods WHERE id = ?`, contactID).Scan(&email))
	assert.Equal(t, "Dale Rutherforde", name, "both ticked changes reach the record")
	assert.Equal(t, "dale.new@example.test", email,
		"and the email lands as the officer corrected it, not as it was typed")

	// What the member asked for is not rewritten, and what was applied is kept
	// beside it.
	var proposed, applied string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT proposed_value, COALESCE(applied_value, '') FROM member_change_request_items WHERE id = ?`,
		emailItem).Scan(&proposed, &applied))
	assert.Equal(t, "email:dale.new@example.testt", proposed,
		"the member's own words stay on the record")
	assert.Equal(t, "dale.new@example.test", applied,
		"and the officer's correction is recorded as what was applied")
}

// Unticking declines nothing on its own: the item is left pending, because a
// member is entitled to a reason and an empty checkbox is not one.
func TestAnUntickedChangeIsLeftPendingRatherThanRejected(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.grant(t, "Dale Rutherford")
	contactID := e.seedContact(t, personID, "phone", "814-555-0113")

	cookie := e.signInMember(t)
	w := e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), url.Values{
		"display_name":                       {"Dale Rutherforde"},
		fmt.Sprintf("contact_%d", contactID): {"814-555-0199"},
	}, cookie)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	var requestID, nameItem int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT request_id, id FROM member_change_request_items
		  WHERE operation = 'person.display_name.set' ORDER BY id DESC LIMIT 1`).Scan(&requestID, &nameItem))

	w = e.post(t, fmt.Sprintf("%s/%d/apply", RouteAdminRequests, requestID), url.Values{
		"include":                         {fmt.Sprint(nameItem)},
		fmt.Sprintf("value_%d", nameItem): {"Dale Rutherforde"},
	}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	var phone string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT value_raw FROM contact_methods WHERE id = ?`, contactID).Scan(&phone))
	assert.Equal(t, "814-555-0113", phone, "the unticked change did not reach the record")

	var status string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT status FROM member_change_request_items
		  WHERE operation = 'contact_method.update' ORDER BY id DESC LIMIT 1`).Scan(&status))
	assert.Equal(t, "pending", status,
		"an unticked change is still waiting for a decision, not silently refused")

	// Declining the rest is the deliberate act, and it carries the reason.
	w = e.post(t, fmt.Sprintf("%s/%d/decline", RouteAdminRequests, requestID), url.Values{
		"reason": {"Spoke to Dale at the meeting; that number is right."},
	}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	var declinedStatus, reason string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT status, COALESCE(decision_reason, '') FROM member_change_request_items
		  WHERE operation = 'contact_method.update' ORDER BY id DESC LIMIT 1`).Scan(&declinedStatus, &reason))
	assert.Equal(t, "rejected", declinedStatus)
	assert.Contains(t, reason, "Spoke to Dale")
}

// Declining without a reason is refused. The member reads that line.
func TestDecliningStillNeedsAReason(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.grant(t, "Dale Rutherford")
	req := e.seedOwnRequest(t, personID, "Dale Rutherford Jr")

	w := e.post(t, fmt.Sprintf("%s/%d/decline", RouteAdminRequests, req.ID), url.Values{
		"reason": {"   "},
	}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code)
	assert.Contains(t, w.Header().Get("Location"), "error=Give+a+reason")

	var status string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT status FROM member_change_request_items WHERE id = ?`, req.Items[0].ID).Scan(&status))
	assert.Equal(t, "pending", status, "nothing is decided without a reason")
}

// A sensitive change still needs its verification note, and the note is asked
// for once for the whole apply rather than once per row.
func TestASensitiveChangeStillNeedsItsVerificationNote(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.grant(t, "Dale Rutherford")

	cookie := e.signInMember(t)
	w := e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), url.Values{
		"display_name": {"Dale Rutherford"},
		"call_sign":    {"W3NEW"},
	}, cookie)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	var requestID, itemID int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT request_id, id FROM member_change_request_items ORDER BY id DESC LIMIT 1`).
		Scan(&requestID, &itemID))

	w = e.post(t, fmt.Sprintf("%s/%d/apply", RouteAdminRequests, requestID), url.Values{
		"include":                       {fmt.Sprint(itemID)},
		fmt.Sprintf("value_%d", itemID): {"W3NEW"},
	}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code)
	assert.Contains(t, w.Header().Get("Location"), "error=",
		"a sensitive change may not be applied without saying how it was verified")

	var callSign sql.NullString
	require.NoError(t, e.h.db.QueryRow(`SELECT call_sign FROM persons WHERE id = ?`, personID).Scan(&callSign))
	assert.Empty(t, callSign.String, "and nothing reached the record")

	w = e.post(t, fmt.Sprintf("%s/%d/apply", RouteAdminRequests, requestID), url.Values{
		"include":                       {fmt.Sprint(itemID)},
		fmt.Sprintf("value_%d", itemID): {"W3NEW"},
		"verification_note":             {"Checked against the FCC record."},
	}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.NoError(t, e.h.db.QueryRow(`SELECT call_sign FROM persons WHERE id = ?`, personID).Scan(&callSign))
	assert.Equal(t, "W3NEW", callSign.String)
}

// A note is closed by an officer saying so, because nothing in it can be
// decided (bcars-portal-ssz.6, ADR-0014.4).
func TestAnOfficerMarksANoteDoneAndItLeavesTheQueue(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	member := e.signInMember(t)

	w := e.post(t, RouteMemberSuggest, url.Values{
		"about_name": {"Marguerite Ashby"},
		"summary":    {"Her mobile number has changed to 814-555-0177."},
	}, member)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	var requestID, version int64
	var status string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT id, version, status FROM member_change_requests ORDER BY id DESC LIMIT 1`).
		Scan(&requestID, &version, &status))
	assert.Equal(t, "submitted", status,
		"a note arrives outstanding: nothing may resolve it on the way in")

	// It is in the queue an officer opens.
	queue := e.getAs(t, RouteAdminRequests, officer).Body.String()
	assert.Contains(t, queue, "Marguerite Ashby")

	// The review screen offers nothing to apply, because a note proposes
	// nothing an adapter could write.
	detail := e.getAs(t, fmt.Sprintf("%s/%d", RouteAdminRequests, requestID), officer).Body.String()
	assert.NotContains(t, detail, "Apply the ticked changes",
		"there is nothing here to apply; an officer edits the record instead")
	assert.Contains(t, detail, "Mark done")

	w = e.post(t, fmt.Sprintf("%s/%d/done", RouteAdminRequests, requestID), url.Values{
		"version":         {fmt.Sprint(version)},
		"resolution_note": {"Added the new mobile to her record."},
	}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	var resolvedBy sql.NullInt64
	var resolvedAt, note sql.NullString
	require.NoError(t, e.h.db.QueryRow(
		`SELECT status, resolved_at, resolved_by, resolution_note
		   FROM member_change_requests WHERE id = ?`, requestID).
		Scan(&status, &resolvedAt, &resolvedBy, &note))
	assert.Equal(t, "resolved", status)
	assert.True(t, resolvedAt.Valid)
	assert.True(t, resolvedBy.Valid, "who finished with it is recorded, not only when")
	assert.Equal(t, "Added the new mobile to her record.", note.String)

	// And the queue an officer opens no longer holds it.
	queue = e.getAs(t, RouteAdminRequests, officer).Body.String()
	assert.NotContains(t, queue, "Marguerite Ashby",
		"finished work must not sit in the pile of outstanding work")
	assert.Contains(t, e.getAs(t, RouteAdminRequests+"?status=any", officer).Body.String(), "Marguerite Ashby",
		"but it is still there for an officer who asks for it")
}

// Marking done must not swallow a change nobody decided. The member would be
// told their suggestion was dealt with when no officer ever looked at it.
func TestMarkingDoneIsRefusedWhileAChangeIsStillPending(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.grant(t, "Dale Rutherford")
	req := e.seedOwnRequest(t, personID, "Dale Rutherford Jr")

	w := e.post(t, fmt.Sprintf("%s/%d/done", RouteAdminRequests, req.ID), url.Values{
		"version": {fmt.Sprint(req.Version)},
	}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code)
	assert.Contains(t, w.Header().Get("Location"), "error=Apply+or+decline")

	var status string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT status FROM member_change_requests WHERE id = ?`, req.ID).Scan(&status))
	assert.NotEqual(t, "resolved", status, "the request stays open until that change is answered")
}

// An item that names no record is not offered for apply. Linking the REQUEST
// does not give the ITEM a target, so offering it produced the loop
// bcars-portal-3la describes: the officer is told to link what they just linked.
func TestAnItemWithNoTargetIsNotOfferedForApply(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.seedPersonRow(t, "Marguerite Ashby", "W3MGA")

	// A stored item of the old shape: an operation with an adapter, and no
	// target. Nothing creates these now; rows like it exist from before.
	req, err := e.h.changeRequests.Create(context.Background(),
		&authz.Principal{UserID: e.memberUserID},
		changerequests.CreateParams{
			Source:          changerequests.SourceMember,
			RequesterUserID: e.memberUserID,
			SuppliedName:    "Marguerite Ashby",
			Summary:         "Her call sign is printed wrong.",
			Items:           []changerequests.ItemInput{{Operation: "person.call_sign.set", ProposedValue: "W3MGB"}},
		}, "legacy-untargeted", time.Now().UTC())
	require.NoError(t, err)

	// Even once linked, the item is presented as words rather than as work.
	_, err = e.h.changeRequests.Triage(context.Background(), e.officerPrincipal(), req.ID,
		changerequests.TriageParams{TargetPersonID: personID, ExpectedVersion: req.Version}, time.Now().UTC())
	require.NoError(t, err)

	detail := e.getAs(t, fmt.Sprintf("%s/%d", RouteAdminRequests, req.ID), officer).Body.String()
	assert.NotContains(t, detail, "Apply the ticked changes",
		"an item naming no record must not be offered for apply after linking")
	assert.Contains(t, detail, "Mark done",
		"the officer closes it after acting on the record")
}

// An undecided item on a request an officer finished with is not "awaiting" a
// decision that is never coming. Nobody refused it and nobody applied it: the
// officer dealt with the request another way, which is what a note is.
func TestAnUndecidedItemOnAClosedRequestSaysSo(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	member := e.signInMember(t)

	w := e.post(t, RouteMemberSuggest, url.Values{
		"about_name": {"Marguerite Ashby"},
		"summary":    {"Her mobile number has changed."},
	}, member)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	var requestID, version int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT id, version FROM member_change_requests ORDER BY id DESC LIMIT 1`).Scan(&requestID, &version))

	before := e.getAs(t, fmt.Sprintf("%s/%d", RouteAdminRequests, requestID), officer).Body.String()
	assert.Contains(t, before, "Awaiting decision", "while it is open, it is genuinely awaiting one")

	w = e.post(t, fmt.Sprintf("%s/%d/done", RouteAdminRequests, requestID), url.Values{
		"version": {fmt.Sprint(version)},
	}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	after := e.getAs(t, fmt.Sprintf("%s/%d", RouteAdminRequests, requestID), officer).Body.String()
	assert.Contains(t, after, "Closed with the request")
	assert.NotContains(t, after, "Awaiting decision",
		"a closed request must not claim to be waiting for anything")
	assert.Contains(t, after, "Marked done",
		"and the page says who finished with it, permanently rather than as a flash")
}

// The member is owed both halves of the story: what they asked for and what
// the officer actually wrote. ADR-0014.6 makes the two deliberately capable of
// differing, so a page that renders only the proposal beside an "Applied"
// badge tells the member their value reached the record when it did not
// (bcars-portal-ssz.7).
//
// This asserts through the member's own page rather than the API. The API had
// carried applied_value since ssz.1 while this template never rendered it, so
// an assertion one layer down reports a property the member cannot see.
func TestTheMemberSeesTheValueTheOfficerAppliedNotOnlyTheirOwn(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	member := e.signInMember(t)
	personID := e.grant(t, "Dale Rutherford")

	req := e.seedOwnRequest(t, personID, "Dale Rutherfrod")

	// The officer corrects the member's typo before applying it, which is the
	// whole point of the editable review field.
	w := e.post(t, fmt.Sprintf("%s/%d/apply", RouteAdminRequests, req.ID), url.Values{
		"version":                                {strconv.FormatInt(req.Version, 10)},
		"include":                                {strconv.FormatInt(req.Items[0].ID, 10)},
		fmt.Sprintf("value_%d", req.Items[0].ID): {"Dale Rutherford"},
	}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	body := e.getAs(t, fmt.Sprintf("%s/%d", RouteMemberRequests, req.ID), member).Body.String()

	assert.Contains(t, body, "Dale Rutherfrod",
		"what the member proposed must survive on their own page")
	assert.Contains(t, body, "Dale Rutherford",
		"the member must be able to see the value the officer actually applied")
}

// The reverse case is what stops the fix becoming noise: an officer who
// applies exactly what was proposed has added nothing for the member to read,
// and saying "applied as <the same string>" twice invites them to hunt for a
// difference that is not there.
func TestAnUnamendedApplyDoesNotRepeatItselfToTheMember(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	member := e.signInMember(t)
	personID := e.grant(t, "Dale Rutherford")

	req := e.seedOwnRequest(t, personID, "Dale R Rutherford")

	w := e.post(t, fmt.Sprintf("%s/%d/apply", RouteAdminRequests, req.ID), url.Values{
		"version":                                {strconv.FormatInt(req.Version, 10)},
		"include":                                {strconv.FormatInt(req.Items[0].ID, 10)},
		fmt.Sprintf("value_%d", req.Items[0].ID): {"Dale R Rutherford"},
	}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	body := e.getAs(t, fmt.Sprintf("%s/%d", RouteMemberRequests, req.ID), member).Body.String()

	assert.NotContains(t, body, "Applied as",
		"an unamended apply has nothing extra to tell the member")
}

// The applied value is a fact about what the club's records now hold, so it
// travels with the right to see that record rather than with authorship of the
// request. A member who could see a record when they wrote, and cannot when
// they come back, must not learn the current value from their own suggestion
// page -- that would make the page a back door to a record an officer revoked.
func TestARevokedGrantHidesTheAppliedValueFromTheMember(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	member := e.signInMember(t)
	personID := e.grant(t, "Dale Rutherford")

	req := e.seedOwnRequest(t, personID, "Dale Rutherfrod")

	w := e.post(t, fmt.Sprintf("%s/%d/apply", RouteAdminRequests, req.ID), url.Values{
		"version":                                {strconv.FormatInt(req.Version, 10)},
		"include":                                {strconv.FormatInt(req.Items[0].ID, 10)},
		fmt.Sprintf("value_%d", req.Items[0].ID): {"Dale Rutherford"},
	}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	// While the grant stands, the member sees what was written.
	target := fmt.Sprintf("%s/%d", RouteMemberRequests, req.ID)
	require.Contains(t, e.getAs(t, target, member).Body.String(), "Applied as",
		"precondition: a granted member sees the applied value")

	_, err := e.access.RevokeAccess(context.Background(),
		&authz.Principal{UserID: e.officerUserID}, e.memberUserID,
		memberaccess.RevokeParams{PersonID: personID, Reason: "test revocation"},
		time.Now().UTC())
	require.NoError(t, err)

	body := e.getAs(t, target, member).Body.String()

	assert.Contains(t, body, "Dale Rutherfrod",
		"their own words stay on their own page")
	assert.NotContains(t, body, "Applied as",
		"a revoked grant must take the record's current value with it")
}
