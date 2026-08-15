package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openBatch opens a batch through the real form and returns its id.
func openBatch(t *testing.T, e *testEnv, label string) int64 {
	t.Helper()
	w := e.postForm(t, "/admin/treasury/batches", url.Values{"label": {label}})
	require.Equal(t, http.StatusSeeOther, w.Code)

	var id int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT id FROM payment_batches ORDER BY id DESC LIMIT 1`).Scan(&id))
	return id
}

func batchVersion(t *testing.T, e *testEnv, batchID int64) string {
	t.Helper()
	var v int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT version FROM payment_batches WHERE id = ?`, batchID).Scan(&v))
	return strconv.FormatInt(v, 10)
}

// addRow adds one draft row through the real form.
func addRow(t *testing.T, e *testEnv, batchID, membershipID int64, amount, method, key string) {
	t.Helper()
	w := e.postForm(t, "/admin/treasury/batches/"+itoa(batchID)+"/entries", url.Values{
		"membership_id":   {itoa(membershipID)},
		"amount":          {amount},
		"method":          {method},
		"received_on":     {"2026-07-05"},
		"paid_through":    {"2026-12-31"},
		"idempotency_key": {key},
	})
	require.Equal(t, http.StatusSeeOther, w.Code)
}

// TestBatchGridShowsServerTotals proves the reconciliation panel a treasurer
// counts the cash tin against comes from the server.
func TestBatchGridShowsServerTotals(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	a := seedMember(t, e, "Alpha Member", "W3AAA", "2020-12-31")
	b := seedMember(t, e, "Bravo Member", "W3BBB", "2020-12-31")
	batchID := openBatch(t, e, "Meeting night")

	addRow(t, e, batchID, a, "40.00", "cash", "row-1")
	addRow(t, e, batchID, b, "100.00", "check", "row-2")

	w := e.get(t, "/admin/treasury/batches/"+itoa(batchID))
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	assert.Contains(t, body, "What this batch adds up to")
	assert.Contains(t, body, "$40.00")
	assert.Contains(t, body, "$100.00")
	assert.Contains(t, body, "$140.00", "the server's net total")
	assert.Contains(t, body, "These totals are calculated by the server")
	assert.Contains(t, body, "Alpha Member")
}

// TestDraftGridChangesNoStanding is the isolation proof at the UI level.
func TestDraftGridChangesNoStanding(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	m := seedMember(t, e, "Draft Member", "W3DFT", "")
	batchID := openBatch(t, e, "Draft only")
	addRow(t, e, batchID, m, "40.00", "cash", "row-1")

	var payments, coverage int
	require.NoError(t, e.h.db.QueryRow(`SELECT count(*) FROM payments`).Scan(&payments))
	require.NoError(t, e.h.db.QueryRow(`SELECT count(*) FROM coverage_events`).Scan(&coverage))
	assert.Zero(t, payments, "a draft row is not a payment")
	assert.Zero(t, coverage)

	w := e.get(t, "/admin/treasury/memberships/"+itoa(m)+"/payment")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Dues paid through: not recorded")
}

// TestBatchPostRequiresConfirmationAndCurrentVersion proves the two guards the
// posting form carries.
func TestBatchPostRequiresConfirmationAndCurrentVersion(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	m := seedMember(t, e, "Posting Member", "W3PST", "2020-12-31")
	batchID := openBatch(t, e, "To post")
	addRow(t, e, batchID, m, "40.00", "cash", "row-1")

	t.Run("without the confirmation box", func(t *testing.T) {
		w := e.postForm(t, "/admin/treasury/batches/"+itoa(batchID)+"/post", url.Values{
			"version":         {batchVersion(t, e, batchID)},
			"idempotency_key": {"post-1"},
		})
		assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
		assert.Contains(t, w.Body.String(), "Tick the confirmation box")

		var payments int
		require.NoError(t, e.h.db.QueryRow(`SELECT count(*) FROM payments`).Scan(&payments))
		assert.Zero(t, payments)
	})

	t.Run("with a stale version", func(t *testing.T) {
		w := e.postForm(t, "/admin/treasury/batches/"+itoa(batchID)+"/post", url.Values{
			"version": {"999"}, "confirm": {"yes"}, "idempotency_key": {"post-2"},
		})
		assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
		assert.Contains(t, w.Body.String(), "Someone else changed this batch")

		var payments int
		require.NoError(t, e.h.db.QueryRow(`SELECT count(*) FROM payments`).Scan(&payments))
		assert.Zero(t, payments, "a stale post writes nothing")
	})

	t.Run("posting moves the standing exactly once", func(t *testing.T) {
		w := e.postForm(t, "/admin/treasury/batches/"+itoa(batchID)+"/post", url.Values{
			"version": {batchVersion(t, e, batchID)}, "confirm": {"yes"},
			"idempotency_key": {"post-3"},
		})
		require.Equal(t, http.StatusSeeOther, w.Code)

		var payments int
		require.NoError(t, e.h.db.QueryRow(`SELECT count(*) FROM payments`).Scan(&payments))
		assert.Equal(t, 1, payments)

		var paidThrough string
		require.NoError(t, e.h.db.QueryRow(`
			SELECT paid_through FROM coverage_events WHERE membership_id = ?
			 ORDER BY id DESC LIMIT 1`, m).Scan(&paidThrough))
		assert.Equal(t, "2026-12-31", paidThrough)
	})
}

