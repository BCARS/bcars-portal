package web

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// generateSheet creates a worksheet run through the real form and returns its id.
func generateSheet(t *testing.T, e *testEnv, form url.Values) int64 {
	t.Helper()
	w := e.postForm(t, "/admin/treasury/worksheets", form)
	require.Equal(t, http.StatusSeeOther, w.Code)

	var id int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT id FROM dues_worksheet_runs ORDER BY id DESC LIMIT 1`).Scan(&id))
	return id
}

// TestPrintedSheetCarriesEverythingItNeeds walks the checklist a treasurer
// filling this in by hand depends on.
func TestPrintedSheetCarriesEverythingItNeeds(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	seedMember(t, e, "Alpha Member", "W3AAA", "2020-12-31")
	seedMember(t, e, "Bravo Member", "", "")

	id := generateSheet(t, e, url.Values{
		"label": {"July meeting"}, "filter_kind": {"owes"},
		"sort_order": {"last_name"}, "as_of": {"2026-07-01"},
	})

	w := e.get(t, "/admin/treasury/worksheets/"+itoa(id)+"?guests=3")
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	t.Run("club identity and the rules of the sheet", func(t *testing.T) {
		assert.Contains(t, body, "Bedford County Amateur Radio Society")
		assert.Contains(t, body, "Annual dues for")
		assert.Contains(t, body, "The club dues year ends 31 December")
		assert.Contains(t, body, "the treasurer may record any date",
			"the year-end rule is stated as a convention, not a hard rule")
	})

	t.Run("sheet identity and dates", func(t *testing.T) {
		assert.Contains(t, body, "Sheet "+itoa(id))
		assert.Contains(t, body, "printed ")
		assert.Contains(t, body, "judged as of 2026-07-01")
		assert.Contains(t, body, "details good as of ")
	})

	t.Run("member rows and hand-written boxes", func(t *testing.T) {
		assert.Contains(t, body, "Alpha Member")
		assert.Contains(t, body, "W3AAA")
		assert.Contains(t, body, "Not recorded", "a member with no coverage reads as text")
		assert.Contains(t, body, "Expired", "and an expired one says so in words")
		for _, column := range []string{"Amount", "Method", "Check no.", "Date", "Done"} {
			assert.Contains(t, body, ">"+column+"<", "the sheet has a %s column", column)
		}
	})

	t.Run("blank guest rows", func(t *testing.T) {
		assert.Contains(t, body, ">G1<")
		assert.Contains(t, body, ">G2<")
		assert.Contains(t, body, ">G3<")
		assert.NotContains(t, body, ">G4<")
	})

	t.Run("reconciliation totals", func(t *testing.T) {
		assert.Contains(t, body, "Reconciliation")
		assert.Contains(t, body, "Cash counted")
		assert.Contains(t, body, "Checks counted")
		assert.Contains(t, body, "Total handed to the treasurer")
	})
}

// TestPrintStylesheetRules pins the print behaviour the sheet depends on. These
// are CSS rules rather than rendered output, but a headless test cannot print;
// asserting the rules exist is the honest available check.
func TestPrintStylesheetRules(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	seedMember(t, e, "Alpha Member", "W3AAA", "2020-12-31")
	id := generateSheet(t, e, url.Values{
		"filter_kind": {"owes"}, "sort_order": {"last_name"},
	})

	body := e.get(t, "/admin/treasury/worksheets/"+itoa(id)).Body.String()

	assert.Contains(t, body, "@page { size: letter portrait;", "letter portrait")
	assert.Contains(t, body, "display: table-header-group", "headers repeat on every page")
	assert.Contains(t, body, "page-break-inside: avoid", "a row never splits across pages")
	assert.Contains(t, body, "break-inside: avoid")
	assert.Contains(t, body, "font-size: 12pt", "body text is at least 12pt")
	assert.Contains(t, body, ".header, .nav-tabs, .no-print", "app chrome is excluded from print")
}

// TestContactColumnsAppearOnlyWhenRequestedAndAllowed proves the sheet never
// carries a column the run did not record.
func TestContactColumnsAppearOnlyWhenRequestedAndAllowed(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	m := seedMember(t, e, "Contact Member", "W3CON", "2020-12-31")
	_, err := e.h.db.Exec(`
		INSERT INTO contact_methods (person_id, kind, value_raw, value_norm, is_primary)
		SELECT person_id, 'email', 'member@example.test', 'member@example.test', 1
		  FROM memberships WHERE id = ?`, m)
	require.NoError(t, err)
	seedMember(t, e, "Quiet Member", "W3QUI", "2020-12-31")

	t.Run("without the option", func(t *testing.T) {
		id := generateSheet(t, e, url.Values{
			"filter_kind": {"owes"}, "sort_order": {"last_name"},
		})
		body := e.get(t, "/admin/treasury/worksheets/"+itoa(id)).Body.String()
		assert.NotContains(t, body, "member@example.test")
	})

	t.Run("with the option", func(t *testing.T) {
		id := generateSheet(t, e, url.Values{
			"filter_kind": {"owes"}, "sort_order": {"last_name"}, "include_email": {"yes"},
		})
		body := e.get(t, "/admin/treasury/worksheets/"+itoa(id)).Body.String()
		assert.Contains(t, body, "member@example.test")
		assert.Contains(t, body, "Not shared",
			"a member with no email reads as text, not an unexplained blank")
	})
}

// TestFollowUpSheetMarksRowsPaidSince proves the second sheet visibly
// distinguishes the lines already dealt with.
func TestFollowUpSheetMarksRowsPaidSince(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	paid := seedMember(t, e, "Paid Member", "W3PD1", "2020-12-31")
	seedMember(t, e, "Owing Member", "W3OWE", "2020-12-31")

	first := generateSheet(t, e, url.Values{
		"filter_kind": {"owes"}, "sort_order": {"last_name"},
	})

	// Backdate the run so the payment is unambiguously later. "Paid since this
	// sheet" compares entered_at > generated_at at millisecond resolution, and a
	// test that generates a sheet and pays in the same millisecond ties. In
	// practice a sheet is printed and payments arrive later, and a tie would
	// only leave one extra row on a follow-up sheet.
	_, err := e.h.db.Exec(`
		UPDATE dues_worksheet_runs SET generated_at = '2026-01-01T00:00:00.000Z' WHERE id = ?`,
		first)
	require.NoError(t, err)

	// One member pays after the sheet was printed.
	require.Equal(t, http.StatusOK, e.postForm(t,
		"/admin/treasury/memberships/"+itoa(paid)+"/payment", url.Values{
			"amount": {"40.00"}, "method": {"cash"},
			"received_on": {"2026-07-05"}, "paid_through": {"2026-12-31"},
			"idempotency_key": {"pay-1"},
		}).Code)

	// Reprinting the first sheet still says what it said, and marks the line.
	body := e.get(t, "/admin/treasury/worksheets/"+itoa(first)).Body.String()
	assert.Contains(t, body, "Paid Member")
	assert.Contains(t, body, "2020-12-31", "the snapshot is unchanged")

	second := generateSheet(t, e, url.Values{
		"filter_kind": {"unpaid_since_run"}, "source_run_id": {itoa(first)},
		"sort_order": {"last_name"},
	})
	body = e.get(t, "/admin/treasury/worksheets/"+itoa(second)).Body.String()
	assert.Contains(t, body, "Owing Member")
	assert.NotContains(t, body, "Paid Member", "the member who paid is not chased again")
	assert.Contains(t, body, "Paid since last sheet",
		"the follow-up sheet carries the column that explains itself")
}

// TestEnterThisSheetNowOpensALinkedBatch proves the flow from paper back to the
// grid, in the same order, with nothing filled in.
func TestEnterThisSheetNowOpensALinkedBatch(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	seedMember(t, e, "Zulu Member", "W3ZZZ", "2020-12-31")
	seedMember(t, e, "Alpha Member", "W3AAA", "2020-12-31")

	id := generateSheet(t, e, url.Values{
		"label": {"July meeting"}, "filter_kind": {"owes"}, "sort_order": {"last_name"},
	})

	w := e.postForm(t, "/admin/treasury/worksheets/"+itoa(id)+"/batch", url.Values{})
	require.Equal(t, http.StatusSeeOther, w.Code)

	var batchID, linked int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT id, worksheet_run_id FROM payment_batches ORDER BY id DESC LIMIT 1`).
		Scan(&batchID, &linked))
	assert.Equal(t, id, linked, "the batch names the sheet it came from")

	var entries int
	require.NoError(t, e.h.db.QueryRow(
		`SELECT count(*) FROM payment_batch_entries WHERE batch_id = ?`, batchID).Scan(&entries))
	assert.Zero(t, entries, "no amounts are invented; the treasurer types what is on the paper")

	// The sheet's own order is what the client presents.
	rows, err := e.h.db.Query(
		`SELECT display_name FROM dues_worksheet_rows WHERE run_id = ? ORDER BY ordinal`, id)
	require.NoError(t, err)
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		names = append(names, n)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"Alpha Member", "Zulu Member"}, names)
}

