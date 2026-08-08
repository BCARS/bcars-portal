package httpapi_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// auditPage mirrors httpapi.Page[AuditEventResponse] for assertions.
type auditPage struct {
	Data []struct {
		ID           int64  `json:"id"`
		Action       string `json:"action"`
		ActorUserID  int64  `json:"actor_user_id"`
		ResourceKind string `json:"resource_kind"`
		ResourceID   int64  `json:"resource_id"`
		Outcome      string `json:"outcome"`
		OccurredAt   string `json:"occurred_at"`
	} `json:"data"`
	NextCursor string `json:"next_cursor"`
}

// insertAuditEvent writes one synthetic audit row. Tests use explicit rows so
// filter assertions do not depend on which events the middleware happens to
// write for the requests the test itself makes.
func insertAuditEvent(t *testing.T, d *sql.DB, occurredAt, action string, actorUserID int64, resourceKind string, resourceID int64) {
	t.Helper()
	_, err := d.Exec(
		`INSERT INTO audit_events (occurred_at, action, actor_user_id, resource_kind, resource_id, outcome)
		 VALUES (?, ?, ?, ?, ?, 'success')`,
		occurredAt, action, actorUserID, resourceKind, resourceID)
	require.NoError(t, err)
}

func getAuditPage(t *testing.T, env *authzEnv, cookie *http.Cookie, query string) auditPage {
	t.Helper()
	resp := env.do(t, http.MethodGet, "/api/v1/audit-events"+query, cookie, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var page auditPage
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&page))
	return page
}

func actionsOf(page auditPage) []string {
	out := make([]string, len(page.Data))
	for i, e := range page.Data {
		out[i] = e.Action
	}
	return out
}

// seedAuditFixtures writes a small, deliberately overlapping set of events so
// each filter selects a different subset and combinations select intersections.
func seedAuditFixtures(t *testing.T, d *sql.DB) {
	t.Helper()
	// actor_user_id is a foreign key; setupAuthzTest seeds only user 1.
	_, err := d.Exec(`INSERT INTO users (id, email, password_hash, is_active) VALUES (2, 'second@bcars.org', 'x', 1)`)
	require.NoError(t, err)

	// occurred_at descending: the first row inserted is the newest.
	insertAuditEvent(t, d, "2026-01-05T00:00:00.000Z", "member.create", 1, "member", 10)
	insertAuditEvent(t, d, "2026-01-04T00:00:00.000Z", "member.update", 1, "member", 11)
	insertAuditEvent(t, d, "2026-01-03T00:00:00.000Z", "member.update", 2, "member", 10)
	insertAuditEvent(t, d, "2026-01-02T00:00:00.000Z", "membership.approve", 2, "membership", 10)
	insertAuditEvent(t, d, "2026-01-01T00:00:00.000Z", "import.commit", 2, "import", 99)
}

func TestAuditEventsList_ActionPrefixFilter(t *testing.T) {
	env := setupAuthzTest(t, "administrator")
	cookie := env.signIn(t)
	seedAuditFixtures(t, env.db)

	// "member." is a prefix filter: it must match member.create and
	// member.update but must NOT match membership.approve.
	page := getAuditPage(t, env, cookie, "?action=member.")
	assert.Equal(t, []string{"member.create", "member.update", "member.update"}, actionsOf(page))

	// A full action name still works as a (degenerate) prefix.
	page = getAuditPage(t, env, cookie, "?action=member.create")
	assert.Equal(t, []string{"member.create"}, actionsOf(page))

	// A prefix that matches nothing returns an empty page, not everything.
	page = getAuditPage(t, env, cookie, "?action=nosuch.")
	assert.Empty(t, page.Data)
	assert.Empty(t, page.NextCursor)

	// LIKE wildcards are not special: they are matched literally.
	page = getAuditPage(t, env, cookie, "?action=%25")
	assert.Empty(t, page.Data)
}

func TestAuditEventsList_ActorFilter(t *testing.T) {
	env := setupAuthzTest(t, "administrator")
	cookie := env.signIn(t)
	seedAuditFixtures(t, env.db)

	page := getAuditPage(t, env, cookie, "?actor_user_id=2&action=member.")
	assert.Equal(t, []string{"member.update"}, actionsOf(page))
	for _, e := range page.Data {
		assert.Equal(t, int64(2), e.ActorUserID)
	}

	// An actor with no events yields nothing.
	page = getAuditPage(t, env, cookie, "?actor_user_id=999")
	assert.Empty(t, page.Data)
}