// TestEmptyBatchCannotPost proves posting nothing is refused.
func TestEmptyBatchCannotPost(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	batchID := openBatch(t, e, "Empty")

	w := e.postForm(t, "/admin/treasury/batches/"+itoa(batchID)+"/post", url.Values{
		"version": {batchVersion(t, e, batchID)}, "confirm": {"yes"},
		"idempotency_key": {"post-1"},
	})
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Contains(t, w.Body.String(), "There is nothing to post")
}

// TestTerminalBatchCannotBeEdited proves a posted batch refuses every grid
// action, with a message rather than a stack trace.
func TestTerminalBatchCannotBeEdited(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	m := seedMember(t, e, "Terminal Member", "W3TRM", "2020-12-31")
	batchID := openBatch(t, e, "To post")
	addRow(t, e, batchID, m, "40.00", "cash", "row-1")

	var entryID, entryVersion int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT id, version FROM payment_batch_entries WHERE batch_id = ?`, batchID).
		Scan(&entryID, &entryVersion))

	require.Equal(t, http.StatusSeeOther,
		e.postForm(t, "/admin/treasury/batches/"+itoa(batchID)+"/post", url.Values{
			"version": {batchVersion(t, e, batchID)}, "confirm": {"yes"},
			"idempotency_key": {"post-1"},
		}).Code)

	for _, tc := range []struct {
		name   string
		target string
		form   url.Values
	}{
		{"add a row", "/admin/treasury/batches/" + itoa(batchID) + "/entries", url.Values{
			"membership_id": {itoa(m)}, "amount": {"10.00"}, "method": {"cash"},
			"received_on": {"2026-07-05"}, "paid_through": {"2026-12-31"},
		}},
		{"remove a row", "/admin/treasury/batches/" + itoa(batchID) + "/entries/" + itoa(entryID) + "/delete",
			url.Values{"version": {itoa(entryVersion)}}},
		{"change defaults", "/admin/treasury/batches/" + itoa(batchID) + "/defaults",
			url.Values{"label": {"Renamed"}, "version": {batchVersion(t, e, batchID)}}},
		{"abandon", "/admin/treasury/batches/" + itoa(batchID) + "/abandon",
			url.Values{"reason": {"Too late"}, "version": {batchVersion(t, e, batchID)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := e.postForm(t, tc.target, tc.form)
			assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
			assert.Contains(t, w.Body.String(), "already been posted or abandoned")
		})
	}
}

// TestPostedReviewShowsCorrectionsPlainly proves the review page keeps the
// original visible and explains what happened in an officer's words.
func TestPostedReviewShowsCorrectionsPlainly(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	m := seedMember(t, e, "Corrected Member", "W3COR", "2020-12-31")
	batchID := openBatch(t, e, "Meeting night")
	addRow(t, e, batchID, m, "400.00", "check", "row-1")
	require.Equal(t, http.StatusSeeOther,
		e.postForm(t, "/admin/treasury/batches/"+itoa(batchID)+"/post", url.Values{
			"version": {batchVersion(t, e, batchID)}, "confirm": {"yes"},
			"idempotency_key": {"post-1"},
		}).Code)

	var paymentID int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT id FROM payments WHERE batch_id = ?`, batchID).Scan(&paymentID))

	w := e.postForm(t, "/admin/treasury/payments/"+itoa(paymentID)+"/correct", url.Values{
		"amount": {"40.00"}, "method": {"check"}, "received_on": {"2026-07-05"},
		"paid_through": {"2026-12-31"}, "reason": {"Typed 400 instead of 40"},
		"revision": {"0"}, "idempotency_key": {"fix-1"},
	})
	require.Equal(t, http.StatusSeeOther, w.Code)

	w = e.get(t, "/admin/treasury/batches/"+itoa(batchID))
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	assert.Contains(t, body, "<s>$400.00</s>", "the corrected entry is struck through, not hidden")
	assert.Contains(t, body, "(corrected)")
	assert.Contains(t, body, "$40.00", "and the entry now in force is shown")
	assert.Contains(t, body, "What happened to this batch")
	assert.Contains(t, body, "Typed 400 instead of 40", "in the treasurer's own words")
	assert.Contains(t, body, "/receipt", "with a printable receipt link")

	// Newest first: the correction is the first line of the history.
	correctionAt := indexOf(body, "Changed Corrected Member")
	openedAt := indexOf(body, "Opened the batch")
	assert.Less(t, correctionAt, openedAt, "the newest event is listed first")
}

