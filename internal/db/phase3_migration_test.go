package db

import (
	"context"
	"database/sql"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
)

// preAccessVersion is the last migration before the Phase 3 access foundation.
// Tests that exercise the users.person_id backfill stop here, create the
// identities an existing installation would already have, and then migrate the
// rest of the way.
const (
	preAccessVersion = 8
	accessVersion    = 9
)

// seedUser creates a synthetic user, optionally linked to a person the way
// Phase 1 linked officer identities. All data is synthetic.
func seedUser(t *testing.T, db *sql.DB, email string, personID *int64) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO users (email, person_id) VALUES (?, ?)`, email, personID)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

// queries returns the generated query set. Tests assert through this, not
// through hand-written SQL, so a change to the shipped query is what breaks
// them.
func queries(db *sql.DB) *sqlcgen.Queries { return sqlcgen.New(db) }

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
func nullInt(i int64) sql.NullInt64      { return sql.NullInt64{Int64: i, Valid: true} }

// TestAccessGrantBackfill proves the ADR-0010 carry-forward: every existing
// users.person_id link becomes exactly one active self grant attributed to no
// officer, and a user with no link gets nothing invented for it.
func TestAccessGrantBackfill(t *testing.T) {
	database := openTestDB(t)
	migrateTo(t, database, preAccessVersion)

	linkedPerson := seedPerson(t, database, "Linked Officer")
	linked := seedUser(t, database, "officer@example.test", &linkedPerson)
	unlinked := seedUser(t, database, "service@example.test", nil)

	migrateTo(t, database, accessVersion)

	var (
		personID   int64
		accessKind string
		grantedBy  sql.NullInt64
		reason     sql.NullString
	)
	require.NoError(t, database.QueryRow(`
		SELECT person_id, access_kind, granted_by, reason
		  FROM member_access_grants
		 WHERE user_id = ? AND revoked_at IS NULL`, linked).
		Scan(&personID, &accessKind, &grantedBy, &reason))
	assert.Equal(t, linkedPerson, personID)
	assert.Equal(t, "self", accessKind)
	assert.False(t, grantedBy.Valid, "a system backfill has no granting officer")
	assert.Contains(t, reason.String, "Carried forward")

	var forUnlinked int
	require.NoError(t, database.QueryRow(
		`SELECT count(*) FROM member_access_grants WHERE user_id = ?`, unlinked).Scan(&forUnlinked))
	assert.Equal(t, 0, forUnlinked, "a user with no person link gets no invented access")
}

// TestAccessBackfillSurvivesDownUp proves the round trip does not duplicate
// authority: users.person_id is still the source on the way back up, and the
// user ends with exactly one active grant, not two.
func TestAccessBackfillSurvivesDownUp(t *testing.T) {
	database := openTestDB(t)
	migrateTo(t, database, preAccessVersion)

	personID := seedPerson(t, database, "Round Trip")
	userID := seedUser(t, database, "roundtrip@example.test", &personID)

	migrateTo(t, database, accessVersion)

	goose.SetBaseFS(migrations)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.DownTo(database, "migrations", preAccessVersion))

	// The link the backfill read is untouched, which is what makes the down
	// migration lossless.
	var stillLinked sql.NullInt64
	require.NoError(t, database.QueryRow(
		`SELECT person_id FROM users WHERE id = ?`, userID).Scan(&stillLinked))
	assert.Equal(t, personID, stillLinked.Int64)

	migrateTo(t, database, accessVersion)

	var n int
	require.NoError(t, database.QueryRow(
		`SELECT count(*) FROM member_access_grants WHERE user_id = ? AND revoked_at IS NULL`,
		userID).Scan(&n))
	assert.Equal(t, 1, n, "re-running the backfill must not duplicate authority")
}

// TestOnlyAnActiveGrantConfersAccess is the central Phase 3 security property,
// asserted through the shipped authorization query rather than through the
// table. The user still carries users.person_id, so if the query ever grew a
// fallback to that column, or stopped honoring revocation, this goes red.
func TestOnlyAnActiveGrantConfersAccess(t *testing.T) {
	database := openTestDB(t)
	// Seed the identity the way an existing installation carries it, so the
	// grant under test is the one the backfill produced.
	migrateTo(t, database, preAccessVersion)
	personID := seedPerson(t, database, "Ada Member")
	userID := seedUser(t, database, "ada@example.test", &personID)
	migrateTo(t, database, accessVersion)

	ctx := context.Background()
	q := queries(database)

	// The backfill granted access, so the user starts able to see the record.
	granted, err := q.ListActiveAccessGrantsForUser(ctx, userID)
	require.NoError(t, err)
	require.Len(t, granted, 1)
	assert.Equal(t, personID, granted[0].PersonID)
	assert.Equal(t, "Ada Member", granted[0].DisplayName)

	// Revoke it. users.person_id is deliberately left in place.
	revoked, err := q.RevokeMemberAccess(ctx, sqlcgen.RevokeMemberAccessParams{
		RevokedAt:    nullString("2026-08-09T12:00:00.000Z"),
		RevokedBy:    nullInt(userID),
		RevokeReason: nullString("left the club"),
		ID:           granted[0].GrantID,
		Version:      1,
	})
	require.NoError(t, err)
	assert.True(t, revoked.RevokedAt.Valid)

	var linkIntact sql.NullInt64
	require.NoError(t, database.QueryRow(
		`SELECT person_id FROM users WHERE id = ?`, userID).Scan(&linkIntact))
	require.True(t, linkIntact.Valid, "the test is only meaningful while the legacy link remains")

	after, err := q.ListActiveAccessGrantsForUser(ctx, userID)
	require.NoError(t, err)
	assert.Empty(t, after, "a revoked grant confers no access even though users.person_id still points at the record")

	count, err := q.CountActiveAccessGrant(ctx, sqlcgen.CountActiveAccessGrantParams{
		UserID: userID, PersonID: personID,
	})
	require.NoError(t, err)
	assert.Zero(t, count, "the single-record probe must agree with the list")
}

// TestRevokeIsVersionGuarded proves a stale revoke is detected rather than
// silently no-opping.
func TestRevokeIsVersionGuarded(t *testing.T) {
	database := openTestDB(t)
	require.NoError(t, Migrate(database))
	ctx := context.Background()
	q := queries(database)

	personID := seedPerson(t, database, "Guarded")
	userID := seedUser(t, database, "guarded@example.test", nil)
	grant, err := q.GrantMemberAccess(ctx, sqlcgen.GrantMemberAccessParams{
		UserID: userID, PersonID: personID, AccessKind: "self",
		GrantedAt: "2026-08-09T12:00:00.000Z",
	})
	require.NoError(t, err)

	revoke := func(version int64) error {
		_, err := q.RevokeMemberAccess(ctx, sqlcgen.RevokeMemberAccessParams{
			RevokedAt: nullString("2026-08-09T12:05:00.000Z"),
			ID:        grant.ID,
			Version:   version,
		})
		return err
	}
	require.NoError(t, revoke(grant.Version))
	assert.ErrorIs(t, revoke(grant.Version), sql.ErrNoRows,
		"a stale revoke must report no rows so the caller can map it to ErrStale")
}

// TestSharedMailboxReachesOnlyGrantedRecords proves the household case: one
// user may hold several grants, and sees exactly those records and no others.
func TestSharedMailboxReachesOnlyGrantedRecords(t *testing.T) {
	database := openTestDB(t)
	require.NoError(t, Migrate(database))
	ctx := context.Background()
	q := queries(database)

	first := seedPerson(t, database, "Alpha Household")
	second := seedPerson(t, database, "Bravo Household")
	stranger := seedPerson(t, database, "Zulu Stranger")
	userID := seedUser(t, database, "household@example.test", nil)

	for _, p := range []int64{first, second} {
		_, err := q.GrantMemberAccess(ctx, sqlcgen.GrantMemberAccessParams{
			UserID: userID, PersonID: p, AccessKind: "self",
			GrantedAt: "2026-08-09T12:00:00.000Z",
		})
		require.NoError(t, err)
	}

	rows, err := q.ListActiveAccessGrantsForUser(ctx, userID)
	require.NoError(t, err)
	reachable := make([]int64, 0, len(rows))
	for _, r := range rows {
		reachable = append(reachable, r.PersonID)
	}
	assert.ElementsMatch(t, []int64{first, second}, reachable)
	assert.NotContains(t, reachable, stranger, "an ungranted record is not reachable")
}

// TestActiveGrantIsUniquePerPair proves the database refuses a second active
// grant for the same pair, and that re-granting after revocation is a new row
// rather than an undelete.
func TestActiveGrantIsUniquePerPair(t *testing.T) {
	database := openTestDB(t)
	require.NoError(t, Migrate(database))
	ctx := context.Background()
	q := queries(database)

	personID := seedPerson(t, database, "Once Only")
	userID := seedUser(t, database, "once@example.test", nil)

	grant := func() (sqlcgen.MemberAccessGrant, error) {
		return q.GrantMemberAccess(ctx, sqlcgen.GrantMemberAccessParams{
			UserID: userID, PersonID: personID, AccessKind: "self",
			GrantedAt: "2026-08-09T12:00:00.000Z",
		})
	}
	first, err := grant()
	require.NoError(t, err)
	_, err = grant()
	assert.Error(t, err, "a second active grant for the same pair must fail")

	_, err = q.RevokeMemberAccess(ctx, sqlcgen.RevokeMemberAccessParams{
		RevokedAt: nullString("2026-08-09T12:05:00.000Z"),
		ID:        first.ID, Version: first.Version,
	})
	require.NoError(t, err)

	second, err := grant()
	require.NoError(t, err, "re-granting after revocation is allowed")
	assert.NotEqual(t, first.ID, second.ID, "re-granting creates a new row, keeping the history")
}

// TestRelationshipConfersNoAccess proves a family link is informational. Two
// people are related in both directions; the user granted only one record still
// reaches only that record.
func TestRelationshipConfersNoAccess(t *testing.T) {
	database := openTestDB(t)
	require.NoError(t, Migrate(database))
	ctx := context.Background()
	q := queries(database)

	spouseA := seedPerson(t, database, "Spouse Aye")
	spouseB := seedPerson(t, database, "Spouse Bee")
	userID := seedUser(t, database, "spousea@example.test", nil)

	_, err := q.GrantMemberAccess(ctx, sqlcgen.GrantMemberAccessParams{
		UserID: userID, PersonID: spouseA, AccessKind: "self",
		GrantedAt: "2026-08-09T12:00:00.000Z",
	})
	require.NoError(t, err)

	_, err = q.CreatePersonRelationship(ctx, sqlcgen.CreatePersonRelationshipParams{
		FromPersonID: spouseA, ToPersonID: spouseB, Kind: "spouse_partner",
	})
	require.NoError(t, err)

	rows, err := q.ListActiveAccessGrantsForUser(ctx, userID)
	require.NoError(t, err)
	require.Len(t, rows, 1, "a relationship must not add a reachable record")
	assert.Equal(t, spouseA, rows[0].PersonID)

	count, err := q.CountActiveAccessGrant(ctx, sqlcgen.CountActiveAccessGrantParams{
		UserID: userID, PersonID: spouseB,
	})
	require.NoError(t, err)
	assert.Zero(t, count, "being someone's spouse is not authority over their record")

	// The relationship is visible as information from either side.
	related, err := q.ListRelationshipsForPerson(ctx, spouseB)
	require.NoError(t, err)
	require.Len(t, related, 1)
	assert.Equal(t, "incoming", related[0].Direction)
	assert.Equal(t, "Spouse Aye", related[0].OtherDisplayName)
}

// TestRelationshipTableCarriesNoAuthority proves the claim structurally: the
// table has no column that could reference a user as a subject of access, and
// no foreign key into member_access_grants.
func TestRelationshipTableCarriesNoAuthority(t *testing.T) {
	database := openTestDB(t)
	require.NoError(t, Migrate(database))

	rows, err := database.Query(`SELECT name FROM pragma_table_info('person_relationships')`)
	require.NoError(t, err)
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		columns = append(columns, name)
	}
	require.NoError(t, rows.Err())

	// created_by and archived_by are actor provenance: who recorded the fact.
	// Any other user reference would be a subject of access.
	for _, forbidden := range []string{"user_id", "grant_id", "access_kind", "granted_by"} {
		assert.NotContains(t, columns, forbidden,
			"person_relationships must not carry %s; access lives in member_access_grants", forbidden)
	}

	fkRows, err := database.Query(`SELECT "table" FROM pragma_foreign_key_list('person_relationships')`)
	require.NoError(t, err)
	defer fkRows.Close()
	for fkRows.Next() {
		var table string
		require.NoError(t, fkRows.Scan(&table))
		assert.NotEqual(t, "member_access_grants", table,
			"a relationship must not reference an access grant")
	}
	require.NoError(t, fkRows.Err())
}

// TestRelationshipConstraints proves the schema rejects the shapes the design
// forbids.
func TestRelationshipConstraints(t *testing.T) {
	database := openTestDB(t)
	require.NoError(t, Migrate(database))
	ctx := context.Background()
	q := queries(database)

	one := seedPerson(t, database, "Person One")
	two := seedPerson(t, database, "Person Two")

	t.Run("nobody is related to themselves", func(t *testing.T) {
		_, err := q.CreatePersonRelationship(ctx, sqlcgen.CreatePersonRelationshipParams{
			FromPersonID: one, ToPersonID: one, Kind: "household",
		})
		assert.Error(t, err)
	})

	t.Run("kind must be known", func(t *testing.T) {
		_, err := q.CreatePersonRelationship(ctx, sqlcgen.CreatePersonRelationshipParams{
			FromPersonID: one, ToPersonID: two, Kind: "business_partner",
		})
		assert.Error(t, err)
	})

	t.Run("one active link per pair and kind", func(t *testing.T) {
		add := func() error {
			_, err := q.CreatePersonRelationship(ctx, sqlcgen.CreatePersonRelationshipParams{
				FromPersonID: one, ToPersonID: two, Kind: "spouse_partner",
			})
			return err
		}
		require.NoError(t, add())
		assert.Error(t, add())
	})

	t.Run("a relationship needs real people", func(t *testing.T) {
		_, err := q.CreatePersonRelationship(ctx, sqlcgen.CreatePersonRelationshipParams{
			FromPersonID: one, ToPersonID: 9999, Kind: "household",
		})
		assert.Error(t, err)
	})
}

// seedRequest creates a submitted officer-entered request and returns its ID.
func seedRequest(t *testing.T, database *sql.DB, targetPerson *int64) int64 {
	t.Helper()
	ctx := context.Background()
	req, err := queries(database).CreateChangeRequest(ctx, sqlcgen.CreateChangeRequestParams{
		Source:  "officer_phone",
		Status:  "submitted",
		Summary: "Caller reports a new mobile number.",
		TargetPersonID: func() sql.NullInt64 {
			if targetPerson == nil {
				return sql.NullInt64{}
			}
			return nullInt(*targetPerson)
		}(),
		SubmittedAt: "2026-08-09T12:00:00.000Z",
	})
	require.NoError(t, err)
	return req.ID
}

// TestChangeRequestSourceConstraints proves the intake rules the design states:
// an authenticated member request names its requester, and blind public intake
// never carries one.
func TestChangeRequestSourceConstraints(t *testing.T) {
	database := openTestDB(t)
	require.NoError(t, Migrate(database))
	ctx := context.Background()
	q := queries(database)

	userID := seedUser(t, database, "member@example.test", nil)

	create := func(source string, requester sql.NullInt64) error {
		_, err := q.CreateChangeRequest(ctx, sqlcgen.CreateChangeRequestParams{
			Source: source, Status: "submitted", RequesterUserID: requester,
			Summary: "Please correct my call sign.", SubmittedAt: "2026-08-09T12:00:00.000Z",
		})
		return err
	}

	assert.Error(t, create("member", sql.NullInt64{}),
		"an authenticated member request must name its requester")
	assert.NoError(t, create("member", nullInt(userID)))

	assert.Error(t, create("public", nullInt(userID)),
		"blind public intake authenticates nobody and must not carry a requester")
	assert.NoError(t, create("public", sql.NullInt64{}))

	assert.Error(t, create("carrier_pigeon", sql.NullInt64{}), "an unknown source is refused")

	t.Run("a request needs a summary", func(t *testing.T) {
		_, err := q.CreateChangeRequest(ctx, sqlcgen.CreateChangeRequestParams{
			Source: "officer_mail", Status: "submitted", Summary: "   ",
			SubmittedAt: "2026-08-09T12:00:00.000Z",
		})
		assert.Error(t, err)
	})
}

// TestBlindIntakeStoresHintsWithoutCanonicalData proves the non-disclosure
// shape at the storage layer: a public request records what was supplied,
// creates no person, and is not linked to one until an officer triages it.
func TestBlindIntakeStoresHintsWithoutCanonicalData(t *testing.T) {
	database := openTestDB(t)
	require.NoError(t, Migrate(database))
	ctx := context.Background()
	q := queries(database)

	// A real member exists whose name the submitter half-remembers.
	existing := seedPerson(t, database, "Kilo Member")

	var personsBefore int
	require.NoError(t, database.QueryRow(`SELECT count(*) FROM persons`).Scan(&personsBefore))

	req, err := q.CreateChangeRequest(ctx, sqlcgen.CreateChangeRequestParams{
		Source: "public", Status: "submitted",
		SuppliedName:     nullString("K. Member"),
		SuppliedCallSign: nullString("W3ABC"),
		SuppliedContact:  nullString("someone@example.test"),
		Summary:          "Their phone number in the roster is out of date.",
		SubmittedAt:      "2026-08-09T12:00:00.000Z",
	})
	require.NoError(t, err)
	assert.False(t, req.TargetPersonID.Valid, "blind intake performs no lookup")

	var personsAfter int
	require.NoError(t, database.QueryRow(`SELECT count(*) FROM persons`).Scan(&personsAfter))
	assert.Equal(t, personsBefore, personsAfter, "intake must not create canonical records")

	// The unlinked queue is what an officer triages.
	pending, err := q.ListUntargetedChangeRequests(ctx, sqlcgen.ListUntargetedChangeRequestsParams{
		Limit: 10, Offset: 0,
	})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, req.ID, pending[0].ID)

	// Linking it later leaves the supplied snapshot exactly as submitted.
	linked, err := q.SetChangeRequestTarget(ctx, sqlcgen.SetChangeRequestTargetParams{
		TargetPersonID: nullInt(existing),
		TriagedBy:      nullInt(seedUser(t, database, "officer@example.test", nil)),
		TriagedAt:      nullString("2026-08-09T13:00:00.000Z"),
		ID:             req.ID, Version: req.Version,
	})
	require.NoError(t, err)
	assert.Equal(t, existing, linked.TargetPersonID.Int64)
	assert.Equal(t, "K. Member", linked.SuppliedName.String,
		"triage records the officer's conclusion without rewriting what was supplied")
}

// TestUnsupportedItemCanNeverBeApproved proves the 'other' escape hatch stays
// unapprovable at the storage layer, so no future service can route a
// membership, FCC, or dues suggestion through a generic mutation path.
func TestUnsupportedItemCanNeverBeApproved(t *testing.T) {
	database := openTestDB(t)
	require.NoError(t, Migrate(database))
	ctx := context.Background()
	q := queries(database)

	requestID := seedRequest(t, database, nil)
	reviewer := seedUser(t, database, "reviewer@example.test", nil)

	item, err := q.CreateChangeRequestItem(ctx, sqlcgen.CreateChangeRequestItemParams{
		RequestID: requestID, Ordinal: 0, Operation: "other",
		ProposedValue: nullString("Please make me an honorary member."),
		Sensitivity:   "ordinary",
	})
	require.NoError(t, err)

	_, err = q.DecideChangeRequestItem(ctx, sqlcgen.DecideChangeRequestItemParams{
		Status: "approved", ReviewedBy: nullInt(reviewer),
		ReviewedAt: nullString("2026-08-09T13:00:00.000Z"),
		ID:         item.ID, Version: item.Version,
	})
	assert.Error(t, err, "an unsupported suggestion must not be approvable")

	// It can still be rejected or sent for verification, which is how an
	// officer clears the queue.
	_, err = q.DecideChangeRequestItem(ctx, sqlcgen.DecideChangeRequestItemParams{
		Status: "rejected", ReviewedBy: nullInt(reviewer),
		ReviewedAt:     nullString("2026-08-09T13:00:00.000Z"),
		DecisionReason: nullString("Honorary status is a board decision."),
		ID:             item.ID, Version: item.Version,
	})
	assert.NoError(t, err)

	// An unknown operation cannot be stored at all.
	_, err = q.CreateChangeRequestItem(ctx, sqlcgen.CreateChangeRequestItemParams{
		RequestID: requestID, Ordinal: 1, Operation: "membership.lifecycle.set",
		Sensitivity: "ordinary",
	})
	assert.Error(t, err, "only allowlisted operations exist")
}

// TestItemDecisionHasASingleWinner proves two reviewers deciding the same item
// do not both succeed: the second gets no rows rather than overwriting.
func TestItemDecisionHasASingleWinner(t *testing.T) {
	database := openTestDB(t)
	require.NoError(t, Migrate(database))
	ctx := context.Background()
	q := queries(database)

	requestID := seedRequest(t, database, nil)
	first := seedUser(t, database, "first@example.test", nil)
	second := seedUser(t, database, "second@example.test", nil)

	item, err := q.CreateChangeRequestItem(ctx, sqlcgen.CreateChangeRequestItemParams{
		RequestID: requestID, Ordinal: 0, Operation: "person.call_sign.set",
		ProposedValue: nullString("W3XYZ"), Sensitivity: "ordinary",
	})
	require.NoError(t, err)

	decide := func(reviewer int64, status string, version int64) error {
		_, err := q.DecideChangeRequestItem(ctx, sqlcgen.DecideChangeRequestItemParams{
			Status: status, ReviewedBy: nullInt(reviewer),
			ReviewedAt: nullString("2026-08-09T13:00:00.000Z"),
			ID:         item.ID, Version: version,
		})
		return err
	}

	require.NoError(t, decide(first, "approved", item.Version))
	assert.ErrorIs(t, decide(second, "rejected", item.Version), sql.ErrNoRows,
		"a stale second decision must not overwrite the first")

	// Even at the current version, a decided item is no longer pending.
	current, err := q.GetChangeRequestItem(ctx, item.ID)
	require.NoError(t, err)
	assert.ErrorIs(t, decide(second, "rejected", current.Version), sql.ErrNoRows,
		"a decided item cannot be decided again")

	assert.Equal(t, "approved", current.Status)
	assert.Equal(t, first, current.ReviewedBy.Int64)
}

// TestApprovedItemAppliesExactlyOnce proves the exactly-once stamp: a replayed
// apply changes nothing and the recorded outcome stands.
func TestApprovedItemAppliesExactlyOnce(t *testing.T) {
	database := openTestDB(t)
	require.NoError(t, Migrate(database))
	ctx := context.Background()
	q := queries(database)

	personID := seedPerson(t, database, "Applied Once")
	requestID := seedRequest(t, database, &personID)
	reviewer := seedUser(t, database, "applier@example.test", nil)

	item, err := q.CreateChangeRequestItem(ctx, sqlcgen.CreateChangeRequestItemParams{
		RequestID: requestID, Ordinal: 0, Operation: "person.display_name.set",
		ProposedValue: nullString("Applied Once Jr."),
		TargetKind:    nullString("person"), TargetID: nullInt(personID),
		TargetVersion: nullInt(1), Sensitivity: "ordinary",
	})
	require.NoError(t, err)

	approved, err := q.DecideChangeRequestItem(ctx, sqlcgen.DecideChangeRequestItemParams{
		Status: "approved", ReviewedBy: nullInt(reviewer),
		ReviewedAt: nullString("2026-08-09T13:00:00.000Z"),
		ID:         item.ID, Version: item.Version,
	})
	require.NoError(t, err)

	apply := func(version int64, resourceID int64) error {
		_, err := q.MarkChangeRequestItemApplied(ctx, sqlcgen.MarkChangeRequestItemAppliedParams{
			AppliedAt:           nullString("2026-08-09T13:01:00.000Z"),
			AppliedResourceKind: nullString("person"),
			AppliedResourceID:   nullInt(resourceID),
			ID:                  item.ID, Version: version,
		})
		return err
	}
	require.NoError(t, apply(approved.Version, personID))

	applied, err := q.GetChangeRequestItem(ctx, item.ID)
	require.NoError(t, err)
	assert.ErrorIs(t, apply(applied.Version, 4242), sql.ErrNoRows,
		"an already-applied item must not apply a second time")

	unchanged, err := q.GetChangeRequestItem(ctx, item.ID)
	require.NoError(t, err)
	assert.Equal(t, personID, unchanged.AppliedResourceID.Int64,
		"the recorded outcome stands after a replay")
}

// TestPendingItemsGateResolution proves a request resolves only when every item
// is terminal, using the counter the review service reads.
func TestPendingItemsGateResolution(t *testing.T) {
	database := openTestDB(t)
	require.NoError(t, Migrate(database))
	ctx := context.Background()
	q := queries(database)

	requestID := seedRequest(t, database, nil)
	reviewer := seedUser(t, database, "gate@example.test", nil)

	var items []sqlcgen.MemberChangeRequestItem
	for i, op := range []string{"person.call_sign.set", "contact_method.add"} {
		item, err := q.CreateChangeRequestItem(ctx, sqlcgen.CreateChangeRequestItemParams{
			RequestID: requestID, Ordinal: int64(i), Operation: op,
			ProposedValue: nullString("value"), Sensitivity: "ordinary",
		})
		require.NoError(t, err)
		items = append(items, item)
	}

	pending, err := q.CountPendingChangeRequestItems(ctx, requestID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), pending)

	// A mixed decision: one approved, one rejected. Both are terminal.
	_, err = q.DecideChangeRequestItem(ctx, sqlcgen.DecideChangeRequestItemParams{
		Status: "approved", ReviewedBy: nullInt(reviewer),
		ReviewedAt: nullString("2026-08-09T13:00:00.000Z"),
		ID:         items[0].ID, Version: items[0].Version,
	})
	require.NoError(t, err)

	pending, err = q.CountPendingChangeRequestItems(ctx, requestID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), pending, "one decision does not resolve the request")

	_, err = q.DecideChangeRequestItem(ctx, sqlcgen.DecideChangeRequestItemParams{
		Status: "rejected", ReviewedBy: nullInt(reviewer),
		ReviewedAt:     nullString("2026-08-09T13:00:00.000Z"),
		DecisionReason: nullString("Could not verify."),
		ID:             items[1].ID, Version: items[1].Version,
	})
	require.NoError(t, err)

	pending, err = q.CountPendingChangeRequestItems(ctx, requestID)
	require.NoError(t, err)
	assert.Zero(t, pending)

	// The rejected item keeps its decision; approval of one did not sweep it.
	rejected, err := q.GetChangeRequestItem(ctx, items[1].ID)
	require.NoError(t, err)
	assert.Equal(t, "rejected", rejected.Status)
	assert.False(t, rejected.AppliedAt.Valid, "a rejected item is never applied")
}

// TestItemDecisionConstraints proves the remaining storage-level rules a review
// service must not be able to violate.
func TestItemDecisionConstraints(t *testing.T) {
	database := openTestDB(t)
	require.NoError(t, Migrate(database))
	requestID := seedRequest(t, database, nil)

	insert := func(cols, vals string, args ...any) error {
		_, err := database.Exec(
			`INSERT INTO member_change_request_items (request_id, ordinal, operation, `+cols+
				`) VALUES (?, ?, 'person.call_sign.set', `+vals+`)`,
			append([]any{requestID}, args...)...)
		return err
	}

	t.Run("a decision names its reviewer", func(t *testing.T) {
		assert.Error(t, insert("status", "'approved'", int64(10)))
	})

	t.Run("a pending item has no reviewer", func(t *testing.T) {
		assert.Error(t, insert("status, reviewed_by, reviewed_at",
			"'pending', 1, '2026-08-09T13:00:00.000Z'", int64(11)))
	})

	t.Run("a sensitive approval carries a verification note", func(t *testing.T) {
		reviewer := seedUser(t, database, "sensitive@example.test", nil)
		assert.Error(t, insert("sensitivity, status, reviewed_by, reviewed_at",
			"'sensitive', 'approved', ?, '2026-08-09T13:00:00.000Z'", int64(12), reviewer))
		assert.NoError(t, insert("sensitivity, status, reviewed_by, reviewed_at, verification_note",
			"'sensitive', 'approved', ?, '2026-08-09T13:00:00.000Z', 'Called the published number back.'",
			int64(13), reviewer))
	})

	t.Run("only an approved item is applied", func(t *testing.T) {
		reviewer := seedUser(t, database, "applier2@example.test", nil)
		assert.Error(t, insert(
			"status, reviewed_by, reviewed_at, applied_at, applied_resource_kind, applied_resource_id",
			"'rejected', ?, '2026-08-09T13:00:00.000Z', '2026-08-09T13:01:00.000Z', 'person', 1",
			int64(14), reviewer))
	})

	t.Run("an item belongs to a real request", func(t *testing.T) {
		_, err := database.Exec(`
			INSERT INTO member_change_request_items (request_id, ordinal, operation)
			VALUES (9999, 0, 'person.call_sign.set')`)
		assert.Error(t, err)
	})
}

// TestChangeRequestTerminalStates proves a terminal request records when it
// became terminal, so "resolved" always has a resolution time.
func TestChangeRequestTerminalStates(t *testing.T) {
	database := openTestDB(t)
	require.NoError(t, Migrate(database))
	requestID := seedRequest(t, database, nil)

	_, err := database.Exec(`UPDATE member_change_requests SET status = 'resolved' WHERE id = ?`, requestID)
	assert.Error(t, err, "resolved requires a resolution time")

	_, err = database.Exec(`UPDATE member_change_requests SET status = 'withdrawn' WHERE id = ?`, requestID)
	assert.Error(t, err, "withdrawn requires a withdrawal time")

	_, err = database.Exec(`
		UPDATE member_change_requests SET status = 'resolved', resolved_at = '2026-08-09T14:00:00.000Z'
		 WHERE id = ?`, requestID)
	assert.NoError(t, err)
}

// TestPhase3RoleMatrix pins the authorization decision from the design: the
// executive officers review requests and manage access, the member role gets
// only its own record and the directory door, and nobody else gains anything.
func TestPhase3RoleMatrix(t *testing.T) {
	database := openTestDB(t)
	require.NoError(t, Migrate(database))

	officerCaps := []string{
		"change_request.manage", "change_request.review",
		"member_access.manage", "relationship.manage",
	}
	memberCaps := []string{
		"profile.self.read", "change_request.submit.member", "directory.read",
	}

	granted := func(t *testing.T, role string, category string) []string {
		t.Helper()
		rows, err := database.Query(`
			SELECT capability_code FROM role_capabilities
			 WHERE role_code = ? AND capability_code IN (
				SELECT code FROM capabilities WHERE category = ?)
			 ORDER BY capability_code`, role, category)
		require.NoError(t, err)
		defer rows.Close()

		var codes []string
		for rows.Next() {
			var c string
			require.NoError(t, rows.Scan(&c))
			codes = append(codes, c)
		}
		require.NoError(t, rows.Err())
		return codes
	}

	for _, code := range memberCaps {
		var category string
		require.NoError(t, database.QueryRow(
			`SELECT category FROM capabilities WHERE code = ?`, code).Scan(&category))
		assert.Equal(t, "member", category,
			"%s is member self-service, not an administrative membership read", code)
	}

	for _, role := range []string{"president", "vice_president", "secretary", "treasurer", "administrator"} {
		for _, code := range officerCaps {
			assert.Contains(t, granted(t, role, "membership"), code, "%s should hold %s", role, code)
		}
	}

	// The member role holds exactly the self-service set.
	assert.ElementsMatch(t, memberCaps, granted(t, "member", "member"))
	assert.Empty(t, granted(t, "member", "membership"),
		"the member role holds no administrative membership capability")
	assert.Empty(t, granted(t, "member", "treasury"),
		"the member role holds no treasury capability")

	// No officer role is handed the member self-service set by accident. The
	// administrator keeps its catalog-wide grant by design.
	for _, role := range []string{"president", "vice_president", "secretary", "treasurer",
		"webmaster", "trustee", "activities_manager", "acs_coordinator"} {
		assert.Empty(t, granted(t, role, "member"),
			"%s should not receive member self-service capabilities", role)
	}

	// Roles outside the executive set gain nothing from Phase 3.
	for _, role := range []string{"webmaster", "trustee", "activities_manager", "acs_coordinator"} {
		for _, code := range officerCaps {
			assert.NotContains(t, granted(t, role, "membership"), code,
				"%s should not receive %s", role, code)
		}
	}
}

// TestMemberRoleIsClassifiedAsMember proves the role kind was corrected: the
// member role is not an officer role now that it has a member-facing surface.
func TestMemberRoleIsClassifiedAsMember(t *testing.T) {
	database := openTestDB(t)
	require.NoError(t, Migrate(database))

	var kind string
	require.NoError(t, database.QueryRow(
		`SELECT kind FROM roles WHERE code = 'member'`).Scan(&kind))
	assert.Equal(t, "member", kind)

	// The officer roles keep their classification.
	for role, want := range map[string]string{
		"president": "executive", "treasurer": "executive",
		"trustee": "officer", "administrator": "technical",
	} {
		var got string
		require.NoError(t, database.QueryRow(
			`SELECT kind FROM roles WHERE code = ?`, role).Scan(&got))
		assert.Equal(t, want, got, "role %s", role)
	}
}
