package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedMember creates a person and approved membership, optionally with a
// coverage decision, and returns the membership id.
func seedMember(t *testing.T, e *testEnv, name, callSign, paidThrough string) int64 {
	t.Helper()
	d := e.h.db

	res, err := d.Exec(`INSERT INTO persons (display_name, sort_name, call_sign) VALUES (?, ?, ?)`,
		name, name, nullIfEmpty(callSign))
	require.NoError(t, err)
	personID, err := res.LastInsertId()
	require.NoError(t, err)

	res, err = d.Exec(
		`INSERT INTO memberships (person_id, base_type, lifecycle) VALUES (?, 'full', 'approved')`,
		personID)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)

	if paidThrough != "" {
		_, err = d.Exec(`
			INSERT INTO coverage_events (membership_id, paid_through, reason_kind, decided_at)
			VALUES (?, ?, 'adjustment', '2026-01-01T00:00:00.000Z')`, id, paidThrough)
		require.NoError(t, err)
	}
	return id
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (e *testEnv) get(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, e.authedRequest("GET", target))
	return w
}

func (e *testEnv) postForm(t *testing.T, target string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(e.cookie)
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	return w
}

// TestTreasuryHomeCountsEveryStanding proves the summary a treasurer opens to
// find who needs chasing.
func TestTreasuryHomeCountsEveryStanding(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	seedMember(t, e, "Expired Member", "W3EXP", "2020-12-31")
	seedMember(t, e, "Unknown Member", "W3UNK", "")
	seedMember(t, e, "Current Member", "W3CUR", "2099-12-31")

	w := e.get(t, "/admin/treasury")
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	assert.Contains(t, body, "Dues expired")
	assert.Contains(t, body, "No dues recorded")
	assert.Contains(t, body, "Dues waived")
	assert.Contains(t, body, "/admin/treasury/standing?status=expired")
}