// TestCorrectionDialogSpeaksPlainly proves the dialog explains the consequence
// without ledger jargon and keeps the reason mandatory.
func TestCorrectionDialogSpeaksPlainly(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	m := seedMember(t, e, "Dialog Member", "W3DLG", "2020-12-31")
	batchID := openBatch(t, e, "Meeting night")
	addRow(t, e, batchID, m, "400.00", "check", "row-1")
	require.Equal(t, http.StatusSeeOther,
		e.postForm(t, "/admin/treasury/batches/"+itoa(batchID)+"/post", url.Values{
			"version": {batchVersion(t, e, batchID)}, "confirm": {"yes"},
			"idempotency_key": {"post-1"},
		}).Code)

	var paymentID int64
	require.NoError(t, e.h.db.QueryRow(
		`SELECT id FROM payments WHERE batch_id = ?`, batchID).Scan(&paymentID))

	w := e.get(t, "/admin/treasury/payments/"+itoa(paymentID)+"/correct")
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	assert.Contains(t, body, `role="dialog"`)
	assert.Contains(t, body, `aria-modal="true"`)
	assert.Contains(t, body, "The original entry of $400.00 stays in the records")
	assert.Contains(t, body, "Changing the amount on its own")
	assert.Contains(t, body, `name="reason"`)
	assert.Contains(t, body, "required")

	// No ledger vocabulary in the prose an officer reads. "idempotency_key" is
	// deliberately not checked: it is a hidden field name, never displayed.
	//
	// The scan runs over the visible prose rather than the whole document. The
	// dialog carries a hidden idempotency key built from the clock, so scanning
	// the raw HTML for "412" failed on every run whose nanosecond timestamp
	// happened to contain those digits (bcars-portal-1hl).
	prose := visibleProse(body)
	for _, jargon := range []string{"supersed", "entry_kind", "coverage_event", "ETag", "412"} {
		assert.NotContains(t, prose, jargon, "the dialog avoids %q", jargon)
	}

	t.Run("a missing reason is refused with a plain message", func(t *testing.T) {
		w := e.postForm(t, "/admin/treasury/payments/"+itoa(paymentID)+"/correct", url.Values{
			"amount": {"40.00"}, "method": {"check"}, "received_on": {"2026-07-05"},
			"paid_through": {"2026-12-31"}, "reason": {"  "},
			"revision": {"0"}, "idempotency_key": {"fix-1"},
		})
		assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
		assert.Contains(t, w.Body.String(), "Say why")

		var corrections int
		require.NoError(t, e.h.db.QueryRow(`SELECT count(*) FROM payment_corrections`).Scan(&corrections))
		assert.Zero(t, corrections)
	})

	t.Run("a stale revision is refused", func(t *testing.T) {
		w := e.postForm(t, "/admin/treasury/payments/"+itoa(paymentID)+"/correct", url.Values{
			"amount": {"40.00"}, "method": {"check"}, "received_on": {"2026-07-05"},
			"paid_through": {"2026-12-31"}, "reason": {"Stale"},
			"revision": {"7"}, "idempotency_key": {"fix-2"},
		})
		assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
		assert.Contains(t, w.Body.String(), "Someone else changed this batch")
	})

	t.Run("correcting the same entry twice is refused", func(t *testing.T) {
		require.Equal(t, http.StatusSeeOther,
			e.postForm(t, "/admin/treasury/payments/"+itoa(paymentID)+"/correct", url.Values{
				"amount": {"40.00"}, "method": {"check"}, "received_on": {"2026-07-05"},
				"paid_through": {"2026-12-31"}, "reason": {"First fix"},
				"revision": {"0"}, "idempotency_key": {"fix-3"},
			}).Code)

		w := e.postForm(t, "/admin/treasury/payments/"+itoa(paymentID)+"/correct", url.Values{
			"amount": {"50.00"}, "method": {"check"}, "received_on": {"2026-07-05"},
			"paid_through": {"2026-12-31"}, "reason": {"Second fix"},
			"revision": {"1"}, "idempotency_key": {"fix-4"},
		})
		assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
		assert.Contains(t, w.Body.String(), "already been corrected")
	})
}

