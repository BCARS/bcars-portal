package web

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The route from the Pending Approval tile to the memberships waiting on it
// (bcars-portal-ges).
//
// The walkthrough found the tile showing 2 with no way to reach the two: the
// number was not a link, the members list had no lifecycle filter, and the
// approve control lived on a record an officer could only open by already
// knowing the name. Finding them took a hand-written SQLite query.

// seedPending creates a person with a pending membership and returns both ids.
func seedPending(t *testing.T, e *testEnv, name string) (personID, membershipID int64) {
	t.Helper()
	res, err := e.h.db.Exec(
		`INSERT INTO persons (display_name, sort_name) VALUES (?, ?)`, name, name)
	require.NoError(t, err)
	personID, err = res.LastInsertId()
	require.NoError(t, err)

	res, err = e.h.db.Exec(
		`INSERT INTO memberships (person_id, base_type, lifecycle) VALUES (?, 'full', 'pending')`, personID)
	require.NoError(t, err)
	membershipID, err = res.LastInsertId()
	require.NoError(t, err)
	return personID, membershipID
}

// TestAnOfficerReachesAPendingMembershipKnowingNoNames is the bead's acceptance
// criterion, driven the way an officer meets it: open the dashboard, follow the
// number, decide. Nothing here knows a name in advance, which is what the
// walkthrough could not do without SQL.
func TestAnOfficerReachesAPendingMembershipKnowingNoNames(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")
	seedPending(t, e, "Perry Waiting")
	seedPending(t, e, "Wanda Waiting")

	dash := e.get(t, "/admin/").Body.String()
	require.Contains(t, dash, "Pending Approval")
	assert.Contains(t, dash, `href="/admin/memberships/pending"`,
		"the count must be a route, not only a number")

	queue := e.get(t, "/admin/memberships/pending")
	require.Equal(t, http.StatusOK, queue.Code)
	body := queue.Body.String()
	assert.Contains(t, body, "Perry Waiting")
	assert.Contains(t, body, "Wanda Waiting")

	// From the queue to the record, still without typing a URL.
	links := regexp.MustCompile(`href="(/admin/members/\d+)"`).FindAllStringSubmatch(body, -1)
	require.NotEmpty(t, links, "each row must link to the record it concerns")
	record := e.get(t, links[0][1])
	assert.Equal(t, http.StatusOK, record.Code)

	// And the decision can be taken from the queue itself.
	forms := regexp.MustCompile(`action="(/admin/members/\d+/memberships/(\d+)/approve)"`).FindAllStringSubmatch(body, -1)
	require.Len(t, forms, 2, "every waiting membership offers its decision")

	w := e.postForm(t, forms[0][1], url.Values{"version": {"1"}, "base_type": {"full"}})
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	var lifecycle string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT lifecycle FROM memberships WHERE id = ?`, forms[0][2]).Scan(&lifecycle))
	assert.Equal(t, "approved", lifecycle, "approving from the queue reaches the record")

	// The queue shrinks by exactly what was decided.
	after := e.get(t, "/admin/memberships/pending").Body.String()
	assert.NotContains(t, after, "Perry Waiting")
	assert.Contains(t, after, "Wanda Waiting")
}

// TestTheTileAndTheQueueShowTheSameNumber holds the second half of the
// acceptance criterion at the page level: the count and the list are one
// population, so they cannot disagree in front of an officer.
func TestTheTileAndTheQueueShowTheSameNumber(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")
	for i := 0; i < 3; i++ {
		seedPending(t, e, fmt.Sprintf("Waiting %d", i))
	}
	// Rows that look pending but are not, so a count reading the raw lifecycle
	// column without the rest of the predicate would report four.
	_, mid := seedPending(t, e, "Withdrawn Application")
	_, err := e.h.db.Exec(`UPDATE memberships SET ended_on = '2026-02-01' WHERE id = ?`, mid)
	require.NoError(t, err)

	tile := regexp.MustCompile(`Pending Approval</div>\s*<div class="value"><a href="/admin/memberships/pending">(\d+)</a>`).
		FindStringSubmatch(e.get(t, "/admin/").Body.String())
	require.Len(t, tile, 2, "the dashboard tile should carry a linked count")

	queue := e.get(t, "/admin/memberships/pending").Body.String()
	rows := regexp.MustCompile(`href="/admin/members/\d+"`).FindAllString(queue, -1)

	assert.Equal(t, "3", tile[1], "the withdrawn application is not waiting on anyone")
	assert.Len(t, rows, 3, "and the queue shows exactly what the tile counted")
	assert.NotContains(t, queue, "Withdrawn Application")
}

// The queue is reachable when empty, so an officer can confirm there is
// nothing waiting rather than infer it from a link that is not there.
func TestTheQueueSaysWhenNothingIsWaiting(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")

	w := e.get(t, "/admin/memberships/pending")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "No memberships are waiting for a decision")
	assert.Contains(t, e.get(t, "/admin/members").Body.String(), `href="/admin/memberships/pending"`,
		"the members list offers the route whether or not anything is waiting")
}

// TestTheMembersListSaysWhatEachPersonIs is the defect the bead notes beside
// the missing route: every row's Type column read as a dash, because the list
// query never read the membership.
func TestTheMembersListSaysWhatEachPersonIs(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")

	res, err := e.h.db.Exec(`INSERT INTO persons (display_name, sort_name) VALUES ('Ada Full', 'Full, Ada')`)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	_, err = e.h.db.Exec(
		`INSERT INTO memberships (person_id, base_type, lifecycle) VALUES (?, 'associate', 'approved')`, id)
	require.NoError(t, err)

	body := e.get(t, "/admin/members").Body.String()
	row := regexp.MustCompile(`(?s)Ada Full.*?</tr>`).FindString(body)
	require.NotEmpty(t, row, "the member should be listed")
	assert.Contains(t, row, "associate", "the list must say what the record says")
}
