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
		ID             int64    `json:"id"`
		Action         string   `json:"action"`
		ActorUserID    int64    `json:"actor_user_id"`
		ActorRoleCodes []string `json:"actor_role_codes"`
		ResourceKind   string   `json:"resource_kind"`
		ResourceID     int64    `json:"resource_id"`
		Outcome        string   `json:"outcome"`
		ReasonCode     string   `json:"reason_code"`
		OccurredAt     string   `json:"occurred_at"`
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

// --- reason_code, actor_role_codes, and the outcome filter (fmc.16) ---
//
// The question these exist to answer is the one an officer reviewing the log
// actually has: was this somebody not signed in, or somebody signed in without
// permission? Before this the API could not tell them apart, and finding
// denials at all meant knowing the authz.denied action-prefix convention.

// TestAuditDenialsCarryReasonAndRoles drives both refusals through the real
// middleware rather than seeding rows, because the point is that the shipped
// path records these fields.
func TestAuditDenialsCarryReasonAndRoles(t *testing.T) {
	env := setupAuthzTest(t, "administrator")
	admin := env.signIn(t)

	// Refusal one: nobody is signed in.
	resp := env.do(t, http.MethodGet, "/api/v1/audit-events", nil, "")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// Refusal two: signed in, without the capability. A second account holding
	// a role that does not carry audit.read.
	other := setupAuthzTest(t, "acs_coordinator")
	otherCookie := other.signIn(t)
	resp = other.do(t, http.MethodGet, "/api/v1/audit-events", otherCookie, "")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	// The unauthenticated denial, read back through the API.
	page := getAuditPage(t, env, admin, "?outcome=denied")
	require.NotEmpty(t, page.Data)

	var sawUnauthenticated bool
	for _, e := range page.Data {
		if e.ReasonCode == "unauthenticated" {
			sawUnauthenticated = true
			assert.Empty(t, e.ActorRoleCodes,
				"an unauthenticated actor holds no roles, which is the distinction being drawn")
			assert.Zero(t, e.ActorUserID)
		}
	}
	assert.True(t, sawUnauthenticated, "a signed-out refusal must be readable as such")

	// The capability refusal lives in the other environment's log, which that
	// account cannot read -- it was refused audit.read, which is the whole
	// point -- so it is asserted at the table.
	var missingCapability int
	require.NoError(t, other.db.QueryRow(`
		SELECT count(*) FROM audit_events
		 WHERE outcome = 'denied' AND reason_code = 'missing_capability'
		   AND actor_role_codes = 'acs_coordinator'`).Scan(&missingCapability))
	assert.Positive(t, missingCapability,
		"a signed-in refusal must record both the reason and the roles the actor held")
}