func TestWorksheetOptionValidation(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")

	w := e.postForm(t, "/admin/treasury/worksheets", url.Values{
		"filter_kind": {"everybody"}, "sort_order": {"last_name"},
	})
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Contains(t, w.Body.String(), "Choose who the sheet should list")

	w = e.postForm(t, "/admin/treasury/worksheets", url.Values{
		"filter_kind": {"unpaid_since_run"}, "sort_order": {"last_name"},
	})
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Contains(t, w.Body.String(), "Choose which earlier sheet")
}

// TestWorksheetPagesDenyNonTreasurers proves the sheet is treasury-only.
func TestWorksheetPagesDenyNonTreasurers(t *testing.T) {
	e := setupHandlerWithRoles(t, "member")
	seedMember(t, e, "Hidden Member", "W3HID", "2020-12-31")

	for _, target := range []string{
		"/admin/treasury/worksheets",
		"/admin/treasury/worksheets/1",
	} {
		t.Run(target, func(t *testing.T) {
			w := e.get(t, target)
			assert.Equal(t, http.StatusForbidden, w.Code)
			assert.NotContains(t, w.Body.String(), "Hidden Member")
		})
	}

	w := e.postForm(t, "/admin/treasury/worksheets", url.Values{
		"filter_kind": {"owes"}, "sort_order": {"last_name"},
	})
	assert.Equal(t, http.StatusForbidden, w.Code)

	var runs int
	require.NoError(t, e.h.db.QueryRow(`SELECT count(*) FROM dues_worksheet_runs`).Scan(&runs))
	assert.Zero(t, runs)
}

