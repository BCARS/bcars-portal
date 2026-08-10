package httpapi_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/httpapi"
)

type paymentChainResponse struct {
	Effective struct {
		ID          int64  `json:"id"`
		AmountCents int64  `json:"amount_cents"`
		Method      string `json:"method"`
		EntryKind   string `json:"entry_kind"`
		ReceiptCode string `json:"receipt_code"`
	} `json:"effective_payment"`
	Chain []struct {
		ID          int64  `json:"id"`
		AmountCents int64  `json:"amount_cents"`
		EntryKind   string `json:"entry_kind"`
	} `json:"chain"`
	Corrections []struct {
		ID                   int64  `json:"id"`
		OriginalPaymentID    int64  `json:"original_payment_id"`
		ReversalPaymentID    int64  `json:"reversal_payment_id"`
		ReplacementPaymentID int64  `json:"replacement_payment_id"`
		Reason               string `json:"reason"`
		CorrectedByUserID    int64  `json:"corrected_by_user_id"`
	} `json:"corrections"`
	Coverage *struct {
		ID          int64  `json:"id"`
		PaidThrough string `json:"paid_through"`
		ReasonKind  string `json:"reason_kind"`
	} `json:"coverage_event"`
	PaidThrough string `json:"paid_through"`
	Standing    *struct {
		Status      string `json:"status"`
		PaidThrough string `json:"paid_through"`
	} `json:"standing"`
	Batch        batchResponse `json:"batch"`
	LedgerTotals struct {
		PaymentCount  int64 `json:"payment_count"`
		NetTotalCents int64 `json:"net_total_cents"`
	} `json:"ledger_totals"`
	Revision int64 `json:"revision"`
}

// correctPayment posts a correction with the given precondition.
func correctPayment(t *testing.T, env *authzEnv, cookie *http.Cookie, paymentID, revision int64, body, key string) *http.Response {
	t.Helper()
	return doWithHeaders(t, env, http.MethodPost,
		fmt.Sprintf("/api/v1/payments/%d/corrections", paymentID), cookie, body,
		map[string]string{
			"If-Match":        fmt.Sprintf(`"%d"`, revision),
			"Idempotency-Key": key,
		})
}

// TestCorrect400To40OverHTTP is the epic's reference case driven through the
// real router: a $400 mistype in a $510 batch becomes $40 and the net ledger
// total becomes $150, with the original untouched.
func TestCorrect400To40OverHTTP(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	wrong := seedMemberWithCoverage(t, env, "Mistyped Person", "")
	other := seedMemberWithCoverage(t, env, "Correct Person", "")

	b, _ := openBatchViaAPI(t, env, cookie, "Meeting night")
	addEntryViaAPI(t, env, cookie, b.ID, wrong, 40000, "check")
	addEntryViaAPI(t, env, cookie, b.ID, other, 7000, "cash")
	last := addEntryViaAPI(t, env, cookie, b.ID, other, 4000, "cash")

	resp := postBatch(t, env, cookie, b.ID, last.Batch.Version, "post-1", true)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var posted postResultResponse
	decodeBody(t, resp, &posted)
	require.Equal(t, int64(51000), posted.Batch.Totals.NetTotalCents)

	original := posted.Payments[0]
	require.Equal(t, int64(40000), original.AmountCents)

	// Read the payment first: its ETag is the chain revision to send back.
	resp = env.do(t, http.MethodGet, fmt.Sprintf("/api/v1/payments/%d", original.ID), cookie, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, `"0"`, resp.Header.Get("ETag"), "an uncorrected chain is revision 0")

	resp = correctPayment(t, env, cookie, original.ID, 0,
		`{"amount_cents":4000,"method":"check","received_on":"2026-01-15",
		  "paid_through":"2026-12-31","reason":"Typed 400 instead of 40"}`,
		"correct-1")
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var result paymentChainResponse
	decodeBody(t, resp, &result)

	assert.Equal(t, int64(15000), result.LedgerTotals.NetTotalCents,
		"a $510 batch corrected from $400 to $40 nets $150")
	assert.Equal(t, int64(4000), result.Effective.AmountCents)
	assert.Equal(t, "replacement", result.Effective.EntryKind)
	assert.Equal(t, `"1"`, resp.Header.Get("ETag"))

	require.Len(t, result.Chain, 3)
	assert.Equal(t, int64(40000), result.Chain[0].AmountCents)
	assert.Equal(t, int64(-40000), result.Chain[1].AmountCents)
	assert.Equal(t, "reversal", result.Chain[1].EntryKind)
	assert.Equal(t, int64(4000), result.Chain[2].AmountCents)

	require.Len(t, result.Corrections, 1)
	assert.Equal(t, "Typed 400 instead of 40", result.Corrections[0].Reason)
	assert.Equal(t, original.ID, result.Corrections[0].OriginalPaymentID)
	assert.NotZero(t, result.Corrections[0].CorrectedByUserID)

	// The batch's draft entries still record what was typed on the night.
	assert.Equal(t, int64(51000), result.Batch.Totals.NetTotalCents)

	var stored int64
	require.NoError(t, env.db.QueryRow(
		`SELECT amount_cents FROM payments WHERE id = ?`, original.ID).Scan(&stored))
	assert.Equal(t, int64(40000), stored, "the original is never overwritten")

	events := env.auditEvents(t, "payment.correct")
	require.Len(t, events, 1)
	assert.Equal(t, "success", events[0].Outcome)
	assert.Equal(t, "payment", events[0].ResourceKind.String)
}