// TestGridWorksWithoutJavaScript proves every action is an ordinary form.
func TestGridWorksWithoutJavaScript(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	m := seedMember(t, e, "Plain Member", "W3PLN", "2020-12-31")
	batchID := openBatch(t, e, "Plain forms")
	addRow(t, e, batchID, m, "40.00", "cash", "row-1")

	w := e.get(t, "/admin/treasury/batches/"+itoa(batchID)+"?member=Plain")
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	// Every mutation is a POST form with an action, not a fetch call.
	for _, action := range []string{
		"/entries", "/post", "/abandon", "/defaults",
	} {
		assert.Contains(t, body, `method="post"`)
		assert.Contains(t, body, action)
	}
	assert.NotContains(t, body, "hx-post", "the grid does not depend on htmx to work")
	assert.NotContains(t, body, "hx-get")
	// The add-row control is a real form with a real action, so it submits with
	// scripting off. Go's template escaper strips comments from <script>, so the
	// property is asserted on the markup rather than on a comment.
	assert.Contains(t, body, `<form action="/admin/treasury/batches/`+itoa(batchID)+`/entries" method="post"`)
}

// TestBatchPagesDenyNonTreasurers proves the whole surface is treasury-only.
func TestBatchPagesDenyNonTreasurers(t *testing.T) {
	e := setupHandlerWithRoles(t, "member")
	_, err := e.h.db.Exec(`
		INSERT INTO payment_batches (id, label, opened_by, opened_at)
		VALUES (1, 'Treasury only', 1, '2026-01-15T00:00:00.000Z')`)
	require.NoError(t, err)

	for _, target := range []string{
		"/admin/treasury/batches",
		"/admin/treasury/batches/1",
		"/admin/treasury/payments/1/correct",
		"/admin/treasury/payments/1/receipt",
	} {
		t.Run(target, func(t *testing.T) {
			w := e.get(t, target)
			assert.Equal(t, http.StatusForbidden, w.Code)
			assert.NotContains(t, w.Body.String(), "Treasury only")
		})
	}

	w := e.postForm(t, "/admin/treasury/batches", url.Values{"label": {"Nope"}})
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// indexOf reports where a substring starts, or a large number when absent, so
// ordering assertions fail loudly rather than passing on -1.
func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return 1 << 30
}

// visibleProse strips tags from rendered HTML so a wording assertion reads what
// an officer reads. Attribute values -- hidden field contents above all -- are
// not prose, and matching against them makes a copy test fail for reasons that
// have nothing to do with copy.
func visibleProse(html string) string {
	var out strings.Builder
	depth := 0
	for _, r := range html {
		switch {
		case r == '<':
			depth++
		case r == '>':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			out.WriteRune(r)
		}
	}
	return out.String()
}

// The amount box's placeholder is read as "the usual amount". It used to be a
// literal 40.00 in the template, which no configuration produced: a club whose
// annual dues are $20 was shown $40 in every empty box on the grid and on the
// add-a-row form (bcars-portal-i95).

