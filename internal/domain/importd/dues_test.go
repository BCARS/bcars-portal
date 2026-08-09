package importd

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Phase 2 import cutover (pma.13). Before this change the commit path
// normalized Current Until and recognised the known lifetime rows, then wrote
// neither: an officer who imported a member could not see when they last paid.

const csvHeader = "Contact Name,Call Sign,Current Until,Note,Membership Type,Class,Phone,Email,Street Address,City,Postal Code,State/Province,Volunteer Examiner\n"

// csvRow builds one synthetic Groups.io row.
func csvRow(name, callSign, currentUntil, membershipType, email string) string {
	return fmt.Sprintf("%s,%s,%s,,%s,General,555-000-0000,%s,1 Main,Bedford,15522,PA,false\n",
		name, callSign, currentUntil, membershipType, email)
}

// commitCSV runs a whole import: upload, preview, commit.
func commitCSV(t *testing.T, svc *Service, csv, key string) int64 {
	t.Helper()
	up, err := svc.Upload(context.Background(), strings.NewReader(csvHeader+csv), "csv", "test.csv", 1, key)
	require.NoError(t, err)
	_, err = svc.Preview(context.Background(), up.RunID)
	require.NoError(t, err)
	_, err = svc.Commit(context.Background(), up.RunID, 1)
	require.NoError(t, err)
	return up.RunID
}

// coverageFor reads the coverage events recorded for a person's membership.
type coverageRow struct {
	PaidThrough string
	ReasonKind  string
	ImportRunID sql.NullInt64
}

