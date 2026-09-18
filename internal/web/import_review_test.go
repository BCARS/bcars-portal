package web

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/domain/importd"
)

// What the import page tells an officer about who settled each row
// (bcars-portal-7kp).
//
// Recording a decision clears requires_manual, which is correct -- the row no
// longer needs one. The page read that flag as "the matcher handled it", so a
// run where three rows were ruled on by hand reported 21 of 21 automatic, and
// the decisions were visible nowhere. This is the evidence an officer reviews
// before committing real member data, so overstating how much was automatic is
// the whole defect.

const importCSVHeader = "Contact Name,Call Sign,Current Until,Note,Membership Type,Class,Phone,Email,Street Address,City,Postal Code,State/Province,Volunteer Examiner\n"

// seedImportRun uploads a two-row CSV: one the matcher settles by itself, and
// one it cannot, and previews it.
func seedImportRun(t *testing.T, e *testEnv) int64 {
	t.Helper()
	csv := importCSVHeader +
		"Clean Row,KA1AAA,12/31/2026,,Full,General,555-111-1111,clean@example.invalid,1 Main,Bedford,15522,PA,false\n" +
		"Puzzling Row,KA1BBB,,,Honorary,,555-222-2222,puzzle@example.invalid,2 Main,Bedford,15522,PA,false\n"

	up, err := e.h.imports.Upload(context.Background(), strings.NewReader(csv), "csv", "test.csv", 1, "7kp-1")
	require.NoError(t, err)
	require.Equal(t, 1, up.ManualRows, "the honorary row with no type needs a person")

	_, err = e.h.imports.Preview(context.Background(), up.RunID)
	require.NoError(t, err)
	return up.RunID
}

// reviewSection isolates the "Rows the matcher could not settle" card, so an
// assertion about it cannot be satisfied by the All Staged Rows table below.
func reviewSection(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, "Rows the matcher could not settle")
	require.NotEqual(t, -1, start, "the review section should be on the page")
	end := strings.Index(body[start:], "All Staged Rows")
	require.NotEqual(t, -1, end, "the review section should end before the full table")
	return body[start : start+end]
}

// tileValue reads a stat tile's number off the rendered page.
func tileValue(t *testing.T, body, label string) string {
	t.Helper()
	re := regexp.MustCompile(`(?s)<div class="label">` + regexp.QuoteMeta(label) + `</div>\s*<div class="value">(\d+)</div>`)
	m := re.FindStringSubmatch(body)
	require.Len(t, m, 2, "tile %q not found", label)
	return m[1]
}

func TestADecidedRowIsNotCountedAsAutomatic(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")
	runID := seedImportRun(t, e)

	body := e.get(t, fmt.Sprintf("/admin/imports/%d", runID)).Body.String()
	assert.Equal(t, "1", tileValue(t, body, "Matched automatically"))
	assert.Equal(t, "0", tileValue(t, body, "Decided by an officer"))
	assert.Equal(t, "1", tileValue(t, body, "Awaiting a decision"))

	// The officer rules on the row the matcher could not settle.
	var rowID int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT id FROM staged_import_rows WHERE import_run_id = ? AND requires_manual = 1`, runID).Scan(&rowID))
	_, err := e.h.imports.RecordDecision(context.Background(), runID, importd.DecisionInput{
		RowID: rowID, DecidedBy: 1, Action: "skip",
	})
	require.NoError(t, err)

	body = e.get(t, fmt.Sprintf("/admin/imports/%d", runID)).Body.String()
	assert.Equal(t, "1", tileValue(t, body, "Matched automatically"),
		"a decided row must not join the rows the matcher settled by itself")
	assert.Equal(t, "1", tileValue(t, body, "Decided by an officer"))
	assert.Equal(t, "0", tileValue(t, body, "Awaiting a decision"))
}

func TestADecidedRowSaysWhoDecidedIt(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")
	runID := seedImportRun(t, e)

	var rowID int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT id FROM staged_import_rows WHERE import_run_id = ? AND requires_manual = 1`, runID).Scan(&rowID))
	_, err := e.h.imports.RecordDecision(context.Background(), runID, importd.DecisionInput{
		RowID: rowID, DecidedBy: 1, Action: "skip",
	})
	require.NoError(t, err)

	body := e.get(t, fmt.Sprintf("/admin/imports/%d", runID)).Body.String()

	// The row stays in the REVIEW section rather than vanishing from it. The
	// section is isolated on purpose: asserting the name against the whole
	// page would pass on the All Staged Rows table alone, which never dropped
	// the row and so proves nothing about the defect.
	review := reviewSection(t, body)
	assert.Contains(t, review, "Puzzling Row",
		"a decided row must stay visible in the section that reviewed it")
	assert.Contains(t, review, "test@test.local",
		"and must say who decided it")

	// And the All Staged Rows line says a person settled it, not the matcher.
	assert.Contains(t, body, "Decided by test@test.local")
	// The clean row is still automatic, so "Decided by" is not simply printed
	// on every row.
	cleanRow := regexp.MustCompile(`(?s)Clean Row.*?</tr>`).FindString(body)
	require.NotEmpty(t, cleanRow)
	assert.Contains(t, cleanRow, "Automatic")
	assert.NotContains(t, cleanRow, "Decided by")
}