func TestTreasuryStandingListFiltersAndPages(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	seedMember(t, e, "Expired Member", "W3EXP", "2020-12-31")
	seedMember(t, e, "Current Member", "W3CUR", "2099-12-31")

	w := e.get(t, "/admin/treasury/standing?status=expired")
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "Expired Member")
	assert.NotContains(t, body, "Current Member")
	assert.Contains(t, body, "Dues paid through", "the copy uses plain language")

	t.Run("search narrows the list", func(t *testing.T) {
		w := e.get(t, "/admin/treasury/standing?q=W3CUR")
		require.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "Current Member")
		assert.NotContains(t, w.Body.String(), "Expired Member")
	})

	t.Run("an unknown status is refused", func(t *testing.T) {
		w := e.get(t, "/admin/treasury/standing?status=lapsed")
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

// TestPaymentFormSeparatesMoneyFromCoverage proves the screen keeps the two
// facts visibly apart, which is the whole point of the ledger design.
func TestPaymentFormSeparatesMoneyFromCoverage(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	id := seedMember(t, e, "Paying Member", "W3PAY", "2020-12-31")

	w := e.get(t, "/admin/treasury/memberships/"+itoa(id)+"/payment")
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	assert.Contains(t, body, "Money received")
	assert.Contains(t, body, "Dues paid through")
	assert.Contains(t, body,
		"The amount received and the date dues are paid through are recorded separately.")
	assert.Contains(t, body, `name="amount"`)
	assert.Contains(t, body, `name="paid_through"`)
	assert.Contains(t, body, `name="idempotency_key"`)
	assert.Contains(t, body, "Suggestions", "suggestions are offered")
	assert.Contains(t, body, "type any date instead", "and are optional")

	// A plain form with no JavaScript: no fetch, no hx- attributes on this page.
	assert.NotContains(t, body, "hx-post")
	assert.Contains(t, body, `method="post"`)
}

// TestPaymentSubmitRecordsThroughTheSharedOperation drives the real form and
// then checks the ledger, so no alternate persistence path can hide here.
func TestPaymentSubmitRecordsThroughTheSharedOperation(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	id := seedMember(t, e, "Paying Member", "W3PAY", "2020-12-31")

	form := url.Values{
		"amount":              {"40.00"},
		"method":              {"check"},
		"reference":           {"1042"},
		"received_on":         {"2026-07-05"},
		"received_by_officer": {"K3ABC"},
		"paid_through":        {"2026-12-31"},
		"note":                {"Paid at the meeting"},
		"idempotency_key":     {"web-test-1"},
	}
	w := e.postForm(t, "/admin/treasury/memberships/"+itoa(id)+"/payment", form)
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "Saved.")
	assert.Contains(t, body, "Recorded $40.00 and set Dues paid through to 2026-12-31.")

	var amount int64
	var method, reference, receipt string
	require.NoError(t, e.h.db.QueryRow(`
		SELECT amount_cents, method, reference, receipt_code FROM payments WHERE membership_id = ?`,
		id).Scan(&amount, &method, &reference, &receipt))
	assert.Equal(t, int64(4000), amount, "40.00 becomes 4000 cents with no float error")
	assert.Equal(t, "check", method)
	assert.Equal(t, "1042", reference)
	assert.NotEmpty(t, receipt)

	var paidThrough, reasonKind string
	require.NoError(t, e.h.db.QueryRow(`
		SELECT paid_through, reason_kind FROM coverage_events
		 WHERE membership_id = ? ORDER BY id DESC LIMIT 1`, id).Scan(&paidThrough, &reasonKind))
	assert.Equal(t, "2026-12-31", paidThrough)
	assert.Equal(t, "payment", reasonKind)

	t.Run("resubmitting the same form records nothing new", func(t *testing.T) {
		w := e.postForm(t, "/admin/treasury/memberships/"+itoa(id)+"/payment", form)
		require.Equal(t, http.StatusOK, w.Code)

		var payments int
		require.NoError(t, e.h.db.QueryRow(
			`SELECT count(*) FROM payments WHERE membership_id = ?`, id).Scan(&payments))
		assert.Equal(t, 1, payments, "the idempotency key makes a double submit safe")
	})
}

// TestPaymentAmountParsing proves the typed amount reaches the ledger exactly,
// and that a bad one is reported without losing the treasurer's entry.
func TestPaymentAmountParsing(t *testing.T) {
	for _, tc := range []struct {
		in    string
		cents int64
	}{
		{"40", 4000},
		{"40.00", 4000},
		{"40.5", 4050},
		{"40.55", 4055},
		{"$40.00", 4000},
		{"1,040.00", 104000},
		{" 40 ", 4000},
		{".50", 50},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseAmountCents(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.cents, got)
		})
	}

	for _, bad := range []string{"", "abc", "-5", "0", "40.001", "4 0"} {
		t.Run("rejects "+bad, func(t *testing.T) {
			_, err := parseAmountCents(bad)
			assert.Error(t, err)
		})
	}
}

func TestPaymentValidationKeepsTheEntry(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	id := seedMember(t, e, "Paying Member", "W3PAY", "")

	form := url.Values{
		"amount":          {"not a number"},
		"method":          {"cash"},
		"received_on":     {"2026-07-05"},
		"paid_through":    {"2026-12-31"},
		"reference":       {"1042"},
		"idempotency_key": {"web-test-1"},
	}
	w := e.postForm(t, "/admin/treasury/memberships/"+itoa(id)+"/payment", form)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "Enter the amount as a number")
	assert.Contains(t, body, "1042", "what the treasurer typed is still on the form")

	var payments int
	require.NoError(t, e.h.db.QueryRow(`SELECT count(*) FROM payments`).Scan(&payments))
	assert.Zero(t, payments, "a rejected form records nothing")

	t.Run("a bad date is reported the same way", func(t *testing.T) {
		form.Set("amount", "40.00")
		form.Set("paid_through", "31/12/2026")
		w := e.postForm(t, "/admin/treasury/memberships/"+itoa(id)+"/payment", form)
		assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
		assert.Contains(t, w.Body.String(), "YYYY-MM-DD")
	})
}