func TestAuditEventsList_SubjectFilters(t *testing.T) {
	env := setupAuthzTest(t, "administrator")
	cookie := env.signIn(t)
	seedAuditFixtures(t, env.db)

	page := getAuditPage(t, env, cookie, "?subject_kind=membership")
	assert.Equal(t, []string{"membership.approve"}, actionsOf(page))

	// subject_id alone spans kinds: member/10 and membership/10.
	page = getAuditPage(t, env, cookie, "?subject_id=10")
	assert.Equal(t, []string{"member.create", "member.update", "membership.approve"}, actionsOf(page))

	// kind + id narrows to one row.
	page = getAuditPage(t, env, cookie, "?subject_kind=membership&subject_id=10")
	assert.Equal(t, []string{"membership.approve"}, actionsOf(page))
}

func TestAuditEventsList_FiltersCompose(t *testing.T) {
	env := setupAuthzTest(t, "administrator")
	cookie := env.signIn(t)
	seedAuditFixtures(t, env.db)

	page := getAuditPage(t, env, cookie, "?action=member.&actor_user_id=2&subject_kind=member&subject_id=10")
	require.Len(t, page.Data, 1)
	assert.Equal(t, "member.update", page.Data[0].Action)
	assert.Equal(t, int64(2), page.Data[0].ActorUserID)
	assert.Equal(t, "member", page.Data[0].ResourceKind)
	assert.Equal(t, int64(10), page.Data[0].ResourceID)

	// A combination whose parts each match rows but whose intersection is
	// empty must return nothing rather than the union.
	page = getAuditPage(t, env, cookie, "?action=import.&actor_user_id=1")
	assert.Empty(t, page.Data)
}

func TestAuditEventsList_CursorWalkTerminates(t *testing.T) {
	env := setupAuthzTest(t, "administrator")
	cookie := env.signIn(t)

	const total = 25
	for i := range total {
		insertAuditEvent(t, env.db,
			fmt.Sprintf("2026-02-01T00:00:%02d.000Z", i),
			fmt.Sprintf("walk.event.%02d", i), 1, "walk", int64(i))
	}

	var seen []string
	cursor := ""
	for pages := 0; ; pages++ {
		require.Less(t, pages, 20, "cursor walk did not terminate")
		q := "?action=walk.&limit=7"
		if cursor != "" {
			q += "&cursor=" + cursor
		}
		page := getAuditPage(t, env, cookie, q)
		seen = append(seen, actionsOf(page)...)
		if page.NextCursor == "" {
			// Final page must be short and carry no cursor.
			assert.LessOrEqual(t, len(page.Data), 7)
			break
		}
		assert.Len(t, page.Data, 7, "a page with next_cursor must be full")
		require.NotEqual(t, cursor, page.NextCursor, "cursor must advance")
		cursor = page.NextCursor
	}

	require.Len(t, seen, total, "walk must visit every row exactly once")
	// Newest first, no duplicates, no gaps.
	for i, action := range seen {
		assert.Equal(t, fmt.Sprintf("walk.event.%02d", total-1-i), action)
	}
}

func TestAuditEventsList_RejectsMalformedCursor(t *testing.T) {
	env := setupAuthzTest(t, "administrator")
	cookie := env.signIn(t)
	seedAuditFixtures(t, env.db)

	for _, cursor := range []string{"not-base64!!", "bm90LWEtbnVtYmVy"} {
		resp := env.do(t, http.MethodGet, "/api/v1/audit-events?cursor="+cursor, cookie, "")
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "cursor=%s", cursor)
	}
}

// TestAuditEventsList_FiltersRealMiddlewareEvents checks the filters against
// rows the audit middleware itself wrote, not just hand-inserted ones.
func TestAuditEventsList_FiltersRealMiddlewareEvents(t *testing.T) {
	env := setupAuthzTest(t, "administrator")
	cookie := env.signIn(t)

	// A denial: administrator's own requests are permitted, so drive one
	// without a session to produce an authz.denied.* event.
	resp := env.do(t, http.MethodGet, "/api/v1/audit-events", nil, "")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	page := getAuditPage(t, env, cookie, "?action=authz.denied.")
	require.NotEmpty(t, page.Data, "middleware-written denial events must be filterable")
	for _, e := range page.Data {
		assert.Equal(t, "denied", e.Outcome)
	}

	// The same prefix filter must exclude everything else in the log.
	all := getAuditPage(t, env, cookie, "?limit=200")
	assert.Greater(t, len(all.Data), len(page.Data))
}