// TestCorrectAmountOnlyLeavesCoverageAlone proves a money-only correction does
// not move the member's coverage.
func TestCorrectAmountOnlyLeavesCoverageAlone(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	m := seedMemberWithCoverage(t, env, "Amount Only Person", "")
	payment := singlePaymentViaAPI(t, env, cookie, m, 40000, "2026-12-31", "single-1")

	var before int
	require.NoError(t, env.db.QueryRow(`SELECT count(*) FROM coverage_events`).Scan(&before))

	resp := correctPayment(t, env, cookie, payment, 0,
		`{"amount_cents":4000,"method":"check","received_on":"2026-01-15",
		  "paid_through":"2026-12-31","reason":"Amount only"}`,
		"correct-1")
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var result paymentChainResponse
	decodeBody(t, resp, &result)
	assert.Nil(t, result.Coverage, "no coverage event when the date is unchanged")
	assert.Equal(t, "2026-12-31", result.PaidThrough)
	require.NotNil(t, result.Standing)
	assert.Equal(t, "2026-12-31", result.Standing.PaidThrough)

	var after int
	require.NoError(t, env.db.QueryRow(`SELECT count(*) FROM coverage_events`).Scan(&after))
	assert.Equal(t, before, after, "coverage history is untouched")
}

// TestCorrectPaidThroughAppendsCoverage proves changing the date supersedes the
// prior decision without erasing it.
func TestCorrectPaidThroughAppendsCoverage(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	m := seedMemberWithCoverage(t, env, "Date Change Person", "")
	payment := singlePaymentViaAPI(t, env, cookie, m, 4000, "2026-12-31", "single-1")

	resp := correctPayment(t, env, cookie, payment, 0,
		`{"amount_cents":8000,"method":"check","received_on":"2026-01-15",
		  "paid_through":"2027-12-31","reason":"Paid for two years"}`,
		"correct-1")
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var result paymentChainResponse
	decodeBody(t, resp, &result)
	require.NotNil(t, result.Coverage)
	assert.Equal(t, "correction", result.Coverage.ReasonKind)
	assert.Equal(t, "2027-12-31", result.Coverage.PaidThrough)
	assert.Equal(t, "2027-12-31", result.PaidThrough)

	resp = env.do(t, http.MethodGet,
		fmt.Sprintf("/api/v1/memberships/%d/coverage-events", m), cookie, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var page struct {
		Data []struct {
			PaidThrough string `json:"paid_through"`
		} `json:"data"`
	}
	decodeBody(t, resp, &page)
	assert.Len(t, page.Data, 2, "the superseded decision stays readable")
}

// TestCorrectPreconditions covers every refusal path and asserts each writes
// nothing.
func TestCorrectPreconditions(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	m := seedMemberWithCoverage(t, env, "Precondition Person", "")
	payment := singlePaymentViaAPI(t, env, cookie, m, 40000, "2026-12-31", "single-1")

	valid := `{"amount_cents":4000,"method":"check","received_on":"2026-01-15",
		"paid_through":"2026-12-31","reason":"Fix"}`
	path := fmt.Sprintf("/api/v1/payments/%d/corrections", payment)

	t.Run("If-Match is required", func(t *testing.T) {
		resp := doWithHeaders(t, env, http.MethodPost, path, cookie, valid,
			map[string]string{"Idempotency-Key": "k1"})
		assert.Equal(t, http.StatusPreconditionRequired, resp.StatusCode)
	})

	t.Run("a stale revision is refused", func(t *testing.T) {
		resp := correctPayment(t, env, cookie, payment, 7, valid, "k2")
		assert.Equal(t, http.StatusPreconditionFailed, resp.StatusCode)
	})

	t.Run("an idempotency key is required", func(t *testing.T) {
		resp := doWithHeaders(t, env, http.MethodPost, path, cookie, valid,
			map[string]string{"If-Match": `"0"`})
		assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	})

	t.Run("confirmation is required", func(t *testing.T) {
		// Stated with the header now, and enforced generically from the
		// declared ConfirmationLevel rather than by this handler.
		resp := doWithHeaders(t, env, http.MethodPost, path, cookie, valid,
			map[string]string{
				"If-Match":            `"0"`,
				"Idempotency-Key":     "k3",
				httpapi.ConfirmHeader: "false",
			})
		assert.Equal(t, http.StatusPreconditionRequired, resp.StatusCode)
	})

	t.Run("a reason is required", func(t *testing.T) {
		reasonless := `{"amount_cents":4000,"method":"check","received_on":"2026-01-15",
			"paid_through":"2026-12-31","reason":""}`
		resp := correctPayment(t, env, cookie, payment, 0, reasonless, "k4")
		assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	})

	t.Run("nothing was written by any refused correction", func(t *testing.T) {
		var corrections, payments int
		require.NoError(t, env.db.QueryRow(`SELECT count(*) FROM payment_corrections`).Scan(&corrections))
		require.NoError(t, env.db.QueryRow(`SELECT count(*) FROM payments`).Scan(&payments))
		assert.Zero(t, corrections)
		assert.Equal(t, 1, payments)
	})

	t.Run("a superseded payment cannot be corrected again", func(t *testing.T) {
		resp := correctPayment(t, env, cookie, payment, 0, valid, "k5")
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		// Same target, now with the current revision: the row itself is stale.
		resp = correctPayment(t, env, cookie, payment, 1, valid, "k6")
		assert.Equal(t, http.StatusConflict, resp.StatusCode)
	})
}

// TestCorrectIsIdempotentOverHTTP proves a retry appends no second pair.
func TestCorrectIsIdempotentOverHTTP(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	m := seedMemberWithCoverage(t, env, "Retry Correction Person", "")
	payment := singlePaymentViaAPI(t, env, cookie, m, 40000, "2026-12-31", "single-1")

	body := `{"amount_cents":4000,"method":"check","received_on":"2026-01-15",
		"paid_through":"2026-12-31","reason":"Typed 400 instead of 40"}`

	resp := correctPayment(t, env, cookie, payment, 0, body, "correct-1")
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var first paymentChainResponse
	decodeBody(t, resp, &first)

	resp = correctPayment(t, env, cookie, payment, 0, body, "correct-1")
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var second paymentChainResponse
	decodeBody(t, resp, &second)

	assert.Equal(t, first.Effective.ID, second.Effective.ID)
	assert.Equal(t, first.LedgerTotals.NetTotalCents, second.LedgerTotals.NetTotalCents)

	var payments, corrections int
	require.NoError(t, env.db.QueryRow(`SELECT count(*) FROM payments`).Scan(&payments))
	require.NoError(t, env.db.QueryRow(`SELECT count(*) FROM payment_corrections`).Scan(&corrections))
	assert.Equal(t, 3, payments)
	assert.Equal(t, 1, corrections)
}

// TestPostedPaymentsHaveNoEditRoute proves the API exposes no way to overwrite
// or remove a posted payment. Correction is the only path.
func TestPostedPaymentsHaveNoEditRoute(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	m := seedMemberWithCoverage(t, env, "Immutable Person", "")
	payment := singlePaymentViaAPI(t, env, cookie, m, 4000, "2026-12-31", "single-1")

	path := fmt.Sprintf("/api/v1/payments/%d", payment)
	for _, method := range []string{http.MethodPatch, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			resp := env.do(t, method, path, cookie, `{"amount_cents":1}`)
			assert.Contains(t,
				[]int{http.StatusNotFound, http.StatusMethodNotAllowed},
				resp.StatusCode,
				"%s on a posted payment must not be routed", method)
		})
	}

	var amount int64
	require.NoError(t, env.db.QueryRow(
		`SELECT amount_cents FROM payments WHERE id = ?`, payment).Scan(&amount))
	assert.Equal(t, int64(4000), amount)
}