func coverageFor(t *testing.T, d *sql.DB, displayName string) []coverageRow {
	t.Helper()
	rows, err := d.Query(`
		SELECT c.paid_through, c.reason_kind, c.import_run_id
		  FROM coverage_events c
		  JOIN memberships m ON m.id = c.membership_id
		  JOIN persons p ON p.id = m.person_id
		 WHERE p.display_name = ?
		 ORDER BY c.id`, displayName)
	require.NoError(t, err)
	defer rows.Close()

	var out []coverageRow
	for rows.Next() {
		var r coverageRow
		require.NoError(t, rows.Scan(&r.PaidThrough, &r.ReasonKind, &r.ImportRunID))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

type grantRow struct {
	IsLifetime int64
	BaseType   string
}

func grantsFor(t *testing.T, d *sql.DB, displayName string) []grantRow {
	t.Helper()
	rows, err := d.Query(`
		SELECT g.is_lifetime, m.base_type
		  FROM honorary_grants g
		  JOIN memberships m ON m.id = g.membership_id
		  JOIN persons p ON p.id = m.person_id
		 WHERE p.display_name = ?
		 ORDER BY g.id`, displayName)
	require.NoError(t, err)
	defer rows.Close()

	var out []grantRow
	for rows.Next() {
		var r grantRow
		require.NoError(t, rows.Scan(&r.IsLifetime, &r.BaseType))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

// TestImportRecordsOrdinaryPaidThrough is the first reproduction from the bead:
// an ordinary Current Until must survive the import as a coverage event linked
// to the run that carried it.
func TestImportRecordsOrdinaryPaidThrough(t *testing.T) {
	svc, d := setupServiceDB(t)

	runID := commitCSV(t, svc,
		csvRow("Ordinary Member", "KA1ORD", "12/31/2026", "Full", "ordinary@example.invalid"),
		"cutover-1")

	events := coverageFor(t, d, "Ordinary Member")
	require.Len(t, events, 1, "an imported Current Until must produce exactly one coverage event")
	assert.Equal(t, "2026-12-31", events[0].PaidThrough)
	assert.Equal(t, "import", events[0].ReasonKind)
	assert.Equal(t, runID, events[0].ImportRunID.Int64, "the event names the run that carried it")

	assert.Empty(t, grantsFor(t, d, "Ordinary Member"), "an ordinary row grants nothing honorary")
}

// TestImportRecordsNoCoverageForBlankOrSentinelDates proves the import never
// invents a date it was not given.
func TestImportRecordsNoCoverageForBlankOrSentinelDates(t *testing.T) {
	svc, d := setupServiceDB(t)

	commitCSV(t, svc,
		csvRow("Blank Date", "KA1BLK", "", "Full", "blank@example.invalid")+
			csvRow("Sentinel Date", "KA1SEN", "01/01/0001", "Full", "sentinel@example.invalid")+
			csvRow("Garbled Date", "KA1GAR", "sometime last year", "Full", "garbled@example.invalid"),
		"cutover-1")

	assert.Empty(t, coverageFor(t, d, "Blank Date"), "a blank date records nothing")
	assert.Empty(t, coverageFor(t, d, "Sentinel Date"), "a sentinel null records nothing")

	// Normalization passes an unparseable date through verbatim. Writing it
	// would fail the schema's date check and abort the entire import over one
	// bad cell, so the import records no date and carries on.
	assert.Empty(t, coverageFor(t, d, "Garbled Date"), "an unreadable date records nothing")

	var persons int
	require.NoError(t, d.QueryRow(`SELECT count(*) FROM persons`).Scan(&persons))
	assert.Equal(t, 3, persons, "one unreadable date must not cost the whole import")
}

// TestImportKnownLifetimeCreatesGrantNotCoverage is the second reproduction:
// the known 12/31/2055 rows become real lifetime honorary Associate grants, and
// must never be turned into a paid-through date, because nobody paid through
// 2055.
func TestImportKnownLifetimeCreatesGrantNotCoverage(t *testing.T) {
	svc, d := setupServiceDB(t)

	// The lifetime decision rides on the external ID, which only the JSON
	// export carries. Two synthetic rows, in the Groups.io export shape.
	export := `{
	  "table": {"columns": [
	    {"id": 1, "name": "Contact Name"},
	    {"id": 2, "name": "Call Sign"},
	    {"id": 3, "name": "Current Until"},
	    {"id": 4, "name": "Membership Type"},
	    {"id": 5, "name": "Email"}
	  ]},
	  "rows": [
	    {"id": 900001, "vals": [
	      {"col_id": 1, "text": "Lifetimetest One"},
	      {"col_id": 2, "text": "KA1LT1"},
	      {"col_id": 3, "text": "12/31/2055"},
	      {"col_id": 4, "text": "Honorary"},
	      {"col_id": 5, "text": "lt1@example.invalid"}
	    ]},
	    {"id": 900002, "vals": [
	      {"col_id": 1, "text": "Lifetimetest Two"},
	      {"col_id": 2, "text": "KA1LT2"},
	      {"col_id": 3, "text": "12/31/2055"},
	      {"col_id": 4, "text": "Honorary"},
	      {"col_id": 5, "text": "lt2@example.invalid"}
	    ]}
	  ]
	}`

	up, err := svc.Upload(context.Background(), strings.NewReader(export), "json", "export.json", 1, "lifetime-1")
	require.NoError(t, err)
	require.Zero(t, up.ManualRows, "the known lifetime rows resolve without a human")

	_, err = svc.Preview(context.Background(), up.RunID)
	require.NoError(t, err)
	_, err = svc.Commit(context.Background(), up.RunID, 1)
	require.NoError(t, err)

	for _, name := range []string{"Lifetimetest One", "Lifetimetest Two"} {
		t.Run(name, func(t *testing.T) {
			grants := grantsFor(t, d, name)
			require.Len(t, grants, 1, "a known lifetime row creates one honorary grant")
			assert.Equal(t, int64(1), grants[0].IsLifetime)
			assert.Equal(t, "associate", grants[0].BaseType,
				"a lifetime honorary member holds Associate base rights")

			assert.Empty(t, coverageFor(t, d, name),
				"a lifetime grant must never fabricate a 2055 paid-through")
		})
	}
}

// TestImportUnknownLifetimeStaysManual proves an unconfirmed lifetime-like date
// is never resolved by the machine.
func TestImportUnknownLifetimeStaysManual(t *testing.T) {
	svc, _ := setupServiceDB(t)

	up, err := svc.Upload(context.Background(),
		strings.NewReader(csvHeader+csvRow("Unknown Lifetime", "KA1UNK", "12/31/2055", "Full", "unk@example.invalid")),
		"csv", "test.csv", 1, "unknown-1")
	require.NoError(t, err)
	assert.Equal(t, 1, up.ManualRows, "an unconfirmed lifetime-like date needs a human")

	_, err = svc.Commit(context.Background(), up.RunID, 1)
	assert.ErrorIs(t, err, ErrInvalidTransition)
}

// TestOfficerDecisionControlsTheOutcome proves the recorded decision, not the
// guess, determines base type and whether the row produces a grant or coverage.
func TestOfficerDecisionControlsTheOutcome(t *testing.T) {
	for _, tc := range []struct {
		name        string
		payload     string
		wantGrants  int
		wantDate    string
		wantBase    string
		description string
	}{
		{
			name:        "officer confirms a lifetime honorary",
			payload:     `{"base_type":"associate","honorary":"lifetime"}`,
			wantGrants:  1,
			wantDate:    "",
			wantBase:    "associate",
			description: "a confirmed lifetime grant records no paid-through",
		},
		{
			name:        "officer says it is an ordinary full member",
			payload:     `{"base_type":"full","honorary":"none","paid_through":"2026-12-31"}`,
			wantGrants:  0,
			wantDate:    "2026-12-31",
			wantBase:    "full",
			description: "the officer's date is what gets recorded",
		},
		{
			name:        "officer records no coverage at all",
			payload:     `{"base_type":"full","honorary":"none","coverage":"none"}`,
			wantGrants:  0,
			wantDate:    "",
			wantBase:    "full",
			description: "an explicit refusal records nothing",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, d := setupServiceDB(t)

			// An unconfirmed lifetime-like date: exactly the row a human must resolve.
			up, err := svc.Upload(context.Background(),
				strings.NewReader(csvHeader+csvRow("Decided Member", "KA1DEC", "12/31/2055", "Full", "dec@example.invalid")),
				"csv", "test.csv", 1, "decide-"+tc.name)
			require.NoError(t, err)

			rows, err := svc.ListRows(context.Background(), up.RunID, 10, 0)
			require.NoError(t, err)
			require.Len(t, rows, 1)

			_, err = svc.RecordDecision(context.Background(), up.RunID, DecisionInput{
				RowID:       rows[0].ID,
				DecidedBy:   1,
				Action:      "approve_create",
				PayloadJSON: tc.payload,
			})
			require.NoError(t, err)

			_, err = svc.Preview(context.Background(), up.RunID)
			require.NoError(t, err)
			_, err = svc.Commit(context.Background(), up.RunID, 1)
			require.NoError(t, err)

			assert.Len(t, grantsFor(t, d, "Decided Member"), tc.wantGrants, tc.description)

			events := coverageFor(t, d, "Decided Member")
			if tc.wantDate == "" {
				assert.Empty(t, events, tc.description)
			} else {
				require.Len(t, events, 1)
				assert.Equal(t, tc.wantDate, events[0].PaidThrough)
			}

			var baseType string
			require.NoError(t, d.QueryRow(`
				SELECT m.base_type FROM memberships m
				  JOIN persons p ON p.id = m.person_id
				 WHERE p.display_name = 'Decided Member'`).Scan(&baseType))
			assert.Equal(t, tc.wantBase, baseType, "the decision controls the base type")
		})
	}
}

// TestImportCoversMatchedMemberships proves a re-import that matches an
// existing person records the new paid-through on their membership rather than
// dropping it, which is the ordinary yearly-renewal case.
func TestImportCoversMatchedMemberships(t *testing.T) {
	svc, d := setupServiceDB(t)

	commitCSV(t, svc,
		csvRow("Renewing Member", "KA1REN", "12/31/2025", "Full", "renew@example.invalid"),
		"match-1")

	events := coverageFor(t, d, "Renewing Member")
	require.Len(t, events, 1)
	assert.Equal(t, "2025-12-31", events[0].PaidThrough)

	var personCount int
	require.NoError(t, d.QueryRow(
		`SELECT count(*) FROM persons WHERE display_name = 'Renewing Member'`).Scan(&personCount))
	require.Equal(t, 1, personCount)

	// The following year's roster matches the same person by call sign.
	commitCSV(t, svc,
		csvRow("Renewing Member", "KA1REN", "12/31/2026", "Full", "renew@example.invalid"),
		"match-2")

	require.NoError(t, d.QueryRow(
		`SELECT count(*) FROM persons WHERE display_name = 'Renewing Member'`).Scan(&personCount))
	assert.Equal(t, 1, personCount, "the second import matched rather than duplicated")

	events = coverageFor(t, d, "Renewing Member")
	require.Len(t, events, 2, "the renewal is recorded as a new decision")
	assert.Equal(t, "2026-12-31", events[1].PaidThrough)
	assert.NotZero(t, events[1].ImportRunID.Int64)

	// The newer decision supersedes the older one, so the history is a chain.
	var supersedes sql.NullInt64
	require.NoError(t, d.QueryRow(`
		SELECT supersedes_event_id FROM coverage_events
		 ORDER BY id DESC LIMIT 1`).Scan(&supersedes))
	assert.True(t, supersedes.Valid, "the renewal supersedes the prior decision")
}

// TestReimportingUnchangedDataAddsNothing proves the cutover is safe to run
// twice, which matters because an officer who is unsure whether an import
// completed will run it again.
func TestReimportingUnchangedDataAddsNothing(t *testing.T) {
	svc, d := setupServiceDB(t)

	row := csvRow("Repeated Member", "KA1RPT", "12/31/2026", "Full", "rpt@example.invalid")
	commitCSV(t, svc, row, "repeat-1")
	commitCSV(t, svc, row, "repeat-2")

	events := coverageFor(t, d, "Repeated Member")
	assert.Len(t, events, 1, "re-importing the same date records no second decision")
}

// TestCommitRetryDoesNotDuplicate proves the run-level idempotency still holds
// with the new writes in place.
func TestCommitRetryDoesNotDuplicate(t *testing.T) {
	svc, d := setupServiceDB(t)

	up, err := svc.Upload(context.Background(),
		strings.NewReader(csvHeader+csvRow("Retried Member", "KA1RTY", "12/31/2026", "Full", "rty@example.invalid")),
		"csv", "test.csv", 1, "retry-1")
	require.NoError(t, err)
	_, err = svc.Preview(context.Background(), up.RunID)
	require.NoError(t, err)

	_, err = svc.Commit(context.Background(), up.RunID, 1)
	require.NoError(t, err)
	_, err = svc.Commit(context.Background(), up.RunID, 1)
	require.NoError(t, err, "committing an already-committed run is a no-op")

	events := coverageFor(t, d, "Retried Member")
	assert.Len(t, events, 1, "a retried commit records no second decision")
}

// TestImportFailureRollsBackDues proves the whole import stays atomic now that
// it writes to more tables: a row that fails leaves no coverage behind.
func TestImportFailureRollsBackDues(t *testing.T) {
	svc, d := setupServiceDB(t)

	up, err := svc.Upload(context.Background(),
		strings.NewReader(csvHeader+
			csvRow("Good Member", "KA1GUD", "12/31/2026", "Full", "good@example.invalid")+
			csvRow("Doomed Member", "KA1DOM", "12/31/2026", "Full", "doom@example.invalid")),
		"csv", "test.csv", 1, "atomic-1")
	require.NoError(t, err)
	_, err = svc.Preview(context.Background(), up.RunID)
	require.NoError(t, err)

	// Corrupt the second row's normalized payload so applying it fails partway,
	// after the first row has already written a person and a coverage event.
	rows, err := svc.ListRows(context.Background(), up.RunID, 10, 0)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	_, err = d.Exec(`UPDATE staged_import_rows SET normalized_json = 'not json' WHERE id = ?`,
		rows[1].ID)
	require.NoError(t, err)

	_, err = svc.Commit(context.Background(), up.RunID, 1)
	require.Error(t, err)

	var persons, coverage int
	require.NoError(t, d.QueryRow(`SELECT count(*) FROM persons`).Scan(&persons))
	require.NoError(t, d.QueryRow(`SELECT count(*) FROM coverage_events`).Scan(&coverage))
	assert.Zero(t, persons, "a failed import creates no person")
	assert.Zero(t, coverage, "and no coverage event")

	var status string
	require.NoError(t, d.QueryRow(
		`SELECT status FROM import_runs WHERE id = ?`, up.RunID).Scan(&status))
	assert.Equal(t, "previewed", status, "the run is left ready to retry")
}

// TestMalformedDecisionPayloadChangesNothing proves an unparseable payload
// falls back to the normalized proposal rather than silently importing
// something different.
func TestMalformedDecisionPayloadChangesNothing(t *testing.T) {
	svc, d := setupServiceDB(t)

	up, err := svc.Upload(context.Background(),
		strings.NewReader(csvHeader+csvRow("Malformed Decision", "KA1MAL", "12/31/2055", "Full", "mal@example.invalid")),
		"csv", "test.csv", 1, "malformed-1")
	require.NoError(t, err)

	rows, err := svc.ListRows(context.Background(), up.RunID, 10, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	_, err = svc.RecordDecision(context.Background(), up.RunID, DecisionInput{
		RowID:       rows[0].ID,
		DecidedBy:   1,
		Action:      "approve_create",
		PayloadJSON: "{not valid json",
	})
	require.NoError(t, err)

	_, err = svc.Preview(context.Background(), up.RunID)
	require.NoError(t, err)
	_, err = svc.Commit(context.Background(), up.RunID, 1)
	require.NoError(t, err)

	// The normalized flag was lifetime_unknown, so no date and no grant.
	assert.Empty(t, coverageFor(t, d, "Malformed Decision"))
	assert.Empty(t, grantsFor(t, d, "Malformed Decision"))
}
