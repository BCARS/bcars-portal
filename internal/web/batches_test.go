package web

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"

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
	for _, jargon := range []string{"supersed", "entry_kind", "coverage_event", "ETag", "412"} {
		assert.NotContains(t, body, jargon, "the dialog avoids %q", jargon)
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