// setDuesRate configures the club's annual rate for a year, the way the rate
// screen does.
func setDuesRate(t *testing.T, e *testEnv, year int64, cents int64) {
	t.Helper()
	_, err := e.h.db.Exec(
		`INSERT INTO dues_rates (year, amount_cents, set_by, set_at)
		 VALUES (?, ?, 1, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		 ON CONFLICT(year) DO UPDATE SET amount_cents = excluded.amount_cents`,
		year, cents)
	require.NoError(t, err)
}

func TestTheAmountPlaceholderIsTheConfiguredDuesRate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cents int64
		want  string
	}{
		{"a twenty dollar club", 2000, "20.00"},
		{"a thirty-five dollar club", 3500, "35.00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setupHandlerWithRoles(t, "treasurer")
			setDuesRate(t, e, int64(time.Now().UTC().Year()), tc.cents)
			seedMember(t, e, "Plain Member", "W3PLN", "2020-12-31")
			id := openBatch(t, e, "Meeting night")

			// Searched, because the amount box only exists once the treasurer
			// has found the member they are recording a payment for.
			body := e.body(t, "GET", "/admin/treasury/batches/"+itoa(id)+"?member=Plain", "")

			assert.Containsf(t, body, `placeholder="`+tc.want+`"`,
				"the grid should suggest the configured rate")
			assert.NotContains(t, body, `placeholder="40.00"`,
				"the grid is still advertising a rate nobody configured")
		})
	}
}

func TestNoDuesRateMeansNoSuggestion(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	seedMember(t, e, "Plain Member", "W3PLN", "2020-12-31")
	id := openBatch(t, e, "Meeting night")

	body := e.body(t, "GET", "/admin/treasury/batches/"+itoa(id)+"?member=Plain", "")

	assert.Contains(t, body, `placeholder=""`,
		"an unconfigured club should be asked for the amount, not given a made-up one")
	assert.NotContains(t, body, `placeholder="40.00"`)
}

// TestTheSuggestionFollowsTheCoverageYear covers the December case: a batch made
// up at the last meeting of the year collects next year's dues, and next year's
// rate is the one the treasurer is taking.
func TestTheSuggestionFollowsTheCoverageYear(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	seedMember(t, e, "Plain Member", "W3PLN", "2020-12-31")

	thisYear := int64(time.Now().UTC().Year())
	setDuesRate(t, e, thisYear, 2000)
	setDuesRate(t, e, thisYear+1, 2500)

	id := openBatch(t, e, "December meeting")
	w := e.postForm(t, "/admin/treasury/batches/"+itoa(id)+"/defaults", url.Values{
		"version":              {batchVersion(t, e, id)},
		"label":                {"December meeting"},
		"default_paid_through": {strconv.FormatInt(thisYear+1, 10) + "-12-31"},
	})
	require.Equal(t, http.StatusSeeOther, w.Code)

	body := e.body(t, "GET", "/admin/treasury/batches/"+itoa(id)+"?member=Plain", "")
	assert.Contains(t, body, `placeholder="25.00"`,
		"a batch covering next year should suggest next year's rate")
}

// Four findings from a treasurer's walkthrough of the payment grid, all on the
// same screen (bcars-portal-yec).

// TestAddingFromTheSheetReturnsToThatRow is the one worth a test rather than an
// eyeball: working down a printed sheet used to bounce the treasurer to the
// foot of the page after every single entry, because every add redirected to
// the add-a-row card at the bottom.
func TestAddingFromTheSheetReturnsToThatRow(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	m := seedMember(t, e, "Plain Member", "W3PLN", "2020-12-31")
	batchID := openBatch(t, e, "Meeting night")

	w := e.postForm(t, "/admin/treasury/batches/"+itoa(batchID)+"/entries", url.Values{
		"membership_id":   {itoa(m)},
		"amount":          {"20.00"},
		"method":          {"cash"},
		"received_on":     {"2026-08-15"},
		"paid_through":    {"2026-12-31"},
		"idempotency_key": {"sheet-row-1"},
		"return_to":       {"member-" + itoa(m)},
	})

	require.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t,
		"/admin/treasury/batches/"+itoa(batchID)+"#member-"+itoa(m),
		w.Header().Get("Location"),
		"the treasurer should land back on the row they just entered")
}

