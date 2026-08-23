package importd

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/db/dbtest"
)

// --- Parser tests ---

func TestParseCSVFixture(t *testing.T) {
	f, err := os.Open("../../../fixtures/synthetic/groupsio_contact.csv")
	require.NoError(t, err)
	defer f.Close()

	records, err := ParseCSV(f)
	require.NoError(t, err)
	assert.Len(t, records, 21, "fixture has 21 data rows")

	// Spot-check first record.
	assert.Equal(t, "Fulltest1 Member", records[0].ContactName)
	assert.Equal(t, "KA9F01X", records[0].CallSign)
	assert.Equal(t, "12/31/2026", records[0].CurrentUntil)
	assert.Equal(t, "Full", records[0].MembershipType)
	assert.Equal(t, "full1@example.invalid", records[0].Email)
}

func TestParseJSONFixture(t *testing.T) {
	f, err := os.Open("../../../fixtures/synthetic/groupsio_contact.json")
	require.NoError(t, err)
	defer f.Close()

	records, err := ParseJSON(f)
	require.NoError(t, err)
	assert.Len(t, records, 21, "fixture has 21 rows")

	// Spot-check first record.
	assert.Equal(t, "Fulltest1 Member", records[0].ContactName)
	assert.Equal(t, "KA9F01X", records[0].CallSign)
	assert.Equal(t, "900000", records[0].ExternalID)
	assert.Equal(t, "Full", records[0].MembershipType)
}

