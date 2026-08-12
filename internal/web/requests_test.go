package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/domain/changerequests"
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