// TestAddingFromTheSearchStillReturnsToTheSearch keeps the other half: a row
// added by searching has no grid row to go back to.
func TestAddingFromTheSearchStillReturnsToTheSearch(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	m := seedMember(t, e, "Plain Member", "W3PLN", "2020-12-31")
	batchID := openBatch(t, e, "Meeting night")

	w := e.postForm(t, "/admin/treasury/batches/"+itoa(batchID)+"/entries", url.Values{
		"membership_id":   {itoa(m)},
		"amount":          {"20.00"},
		"method":          {"cash"},
		"received_on":     {"2026-08-15"},
		"paid_through":    {"2026-12-31"},
		"idempotency_key": {"search-row-1"},
	})

	require.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/admin/treasury/batches/"+itoa(batchID)+"#add-row",
		w.Header().Get("Location"))
}

// TestTheReturnAnchorIsNotReflected: the anchor comes from the form, so it is
// validated rather than echoed into the redirect.
func TestTheReturnAnchorIsNotReflected(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	m := seedMember(t, e, "Plain Member", "W3PLN", "2020-12-31")
	batchID := openBatch(t, e, "Meeting night")

	for i, bad := range []string{
		"member-notanumber",
		"add-row\nLocation: https://example.invalid",
		"../../admin/members",
		"https://example.invalid",
	} {
		w := e.postForm(t, "/admin/treasury/batches/"+itoa(batchID)+"/entries", url.Values{
			"membership_id":   {itoa(m)},
			"amount":          {"20.00"},
			"method":          {"cash"},
			"received_on":     {"2026-08-15"},
			"paid_through":    {"2026-12-31"},
			"idempotency_key": {"bad-anchor-" + itoa(int64(i))},
			"return_to":       {bad},
		})
		loc := w.Header().Get("Location")
		assert.Equalf(t, "/admin/treasury/batches/"+itoa(batchID)+"#add-row", loc,
			"a return_to of %q must fall back to the add-row anchor", bad)
	}
}

// TestTheGridLabelsBothDatesVisibly covers the complaint directly: the two date
// boxes carried aria-labels only, so a screen reader was told which was which
// and a sighted treasurer was not.
func TestTheGridLabelsBothDatesVisibly(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	seedMember(t, e, "Plain Member", "W3PLN", "2020-12-31")
	batchID := openBatch(t, e, "Meeting night")

	body := e.body(t, "GET", "/admin/treasury/batches/"+itoa(batchID)+"?member=Plain", "")

	// Visible text, not an attribute: an aria-label satisfies neither the
	// complaint nor a sighted user.
	assert.Contains(t, body, "<span>Received</span>")
	assert.Contains(t, body, "<span>Dues paid through</span>")
}

// TestTheDefaultsBlockComesBeforeTheRowsItSeeds: a default discovered after
// entering half a sheet has already missed the rows it was meant to fill in.
func TestTheDefaultsBlockComesBeforeTheRowsItSeeds(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	seedMember(t, e, "Plain Member", "W3PLN", "2020-12-31")
	batchID := openBatch(t, e, "Meeting night")

	body := e.body(t, "GET", "/admin/treasury/batches/"+itoa(batchID)+"?member=Plain", "")

	defaults := strings.Index(body, "Defaults for new rows")
	addRow := strings.Index(body, "Add a row")
	require.NotEqual(t, -1, defaults)
	require.NotEqual(t, -1, addRow)
	assert.Less(t, defaults, addRow,
		"the defaults must appear before the rows they prefill")
}

// TestTheTotalsAreVisibleAtTheAttestation: the checkbox asks the treasurer to
// confirm they counted the cash against the totals, which were a page away.
func TestTheTotalsAreVisibleAtTheAttestation(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	m := seedMember(t, e, "Plain Member", "W3PLN", "2020-12-31")
	batchID := openBatch(t, e, "Meeting night")
	addRow(t, e, batchID, m, "20.00", "cash", "row-1")

	body := e.body(t, "GET", "/admin/treasury/batches/"+itoa(batchID), "")

	attestation := strings.Index(body, "I have counted the cash")
	require.NotEqual(t, -1, attestation)

	// The totals table is rendered twice: once at the top, once beside the
	// attestation. The second must be between the post heading and the
	// checkbox, or the two still cannot be read together.
	postHeading := strings.Index(body, "Post this batch")
	require.NotEqual(t, -1, postHeading)
	totalsNearby := strings.Index(body[postHeading:attestation], "All rows")
	assert.NotEqualf(t, -1, totalsNearby,
		"the totals must be on screen where the treasurer attests to having counted them")
}
