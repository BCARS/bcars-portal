package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/httpapi"
)

// Officer-maintained informational relationships (bcars-portal-4ux.8).
//
// Half of these tests assert what a relationship DOES. The other half assert
// what it must never do, because the failure this bead exists to prevent is not
// a broken feature — it is a working one that quietly starts handing out access
// because "she's his wife" looked like enough.

type apiRelationship struct {
	ID               int64  `json:"id"`
	FromPersonID     int64  `json:"from_person_id"`
	ToPersonID       int64  `json:"to_person_id"`
	Kind             string `json:"kind"`
	Context          string `json:"context"`
	Active           bool   `json:"active"`
	CreatedByUserID  int64  `json:"created_by_user_id"`
	ArchivedByUserID int64  `json:"archived_by_user_id"`
	ArchivedAt       string `json:"archived_at"`
	ArchiveReason    string `json:"archive_reason"`
	Version          int64  `json:"version"`
	Direction        string `json:"direction"`
	OtherPersonID    int64  `json:"other_person_id"`
	OtherDisplayName string `json:"other_display_name"`
	OtherCallSign    string `json:"other_call_sign"`
}

func createRelationship(t *testing.T, env *authzEnv, cookie *http.Cookie, from, to int64, kind, context string) *http.Response {
	t.Helper()
	body := fmt.Sprintf(`{"from_person_id":%d,"to_person_id":%d,"kind":%q,"context":%q}`,
		from, to, kind, context)
	return doWithHeaders(t, env, http.MethodPost, "/api/v1/relationships", cookie, body, nil)
}

func decodeRelationship(t *testing.T, resp *http.Response) apiRelationship {
	t.Helper()
	var r apiRelationship
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&r))
	return r
}

func listRelationships(t *testing.T, env *authzEnv, cookie *http.Cookie, personID int64, includeArchived bool) []apiRelationship {
	t.Helper()
	path := fmt.Sprintf("/api/v1/members/%d/relationships", personID)
	if includeArchived {
		path += "?include_archived=true"
	}
	resp := env.do(t, http.MethodGet, path, cookie, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, readAll(t, resp))
	var out []apiRelationship
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}

func archiveRelationship(t *testing.T, env *authzEnv, cookie *http.Cookie, id, version int64, reason string) *http.Response {
	t.Helper()
	return doWithHeaders(t, env, http.MethodPost,
		fmt.Sprintf("/api/v1/relationships/%d/archive", id), cookie,
		fmt.Sprintf(`{"reason":%q}`, reason),
		map[string]string{httpapi.IfMatchHeader: fmt.Sprintf(`"%d"`, version)})
}

// TestRelationshipRoundTrip covers create, read, and both-direction listing.
func TestRelationshipRoundTrip(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)

	dale := seedPersonForRequest(t, env, "Dale Rutherford")
	marge := seedPersonForRequest(t, env, "Marguerite Rutherford")

	resp := createRelationship(t, env, cookie, dale, marge, "spouse_partner", "Married; she handles the mail.")
	require.Equal(t, http.StatusOK, resp.StatusCode, readAll(t, resp))
	rel := decodeRelationship(t, resp)

	assert.Equal(t, dale, rel.FromPersonID)
	assert.Equal(t, marge, rel.ToPersonID)
	assert.Equal(t, "spouse_partner", rel.Kind)
	assert.Equal(t, "Married; she handles the mail.", rel.Context)
	assert.True(t, rel.Active)
	assert.Equal(t, int64(1), rel.CreatedByUserID, "the officer who recorded it is kept")
	assert.Equal(t, int64(1), rel.Version)

	got := env.do(t, http.MethodGet, fmt.Sprintf("/api/v1/relationships/%d", rel.ID), cookie, "")
	require.Equal(t, http.StatusOK, got.StatusCode)
	assert.Equal(t, rel.ID, decodeRelationship(t, got).ID)

	// The link is one row, and both people can see it from their own side.
	fromDale := listRelationships(t, env, cookie, dale, false)
	require.Len(t, fromDale, 1)
	assert.Equal(t, "outgoing", fromDale[0].Direction)
	assert.Equal(t, marge, fromDale[0].OtherPersonID)
	assert.Equal(t, "Marguerite Rutherford", fromDale[0].OtherDisplayName)

	fromMarge := listRelationships(t, env, cookie, marge, false)
	require.Len(t, fromMarge, 1)
	assert.Equal(t, "incoming", fromMarge[0].Direction)
	assert.Equal(t, dale, fromMarge[0].OtherPersonID)
	assert.Equal(t, rel.ID, fromMarge[0].ID, "one relationship, seen from two sides")
}

