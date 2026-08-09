package db

import (
	"database/sql"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// legacyLedgerVersion is the last migration before the Phase 2 ledger. Tests
// that exercise the ADR-0007 backfill stop here, write legacy data the way an
// earlier or manual path would have, and then migrate the rest of the way.
const (
	legacyLedgerVersion   = 6
	treasuryLedgerVersion = 7
)

// migrateTo runs migrations up to and including version v.
func migrateTo(t *testing.T, db *sql.DB, v int64) {
	t.Helper()
	goose.SetBaseFS(migrations)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, "migrations", v))
}

// seedActor creates the person/user rows the ledger foreign keys require and
// returns the user ID. All data is synthetic.
func seedActor(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO persons (display_name, sort_name) VALUES ('Treasury Tester', 'tester treasury')`)
	require.NoError(t, err)
	personID, err := res.LastInsertId()
	require.NoError(t, err)

	res, err = db.Exec(`INSERT INTO users (email, person_id) VALUES ('treasurer@example.test', ?)`, personID)
	require.NoError(t, err)
	userID, err := res.LastInsertId()
	require.NoError(t, err)
	return userID
}

// seedPerson creates a synthetic person and returns its ID.
func seedPerson(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO persons (display_name, sort_name) VALUES (?, ?)`, name, name)
	require.NoError(t, err)
	personID, err := res.LastInsertId()
	require.NoError(t, err)
	return personID
}

// seedMembership creates a person and an approved membership. Use it after the
// ledger migration, when the legacy columns no longer exist.
func seedMembership(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()
	res, err := db.Exec(`
		INSERT INTO memberships (person_id, base_type, lifecycle)
		VALUES (?, 'full', 'approved')`, seedPerson(t, db, name))
	require.NoError(t, err)
	membershipID, err := res.LastInsertId()
	require.NoError(t, err)
	return membershipID
}

// seedLegacyMembership creates a membership carrying the pre-Phase-2 legacy
// paid-through columns, the way an earlier or manual path would have. Pass nil
// for a membership with no legacy value. Valid only before migration 7.
func seedLegacyMembership(t *testing.T, db *sql.DB, name string, legacyUntil, legacyNote *string) int64 {
	t.Helper()
	res, err := db.Exec(`
		INSERT INTO memberships (person_id, base_type, lifecycle, legacy_current_until, legacy_current_until_note)
		VALUES (?, 'full', 'approved', ?, ?)`,
		seedPerson(t, db, name), legacyUntil, legacyNote)
	require.NoError(t, err)
	membershipID, err := res.LastInsertId()
	require.NoError(t, err)
	return membershipID
}

// TestLegacyCoverageBackfill proves the ADR-0007 conversion: every non-null
// legacy_current_until becomes exactly one legacy_import coverage event with
// the same date, the note survives as restricted source context, memberships
// with no legacy value produce nothing, and the columns are gone afterwards.
func TestLegacyCoverageBackfill(t *testing.T) {
	db := openTestDB(t)
	migrateTo(t, db, legacyLedgerVersion)

	withNote := seedLegacyMembership(t, db, "Paid With Note", new("2025-12-31"), new("check 1042, mailed late"))
	withoutNote := seedLegacyMembership(t, db, "Paid No Note", new("2024-12-31"), nil)
	// A historical value that violates the new December-31 write rule must be
	// preserved as imported rather than rewritten.
	offCycle := seedLegacyMembership(t, db, "Off Cycle", new("2023-06-30"), nil)
	none := seedLegacyMembership(t, db, "Never Recorded", nil, nil)

	migrateTo(t, db, treasuryLedgerVersion)

	var total int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM coverage_events WHERE reason_kind = 'legacy_import'`).Scan(&total))
	assert.Equal(t, 3, total, "one coverage event per non-null legacy value")

	for _, tc := range []struct {
		name         string
		membershipID int64
		paidThrough  string
		note         sql.NullString
	}{
		{"note preserved", withNote, "2025-12-31", sql.NullString{String: "check 1042, mailed late", Valid: true}},
		{"no note", withoutNote, "2024-12-31", sql.NullString{}},
		{"off-cycle date preserved", offCycle, "2023-06-30", sql.NullString{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var (
				paidThrough string
				note        sql.NullString
				decidedBy   sql.NullInt64
				paymentID   sql.NullInt64
			)
			require.NoError(t, db.QueryRow(`
				SELECT paid_through, source_note, decided_by, payment_id
				FROM coverage_events WHERE membership_id = ? AND reason_kind = 'legacy_import'`,
				tc.membershipID).Scan(&paidThrough, &note, &decidedBy, &paymentID))
			assert.Equal(t, tc.paidThrough, paidThrough)
			assert.Equal(t, tc.note, note)
			assert.False(t, decidedBy.Valid, "a system backfill has no deciding officer")
			assert.False(t, paymentID.Valid, "a legacy import invents no payment")
		})
	}

	var forNone int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM coverage_events WHERE membership_id = ?`, none).Scan(&forNone))
	assert.Equal(t, 0, forNone, "a membership with no legacy value gets no coverage event")

	for _, col := range []string{"legacy_current_until", "legacy_current_until_note"} {
		var n int
		require.NoError(t, db.QueryRow(
			`SELECT count(*) FROM pragma_table_info('memberships') WHERE name = ?`, col).Scan(&n))
		assert.Equal(t, 0, n, "column %s should be dropped", col)
	}
}