// TestCorrectionDeniedWithoutCapability proves posting does not imply
// correcting, and that a denied caller sees no ledger detail.
func TestCorrectionDeniedWithoutCapability(t *testing.T) {
	env := setupAuthzTest(t, "president")
	cookie := env.signIn(t)
	m := seedMemberWithCoverage(t, env, "Guarded Correction Person", "")

	_, err := env.db.Exec(`
		INSERT INTO payment_batches (id, label, state, opened_by, opened_at, posted_by, posted_at)
		VALUES (1, 'Treasury only', 'posted', 1, '2026-01-15T00:00:00.000Z', 1, '2026-01-15T00:00:00.000Z')`)
	require.NoError(t, err)
	_, err = env.db.Exec(`
		INSERT INTO payments (id, membership_id, batch_id, amount_cents, method, reference,
			received_on, entered_by, entered_at, receipt_code, entry_kind)
		VALUES (1, ?, 1, 40000, 'check', '1042', '2026-01-15', 1,
			'2026-01-15T00:00:00.000Z', 'RCPT-000001-001', 'original')`, m)
	require.NoError(t, err)

	resp := env.do(t, http.MethodGet, "/api/v1/payments/1", cookie, "")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.NotContains(t, readAll(t, resp), "1042", "a denied read leaks no reference")

	resp = correctPayment(t, env, cookie, 1, 0,
		`{"amount_cents":4000,"method":"check","received_on":"2026-01-15",
		  "paid_through":"2026-12-31","reason":"Not allowed"}`, "k1")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	var corrections int
	require.NoError(t, env.db.QueryRow(`SELECT count(*) FROM payment_corrections`).Scan(&corrections))
	assert.Zero(t, corrections)
}

// singlePaymentViaAPI posts one payment and returns its id.
func singlePaymentViaAPI(t *testing.T, env *authzEnv, cookie *http.Cookie, membershipID, cents int64, paidThrough, key string) int64 {
	t.Helper()
	resp := doWithHeaders(t, env, http.MethodPost, "/api/v1/payments", cookie,
		fmt.Sprintf(`{"membership_id":%d,"amount_cents":%d,"method":"check",
			"received_on":"2026-01-15","paid_through":%q}`,
			membershipID, cents, paidThrough),
		map[string]string{"Idempotency-Key": key})
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var result postResultResponse
	decodeBody(t, resp, &result)
	require.Len(t, result.Payments, 1)
	return result.Payments[0].ID
}