// TestTreasuryPagesDenyNonTreasurers proves navigation and direct requests both
// stop, and that nothing leaks in the refusal.
func TestTreasuryPagesDenyNonTreasurers(t *testing.T) {
	e := setupHandlerWithRoles(t, "member")
	id := seedMember(t, e, "Hidden Member", "W3HID", "2020-12-31")

	for _, target := range []string{
		"/admin/treasury",
		"/admin/treasury/standing",
		"/admin/treasury/standing?status=expired",
		"/admin/treasury/memberships/" + itoa(id) + "/payment",
	} {
		t.Run(target, func(t *testing.T) {
			w := e.get(t, target)
			require.Equal(t, http.StatusForbidden, w.Code)
			assert.NotContains(t, w.Body.String(), "Hidden Member")
		})
	}

	w := e.postForm(t, "/admin/treasury/memberships/"+itoa(id)+"/payment",
		url.Values{"amount": {"40.00"}, "paid_through": {"2026-12-31"}})
	assert.Equal(t, http.StatusForbidden, w.Code)

	var payments int
	require.NoError(t, e.h.db.QueryRow(`SELECT count(*) FROM payments`).Scan(&payments))
	assert.Zero(t, payments)
}

// TestPaymentHistoryNeedsPaymentRead proves an officer who may record a payment
// but not read the books does not see prior amounts on the way past.
func TestPaymentHistoryNeedsPaymentRead(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	id := seedMember(t, e, "Historic Member", "W3HIS", "2020-12-31")

	form := url.Values{
		"amount": {"40.00"}, "method": {"check"}, "reference": {"REF-SECRET"},
		"received_on": {"2026-07-05"}, "paid_through": {"2026-12-31"},
		"idempotency_key": {"web-test-1"},
	}
	require.Equal(t, http.StatusOK,
		e.postForm(t, "/admin/treasury/memberships/"+itoa(id)+"/payment", form).Code)

	w := e.get(t, "/admin/treasury/memberships/"+itoa(id)+"/payment")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Previous payments")
	assert.Contains(t, w.Body.String(), "REF-SECRET", "a treasurer sees the reference")

	// Strip payment.read and the history section disappears.
	_, err := e.h.db.Exec(
		`DELETE FROM role_capabilities WHERE role_code = 'treasurer' AND capability_code = 'payment.read'`)
	require.NoError(t, err)

	w = e.get(t, "/admin/treasury/memberships/"+itoa(id)+"/payment")
	require.Equal(t, http.StatusOK, w.Code)
	assert.NotContains(t, w.Body.String(), "Previous payments")
	assert.NotContains(t, w.Body.String(), "REF-SECRET")
}

// TestSessionCookieIsSameSiteLax records the CSRF defence these forms rely on.
// There is no token: a browser will not attach a SameSite=Lax cookie to a
// cross-site POST, so a form posted from another origin arrives unauthenticated
// and is redirected to login rather than acted on.
func TestSessionCookieIsSameSiteLax(t *testing.T) {
	e := setupHandlerWithRoles(t, "treasurer")
	id := seedMember(t, e, "Target Member", "W3TGT", "2020-12-31")

	assert.Equal(t, http.SameSiteLaxMode, e.cookie.SameSite)
	assert.True(t, e.cookie.HttpOnly)

	// The same POST without the session cookie, which is what a cross-site
	// form submission looks like at the server.
	req := httptest.NewRequest("POST", "/admin/treasury/memberships/"+itoa(id)+"/payment",
		strings.NewReader(url.Values{
			"amount": {"40.00"}, "method": {"cash"},
			"received_on": {"2026-07-05"}, "paid_through": {"2026-12-31"},
		}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/login", w.Header().Get("Location"))

	var payments int
	require.NoError(t, e.h.db.QueryRow(`SELECT count(*) FROM payments`).Scan(&payments))
	assert.Zero(t, payments, "an unauthenticated post records nothing")
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