// TestRelationshipValidation keeps typos from becoming dangling or absurd rows.
func TestRelationshipValidation(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Only Person")
	other := seedPersonForRequest(t, env, "Other Person")

	assert.Equal(t, http.StatusUnprocessableEntity,
		createRelationship(t, env, cookie, person, person, "household", "").StatusCode,
		"a person cannot be related to themselves")

	assert.Equal(t, http.StatusUnprocessableEntity,
		createRelationship(t, env, cookie, person, 999999, "household", "").StatusCode,
		"an unknown person must be refused")

	assert.Equal(t, http.StatusUnprocessableEntity,
		createRelationship(t, env, cookie, person, other, "best_friend", "").StatusCode,
		"the vocabulary is closed")

	var rows int
	require.NoError(t, env.db.QueryRow(`SELECT count(*) FROM person_relationships`).Scan(&rows))
	assert.Zero(t, rows, "no refused attempt may leave a row behind")
}

// TestDuplicateActiveRelationshipIsRefused proves the database decides.
func TestDuplicateActiveRelationshipIsRefused(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	one := seedPersonForRequest(t, env, "House One")
	two := seedPersonForRequest(t, env, "House Two")

	require.Equal(t, http.StatusOK,
		createRelationship(t, env, cookie, one, two, "household", "").StatusCode)
	assert.Equal(t, http.StatusConflict,
		createRelationship(t, env, cookie, one, two, "household", "").StatusCode)

	// A different kind between the same pair is a different fact, not a
	// duplicate: a parent and child can also share a household.
	assert.Equal(t, http.StatusOK,
		createRelationship(t, env, cookie, one, two, "parent_guardian", "").StatusCode)
}

// TestRelationshipUpdateIsVersionGuarded is the stale-write case. Two officers
// tidying the same household at one meeting is ordinary, and the loser must be
// told rather than silently overwrite the winner.
func TestRelationshipUpdateIsVersionGuarded(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	a := seedPersonForRequest(t, env, "Stale A")
	b := seedPersonForRequest(t, env, "Stale B")

	rel := decodeRelationship(t, createRelationship(t, env, cookie, a, b, "household", "Shares an address."))
	path := fmt.Sprintf("/api/v1/relationships/%d", rel.ID)

	missing := doWithHeaders(t, env, http.MethodPatch, path, cookie,
		`{"kind":"spouse_partner"}`, nil)
	assert.Equal(t, http.StatusPreconditionRequired, missing.StatusCode,
		"a missing If-Match is a 428, not a silent write")

	first := doWithHeaders(t, env, http.MethodPatch, path, cookie,
		`{"kind":"spouse_partner","context":"Married last spring."}`,
		map[string]string{httpapi.IfMatchHeader: fmt.Sprintf(`"%d"`, rel.Version)})
	require.Equal(t, http.StatusOK, first.StatusCode, readAll(t, first))
	updated := decodeRelationship(t, first)
	assert.Equal(t, "spouse_partner", updated.Kind)
	assert.Equal(t, "Married last spring.", updated.Context)
	assert.Equal(t, rel.Version+1, updated.Version)

	// The second officer read version 1 and is now behind.
	stale := doWithHeaders(t, env, http.MethodPatch, path, cookie,
		`{"kind":"household"}`,
		map[string]string{httpapi.IfMatchHeader: fmt.Sprintf(`"%d"`, rel.Version)})
	assert.Equal(t, http.StatusPreconditionFailed, stale.StatusCode)

	var kind string
	require.NoError(t, env.db.QueryRow(
		`SELECT kind FROM person_relationships WHERE id = ?`, rel.ID).Scan(&kind))
	assert.Equal(t, "spouse_partner", kind, "the stale write must not have landed")
}

