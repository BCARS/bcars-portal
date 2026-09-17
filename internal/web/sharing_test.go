package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A member's control over what the directory lists about them
// (bcars-portal-qku, ADR-0015).
//
// The choice is a proposal like every other field on the form: an officer
// reviews it before the directory changes. These tests hold that end to end
// through the pages people use, because the property that matters -- the
// directory stops listing the detail -- belongs to a different page from the
// one the member changes.

// shareItems returns the operation and proposed value of every item filed, in
// order.
func shareItems(t *testing.T, e *memberTestEnv) (ops, values []string) {
	t.Helper()
	rows, err := e.h.db.Query(
		`SELECT operation, COALESCE(proposed_value, '') FROM member_change_request_items ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var op, v string
		require.NoError(t, rows.Scan(&op, &v))
		ops = append(ops, op)
		values = append(values, v)
	}
	require.NoError(t, rows.Err())
	return ops, values
}

func TestAMemberLeavesTheDirectoryOnceAnOfficerApplies(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	cookie, personID := e.eligibleMember(t)
	emailID := e.seedContact(t, personID, "email", "dale@example.test")
	postalID := e.seedContact(t, personID, "postal", "1 Main Street")

	// With no choice on file, the club default lists a Full member's email.
	require.Contains(t, e.getAs(t, RouteMemberDirectory, cookie).Body.String(), "dale@example.test",
		"precondition: the club default lists the email")

	form := e.getAs(t, fmt.Sprintf("/member/records/%d/suggest", personID), cookie).Body.String()
	assert.Contains(t, form, fmt.Sprintf(`name="share_%d"`, emailID),
		"an email the directory can list gets a choice")
	assert.NotContains(t, form, fmt.Sprintf(`name="share_%d"`, postalID),
		"the directory never lists an address, so offering the choice would be a control that does nothing")
	assert.Contains(t, form, `<option value="full_members" selected>`,
		"the form shows what the directory does now, not a blank")

	w := e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), url.Values{
		"display_name":                      {"Dale Rutherford"},
		"call_sign":                         {"W3DLR"},
		fmt.Sprintf("contact_%d", emailID):  {"dale@example.test"},
		fmt.Sprintf("contact_%d", postalID): {"1 Main Street"},
		fmt.Sprintf("share_%d", emailID):    {"hidden"},
	}, cookie)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	ops, values := shareItems(t, e)
	assert.Equal(t, []string{"contact_method.visibility.set"}, ops,
		"only the listing changed, so it is the only thing proposed")
	assert.Equal(t, []string{"hidden"}, values)

	// A proposal changes nothing on its own.
	assert.Contains(t, e.getAs(t, RouteMemberDirectory, cookie).Body.String(), "dale@example.test",
		"the directory must not change before an officer applies the request")

	var requestID, itemID int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT request_id, id FROM member_change_request_items ORDER BY id DESC LIMIT 1`).
		Scan(&requestID, &itemID))

	detail := e.getAs(t, fmt.Sprintf("%s/%d", RouteAdminRequests, requestID), officer).Body.String()
	assert.Contains(t, detail, "No choice on file; the club default applies",
		"the reviewer is told what the directory does now, even when that is only the default")
	assert.Contains(t, detail, "Directory listing: Email",
		"the reviewer is told which detail the listing change is about")
	assert.Contains(t, detail, fmt.Sprintf(`<select name="value_%d"`, itemID),
		"a closed set of audiences is chosen, not typed")

	w = e.post(t, fmt.Sprintf("%s/%d/apply", RouteAdminRequests, requestID), url.Values{
		"include":                       {fmt.Sprint(itemID)},
		fmt.Sprintf("value_%d", itemID): {"hidden"},
		"verification_note":             {"Asked Dale at the meeting."},
	}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())
	require.Contains(t, w.Header().Get("Location"), "success=", w.Header().Get("Location"))

	// The property: the directory stops listing it.
	assert.NotContains(t, e.getAs(t, RouteMemberDirectory, cookie).Body.String(), "dale@example.test",
		"once applied, the directory must stop listing the email")

	var source string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT source FROM contact_method_visibility_events
		  WHERE contact_method_id = ? ORDER BY effective_at DESC, id DESC LIMIT 1`, emailID).Scan(&source))
	assert.Equal(t, "member_request", source, "the decision records that the member asked for it")

	// The email's own row, not the page: the postal row beside it still says
	// "club default", correctly.
	record := e.getAs(t, fmt.Sprintf("/member/records/%d", personID), cookie).Body.String()
	at := strings.Index(record, "<td>dale@example.test</td>")
	require.NotEqual(t, -1, at, "the email row is on the record page")
	emailRow := record[at:]
	emailRow = emailRow[:strings.Index(emailRow, "</tr>")]
	assert.Contains(t, emailRow, "<td>Not in the directory</td>",
		"the member's own record reflects the applied choice, and no longer calls it the club default")
}

func TestAShareChoiceOutsideTheFormIsRefused(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, personID := e.eligibleMember(t)
	emailID := e.seedContact(t, personID, "email", "dale@example.test")

	w := e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), url.Values{
		"display_name":                     {"Dale Rutherford"},
		"call_sign":                        {"W3DLR"},
		fmt.Sprintf("contact_%d", emailID): {"dale@example.test"},
		fmt.Sprintf("share_%d", emailID):   {"everyone"},
	}, cookie)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "Please choose whether each detail is listed")

	ops, _ := shareItems(t, e)
	assert.Empty(t, ops, "a refused form files nothing")
}

// Hidden and officers-only both keep a detail out of the directory, so the form
// shows one answer for them, and leaving it alone must not propose a change
// from one to the other.
func TestLeavingAnOfficersOnlyDetailAloneProposesNothing(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, personID := e.eligibleMember(t)
	emailID := e.seedContact(t, personID, "email", "dale@example.test")
	_, err := e.h.db.Exec(`
		INSERT INTO contact_method_visibility_events
			(contact_method_id, audience, effective_at, actor_user_id, source)
		VALUES (?, 'officers_only', '2026-01-01T00:00:00.000Z', 1, 'officer')`, emailID)
	require.NoError(t, err)

	form := e.getAs(t, fmt.Sprintf("/member/records/%d/suggest", personID), cookie).Body.String()
	assert.Contains(t, form, `<option value="hidden" selected>`)

	w := e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), url.Values{
		"display_name":                     {"Dale Rutherford"},
		"call_sign":                        {"W3DLR"},
		fmt.Sprintf("contact_%d", emailID): {"dale@example.test"},
		fmt.Sprintf("share_%d", emailID):   {"hidden"},
		"note":                             {"Just checking in."},
	}, cookie)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	ops, _ := shareItems(t, e)
	for _, op := range ops {
		assert.NotEqual(t, "contact_method.visibility.set", op,
			"officers-only already keeps the email out of the directory")
	}
}

// An officer amends a value in the review form. For a directory listing the
// page offers a select, but the POST is still a string, and one outside the set
// must not reach the record -- the directory would read it as "not
// full_members" and hide the detail with no one having chosen that.
func TestAnOfficerCannotApplyAnUnknownAudience(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	cookie, personID := e.eligibleMember(t)
	emailID := e.seedContact(t, personID, "email", "dale@example.test")

	w := e.post(t, fmt.Sprintf("/member/records/%d/suggest", personID), url.Values{
		"display_name":                     {"Dale Rutherford"},
		"call_sign":                        {"W3DLR"},
		fmt.Sprintf("contact_%d", emailID): {"dale@example.test"},
		fmt.Sprintf("share_%d", emailID):   {"hidden"},
	}, cookie)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	var requestID, itemID int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT request_id, id FROM member_change_request_items ORDER BY id DESC LIMIT 1`).
		Scan(&requestID, &itemID))

	w = e.post(t, fmt.Sprintf("%s/%d/apply", RouteAdminRequests, requestID), url.Values{
		"include":                       {fmt.Sprint(itemID)},
		fmt.Sprintf("value_%d", itemID): {"yes"},
		"verification_note":             {"Asked Dale at the meeting."},
	}, officer)
	require.Equal(t, http.StatusSeeOther, w.Code)
	assert.True(t, strings.Contains(w.Header().Get("Location"), "error="),
		"an unknown audience is refused: %s", w.Header().Get("Location"))

	var events int
	require.NoError(t, e.h.db.QueryRow(
		`SELECT count(*) FROM contact_method_visibility_events WHERE contact_method_id = ?`, emailID).Scan(&events))
	assert.Zero(t, events, "nothing reached the record")
	assert.Contains(t, e.getAs(t, RouteMemberDirectory, cookie).Body.String(), "dale@example.test",
		"and the directory still lists the email")
}