func TestCSVBadHeader(t *testing.T) {
	_, err := ParseCSV(strings.NewReader("Bad,Header\nfoo,bar\n"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "expected at least")
}

func TestJSONMissingColumn(t *testing.T) {
	_, err := ParseJSON(strings.NewReader(`{"table":{"columns":[]},"rows":[]}`))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing required column")
}

func TestCSVAndJSONProduceSameFields(t *testing.T) {
	csvF, err := os.Open("../../../fixtures/synthetic/groupsio_contact.csv")
	require.NoError(t, err)
	defer csvF.Close()
	csvRecs, err := ParseCSV(csvF)
	require.NoError(t, err)

	jsonF, err := os.Open("../../../fixtures/synthetic/groupsio_contact.json")
	require.NoError(t, err)
	defer jsonF.Close()
	jsonRecs, err := ParseJSON(jsonF)
	require.NoError(t, err)

	require.Equal(t, len(csvRecs), len(jsonRecs), "row counts should match")

	for i := range csvRecs {
		assert.Equal(t, csvRecs[i].ContactName, jsonRecs[i].ContactName, "row %d ContactName", i)
		assert.Equal(t, csvRecs[i].CallSign, jsonRecs[i].CallSign, "row %d CallSign", i)
		assert.Equal(t, csvRecs[i].CurrentUntil, jsonRecs[i].CurrentUntil, "row %d CurrentUntil", i)
		assert.Equal(t, csvRecs[i].MembershipType, jsonRecs[i].MembershipType, "row %d MembershipType", i)
		assert.Equal(t, csvRecs[i].Email, jsonRecs[i].Email, "row %d Email", i)
		assert.Equal(t, csvRecs[i].Phone, jsonRecs[i].Phone, "row %d Phone", i)
	}
}

// --- Normalization tests ---

func TestNormalizeCleanFull(t *testing.T) {
	raw := RawRecord{
		ContactName: "Fulltest1 Member", CallSign: "ka9f01x",
		CurrentUntil: "12/31/2026", MembershipType: "Full",
		Class: "General", Phone: "555-010-1001",
		Email: "Full1@Example.Invalid",
	}
	n := Normalize(raw)
	assert.Equal(t, "KA9F01X", n.CallSign)
	assert.Equal(t, "full1@example.invalid", n.Email)
	assert.Equal(t, "Full", n.MembershipType)
	assert.Equal(t, "full", n.BaseType)
	assert.Equal(t, "2026-12-31", n.CurrentUntil)
	assert.Equal(t, "Member, Fulltest1", n.SortName)
	assert.Equal(t, "5550101001", n.Phone)
	assert.True(t, n.PhoneValid)
	assert.False(t, n.RequiresManual)
}

func TestNormalizeCaseFoldMembershipType(t *testing.T) {
	n := Normalize(RawRecord{MembershipType: "full"})
	assert.Equal(t, "Full", n.MembershipType)
	assert.Equal(t, "full", n.BaseType)

	n = Normalize(RawRecord{MembershipType: "ASSOCIATE"})
	assert.Equal(t, "Associate", n.MembershipType)
	assert.Equal(t, "associate", n.BaseType)
}

func TestNormalizeSentinelNull(t *testing.T) {
	n := Normalize(RawRecord{CurrentUntil: "01/01/0001"})
	assert.Empty(t, n.CurrentUntil)
	assert.Equal(t, "sentinel_null", n.CurrentUntilFlag)
	assert.False(t, n.RequiresManual)
}

func TestNormalizeLifetimeKnown(t *testing.T) {
	n := Normalize(RawRecord{
		ExternalID: "900001", CurrentUntil: "12/31/2055",
		MembershipType: "Honorary",
	})
	assert.Equal(t, "2055-12-31", n.CurrentUntil)
	assert.Equal(t, "lifetime_known", n.CurrentUntilFlag)
	assert.Equal(t, "associate", n.BaseType, "known lifetime → associate")
	assert.False(t, n.RequiresManual)
}

func TestNormalizeLifetimeUnknown(t *testing.T) {
	n := Normalize(RawRecord{
		ExternalID: "999999", CurrentUntil: "12/31/2055",
		MembershipType: "Full",
	})
	assert.Equal(t, "lifetime_unknown", n.CurrentUntilFlag)
	assert.True(t, n.RequiresManual)
	assert.Equal(t, "lifetime_like_date_needs_confirmation", n.ManualReason)
}

func TestNormalizeHonoraryUnspecified(t *testing.T) {
	n := Normalize(RawRecord{MembershipType: "Honorary"})
	assert.True(t, n.RequiresManual)
	assert.Equal(t, "honorary_type_unspecified", n.ManualReason)
}

func TestNormalizeBadPhone(t *testing.T) {
	n := Normalize(RawRecord{Phone: "call me maybe"})
	assert.False(t, n.PhoneValid)
	assert.Equal(t, "call me maybe", n.Phone, "invalid phone preserved as-is")
}

func TestNormalizeEmptyPhone(t *testing.T) {
	n := Normalize(RawRecord{Phone: ""})
	assert.True(t, n.PhoneValid)
	assert.Empty(t, n.Phone)
}

func TestNormalizeVolunteerExaminer(t *testing.T) {
	assert.True(t, Normalize(RawRecord{VolunteerExaminer: "true"}).VolunteerExaminer)
	assert.True(t, Normalize(RawRecord{VolunteerExaminer: "checked"}).VolunteerExaminer)
	assert.False(t, Normalize(RawRecord{VolunteerExaminer: "false"}).VolunteerExaminer)
	assert.False(t, Normalize(RawRecord{VolunteerExaminer: ""}).VolunteerExaminer)
}

// --- Full fixture normalization ---

func TestNormalizeAllFixtureRows(t *testing.T) {
	f, err := os.Open("../../../fixtures/synthetic/groupsio_contact.csv")
	require.NoError(t, err)
	defer f.Close()

	records, err := ParseCSV(f)
	require.NoError(t, err)

	var manualCount int
	for _, raw := range records {
		n := Normalize(raw)
		if n.RequiresManual {
			manualCount++
		}
	}

	// CSV has no ExternalID so lifetime_known won't trigger.
	// 2 Honorary lifetime rows (lifetime_unknown) + 1 suspicious date (lifetime_unknown) + 1 honorary_unspecified = 4.
	assert.Equal(t, 4, manualCount, "CSV without external IDs: 3 lifetime_unknown + 1 honorary_unspecified")
}

func TestNormalizeAllFixtureRowsJSON(t *testing.T) {
	f, err := os.Open("../../../fixtures/synthetic/groupsio_contact.json")
	require.NoError(t, err)
	defer f.Close()

	records, err := ParseJSON(f)
	require.NoError(t, err)

	var manualCount int
	var manualReasons []string
	for _, raw := range records {
		n := Normalize(raw)
		if n.RequiresManual {
			manualCount++
			manualReasons = append(manualReasons, n.ManualReason)
		}
	}

	// JSON has ExternalIDs: known lifetime rows (900001, 900002) are NOT manual.
	// Remaining: 1 lifetime_unknown (suspicious date) + 1 honorary_unspecified = 2.
	assert.Equal(t, 2, manualCount, "JSON with external IDs: 1 lifetime_unknown + 1 honorary_unspecified")
	assert.Contains(t, manualReasons, "lifetime_like_date_needs_confirmation")
	assert.Contains(t, manualReasons, "honorary_type_unspecified")
}

// --- Match engine tests ---

func TestMatchByCallSign(t *testing.T) {
	d := dbtest.Open(t)
	var err error

	_, err = d.Exec(`INSERT INTO persons (display_name, sort_name, call_sign) VALUES ('Test', 'Test', 'KA9F01X')`)
	require.NoError(t, err)

	matcher := NewMatcher(d)
	result, err := matcher.Match(NormalizedRecord{CallSign: "KA9F01X"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), result.PersonID)
	assert.Equal(t, "call_sign", result.Method)
}

func TestMatchByEmail(t *testing.T) {
	d := dbtest.Open(t)
	var err error

	_, err = d.Exec(`INSERT INTO persons (display_name, sort_name) VALUES ('Test', 'Test')`)
	require.NoError(t, err)
	_, err = d.Exec(`INSERT INTO contact_methods (person_id, kind, value_raw, value_norm) VALUES (1, 'email', 'test@example.com', 'test@example.com')`)
	require.NoError(t, err)

	matcher := NewMatcher(d)
	result, err := matcher.Match(NormalizedRecord{Email: "test@example.com"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), result.PersonID)
	assert.Equal(t, "email", result.Method)
}

func TestMatchAmbiguousEmail(t *testing.T) {
	d := dbtest.Open(t)
	var err error

	_, err = d.Exec(`INSERT INTO persons (display_name, sort_name) VALUES ('Alice', 'Alice')`)
	require.NoError(t, err)
	_, err = d.Exec(`INSERT INTO persons (display_name, sort_name) VALUES ('Bob', 'Bob')`)
	require.NoError(t, err)
	_, err = d.Exec(`INSERT INTO contact_methods (person_id, kind, value_raw, value_norm) VALUES (1, 'email', 'shared@test.com', 'shared@test.com')`)
	require.NoError(t, err)
	_, err = d.Exec(`INSERT INTO contact_methods (person_id, kind, value_raw, value_norm) VALUES (2, 'email', 'shared@test.com', 'shared@test.com')`)
	require.NoError(t, err)

	matcher := NewMatcher(d)
	result, err := matcher.Match(NormalizedRecord{Email: "shared@test.com"})
	require.NoError(t, err)
	assert.True(t, result.Ambiguous)
	assert.Equal(t, "email", result.Method)
	assert.Equal(t, int64(0), result.PersonID)
}

func TestMatchByExternalID(t *testing.T) {
	d := dbtest.Open(t)
	var err error

	_, err = d.Exec(`INSERT INTO persons (display_name, sort_name) VALUES ('Test', 'Test')`)
	require.NoError(t, err)
	_, err = d.Exec(`INSERT INTO external_ids (entity_kind, entity_id, system, external_id) VALUES ('person', 1, 'groupsio.contact_row', '900000')`)
	require.NoError(t, err)

	matcher := NewMatcher(d)
	result, err := matcher.Match(NormalizedRecord{ExternalID: "900000", CallSign: "NOMATCH"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), result.PersonID)
	assert.Equal(t, "external_id", result.Method, "external_id takes priority over call_sign")
}

func TestMatchNone(t *testing.T) {
	d := dbtest.Open(t)

	matcher := NewMatcher(d)
	result, err := matcher.Match(NormalizedRecord{CallSign: "ZZZZZZZ", Email: "nobody@nowhere.invalid"})
	require.NoError(t, err)
	assert.Equal(t, "none", result.Method)
	assert.Equal(t, int64(0), result.PersonID)
}

// --- Service tests ---

func setupServiceDB(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	d := dbtest.Open(t)
	var err error

	// Create a user for uploaded_by / committed_by.
	_, err = d.Exec(`INSERT INTO users (email, is_active) VALUES ('admin@bcars.org', 1)`)
	require.NoError(t, err)

	return NewService(d), d
}

func TestUploadCSV(t *testing.T) {
	svc, _ := setupServiceDB(t)

	f, err := os.Open("../../../fixtures/synthetic/groupsio_contact.csv")
	require.NoError(t, err)
	defer f.Close()

	result, err := svc.Upload(context.Background(), f, "csv", "test.csv", 1, "test-idem-1")
	require.NoError(t, err)

	assert.Equal(t, 21, result.TotalRows)
	assert.Equal(t, 21, result.NewRows, "all rows are new (empty DB)")
	assert.Equal(t, 0, result.MatchedRows)
	// 4 manual: 3 lifetime_unknown + 1 honorary_unspecified
	assert.Equal(t, 4, result.ManualRows)
	assert.Equal(t, 17, result.AutoRows)
}

func TestUploadJSON(t *testing.T) {
	svc, _ := setupServiceDB(t)

	f, err := os.Open("../../../fixtures/synthetic/groupsio_contact.json")
	require.NoError(t, err)
	defer f.Close()

	result, err := svc.Upload(context.Background(), f, "json", "test.json", 1, "test-idem-2")
	require.NoError(t, err)

	assert.Equal(t, 21, result.TotalRows)
	// JSON: 2 manual (1 lifetime_unknown + 1 honorary_unspecified)
	assert.Equal(t, 2, result.ManualRows)
	assert.Equal(t, 19, result.AutoRows)
}

func TestUploadIdempotency(t *testing.T) {
	svc, _ := setupServiceDB(t)

	csv := "Contact Name,Call Sign,Current Until,Note,Membership Type,Class,Phone,Email,Street Address,City,Postal Code,State/Province,Volunteer Examiner\nTest User,KA1ABC,12/31/2026,,Full,General,555-000-0000,test@example.invalid,1 Main,Bedford,15522,PA,false\n"

	_, err := svc.Upload(context.Background(), strings.NewReader(csv), "csv", "test.csv", 1, "idem-dup")
	require.NoError(t, err)

	_, err = svc.Upload(context.Background(), strings.NewReader(csv), "csv", "test.csv", 1, "idem-dup")
	assert.Error(t, err, "duplicate idempotency key should fail")
}

func TestUploadTransitionsToValidated(t *testing.T) {
	svc, _ := setupServiceDB(t)

	csv := "Contact Name,Call Sign,Current Until,Note,Membership Type,Class,Phone,Email,Street Address,City,Postal Code,State/Province,Volunteer Examiner\nTest User,KA1ABC,12/31/2026,,Full,General,555-000-0000,test@example.invalid,1 Main,Bedford,15522,PA,false\n"

	up, err := svc.Upload(context.Background(), strings.NewReader(csv), "csv", "test.csv", 1, "state-1")
	require.NoError(t, err)

	run, err := svc.GetRun(context.Background(), up.RunID)
	require.NoError(t, err)
	assert.Equal(t, "validated", run.Status, "upload should transition to validated per ADR-0008")
}

func TestListAndGetRuns(t *testing.T) {
	svc, _ := setupServiceDB(t)

	csv := "Contact Name,Call Sign,Current Until,Note,Membership Type,Class,Phone,Email,Street Address,City,Postal Code,State/Province,Volunteer Examiner\nTest User,KA1ABC,12/31/2026,,Full,General,555-000-0000,test@example.invalid,1 Main,Bedford,15522,PA,false\n"

	up, err := svc.Upload(context.Background(), strings.NewReader(csv), "csv", "test.csv", 1, "list-1")
	require.NoError(t, err)

	runs, err := svc.ListRuns(context.Background(), 100, 0)
	require.NoError(t, err)
	assert.Len(t, runs, 1)
	assert.Equal(t, up.RunID, runs[0].ID)

	run, err := svc.GetRun(context.Background(), up.RunID)
	require.NoError(t, err)
	assert.Equal(t, up.RunID, run.ID)
}

func TestListAndGetRows(t *testing.T) {
	svc, _ := setupServiceDB(t)

	csv := "Contact Name,Call Sign,Current Until,Note,Membership Type,Class,Phone,Email,Street Address,City,Postal Code,State/Province,Volunteer Examiner\nAlice,KA1A,12/31/2026,,Full,General,555-111-1111,alice@example.invalid,1 Main,Bedford,15522,PA,false\nBob,KA1B,12/31/2026,,Associate,,555-222-2222,bob@example.invalid,2 Main,Bedford,15522,PA,false\n"

	up, err := svc.Upload(context.Background(), strings.NewReader(csv), "csv", "test.csv", 1, "rows-1")
	require.NoError(t, err)

	rows, err := svc.ListRows(context.Background(), up.RunID, 100, 0)
	require.NoError(t, err)
	assert.Len(t, rows, 2)

	row, err := svc.GetRow(context.Background(), rows[0].ID)
	require.NoError(t, err)
	assert.Equal(t, rows[0].ID, row.ID)
}

func TestPreviewAndCommitHappyPath(t *testing.T) {
	svc, d := setupServiceDB(t)

	csv := "Contact Name,Call Sign,Current Until,Note,Membership Type,Class,Phone,Email,Street Address,City,Postal Code,State/Province,Volunteer Examiner\nAlice Test,KA1AAA,12/31/2026,,Full,General,555-111-1111,alice@example.invalid,1 Main,Bedford,15522,PA,false\nBob Test,KA1BBB,12/31/2026,,Associate,,555-222-2222,bob@example.invalid,2 Main,Bedford,15522,PA,false\n"

	up, err := svc.Upload(context.Background(), strings.NewReader(csv), "csv", "test.csv", 1, "commit-1")
	require.NoError(t, err)
	assert.Equal(t, 2, up.TotalRows)
	assert.Equal(t, 0, up.ManualRows)

	// Preview first.
	preview, err := svc.Preview(context.Background(), up.RunID)
	require.NoError(t, err)
	assert.Equal(t, 2, preview.CreateCount)
	assert.Equal(t, 0, preview.UnresolvedManual)
	assert.True(t, preview.Ready)

	// Verify state is "previewed".
	run, err := svc.GetRun(context.Background(), up.RunID)
	require.NoError(t, err)
	assert.Equal(t, "previewed", run.Status)

	// Commit.
	result, err := svc.Commit(context.Background(), up.RunID, 1)
	require.NoError(t, err)
	assert.Equal(t, 2, result.Created)
	assert.Equal(t, 0, result.Updated)
	assert.Equal(t, 0, result.Errors)

	// Verify persons exist.
	var count int
	err = d.QueryRow(`SELECT count(*) FROM persons`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 2, count)

	// Verify memberships exist.
	err = d.QueryRow(`SELECT count(*) FROM memberships WHERE lifecycle = 'approved'`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 2, count)

	// Verify contact methods.
	err = d.QueryRow(`SELECT count(*) FROM contact_methods WHERE kind = 'email'`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 2, count)

	// Verify run is committed.
	run, err = svc.GetRun(context.Background(), up.RunID)
	require.NoError(t, err)
	assert.Equal(t, "committed", run.Status)
}

func TestCommitRequiresPreviewedState(t *testing.T) {
	svc, _ := setupServiceDB(t)

	csv := "Contact Name,Call Sign,Current Until,Note,Membership Type,Class,Phone,Email,Street Address,City,Postal Code,State/Province,Volunteer Examiner\nTest,KA1TST,12/31/2026,,Full,General,555-000-0000,test@example.invalid,1 Main,Bedford,15522,PA,false\n"

	up, err := svc.Upload(context.Background(), strings.NewReader(csv), "csv", "test.csv", 1, "nopreview-1")
	require.NoError(t, err)

	// Try to commit directly from "validated" — should fail.
	_, err = svc.Commit(context.Background(), up.RunID, 1)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidTransition)
}