// TestArchiveKeepsHistoryAndFreesThePair is the divorce case: the fact stops
// being current without ceasing to have been true.
func TestArchiveKeepsHistoryAndFreesThePair(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	a := seedPersonForRequest(t, env, "Was Married A")
	b := seedPersonForRequest(t, env, "Was Married B")

	rel := decodeRelationship(t, createRelationship(t, env, cookie, a, b, "spouse_partner", ""))

	resp := archiveRelationship(t, env, cookie, rel.ID, rel.Version, "Divorced; separate addresses now.")
	require.Equal(t, http.StatusOK, resp.StatusCode, readAll(t, resp))
	archived := decodeRelationship(t, resp)
	assert.False(t, archived.Active)
	assert.NotEmpty(t, archived.ArchivedAt)
	assert.Equal(t, int64(1), archived.ArchivedByUserID, "who archived it is kept")
	assert.Equal(t, "Divorced; separate addresses now.", archived.ArchiveReason)

	assert.Empty(t, listRelationships(t, env, cookie, a, false),
		"an archived relationship is not current")

	history := listRelationships(t, env, cookie, a, true)
	require.Len(t, history, 1, "but it is still answerable")
	assert.Equal(t, rel.ID, history[0].ID)
	assert.False(t, history[0].Active)
	assert.Equal(t, "Divorced; separate addresses now.", history[0].ArchiveReason)

	// The row is archived, not deleted.
	var rows int
	require.NoError(t, env.db.QueryRow(
		`SELECT count(*) FROM person_relationships WHERE id = ?`, rel.ID).Scan(&rows))
	assert.Equal(t, 1, rows)

	// Archiving frees the pair, so a remarriage records a NEW row rather than
	// un-archiving the old one and losing the gap.
	again := createRelationship(t, env, cookie, a, b, "spouse_partner", "Remarried.")
	require.Equal(t, http.StatusOK, again.StatusCode, readAll(t, again))
	assert.NotEqual(t, rel.ID, decodeRelationship(t, again).ID)
}

// TestArchivedRelationshipsAreImmutable proves history stays history.
func TestArchivedRelationshipsAreImmutable(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	a := seedPersonForRequest(t, env, "Archived A")
	b := seedPersonForRequest(t, env, "Archived B")

	rel := decodeRelationship(t, createRelationship(t, env, cookie, a, b, "household", ""))
	archived := decodeRelationship(t, archiveRelationship(t, env, cookie, rel.ID, rel.Version, "Moved out."))

	edit := doWithHeaders(t, env, http.MethodPatch,
		fmt.Sprintf("/api/v1/relationships/%d", rel.ID), cookie,
		`{"kind":"spouse_partner"}`,
		map[string]string{httpapi.IfMatchHeader: fmt.Sprintf(`"%d"`, archived.Version)})
	assert.Equal(t, http.StatusConflict, edit.StatusCode,
		"an archived relationship cannot be edited back into currency")

	assert.Equal(t, http.StatusConflict,
		archiveRelationship(t, env, cookie, rel.ID, archived.Version, "again").StatusCode)
}

// TestUnknownRelationshipIs404 covers the missing-row paths.
func TestUnknownRelationshipIs404(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)

	assert.Equal(t, http.StatusNotFound,
		env.do(t, http.MethodGet, "/api/v1/relationships/999999", cookie, "").StatusCode)
	assert.Equal(t, http.StatusNotFound,
		archiveRelationship(t, env, cookie, 999999, 1, "nothing there").StatusCode)
}

// TestRelationshipChangesAreAudited covers the audit trail for every mutation.
func TestRelationshipChangesAreAudited(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	a := seedPersonForRequest(t, env, "Audited A")
	b := seedPersonForRequest(t, env, "Audited B")

	rel := decodeRelationship(t, createRelationship(t, env, cookie, a, b, "household", "Shares a mailbox."))
	require.Equal(t, http.StatusOK, doWithHeaders(t, env, http.MethodPatch,
		fmt.Sprintf("/api/v1/relationships/%d", rel.ID), cookie,
		`{"kind":"spouse_partner"}`,
		map[string]string{httpapi.IfMatchHeader: fmt.Sprintf(`"%d"`, rel.Version)}).StatusCode)
	require.Equal(t, http.StatusOK,
		archiveRelationship(t, env, cookie, rel.ID, rel.Version+1, "Ended.").StatusCode)

	for _, action := range []string{"relationship.create", "relationship.update", "relationship.archive"} {
		var n int
		var actor int64
		var kind string
		var resourceID int64
		require.NoError(t, env.db.QueryRow(`
			SELECT count(*), coalesce(max(actor_user_id), 0),
			       coalesce(max(resource_kind), ''), coalesce(max(resource_id), 0)
			  FROM audit_events
			 WHERE action = ? AND outcome = 'success'`, action).Scan(&n, &actor, &kind, &resourceID))
		assert.Equal(t, 1, n, "%s must be audited", action)
		assert.Equal(t, int64(1), actor, "%s must record who did it", action)
		assert.Equal(t, "relationship", kind)
		assert.Equal(t, rel.ID, resourceID)
	}

	// The restricted note is context for an officer, not something to copy
	// into a log line that a wider audience reads.
	var leaked int
	require.NoError(t, env.db.QueryRow(`
		SELECT count(*) FROM audit_events
		 WHERE coalesce(detail_json, '') LIKE '%Shares a mailbox%'`).Scan(&leaked))
	assert.Zero(t, leaked, "a restricted context note must not be copied into audit detail")
}