// TestLinkedBatchPresentsSheetInOrder is the property the earlier tests missed:
// they proved the link and the ordinals existed separately, not that a
// treasurer can work down the sheet from the batch page. This one fails if the
// batch surface ignores worksheet_run_id.
func TestLinkedBatchPresentsSheetInOrder(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	zulu := seedMember(t, e, "Zulu Member", "W3ZZZ", "2020-12-31")
	seedMember(t, e, "Alpha Member", "W3AAA", "2020-12-31")

	id := generateSheet(t, e, url.Values{
		"label": {"July meeting"}, "filter_kind": {"owes"}, "sort_order": {"last_name"},
	})
	require.Equal(t, http.StatusSeeOther,
		e.postForm(t, "/admin/treasury/worksheets/"+itoa(id)+"/batch", url.Values{}).Code)

	var batchID int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT id FROM payment_batches ORDER BY id DESC LIMIT 1`).Scan(&batchID))

	body := e.get(t, "/admin/treasury/batches/"+itoa(batchID)).Body.String()

	assert.Contains(t, body, "Working down sheet "+itoa(id),
		"the batch page must name the sheet it came from")
	assert.Contains(t, body, "Alpha Member")
	assert.Contains(t, body, "Zulu Member")
	assert.Contains(t, body, "0 of 2 entered")

	// Saved ordinal order, not database or alphabetical accident.
	alphaAt := indexOf(body, "Alpha Member")
	zuluAt := indexOf(body, "Zulu Member")
	assert.Less(t, alphaAt, zuluAt, "the sheet's saved order is what the grid shows")

	// Entering a row moves progress without touching the snapshot.
	addRow(t, e, batchID, zulu, "40.00", "cash", "row-1")
	body = e.get(t, "/admin/treasury/batches/"+itoa(batchID)).Body.String()
	assert.Contains(t, body, "1 of 2 entered")

	var snapshotRows int
	require.NoError(t, e.h.db.QueryRow(
		`SELECT count(*) FROM dues_worksheet_rows WHERE run_id = ?`, id).Scan(&snapshotRows))
	assert.Equal(t, 2, snapshotRows, "the worksheet snapshot is unchanged")
}

// TestWorksheetHandoffIsRetrySafe proves a resubmitted "Enter this sheet now"
// returns the same batch instead of opening a second empty one.
func TestWorksheetHandoffIsRetrySafe(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	seedMember(t, e, "Alpha Member", "W3AAA", "2020-12-31")

	id := generateSheet(t, e, url.Values{
		"filter_kind": {"owes"}, "sort_order": {"last_name"},
	})

	form := url.Values{"idempotency_key": {"handoff-1"}}
	first := e.postForm(t, "/admin/treasury/worksheets/"+itoa(id)+"/batch", form)
	require.Equal(t, http.StatusSeeOther, first.Code)
	second := e.postForm(t, "/admin/treasury/worksheets/"+itoa(id)+"/batch", form)
	require.Equal(t, http.StatusSeeOther, second.Code)

	assert.Equal(t, first.Header().Get("Location"), second.Header().Get("Location"),
		"a retry must land on the same batch")

	var batches int
	require.NoError(t, e.h.db.QueryRow(
		`SELECT count(*) FROM payment_batches`).Scan(&batches))
	assert.Equal(t, 1, batches, "a retry must not open a second empty batch")
}

// TestHandoffLeavesNoOrphanWhenLinkingFails proves creation and linkage are one
// transaction: a run that vanishes mid-flight leaves no batch behind.
func TestHandoffLeavesNoOrphanWhenLinkingFails(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	seedMember(t, e, "Alpha Member", "W3AAA", "2020-12-31")

	w := e.postForm(t, "/admin/treasury/worksheets/999/batch", url.Values{})
	assert.Equal(t, http.StatusNotFound, w.Code)

	var batches int
	require.NoError(t, e.h.db.QueryRow(`SELECT count(*) FROM payment_batches`).Scan(&batches))
	assert.Zero(t, batches, "a failed handoff must leave no orphan batch")
}

// TestWorksheetAsOfIsValidatedNotRepaired proves a durable report parameter is
// never silently replaced. The form previously substituted today's date when
// as_of would not parse, so a tampered or mistyped submission produced a
// perfectly valid-looking sheet judged against a date nobody chose.
func TestWorksheetAsOfIsValidatedNotRepaired(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	seedMember(t, e, "Alpha Member", "W3AAA", "2020-12-31")

	t.Run("an invalid date is refused and nothing is written", func(t *testing.T) {
		w := e.postForm(t, "/admin/treasury/worksheets", url.Values{
			"label": {"July meeting"}, "as_of": {"2026-99-99"},
			"filter_kind": {"active"}, "sort_order": {"call_sign"},
			"include_email": {"yes"}, "guest_rows": {"7"},
		})
		require.Equal(t, http.StatusUnprocessableEntity, w.Code)
		body := w.Body.String()
		assert.Contains(t, body, "Enter the as-of date as YYYY-MM-DD")

		var runs, rows int
		require.NoError(t, e.h.db.QueryRow(`SELECT count(*) FROM dues_worksheet_runs`).Scan(&runs))
		require.NoError(t, e.h.db.QueryRow(`SELECT count(*) FROM dues_worksheet_rows`).Scan(&rows))
		assert.Zero(t, runs, "a refused submission creates no run")
		assert.Zero(t, rows)

		// Every submitted choice survives the rejection.
		assert.Contains(t, body, `value="July meeting"`)
		assert.Contains(t, body, `value="2026-99-99"`)
		assert.Contains(t, body, `value="active" checked`)
		assert.Contains(t, body, `value="call_sign" checked`)
		assert.Contains(t, body, `name="include_email" value="yes" checked`)
		assert.Contains(t, body, `value="7"`)
	})

	t.Run("a blank date defaults to today, as the API does", func(t *testing.T) {
		w := e.postForm(t, "/admin/treasury/worksheets", url.Values{
			"as_of": {""}, "filter_kind": {"owes"}, "sort_order": {"last_name"},
		})
		require.Equal(t, http.StatusSeeOther, w.Code)

		var asOf string
		require.NoError(t, e.h.db.QueryRow(
			`SELECT as_of FROM dues_worksheet_runs ORDER BY id DESC LIMIT 1`).Scan(&asOf))
		assert.Equal(t, time.Now().UTC().Format("2006-01-02"), asOf)
	})

	t.Run("a valid date is used as submitted", func(t *testing.T) {
		w := e.postForm(t, "/admin/treasury/worksheets", url.Values{
			"as_of": {"2026-07-01"}, "filter_kind": {"owes"}, "sort_order": {"last_name"},
		})
		require.Equal(t, http.StatusSeeOther, w.Code)

		var asOf string
		require.NoError(t, e.h.db.QueryRow(
			`SELECT as_of FROM dues_worksheet_runs ORDER BY id DESC LIMIT 1`).Scan(&asOf))
		assert.Equal(t, "2026-07-01", asOf)
	})
}