// TestLegacyBackfillLinksImportRun proves the coverage event points back at the
// committed import run that staged the member, when that link is recoverable.
func TestLegacyBackfillLinksImportRun(t *testing.T) {
	db := openTestDB(t)
	migrateTo(t, db, legacyLedgerVersion)

	actor := seedActor(t, db)
	linked := seedLegacyMembership(t, db, "Imported Member", new("2025-12-31"), nil)
	unlinked := seedLegacyMembership(t, db, "Hand Entered", new("2025-12-31"), nil)

	var linkedPerson int64
	require.NoError(t, db.QueryRow(`SELECT person_id FROM memberships WHERE id = ?`, linked).Scan(&linkedPerson))

	res, err := db.Exec(`
		INSERT INTO import_runs (source_kind, source_filename, source_sha256, uploaded_by,
			uploaded_at, status, idempotency_key, committed_by, committed_at)
		VALUES ('groupsio', 'members.csv', 'abc123', ?, '2026-01-02T00:00:00.000Z',
			'committed', 'key-1', ?, '2026-01-02T00:05:00.000Z')`, actor, actor)
	require.NoError(t, err)
	runID, err := res.LastInsertId()
	require.NoError(t, err)

	_, err = db.Exec(`
		INSERT INTO staged_import_rows (import_run_id, source_row_index, raw_json,
			normalized_json, match_person_id, proposed_action)
		VALUES (?, 0, '{}', '{}', ?, 'update')`, runID, linkedPerson)
	require.NoError(t, err)

	migrateTo(t, db, treasuryLedgerVersion)

	var got sql.NullInt64
	require.NoError(t, db.QueryRow(
		`SELECT import_run_id FROM coverage_events WHERE membership_id = ?`, linked).Scan(&got))
	assert.Equal(t, sql.NullInt64{Int64: runID, Valid: true}, got)

	require.NoError(t, db.QueryRow(
		`SELECT import_run_id FROM coverage_events WHERE membership_id = ?`, unlinked).Scan(&got))
	assert.False(t, got.Valid, "no recoverable import run means no invented link")
}