// TestRelationshipsRequireTheCapability covers authorization on every route.
func TestRelationshipsRequireTheCapability(t *testing.T) {
	env := setupAuthzTest(t, "acs_coordinator")
	cookie := env.signIn(t)

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/relationships", `{"from_person_id":1,"to_person_id":2,"kind":"household"}`},
		{http.MethodGet, "/api/v1/relationships/1", ""},
		{http.MethodGet, "/api/v1/members/1/relationships", ""},
		{http.MethodPatch, "/api/v1/relationships/1", `{"kind":"household"}`},
		{http.MethodPost, "/api/v1/relationships/1/archive", `{}`},
	} {
		resp := doWithHeaders(t, env, tc.method, tc.path, cookie, tc.body, nil)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"%s %s must require relationship.manage", tc.method, tc.path)

		anon := doWithHeaders(t, env, tc.method, tc.path, nil, tc.body, nil)
		assert.Equal(t, http.StatusUnauthorized, anon.StatusCode,
			"%s %s must refuse an anonymous caller", tc.method, tc.path)
	}
}

// TestRelationshipManageIsNotRecordAccess is the capability half of ADR-0010.
// An officer may hold relationship.manage and still have no business reading
// the records at either end.
func TestRelationshipManageIsNotRecordAccess(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	a := seedPersonForRequest(t, env, "Linked A")
	b := seedPersonForRequest(t, env, "Linked B")

	require.Equal(t, http.StatusOK,
		createRelationship(t, env, cookie, a, b, "spouse_partner", "").StatusCode)

	var grants int
	require.NoError(t, env.db.QueryRow(
		`SELECT count(*) FROM member_access_grants`).Scan(&grants))
	assert.Zero(t, grants, "recording a relationship must create no access grant")

	var roleGrants int
	require.NoError(t, env.db.QueryRow(
		`SELECT count(*) FROM user_role_grants WHERE user_id != 1`).Scan(&roleGrants))
	assert.Zero(t, roleGrants, "recording a relationship must grant nobody a role")
}

// TestRelationshipOperationsDeclareMetadata guards the catalog.
func TestRelationshipOperationsDeclareMetadata(t *testing.T) {
	meta := httpapi.AllMeta()
	for _, opID := range []string{
		"relationship-create", "relationship-get", "person-relationships-list",
		"relationship-update", "relationship-archive",
	} {
		m, ok := meta[opID]
		require.True(t, ok, "%s must be registered", opID)
		assert.Equal(t, "relationship.manage", m.RequiredCapability,
			"%s must require relationship.manage and nothing weaker", opID)
		assert.NotEmpty(t, m.AuditAction, "%s must declare an audit action", opID)
	}
}

// --- Independence: the three ways this could quietly become authorization ---