// The record of what was done by hand survives the commit. Before, the whole
// Decision column was hidden once the run left the decidable states, which is
// exactly when an officer would want to know what had been decided.
func TestTheDecisionRecordSurvivesTheCommit(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")
	runID := seedImportRun(t, e)

	var rowID int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT id FROM staged_import_rows WHERE import_run_id = ? AND requires_manual = 1`, runID).Scan(&rowID))
	_, err := e.h.imports.RecordDecision(context.Background(), runID, importd.DecisionInput{
		RowID: rowID, DecidedBy: 1, Action: "skip",
	})
	require.NoError(t, err)

	_, err = e.h.imports.Preview(context.Background(), runID)
	require.NoError(t, err)
	result, err := e.h.imports.Commit(context.Background(), runID, 1)
	require.NoError(t, err)
	require.Equal(t, 1, result.Created)

	body := e.get(t, fmt.Sprintf("/admin/imports/%d", runID)).Body.String()
	assert.Equal(t, "1", tileValue(t, body, "Decided by an officer"))
	assert.Contains(t, body, "Decided by test@test.local",
		"after the commit, the page must still say what a person decided")
}

// The reason an officer is asked to rule on a row reads as an instruction, not
// as the staging layer's identifier.
func TestManualReasonsReadAsSentences(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")
	runID := seedImportRun(t, e)

	body := e.get(t, fmt.Sprintf("/admin/imports/%d", runID)).Body.String()
	assert.NotContains(t, body, "honorary_type_unspecified",
		"a reason code is a rule name, not an instruction")
	assert.Contains(t, body, "The export says honorary but not which membership type to grant")
}

func TestManualReasonText(t *testing.T) {
	cases := map[string]string{
		"honorary_type_unspecified":            "The export says honorary but not which membership type to grant. Choose one, or skip the row.",
		"ambiguous_email":                      "More than one member record has this email address, so the matcher cannot tell which one this row is.",
		"ambiguous_call_sign":                  "More than one member record matches this row's call sign.",
		"":                                     "",
		"something_the_ui_has_not_been_taught": "something_the_ui_has_not_been_taught",
	}
	for code, want := range cases {
		assert.Equal(t, want, manualReasonText(code), "code %q", code)
	}
	assert.Contains(t, manualReasonText("lifetime_like_date_needs_confirmation"), "never expires")
}

// An import page for a run with nothing manual still renders, and says so
// without claiming anyone decided anything.
func TestARunWithNothingManualClaimsNoDecisions(t *testing.T) {
	e := setupHandlerWithRoles(t, "administrator")
	csv := importCSVHeader +
		"Clean Row,KA1AAA,12/31/2026,,Full,General,555-111-1111,clean@example.invalid,1 Main,Bedford,15522,PA,false\n"
	up, err := e.h.imports.Upload(context.Background(), strings.NewReader(csv), "csv", "clean.csv", 1, "7kp-clean")
	require.NoError(t, err)

	w := e.get(t, fmt.Sprintf("/admin/imports/%d", up.RunID))
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Equal(t, "1", tileValue(t, body, "Matched automatically"))
	assert.Equal(t, "0", tileValue(t, body, "Decided by an officer"))
	assert.Equal(t, "0", tileValue(t, body, "Awaiting a decision"))
	// "Decided by an officer" is a tile label and is always on the page; what
	// must be absent is any row claiming a person settled it.
	assert.NotContains(t, body, "Decided by test@test.local")
	assert.NotContains(t, body, "Rows the matcher could not settle")
}