// TestAuditSuccessRecordsTheActorsRoles covers the non-denial path, since a
// field populated only on refusals would answer half the question.
func TestAuditSuccessRecordsTheActorsRoles(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodPost, "/api/v1/members", cookie,
		`{"display_name":"Audited Person","sort_name":"audited person","base_type":"full"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode, readAll(t, resp))

	var roles string
	require.NoError(t, env.db.QueryRow(`
		SELECT coalesce(actor_role_codes, '') FROM audit_events
		 WHERE action = 'member.create' AND outcome = 'success'`).Scan(&roles))
	assert.Equal(t, "secretary", roles, "a successful action records what the actor was")
}

// TestAuditRolesAreRecordedNotResolvedOnRead is the reason the column is
// written at all. Roles change; the log must not change with them.
func TestAuditRolesAreRecordedNotResolvedOnRead(t *testing.T) {
	env := setupAuthzTest(t, "administrator", "secretary")
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodPost, "/api/v1/members", cookie,
		`{"display_name":"Before The Change","sort_name":"before","base_type":"full"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	// The secretary role is revoked afterwards.
	_, err := env.db.Exec(
		`UPDATE user_role_grants SET revoked_at = '2026-08-12T00:00:00.000Z'
		  WHERE user_id = 1 AND role_code = 'secretary'`)
	require.NoError(t, err)

	page := getAuditPage(t, env, cookie, "?action=member.create")
	require.NotEmpty(t, page.Data)
	assert.Equal(t, []string{"administrator", "secretary"}, page.Data[0].ActorRoleCodes,
		"the event must still say what the actor held when they acted")
}

// TestAuditOutcomeFilter covers the filter in isolation.
func TestAuditOutcomeFilter(t *testing.T) {
	env := setupAuthzTest(t, "administrator")
	cookie := env.signIn(t)

	_, err := env.db.Exec(`
		INSERT INTO audit_events (occurred_at, action, actor_user_id, outcome, reason_code)
		VALUES ('2026-01-09T00:00:00.000Z', 'member.create', 1, 'success', NULL),
		       ('2026-01-08T00:00:00.000Z', 'member.update', 1, 'failure', 'stale_version'),
		       ('2026-01-07T00:00:00.000Z', 'member.delete', 1, 'denied', 'missing_capability')`)
	require.NoError(t, err)

	for _, tc := range []struct{ outcome, action string }{
		{"success", "member.create"},
		{"failure", "member.update"},
		{"denied", "member.delete"},
	} {
		page := getAuditPage(t, env, cookie, "?outcome="+tc.outcome+"&action=member.")
		require.NotEmpty(t, page.Data, "outcome=%s returned nothing", tc.outcome)
		for _, e := range page.Data {
			assert.Equal(t, tc.outcome, e.Outcome)
		}
		assert.Contains(t, actionsOf(page), tc.action)
	}
}

// TestAuditOutcomeFilterComposes proves it narrows alongside the existing
// filters rather than replacing them.
func TestAuditOutcomeFilterComposes(t *testing.T) {
	env := setupAuthzTest(t, "administrator")
	cookie := env.signIn(t)
	seedAuditFixtures(t, env.db)

	_, err := env.db.Exec(`
		INSERT INTO audit_events (occurred_at, action, actor_user_id, resource_kind, resource_id, outcome, reason_code)
		VALUES ('2026-01-06T00:00:00.000Z', 'member.update', 2, 'member', 10, 'denied', 'missing_capability')`)
	require.NoError(t, err)

	// actor 2 + member. prefix + denied selects exactly the row just written,
	// and not actor 2's other member.update, which succeeded.
	page := getAuditPage(t, env, cookie, "?actor_user_id=2&action=member.&outcome=denied")
	require.Len(t, page.Data, 1, "the three filters must intersect")
	assert.Equal(t, "member.update", page.Data[0].Action)
	assert.Equal(t, "missing_capability", page.Data[0].ReasonCode)

	// Dropping the outcome widens it again, which proves the filter did the
	// narrowing rather than the other two.
	wider := getAuditPage(t, env, cookie, "?actor_user_id=2&action=member.")
	assert.Len(t, wider.Data, 2)

	// A subject filter composes too.
	bySubject := getAuditPage(t, env, cookie, "?subject_kind=member&subject_id=10&outcome=denied")
	require.Len(t, bySubject.Data, 1)
	assert.Equal(t, "member.update", bySubject.Data[0].Action)
}

// TestAuditOutcomeFilterRejectsUnknownValues keeps the parameter from becoming
// a way to probe the column with arbitrary strings.
func TestAuditOutcomeFilterRejectsUnknownValues(t *testing.T) {
	env := setupAuthzTest(t, "administrator")
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodGet, "/api/v1/audit-events?outcome=partial", cookie, "")
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode,
		"only the three recorded outcomes are selectable")
}

// TestAuditDenialsAreFindableWithoutTheActionConvention is the operational
// point of the filter: before it, finding every refusal meant knowing that
// operations without their own audit action are recorded under authz.denied.*.
func TestAuditDenialsAreFindableWithoutTheActionConvention(t *testing.T) {
	env := setupAuthzTest(t, "administrator")
	cookie := env.signIn(t)

	// A denial recorded under an operation's OWN action rather than the
	// authz.denied prefix, which is what the prefix search would miss.
	_, err := env.db.Exec(`
		INSERT INTO audit_events (occurred_at, action, actor_user_id, outcome, reason_code)
		VALUES ('2026-01-05T00:00:00.000Z', 'member.deactivate', 1, 'denied', 'missing_capability')`)
	require.NoError(t, err)

	byConvention := getAuditPage(t, env, cookie, "?action=authz.denied.&limit=200")
	byOutcome := getAuditPage(t, env, cookie, "?outcome=denied&limit=200")

	assert.NotContains(t, actionsOf(byConvention), "member.deactivate",
		"the action prefix cannot see a denial recorded under its own action")
	assert.Contains(t, actionsOf(byOutcome), "member.deactivate",
		"the outcome filter finds every refusal regardless of how it was named")
}
