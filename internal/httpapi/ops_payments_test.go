package httpapi_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/httpapi"
)

type postResultResponse struct {
	Batch    batchResponse `json:"batch"`
	Payments []struct {
		ID           int64  `json:"id"`
		MembershipID int64  `json:"membership_id"`
		BatchID      int64  `json:"batch_id"`
		AmountCents  int64  `json:"amount_cents"`
		Method       string `json:"method"`
		Reference    string `json:"reference"`
		ReceiptCode  string `json:"receipt_code"`
		EntryKind    string `json:"entry_kind"`
	} `json:"payments"`
	Coverage []struct {
		ID          int64  `json:"id"`
		PaidThrough string `json:"paid_through"`
		ReasonKind  string `json:"reason_kind"`
		PaymentID   int64  `json:"payment_id"`
	} `json:"coverage_events"`
}

// standingOf reads a member's derived standing through the API.
func standingOf(t *testing.T, env *authzEnv, cookie *http.Cookie, membershipID int64) string {
	t.Helper()
	resp := env.do(t, http.MethodGet,
		fmt.Sprintf("/api/v1/memberships/%d/dues-standing?as_of=2026-07-01", membershipID), cookie, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body struct {
		Status string `json:"status"`
	}
	decodeBody(t, resp, &body)
	return body.Status
}

// postBatch posts a batch with the given precondition and key.
func postBatch(t *testing.T, env *authzEnv, cookie *http.Cookie, batchID, version int64, key string, confirm bool) *http.Response {
	t.Helper()
	return doWithHeaders(t, env, http.MethodPost,
		fmt.Sprintf("/api/v1/payment-batches/%d/post", batchID), cookie,
		`{}`,
		map[string]string{
			"If-Match":            fmt.Sprintf(`"%d"`, version),
			"Idempotency-Key":     key,
			httpapi.ConfirmHeader: fmt.Sprintf("%t", confirm),
		})
}

// TestPostBatchMovesStandingOnce drives the whole treasurer flow through the
// real router: draft, verify nothing moved, post, verify it moved exactly once.
func TestPostBatchMovesStandingOnce(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	m1 := seedMemberWithCoverage(t, env, "Post One", "")
	m2 := seedMemberWithCoverage(t, env, "Post Two", "")

	b, _ := openBatchViaAPI(t, env, cookie, "Meeting night")
	addEntryViaAPI(t, env, cookie, b.ID, m1, 4000, "cash")
	last := addEntryViaAPI(t, env, cookie, b.ID, m2, 10000, "check")

	assert.Equal(t, "unknown", standingOf(t, env, cookie, m1), "an open batch changes nothing")

	resp := postBatch(t, env, cookie, b.ID, last.Batch.Version, "post-1", true)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result postResultResponse
	decodeBody(t, resp, &result)
	assert.Equal(t, "posted", result.Batch.State)
	require.Len(t, result.Payments, 2)
	require.Len(t, result.Coverage, 2)
	assert.Equal(t, int64(14000), result.Batch.Totals.NetTotalCents)

	for _, p := range result.Payments {
		assert.Equal(t, "original", p.EntryKind)
		assert.NotEmpty(t, p.ReceiptCode)
		assert.Equal(t, b.ID, p.BatchID)
	}
	for _, c := range result.Coverage {
		assert.Equal(t, "payment", c.ReasonKind)
		assert.NotZero(t, c.PaymentID)
	}

	assert.Equal(t, "current", standingOf(t, env, cookie, m1))
	assert.Equal(t, "current", standingOf(t, env, cookie, m2))

	var payments int64
	require.NoError(t, env.db.QueryRow(
		`SELECT count(*) FROM payments WHERE batch_id = ?`, b.ID).Scan(&payments))
	assert.Equal(t, int64(2), payments, "exactly one payment per entry")

	events := env.auditEvents(t, "payment.batch.post")
	require.Len(t, events, 1)
	assert.Equal(t, "success", events[0].Outcome)
	assert.Equal(t, "payment_batch", events[0].ResourceKind.String)
	assert.Equal(t, b.ID, events[0].ResourceID.Int64)
}

// TestPostBatchIsIdempotentOverHTTP proves a retried post returns the original
// result and does not post the money twice.
func TestPostBatchIsIdempotentOverHTTP(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	m := seedMemberWithCoverage(t, env, "Retry Payer", "")

	b, _ := openBatchViaAPI(t, env, cookie, "Retry safety")
	e := addEntryViaAPI(t, env, cookie, b.ID, m, 4000, "cash")

	resp := postBatch(t, env, cookie, b.ID, e.Batch.Version, "post-1", true)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var first postResultResponse
	decodeBody(t, resp, &first)

	resp = postBatch(t, env, cookie, b.ID, e.Batch.Version, "post-1", true)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var second postResultResponse
	decodeBody(t, resp, &second)

	require.Len(t, second.Payments, 1)
	assert.Equal(t, first.Payments[0].ID, second.Payments[0].ID)
	assert.Equal(t, first.Payments[0].ReceiptCode, second.Payments[0].ReceiptCode)

	var payments, coverage int
	require.NoError(t, env.db.QueryRow(`SELECT count(*) FROM payments`).Scan(&payments))
	require.NoError(t, env.db.QueryRow(`SELECT count(*) FROM coverage_events`).Scan(&coverage))
	assert.Equal(t, 1, payments)
	assert.Equal(t, 1, coverage)
}

// TestPostBatchPreconditions covers every way a post is refused, and asserts
// each one writes nothing.
func TestPostBatchPreconditions(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	m := seedMemberWithCoverage(t, env, "Precondition Payer", "")

	b, _ := openBatchViaAPI(t, env, cookie, "Preconditions")
	e := addEntryViaAPI(t, env, cookie, b.ID, m, 4000, "cash")
	path := fmt.Sprintf("/api/v1/payment-batches/%d/post", b.ID)

	t.Run("If-Match is required", func(t *testing.T) {
		resp := doWithHeaders(t, env, http.MethodPost, path, cookie, `{}`,
			map[string]string{"Idempotency-Key": "k1"})
		assert.Equal(t, http.StatusPreconditionRequired, resp.StatusCode)
	})

	t.Run("a stale If-Match is refused", func(t *testing.T) {
		resp := postBatch(t, env, cookie, b.ID, e.Batch.Version+5, "k2", true)
		assert.Equal(t, http.StatusPreconditionFailed, resp.StatusCode)
	})

	t.Run("confirmation is required", func(t *testing.T) {
		resp := postBatch(t, env, cookie, b.ID, e.Batch.Version, "k3", false)
		assert.Equal(t, http.StatusPreconditionRequired, resp.StatusCode)
	})

	t.Run("an idempotency key is required", func(t *testing.T) {
		resp := doWithHeaders(t, env, http.MethodPost, path, cookie, `{}`,
			map[string]string{"If-Match": fmt.Sprintf(`"%d"`, e.Batch.Version)})
		assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	})

	t.Run("an empty batch is refused", func(t *testing.T) {
		empty, _ := openBatchViaAPI(t, env, cookie, "Nothing in it")
		resp := postBatch(t, env, cookie, empty.ID, empty.Version, "k4", true)
		assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	})

	t.Run("nothing was written by any refused post", func(t *testing.T) {
		var payments, coverage int
		require.NoError(t, env.db.QueryRow(`SELECT count(*) FROM payments`).Scan(&payments))
		require.NoError(t, env.db.QueryRow(`SELECT count(*) FROM coverage_events`).Scan(&coverage))
		assert.Zero(t, payments)
		assert.Zero(t, coverage)
		assert.Equal(t, "unknown", standingOf(t, env, cookie, m))
	})

	t.Run("a posted batch cannot be posted again", func(t *testing.T) {
		resp := postBatch(t, env, cookie, b.ID, e.Batch.Version, "k5", true)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var posted postResultResponse
		decodeBody(t, resp, &posted)

		resp = postBatch(t, env, cookie, b.ID, posted.Batch.Version, "k6", true)
		assert.Equal(t, http.StatusConflict, resp.StatusCode)
	})
}

// TestSinglePaymentOverHTTP proves the convenience endpoint returns the same
// shapes and lands in the ledger identically.
func TestSinglePaymentOverHTTP(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	m := seedMemberWithCoverage(t, env, "Single Payer", "")

	body := fmt.Sprintf(`{"membership_id":%d,"amount_cents":4000,"method":"check",
		"reference":"1042","received_on":"2026-01-15","paid_through":"2026-12-31"}`, m)

	resp := doWithHeaders(t, env, http.MethodPost, "/api/v1/payments", cookie, body,
		map[string]string{"Idempotency-Key": "single-1"})
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var result postResultResponse
	decodeBody(t, resp, &result)
	assert.Equal(t, "posted", result.Batch.State, "the server-created batch is posted too")
	require.Len(t, result.Payments, 1)
	require.Len(t, result.Coverage, 1)
	assert.Equal(t, "1042", result.Payments[0].Reference)
	assert.Equal(t, result.Batch.ID, result.Payments[0].BatchID)
	assert.Equal(t, "2026-12-31", result.Coverage[0].PaidThrough)
	assert.Equal(t, "current", standingOf(t, env, cookie, m))

	t.Run("a retry posts no second payment", func(t *testing.T) {
		resp := doWithHeaders(t, env, http.MethodPost, "/api/v1/payments", cookie, body,
			map[string]string{"Idempotency-Key": "single-1"})
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		var again postResultResponse
		decodeBody(t, resp, &again)
		assert.Equal(t, result.Payments[0].ID, again.Payments[0].ID)

		var payments int
		require.NoError(t, env.db.QueryRow(`SELECT count(*) FROM payments`).Scan(&payments))
		assert.Equal(t, 1, payments)
	})

	t.Run("confirmation is required", func(t *testing.T) {
		unconfirmed := fmt.Sprintf(`{"membership_id":%d,"amount_cents":4000,"method":"cash",
			"received_on":"2026-01-15","paid_through":"2026-12-31"}`, m)
		resp := doWithHeaders(t, env, http.MethodPost, "/api/v1/payments", cookie, unconfirmed,
			map[string]string{
				"Idempotency-Key":     "single-2",
				httpapi.ConfirmHeader: "false",
			})
		assert.Equal(t, http.StatusPreconditionRequired, resp.StatusCode)
	})

	t.Run("the payment is audited", func(t *testing.T) {
		events := env.auditEvents(t, "payment.create")
		require.NotEmpty(t, events)
		assert.Equal(t, "success", events[0].Outcome)
		assert.Equal(t, "payment", events[0].ResourceKind.String)
	})
}

// TestPostingDeniedToNonTreasurers proves an executive officer cannot post, and
// that the refusal leaks no ledger detail.
func TestPostingDeniedToNonTreasurers(t *testing.T) {
	env := setupAuthzTest(t, "president")
	cookie := env.signIn(t)
	m := seedMemberWithCoverage(t, env, "Protected Payer", "")

	_, err := env.db.Exec(`
		INSERT INTO payment_batches (id, label, opened_by, opened_at)
		VALUES (1, 'Treasury only', 1, '2026-01-15T00:00:00.000Z')`)
	require.NoError(t, err)
	_, err = env.db.Exec(`
		INSERT INTO payment_batch_entries (batch_id, membership_id, sequence, amount_cents,
			method, received_on, paid_through)
		VALUES (1, ?, 1, 4000, 'cash', '2026-01-15', '2026-12-31')`, m)
	require.NoError(t, err)

	resp := postBatch(t, env, cookie, 1, 1, "k1", true)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	resp = doWithHeaders(t, env, http.MethodPost, "/api/v1/payments", cookie,
		fmt.Sprintf(`{"membership_id":%d,"amount_cents":4000,"method":"cash","received_on":"2026-01-15","paid_through":"2026-12-31"}`, m),
		map[string]string{"Idempotency-Key": "k2"})
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	var payments int
	require.NoError(t, env.db.QueryRow(`SELECT count(*) FROM payments`).Scan(&payments))
	assert.Zero(t, payments, "a denied post writes nothing")
	assert.Equal(t, "unknown", standingOf(t, env, cookie, m),
		"and the president can still read safe standing")
}