// TestRelationshipGrantsNoProfileAccess is the one that matters most. A spouse
// with a relationship row and no grant must see exactly what a stranger sees.
func TestRelationshipGrantsNoProfileAccess(t *testing.T) {
	e := setupMemberSelfService(t)

	// Officer records the household: Dale (granted to his own account) is
	// married to Marguerite (whose record his account was NOT granted).
	resp := createRelationship(t, e.authzEnv, e.officer,
		e.fullPersonID, e.strangerPersonID, "spouse_partner", "Married thirty years.")
	require.Equal(t, http.StatusOK, resp.StatusCode, readAll(t, resp))

	// Dale's own record list is unchanged: one record, his own.
	listed := e.do(t, http.MethodGet, "/api/v1/me/records", e.full, "")
	require.Equal(t, http.StatusOK, listed.StatusCode)
	var records []apiMemberProfile
	require.NoError(t, json.NewDecoder(listed.Body).Decode(&records))
	require.Len(t, records, 1, "a marriage must not add a record to the list")
	assert.Equal(t, e.fullPersonID, records[0].PersonID)

	// And his wife's record answers exactly as it did before: not merely
	// filtered, but indistinguishable from a record that does not exist.
	spouse := e.do(t, http.MethodGet,
		fmt.Sprintf("/api/v1/me/records/%d", e.strangerPersonID), e.full, "")
	missing := e.do(t, http.MethodGet, "/api/v1/me/records/999999", e.full, "")
	assert.Equal(t, http.StatusNotFound, spouse.StatusCode,
		"being someone's spouse must not open their record")
	assert.Equal(t, readAll(t, missing), readAll(t, spouse),
		"a related record must be indistinguishable from one that does not exist")
}

// TestSuggestingAboutAnotherPersonNeedsNoRelationship is the converse. If a
// relationship were quietly required to suggest a correction, the club would
// have invented exactly the family-helper permission this bead refuses.
func TestSuggestingAboutAnotherPersonNeedsNoRelationship(t *testing.T) {
	e := setupMemberSelfService(t)

	var rels int
	require.NoError(t, e.db.QueryRow(`SELECT count(*) FROM person_relationships`).Scan(&rels))
	require.Zero(t, rels, "there is deliberately no relationship row here")

	resp := e.submit(t, e.full, "no-relationship-needed", `{
		"about_name":"Someone I met at the hamfest",
		"stated_relationship":"We are in the same ARES group",
		"summary":"His call sign is printed wrong in the newsletter.",
		"items":[{"operation":"other","proposed_value":"Call sign correction"}]
	}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode, readAll(t, resp))

	require.NoError(t, e.db.QueryRow(`SELECT count(*) FROM person_relationships`).Scan(&rels))
	assert.Zero(t, rels, "submitting a suggestion must not create a relationship either")
}

// TestRelationshipsAreInvisibleToMembers proves the restricted note and the
// links themselves stay on the officer side of the wall.
func TestRelationshipsAreInvisibleToMembers(t *testing.T) {
	e := setupMemberSelfService(t)

	const restricted = "Handles his post since the stroke."
	resp := createRelationship(t, e.authzEnv, e.officer,
		e.fullPersonID, e.strangerPersonID, "spouse_partner", restricted)
	require.Equal(t, http.StatusOK, resp.StatusCode, readAll(t, resp))

	for _, path := range []string{
		"/api/v1/me/records",
		fmt.Sprintf("/api/v1/me/records/%d", e.fullPersonID),
		"/api/v1/me/change-requests",
	} {
		body := readAll(t, e.do(t, http.MethodGet, path, e.full, ""))
		assert.NotContains(t, body, restricted,
			"%s must not carry a restricted officer note", path)
		assert.NotContains(t, strings.ToLower(body), "spouse_partner",
			"%s must not expose relationships at all", path)
	}

	// The member-facing API has no relationship route to reach either.
	assert.Equal(t, http.StatusForbidden,
		e.do(t, http.MethodGet,
			fmt.Sprintf("/api/v1/members/%d/relationships", e.fullPersonID), e.full, "").StatusCode,
		"a member must not be able to list a household, not even their own")
}

// TestDirectoryExposesNoRelationships keeps the links out of the one member
// surface that lists other people.
func TestDirectoryExposesNoRelationships(t *testing.T) {
	env := setupAuthzTest(t, "member", "secretary")
	cookie := env.signIn(t)

	viewer := dirMember(t, env, "Directory Viewer", "W3VIEW", "full", "approved")
	grantAccess(t, env, viewer)
	listed := dirMember(t, env, "Listed Member", "W3LIST", "full", "approved")

	const restricted = "Shares a household with the viewer."
	require.Equal(t, http.StatusOK,
		createRelationship(t, env, cookie, viewer, listed, "household", restricted).StatusCode)

	resp := env.do(t, http.MethodGet, "/api/v1/directory", cookie, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readAll(t, resp)

	assert.NotContains(t, body, restricted)
	assert.NotContains(t, body, "household")
	assert.NotContains(t, body, "relationship")
}