// TestLegacyBackfillSurvivesDownUp proves the round trip loses nothing and does
// not double-insert: the values return to the legacy columns on the way down
// and produce exactly one coverage event again on the way back up.
func TestLegacyBackfillSurvivesDownUp(t *testing.T) {
	db := openTestDB(t)
	migrateTo(t, db, legacyLedgerVersion)

	id := seedLegacyMembership(t, db, "Round Trip", new("2025-12-31"), new("cash at meeting"))

	migrateTo(t, db, treasuryLedgerVersion)

	goose.SetBaseFS(migrations)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.DownTo(db, "migrations", legacyLedgerVersion))

	var (
		until sql.NullString
		note  sql.NullString
	)
	require.NoError(t, db.QueryRow(
		`SELECT legacy_current_until, legacy_current_until_note FROM memberships WHERE id = ?`,
		id).Scan(&until, &note))
	assert.Equal(t, "2025-12-31", until.String)
	assert.Equal(t, "cash at meeting", note.String)

	migrateTo(t, db, treasuryLedgerVersion)

	var n int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM coverage_events WHERE membership_id = ? AND reason_kind = 'legacy_import'`,
		id).Scan(&n))
	assert.Equal(t, 1, n, "re-running the backfill must not duplicate coverage")
}

// TestCoverageLegacyBackfillIsUnique proves the database itself refuses a
// second legacy_import event, so no future code path can duplicate it.
func TestCoverageLegacyBackfillIsUnique(t *testing.T) {
	db := openTestDB(t)
	require.NoError(t, Migrate(db))
	id := seedMembership(t, db, "Only Once")

	insert := func() error {
		_, err := db.Exec(`
			INSERT INTO coverage_events (membership_id, paid_through, reason_kind, decided_at)
			VALUES (?, '2025-12-31', 'legacy_import', '2026-01-01T00:00:00.000Z')`, id)
		return err
	}
	require.NoError(t, insert())
	assert.Error(t, insert(), "a second legacy_import event for one membership must fail")
}

// TestIdempotencyRecordScope proves replay detection is scoped by actor and
// operation: the same key is reusable across actors and operations but unique
// within one.
func TestIdempotencyRecordScope(t *testing.T) {
	db := openTestDB(t)
	require.NoError(t, Migrate(db))
	actorA := seedActor(t, db)

	res, err := db.Exec(`INSERT INTO users (email) VALUES ('other@example.test')`)
	require.NoError(t, err)
	actorB, err := res.LastInsertId()
	require.NoError(t, err)

	insert := func(actor int64, op string) error {
		_, err := db.Exec(`
			INSERT INTO idempotency_records (actor_user_id, operation, idempotency_key, request_hash)
			VALUES (?, ?, 'key-1', 'hash-1')`, actor, op)
		return err
	}

	require.NoError(t, insert(actorA, "batch-post"))
	assert.Error(t, insert(actorA, "batch-post"), "same actor, operation, and key must collide")
	assert.NoError(t, insert(actorA, "payment-create"), "a different operation is a different scope")
	assert.NoError(t, insert(actorB, "batch-post"), "a different actor is a different scope")
}

// TestLedgerForeignKeyIntegrity proves the ledger cannot reference members,
// batches, or users that do not exist.
func TestLedgerForeignKeyIntegrity(t *testing.T) {
	db := openTestDB(t)
	require.NoError(t, Migrate(db))
	actor := seedActor(t, db)
	membership := seedMembership(t, db, "Real Member")

	t.Run("payment needs a real membership", func(t *testing.T) {
		_, err := db.Exec(`
			INSERT INTO payments (membership_id, amount_cents, method, received_on,
				entered_by, entered_at, receipt_code, entry_kind)
			VALUES (9999, 4000, 'cash', '2026-01-05', ?, '2026-01-05T00:00:00.000Z', 'R-1', 'original')`,
			actor)
		assert.Error(t, err)
	})

	t.Run("entry needs a real batch", func(t *testing.T) {
		_, err := db.Exec(`
			INSERT INTO payment_batch_entries (batch_id, membership_id, sequence, amount_cents,
				method, received_on, paid_through)
			VALUES (9999, ?, 1, 4000, 'cash', '2026-01-05', '2026-12-31')`, membership)
		assert.Error(t, err)
	})

	t.Run("coverage needs a real membership", func(t *testing.T) {
		_, err := db.Exec(`
			INSERT INTO coverage_events (membership_id, paid_through, reason_kind, decided_at)
			VALUES (9999, '2026-12-31', 'adjustment', '2026-01-05T00:00:00.000Z')`)
		assert.Error(t, err)
	})
}

// TestLedgerStateConstraints proves the schema rejects the states the design
// forbids, so no service can write them by mistake.
func TestLedgerStateConstraints(t *testing.T) {
	db := openTestDB(t)
	require.NoError(t, Migrate(db))
	actor := seedActor(t, db)
	membership := seedMembership(t, db, "Constrained")

	openBatch := func(t *testing.T) int64 {
		t.Helper()
		res, err := db.Exec(`
			INSERT INTO payment_batches (label, opened_by, opened_at)
			VALUES ('January dues', ?, '2026-01-05T00:00:00.000Z')`, actor)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		return id
	}

	t.Run("batch state must be known", func(t *testing.T) {
		_, err := db.Exec(`
			INSERT INTO payment_batches (label, state, opened_by, opened_at)
			VALUES ('bad', 'closed', ?, '2026-01-05T00:00:00.000Z')`, actor)
		assert.Error(t, err)
	})

	t.Run("posted batch needs actor and timestamp", func(t *testing.T) {
		id := openBatch(t)
		_, err := db.Exec(`UPDATE payment_batches SET state = 'posted' WHERE id = ?`, id)
		assert.Error(t, err)
	})

	t.Run("posted amount cannot be zero", func(t *testing.T) {
		_, err := db.Exec(`
			INSERT INTO payments (membership_id, amount_cents, method, received_on,
				entered_by, entered_at, receipt_code, entry_kind)
			VALUES (?, 0, 'cash', '2026-01-05', ?, '2026-01-05T00:00:00.000Z', 'R-zero', 'original')`,
			membership, actor)
		assert.Error(t, err)
	})

	t.Run("only a reversal is negative", func(t *testing.T) {
		_, err := db.Exec(`
			INSERT INTO payments (membership_id, amount_cents, method, received_on,
				entered_by, entered_at, receipt_code, entry_kind)
			VALUES (?, -4000, 'cash', '2026-01-05', ?, '2026-01-05T00:00:00.000Z', 'R-neg', 'original')`,
			membership, actor)
		assert.Error(t, err)
	})

	t.Run("an original corrects nothing", func(t *testing.T) {
		res, err := db.Exec(`
			INSERT INTO payments (membership_id, amount_cents, method, received_on,
				entered_by, entered_at, receipt_code, entry_kind)
			VALUES (?, 40000, 'check', '2026-01-05', ?, '2026-01-05T00:00:00.000Z', 'R-orig', 'original')`,
			membership, actor)
		require.NoError(t, err)
		original, err := res.LastInsertId()
		require.NoError(t, err)

		_, err = db.Exec(`
			INSERT INTO payments (membership_id, amount_cents, method, received_on,
				entered_by, entered_at, receipt_code, entry_kind, corrects_payment_id)
			VALUES (?, 4000, 'check', '2026-01-05', ?, '2026-01-05T00:00:00.000Z', 'R-orig2', 'original', ?)`,
			membership, actor, original)
		assert.Error(t, err)

		// One reversal per target; a second fork is refused.
		reversal := func(code string) error {
			_, err := db.Exec(`
				INSERT INTO payments (membership_id, amount_cents, method, received_on,
					entered_by, entered_at, receipt_code, entry_kind, corrects_payment_id)
				VALUES (?, -40000, 'check', '2026-01-05', ?, '2026-01-05T00:00:00.000Z', ?, 'reversal', ?)`,
				membership, actor, code, original)
			return err
		}
		require.NoError(t, reversal("R-rev"))
		assert.Error(t, reversal("R-rev2"), "a payment may be reversed only once")
	})

	t.Run("dates must be ISO", func(t *testing.T) {
		_, err := db.Exec(`
			INSERT INTO coverage_events (membership_id, paid_through, reason_kind, decided_at)
			VALUES (?, '12/31/2026', 'adjustment', '2026-01-05T00:00:00.000Z')`, membership)
		assert.Error(t, err)
	})

	t.Run("a payment coverage event names its payment", func(t *testing.T) {
		_, err := db.Exec(`
			INSERT INTO coverage_events (membership_id, paid_through, reason_kind, decided_at)
			VALUES (?, '2026-12-31', 'payment', '2026-01-05T00:00:00.000Z')`, membership)
		assert.Error(t, err)
	})

	t.Run("a correction reason cannot be blank", func(t *testing.T) {
		_, err := db.Exec(`
			INSERT INTO payment_corrections (original_payment_id, reversal_payment_id,
				replacement_payment_id, reason, corrected_by, corrected_at)
			VALUES (1, 2, 3, '   ', ?, '2026-01-05T00:00:00.000Z')`, actor)
		assert.Error(t, err)
	})
}

// TestTreasuryRoleMatrix pins the authorization decision from the design: the
// treasurer works the ledger, the executive officers see standing only, and
// nobody else receives a Phase 2 capability by default.
func TestTreasuryRoleMatrix(t *testing.T) {
	db := openTestDB(t)
	require.NoError(t, Migrate(db))

	treasuryCaps := []string{
		"dues.read", "dues.rate.manage", "coverage.read", "coverage.adjust",
		"payment.read", "payment.batch.manage", "payment.post", "payment.correct",
		"payment.export", "dues.worksheet.manage",
	}

	granted := func(t *testing.T, role string) []string {
		t.Helper()
		rows, err := db.Query(`
			SELECT capability_code FROM role_capabilities
			WHERE role_code = ? AND capability_code IN (
				SELECT code FROM capabilities WHERE category = 'treasury')
			ORDER BY capability_code`, role)
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

	for _, cap := range treasuryCaps {
		var n int
		require.NoError(t, db.QueryRow(
			`SELECT count(*) FROM capabilities WHERE code = ? AND category = 'treasury'`, cap).Scan(&n))
		assert.Equal(t, 1, n, "capability %s should be seeded in the treasury category", cap)
	}

	for _, cap := range treasuryCaps {
		assert.Contains(t, granted(t, "treasurer"), cap, "treasurer should hold %s", cap)
		assert.Contains(t, granted(t, "administrator"), cap, "administrator should hold %s", cap)
	}

	for _, role := range []string{"president", "vice_president", "secretary"} {
		assert.Equal(t, []string{"dues.read"}, granted(t, role),
			"%s should see safe standing and nothing else from Phase 2", role)
	}

	for _, role := range []string{"webmaster", "trustee", "activities_manager", "acs_coordinator", "member"} {
		assert.Empty(t, granted(t, role), "%s should receive no Phase 2 capability", role)
	}
}