func TestCommitRefusesUnresolvedManualRows(t *testing.T) {
	svc, _ := setupServiceDB(t)

	// Honorary without base type → requires manual.
	csv := "Contact Name,Call Sign,Current Until,Note,Membership Type,Class,Phone,Email,Street Address,City,Postal Code,State/Province,Volunteer Examiner\nHonor Test,,12/31/2026,,Honorary,,555-444-4444,hon@example.invalid,4 Main,Bedford,15522,PA,false\n"

	up, err := svc.Upload(context.Background(), strings.NewReader(csv), "csv", "test.csv", 1, "manual-1")
	require.NoError(t, err)
	assert.Equal(t, 1, up.ManualRows)

	// Preview.
	preview, err := svc.Preview(context.Background(), up.RunID)
	require.NoError(t, err)
	assert.False(t, preview.Ready)
	assert.Equal(t, 1, preview.UnresolvedManual)

	// Commit should refuse.
	_, err = svc.Commit(context.Background(), up.RunID, 1)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrUnresolvedManual)
}

func TestRecordDecisionAndCommit(t *testing.T) {
	svc, d := setupServiceDB(t)

	// Honorary without base type → requires manual.
	csv := "Contact Name,Call Sign,Current Until,Note,Membership Type,Class,Phone,Email,Street Address,City,Postal Code,State/Province,Volunteer Examiner\nHonor Test,,12/31/2026,,Honorary,,555-444-4444,hon@example.invalid,4 Main,Bedford,15522,PA,false\n"

	up, err := svc.Upload(context.Background(), strings.NewReader(csv), "csv", "test.csv", 1, "decide-1")
	require.NoError(t, err)

	// Get the manual row.
	rows, err := svc.ListRows(context.Background(), up.RunID, 100, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, int64(1), rows[0].RequiresManual)

	// Record a decision to skip.
	decision, err := svc.RecordDecision(context.Background(), up.RunID, DecisionInput{
		RowID:     rows[0].ID,
		DecidedBy: 1,
		Action:    "skip",
	})
	require.NoError(t, err)
	assert.Equal(t, "skip", decision.Action)

	// Preview should now show ready.
	preview, err := svc.Preview(context.Background(), up.RunID)
	require.NoError(t, err)
	assert.True(t, preview.Ready)
	assert.Equal(t, 0, preview.UnresolvedManual)

	// Commit should succeed.
	result, err := svc.Commit(context.Background(), up.RunID, 1)
	require.NoError(t, err)
	assert.Equal(t, 0, result.Created)
	assert.Equal(t, 1, result.Skipped)

	// Verify no persons created.
	var count int
	err = d.QueryRow(`SELECT count(*) FROM persons`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestRecordDecisionApproveCreate(t *testing.T) {
	svc, d := setupServiceDB(t)

	// Honorary without base type → requires manual.
	csv := "Contact Name,Call Sign,Current Until,Note,Membership Type,Class,Phone,Email,Street Address,City,Postal Code,State/Province,Volunteer Examiner\nHonor Test,,12/31/2026,,Honorary,,555-444-4444,hon@example.invalid,4 Main,Bedford,15522,PA,false\n"

	up, err := svc.Upload(context.Background(), strings.NewReader(csv), "csv", "test.csv", 1, "approve-1")
	require.NoError(t, err)

	rows, err := svc.ListRows(context.Background(), up.RunID, 100, 0)
	require.NoError(t, err)

	// Approve as create.
	_, err = svc.RecordDecision(context.Background(), up.RunID, DecisionInput{
		RowID:     rows[0].ID,
		DecidedBy: 1,
		Action:    "approve_create",
	})
	require.NoError(t, err)

	// Preview and commit.
	_, err = svc.Preview(context.Background(), up.RunID)
	require.NoError(t, err)

	result, err := svc.Commit(context.Background(), up.RunID, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Created)

	// Person was created.
	var count int
	err = d.QueryRow(`SELECT count(*) FROM persons`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestRecordDecisionInvalidRunState(t *testing.T) {
	svc, _ := setupServiceDB(t)

	csv := "Contact Name,Call Sign,Current Until,Note,Membership Type,Class,Phone,Email,Street Address,City,Postal Code,State/Province,Volunteer Examiner\nHonor Test,,12/31/2026,,Honorary,,555-444-4444,hon@example.invalid,4 Main,Bedford,15522,PA,false\n"

	up, err := svc.Upload(context.Background(), strings.NewReader(csv), "csv", "test.csv", 1, "invalid-state-1")
	require.NoError(t, err)

	rows, err := svc.ListRows(context.Background(), up.RunID, 100, 0)
	require.NoError(t, err)

	// Resolve and commit to get to "committed" state.
	_, err = svc.RecordDecision(context.Background(), up.RunID, DecisionInput{
		RowID: rows[0].ID, DecidedBy: 1, Action: "skip",
	})
	require.NoError(t, err)
	_, err = svc.Preview(context.Background(), up.RunID)
	require.NoError(t, err)
	_, err = svc.Commit(context.Background(), up.RunID, 1)
	require.NoError(t, err)

	// Try to add a decision to a committed run.
	_, err = svc.RecordDecision(context.Background(), up.RunID, DecisionInput{
		RowID: rows[0].ID, DecidedBy: 1, Action: "skip",
	})
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidTransition)
}

func TestCommitUpdatesExistingPerson(t *testing.T) {
	svc, d := setupServiceDB(t)

	// Pre-insert a person with a call sign.
	_, err := d.Exec(`INSERT INTO persons (display_name, sort_name, call_sign) VALUES ('Old Name', 'Name, Old', 'KA1UPD')`)
	require.NoError(t, err)

	csv := "Contact Name,Call Sign,Current Until,Note,Membership Type,Class,Phone,Email,Street Address,City,Postal Code,State/Province,Volunteer Examiner\nNew Name,KA1UPD,12/31/2026,,Full,General,555-333-3333,new@example.invalid,3 Main,Bedford,15522,PA,false\n"

	up, err := svc.Upload(context.Background(), strings.NewReader(csv), "csv", "test.csv", 1, "update-1")
	require.NoError(t, err)
	assert.Equal(t, 1, up.MatchedRows)

	// Preview then commit.
	_, err = svc.Preview(context.Background(), up.RunID)
	require.NoError(t, err)

	result, err := svc.Commit(context.Background(), up.RunID, 1)
	require.NoError(t, err)
	assert.Equal(t, 0, result.Created)
	assert.Equal(t, 1, result.Updated)

	// Verify person was updated.
	var displayName string
	err = d.QueryRow(`SELECT display_name FROM persons WHERE call_sign = 'KA1UPD'`).Scan(&displayName)
	require.NoError(t, err)
	assert.Equal(t, "New Name", displayName)
}

func TestDiscard(t *testing.T) {
	svc, _ := setupServiceDB(t)

	csv := "Contact Name,Call Sign,Current Until,Note,Membership Type,Class,Phone,Email,Street Address,City,Postal Code,State/Province,Volunteer Examiner\nDiscard Test,KA1DIS,12/31/2026,,Full,General,555-555-5555,dis@example.invalid,5 Main,Bedford,15522,PA,false\n"

	up, err := svc.Upload(context.Background(), strings.NewReader(csv), "csv", "test.csv", 1, "discard-1")
	require.NoError(t, err)

	err = svc.Discard(context.Background(), up.RunID)
	require.NoError(t, err)

	run, err := svc.GetRun(context.Background(), up.RunID)
	require.NoError(t, err)
	assert.Equal(t, "discarded", run.Status)

	// Preview should fail on discarded run.
	_, err = svc.Preview(context.Background(), up.RunID)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidTransition)

	// Commit should fail on discarded run.
	_, err = svc.Commit(context.Background(), up.RunID, 1)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidTransition)
}

func TestDiscardFromPreviewed(t *testing.T) {
	svc, _ := setupServiceDB(t)

	csv := "Contact Name,Call Sign,Current Until,Note,Membership Type,Class,Phone,Email,Street Address,City,Postal Code,State/Province,Volunteer Examiner\nTest,KA1TST,12/31/2026,,Full,General,555-000-0000,test@example.invalid,1 Main,Bedford,15522,PA,false\n"

	up, err := svc.Upload(context.Background(), strings.NewReader(csv), "csv", "test.csv", 1, "discard-prev-1")
	require.NoError(t, err)

	_, err = svc.Preview(context.Background(), up.RunID)
	require.NoError(t, err)

	err = svc.Discard(context.Background(), up.RunID)
	require.NoError(t, err)

	run, err := svc.GetRun(context.Background(), up.RunID)
	require.NoError(t, err)
	assert.Equal(t, "discarded", run.Status)
}

func TestCommitIdempotentRetry(t *testing.T) {
	svc, _ := setupServiceDB(t)

	csv := "Contact Name,Call Sign,Current Until,Note,Membership Type,Class,Phone,Email,Street Address,City,Postal Code,State/Province,Volunteer Examiner\nTest,KA1IDM,12/31/2026,,Full,General,555-666-6666,idm@example.invalid,6 Main,Bedford,15522,PA,false\n"

	up, err := svc.Upload(context.Background(), strings.NewReader(csv), "csv", "test.csv", 1, "idempotent-1")
	require.NoError(t, err)

	_, err = svc.Preview(context.Background(), up.RunID)
	require.NoError(t, err)

	result1, err := svc.Commit(context.Background(), up.RunID, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, result1.Created)

	// Second commit should return the same result (idempotent).
	result2, err := svc.Commit(context.Background(), up.RunID, 1)
	require.NoError(t, err)
	assert.Equal(t, result1.Created, result2.Created)
	assert.Equal(t, result1.Updated, result2.Updated)
}

// --- Note splitting tests ---

func TestSplitNotes(t *testing.T) {
	cases := []struct {
		input    string
		expected []string
	}{
		{"", nil},
		{"Simple note", []string{"Simple note"}},
		{"Paid via PayPal on 1/1/2024. Paid via PayPal on 1/2/2025.", []string{"Paid via PayPal on 1/1/2024", "Paid via PayPal on 1/2/2025"}},
		{"Duplicate. Duplicate.", []string{"Duplicate"}},
		{"First. Second. First.", []string{"First", "Second"}},
		{"Trailing period.", []string{"Trailing period"}},
		{"  Whitespace  .  Trimmed  ", []string{"Whitespace", "Trimmed"}},
	}
	for _, tc := range cases {
		got := splitNotes(tc.input)
		assert.Equal(t, tc.expected, got, "input: %q", tc.input)
	}
}

func TestCommitCreatesNotes(t *testing.T) {
	svc, d := setupServiceDB(t)

	csv := "Contact Name,Call Sign,Current Until,Note,Membership Type,Class,Phone,Email,Street Address,City,Postal Code,State/Province,Volunteer Examiner\nAlice Test,KA1NTE,12/31/2026,Paid via PayPal on 1/1/2024. Paid via PayPal on 1/2/2025.,Full,General,555-111-1111,alice@example.invalid,1 Main,Bedford,15522,PA,false\n"

	up, err := svc.Upload(context.Background(), strings.NewReader(csv), "csv", "test.csv", 1, "notes-1")
	require.NoError(t, err)

	_, err = svc.Preview(context.Background(), up.RunID)
	require.NoError(t, err)

	result, err := svc.Commit(context.Background(), up.RunID, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Created)

	// Verify notes were created — should be 2 unique sentences.
	var noteCount int
	err = d.QueryRow(`SELECT count(*) FROM notes WHERE subject_kind = 'person'`).Scan(&noteCount)
	require.NoError(t, err)
	assert.Equal(t, 2, noteCount, "should create 2 deduplicated notes")

	// Verify note contents and source.
	type noteRow struct {
		Body   string
		Source string
	}
	noteRows, err := d.Query(`SELECT body, source FROM notes WHERE subject_kind = 'person' ORDER BY id`)
	require.NoError(t, err)
	var notes []noteRow
	for noteRows.Next() {
		var n noteRow
		require.NoError(t, noteRows.Scan(&n.Body, &n.Source))
		notes = append(notes, n)
	}
	noteRows.Close()
	require.Len(t, notes, 2)
	assert.Equal(t, "Paid via PayPal on 1/1/2024", notes[0].Body)
	assert.Equal(t, "Paid via PayPal on 1/2/2025", notes[1].Body)
	assert.Equal(t, "groupsio_import", notes[0].Source)
}

func TestCommitNoteDedup(t *testing.T) {
	svc, d := setupServiceDB(t)

	// First import with a note.
	csv1 := "Contact Name,Call Sign,Current Until,Note,Membership Type,Class,Phone,Email,Street Address,City,Postal Code,State/Province,Volunteer Examiner\nBob Test,KA1DDP,12/31/2026,Original note,Full,General,555-222-2222,bob@example.invalid,2 Main,Bedford,15522,PA,false\n"

	up1, err := svc.Upload(context.Background(), strings.NewReader(csv1), "csv", "test.csv", 1, "dedup-1")
	require.NoError(t, err)
	_, err = svc.Preview(context.Background(), up1.RunID)
	require.NoError(t, err)
	_, err = svc.Commit(context.Background(), up1.RunID, 1)
	require.NoError(t, err)

	// Second import with same call sign — update path, same note + new note.
	csv2 := "Contact Name,Call Sign,Current Until,Note,Membership Type,Class,Phone,Email,Street Address,City,Postal Code,State/Province,Volunteer Examiner\nBob Test,KA1DDP,12/31/2026,Original note. New note added,Full,General,555-222-2222,bob@example.invalid,2 Main,Bedford,15522,PA,false\n"

	up2, err := svc.Upload(context.Background(), strings.NewReader(csv2), "csv", "test.csv", 1, "dedup-2")
	require.NoError(t, err)
	_, err = svc.Preview(context.Background(), up2.RunID)
	require.NoError(t, err)
	_, err = svc.Commit(context.Background(), up2.RunID, 1)
	require.NoError(t, err)

	// Should have 2 notes total (original + new), not 3 (no duplicate of original).
	var noteCount int
	err = d.QueryRow(`SELECT count(*) FROM notes WHERE subject_kind = 'person'`).Scan(&noteCount)
	require.NoError(t, err)
	assert.Equal(t, 2, noteCount, "should deduplicate 'Original note' across imports")
}

func TestCommitIsTransactional(t *testing.T) {
	// Verify commit uses a transaction by checking that the run status
	// and persons are both updated atomically (both exist after commit).
	svc, d := setupServiceDB(t)

	csv := "Contact Name,Call Sign,Current Until,Note,Membership Type,Class,Phone,Email,Street Address,City,Postal Code,State/Province,Volunteer Examiner\nTest1,KA1TX1,12/31/2026,,Full,General,555-000-0001,tx1@example.invalid,1 Main,Bedford,15522,PA,false\nTest2,KA1TX2,12/31/2026,,Full,General,555-000-0002,tx2@example.invalid,2 Main,Bedford,15522,PA,false\n"

	up, err := svc.Upload(context.Background(), strings.NewReader(csv), "csv", "test.csv", 1, "txn-1")
	require.NoError(t, err)

	_, err = svc.Preview(context.Background(), up.RunID)
	require.NoError(t, err)

	result, err := svc.Commit(context.Background(), up.RunID, 1)
	require.NoError(t, err)
	assert.Equal(t, 2, result.Created)

	// Both persons and committed status should exist together.
	var personCount int
	err = d.QueryRow(`SELECT count(*) FROM persons`).Scan(&personCount)
	require.NoError(t, err)
	assert.Equal(t, 2, personCount)

	run, err := svc.GetRun(context.Background(), up.RunID)
	require.NoError(t, err)
	assert.Equal(t, "committed", run.Status)
}

// The licence class and Volunteer Examiner status survive a commit
// (bcars-portal-um9).
//
// They were parsed, normalized and staged before this, and then dropped at the
// last step, so a club importing its real list lost both. Worth noting how that
// went unnoticed: TestNormalizeVolunteerExaminer above covers the checkbox
// parsing carefully and passed the entire time. It tests a piece; this tests
// the property — that what the export says about a member is what the club ends
// up holding.
func TestCommitKeepsLicenceClassAndVolunteerExaminer(t *testing.T) {
	svc, d := setupServiceDB(t)

	csv := "Contact Name,Call Sign,Current Until,Note,Membership Type,Class,Phone,Email,Street Address,City,Postal Code,State/Province,Volunteer Examiner\n" +
		"Vera Examiner,KA1VE,12/31/2026,,Full,Extra,555-111-2222,vera@example.invalid,1 Main,Bedford,15522,PA,true\n" +
		"Nora Novice,KA1NN,12/31/2026,,Full,Technician,555-111-3333,nora@example.invalid,2 Main,Bedford,15522,PA,false\n"

	up, err := svc.Upload(context.Background(), strings.NewReader(csv), "csv", "test.csv", 1, "class-1")
	require.NoError(t, err)
	_, err = svc.Preview(context.Background(), up.RunID)
	require.NoError(t, err)
	_, err = svc.Commit(context.Background(), up.RunID, 1)
	require.NoError(t, err)

	for _, tc := range []struct {
		callSign string
		class    string
		ve       int64
	}{
		{"KA1VE", "extra", 1},
		{"KA1NN", "technician", 0},
	} {
		var class string
		var ve int64
		require.NoError(t, d.QueryRow(
			`SELECT COALESCE(license_class, ''), volunteer_examiner FROM persons WHERE call_sign = ?`,
			tc.callSign).Scan(&class, &ve))

		assert.Equalf(t, tc.class, class, "%s should keep the licence class the export gave", tc.callSign)
		assert.Equalf(t, tc.ve, ve, "%s should keep the Volunteer Examiner status the export gave", tc.callSign)
	}
}

// TestCommitDoesNotWriteAnImportedClaimIntoVerifications keeps the two apart.
// fcc_verifications records what an OFFICER checked — a source, a date, a
// verifier. An imported value is what the old list said, and writing it there
// would manufacture evidence nobody produced.
func TestCommitDoesNotWriteAnImportedClaimIntoVerifications(t *testing.T) {
	svc, d := setupServiceDB(t)

	csv := "Contact Name,Call Sign,Current Until,Note,Membership Type,Class,Phone,Email,Street Address,City,Postal Code,State/Province,Volunteer Examiner\n" +
		"Vera Examiner,KA1VE,12/31/2026,,Full,Extra,555-111-2222,vera@example.invalid,1 Main,Bedford,15522,PA,true\n"

	up, err := svc.Upload(context.Background(), strings.NewReader(csv), "csv", "test.csv", 1, "class-2")
	require.NoError(t, err)
	_, err = svc.Preview(context.Background(), up.RunID)
	require.NoError(t, err)
	_, err = svc.Commit(context.Background(), up.RunID, 1)
	require.NoError(t, err)

	var verifications int
	require.NoError(t, d.QueryRow(`SELECT COUNT(*) FROM fcc_verifications`).Scan(&verifications))
	assert.Zero(t, verifications,
		"an imported licence class is a claim; a verification is something an officer performed")
}

// TestCommitUpdateKeepsWhatTheExportDoesNotSay: an absent column means the
// export is silent, not that the club has been told the answer is no.
func TestCommitUpdateKeepsWhatTheExportDoesNotSay(t *testing.T) {
	svc, d := setupServiceDB(t)

	_, err := d.Exec(
		`INSERT INTO persons (display_name, sort_name, call_sign, license_class, volunteer_examiner)
		 VALUES ('Held Already', 'Already, Held', 'KA1KEP', 'extra', 1)`)
	require.NoError(t, err)

	// Same member, and the export carries no Class value for them.
	csv := "Contact Name,Call Sign,Current Until,Note,Membership Type,Class,Phone,Email,Street Address,City,Postal Code,State/Province,Volunteer Examiner\n" +
		"Held Already,KA1KEP,12/31/2026,,Full,,555-111-4444,held@example.invalid,4 Main,Bedford,15522,PA,true\n"

	up, err := svc.Upload(context.Background(), strings.NewReader(csv), "csv", "test.csv", 1, "class-3")
	require.NoError(t, err)
	_, err = svc.Preview(context.Background(), up.RunID)
	require.NoError(t, err)
	_, err = svc.Commit(context.Background(), up.RunID, 1)
	require.NoError(t, err)

	var class string
	require.NoError(t, d.QueryRow(
		`SELECT COALESCE(license_class, '') FROM persons WHERE call_sign = 'KA1KEP'`).Scan(&class))
	assert.Equal(t, "extra", class,
		"an empty Class column must not erase a licence class the club already holds")
}
