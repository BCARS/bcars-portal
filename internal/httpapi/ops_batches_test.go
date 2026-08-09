package httpapi_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// batchResponse is the shared shape of every batch response body.
type batchResponse struct {
	ID     int64  `json:"id"`
	Label  string `json:"label"`
	State  string `json:"state"`
	Totals struct {
		EntryCount      int64 `json:"entry_count"`
		CashCount       int64 `json:"cash_count"`
		CashTotalCents  int64 `json:"cash_total_cents"`
		CheckCount      int64 `json:"check_count"`
		CheckTotalCents int64 `json:"check_total_cents"`
		OtherTotalCents int64 `json:"other_total_cents"`
		NetTotalCents   int64 `json:"net_total_cents"`
	} `json:"totals"`
	DefaultAmountCents int64 `json:"default_amount_cents"`
	Version            int64 `json:"version"`
	Entries            []struct {
		ID          int64  `json:"id"`
		Sequence    int64  `json:"sequence"`
		AmountCents int64  `json:"amount_cents"`
		Method      string `json:"method"`
		PaidThrough string `json:"paid_through"`
	} `json:"entries"`
}

type entryResponse struct {
	Entry struct {
		ID          int64  `json:"id"`
		Sequence    int64  `json:"sequence"`
		AmountCents int64  `json:"amount_cents"`
		Version     int64  `json:"version"`
		PaidThrough string `json:"paid_through"`
	} `json:"entry"`
	Batch batchResponse `json:"batch"`
}

// openBatchViaAPI opens a batch and returns it with its ETag.
func openBatchViaAPI(t *testing.T, env *authzEnv, cookie *http.Cookie, label string) (batchResponse, string) {
	t.Helper()
	resp := env.do(t, http.MethodPost, "/api/v1/payment-batches", cookie,
		fmt.Sprintf(`{"label":%q,"default_amount_cents":4000,"default_paid_through":"2026-12-31"}`, label))
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var b batchResponse
	decodeBody(t, resp, &b)
	return b, resp.Header.Get("ETag")
}

// addEntryViaAPI adds one draft row.
func addEntryViaAPI(t *testing.T, env *authzEnv, cookie *http.Cookie, batchID, membershipID, cents int64, method string) entryResponse {
	t.Helper()
	resp := env.do(t, http.MethodPost,
		fmt.Sprintf("/api/v1/payment-batches/%d/entries", batchID), cookie,
		fmt.Sprintf(`{"membership_id":%d,"amount_cents":%d,"method":%q,
			"received_on":"2026-01-15","paid_through":"2026-12-31"}`,
			membershipID, cents, method))
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var e entryResponse
	decodeBody(t, resp, &e)
	return e
}

// TestBatchDraftIsolationOverHTTP proves that driving the real API through a
// full draft leaves the ledger and the member's standing untouched.
func TestBatchDraftIsolationOverHTTP(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	membershipID := seedMemberWithCoverage(t, env, "Draft Subject", "")

	b, _ := openBatchViaAPI(t, env, cookie, "January dues")
	addEntryViaAPI(t, env, cookie, b.ID, membershipID, 4000, "cash")
	addEntryViaAPI(t, env, cookie, b.ID, membershipID, 10000, "check")

	var payments, coverage int
	require.NoError(t, env.db.QueryRow(`SELECT count(*) FROM payments`).Scan(&payments))
	require.NoError(t, env.db.QueryRow(`SELECT count(*) FROM coverage_events`).Scan(&coverage))
	assert.Zero(t, payments)
	assert.Zero(t, coverage)

	resp := env.do(t, http.MethodGet,
		fmt.Sprintf("/api/v1/memberships/%d/dues-standing?as_of=2026-07-01", membershipID), cookie, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var standing struct {
		Status string `json:"status"`
	}
	decodeBody(t, resp, &standing)
	assert.Equal(t, "unknown", standing.Status, "drafting must not change dues standing")
}

// TestBatchTotalsAreServerCalculated proves the batch detail reports the
// per-method breakdown the treasurer reconciles against.
func TestBatchTotalsAreServerCalculated(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	m1 := seedMemberWithCoverage(t, env, "Totals One", "")
	m2 := seedMemberWithCoverage(t, env, "Totals Two", "")

	b, _ := openBatchViaAPI(t, env, cookie, "Mixed methods")
	addEntryViaAPI(t, env, cookie, b.ID, m1, 4000, "cash")
	addEntryViaAPI(t, env, cookie, b.ID, m2, 2500, "cash")
	addEntryViaAPI(t, env, cookie, b.ID, m1, 10000, "check")
	addEntryViaAPI(t, env, cookie, b.ID, m2, 750, "other")

	resp := env.do(t, http.MethodGet, fmt.Sprintf("/api/v1/payment-batches/%d", b.ID), cookie, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got batchResponse
	decodeBody(t, resp, &got)
	assert.Equal(t, int64(4), got.Totals.EntryCount)
	assert.Equal(t, int64(2), got.Totals.CashCount)
	assert.Equal(t, int64(6500), got.Totals.CashTotalCents)
	assert.Equal(t, int64(1), got.Totals.CheckCount)
	assert.Equal(t, int64(10000), got.Totals.CheckTotalCents)
	assert.Equal(t, int64(750), got.Totals.OtherTotalCents)
	assert.Equal(t, int64(17250), got.Totals.NetTotalCents)
	assert.Len(t, got.Entries, 4)
	assert.NotEmpty(t, resp.Header.Get("ETag"))
}

// TestBatchVersionMovesWithEntries proves the batch ETag changes whenever its
// rows do, which is what lets a later post reject a stale client.
func TestBatchVersionMovesWithEntries(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	m := seedMemberWithCoverage(t, env, "Version Subject", "")

	b, openETag := openBatchViaAPI(t, env, cookie, "Version tracking")
	e := addEntryViaAPI(t, env, cookie, b.ID, m, 4000, "cash")
	assert.Greater(t, e.Batch.Version, b.Version, "adding a row moves the batch version")

	resp := doWithHeaders(t, env, http.MethodPut,
		fmt.Sprintf("/api/v1/payment-batches/%d/entries/%d", b.ID, e.Entry.ID), cookie,
		fmt.Sprintf(`{"membership_id":%d,"amount_cents":5000,"method":"check","reference":"1042",
			"received_on":"2026-01-15","paid_through":"2026-12-31"}`, m),
		map[string]string{"If-Match": fmt.Sprintf(`"%d"`, e.Entry.Version)})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var edited entryResponse
	decodeBody(t, resp, &edited)
	assert.Equal(t, int64(5000), edited.Entry.AmountCents)
	assert.Greater(t, edited.Batch.Version, e.Batch.Version)

	resp = doWithHeaders(t, env, http.MethodDelete,
		fmt.Sprintf("/api/v1/payment-batches/%d/entries/%d", b.ID, e.Entry.ID), cookie, "",
		map[string]string{"If-Match": fmt.Sprintf(`"%d"`, edited.Entry.Version)})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var afterDelete batchResponse
	decodeBody(t, resp, &afterDelete)
	assert.Zero(t, afterDelete.Totals.EntryCount)
	assert.Greater(t, afterDelete.Version, edited.Batch.Version)
	assert.NotEqual(t, openETag, resp.Header.Get("ETag"))
}

func TestBatchEntryStaleIfMatchIsRejected(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	m := seedMemberWithCoverage(t, env, "Stale Subject", "")
	b, _ := openBatchViaAPI(t, env, cookie, "Stale writes")
	e := addEntryViaAPI(t, env, cookie, b.ID, m, 4000, "cash")

	body := fmt.Sprintf(`{"membership_id":%d,"amount_cents":9000,"method":"cash",
		"received_on":"2026-01-15","paid_through":"2026-12-31"}`, m)
	path := fmt.Sprintf("/api/v1/payment-batches/%d/entries/%d", b.ID, e.Entry.ID)

	resp := doWithHeaders(t, env, http.MethodPut, path, cookie, body,
		map[string]string{"If-Match": `"99"`})
	assert.Equal(t, http.StatusPreconditionFailed, resp.StatusCode)

	t.Run("If-Match is required", func(t *testing.T) {
		resp := env.do(t, http.MethodPut, path, cookie, body)
		assert.Equal(t, http.StatusPreconditionRequired, resp.StatusCode)
	})
}

// TestBatchEntryIdempotentRetry proves a retried add returns the original row.
func TestBatchEntryIdempotentRetry(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	m := seedMemberWithCoverage(t, env, "Retry Subject", "")
	b, _ := openBatchViaAPI(t, env, cookie, "Retry safety")

	body := fmt.Sprintf(`{"membership_id":%d,"amount_cents":4000,"method":"cash",
		"received_on":"2026-01-15","paid_through":"2026-12-31"}`, m)
	path := fmt.Sprintf("/api/v1/payment-batches/%d/entries", b.ID)
	headers := map[string]string{"Idempotency-Key": "row-1"}

	resp := doWithHeaders(t, env, http.MethodPost, path, cookie, body, headers)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var first entryResponse
	decodeBody(t, resp, &first)

	resp = doWithHeaders(t, env, http.MethodPost, path, cookie, body, headers)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var second entryResponse
	decodeBody(t, resp, &second)

	assert.Equal(t, first.Entry.ID, second.Entry.ID)
	assert.Equal(t, int64(1), second.Batch.Totals.EntryCount, "a retry adds no second row")

	t.Run("the same key with a different body is refused", func(t *testing.T) {
		other := fmt.Sprintf(`{"membership_id":%d,"amount_cents":9999,"method":"cash",
			"received_on":"2026-01-15","paid_through":"2026-12-31"}`, m)
		resp := doWithHeaders(t, env, http.MethodPost, path, cookie, other, headers)
		// The existing API contract for a reused key is 422, not 409.
		assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	})
}

// TestTerminalBatchRejectsMutationOverHTTP proves an abandoned batch refuses
// every write with a conflict rather than partially applying one.
func TestTerminalBatchRejectsMutationOverHTTP(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	m := seedMemberWithCoverage(t, env, "Terminal Subject", "")
	b, _ := openBatchViaAPI(t, env, cookie, "To abandon")
	e := addEntryViaAPI(t, env, cookie, b.ID, m, 4000, "cash")

	resp := doWithHeaders(t, env, http.MethodPost,
		fmt.Sprintf("/api/v1/payment-batches/%d/abandon", b.ID), cookie,
		`{"reason":"Duplicate of the paper sheet"}`,
		map[string]string{"If-Match": fmt.Sprintf(`"%d"`, e.Batch.Version)})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var abandoned batchResponse
	decodeBody(t, resp, &abandoned)
	assert.Equal(t, "abandoned", abandoned.State)
	assert.Len(t, abandoned.Entries, 1, "the abandoned rows stay readable")

	entryBody := fmt.Sprintf(`{"membership_id":%d,"amount_cents":100,"method":"cash",
		"received_on":"2026-01-15","paid_through":"2026-12-31"}`, m)

	resp = env.do(t, http.MethodPost, fmt.Sprintf("/api/v1/payment-batches/%d/entries", b.ID),
		cookie, entryBody)
	assert.Equal(t, http.StatusConflict, resp.StatusCode, "add")

	resp = doWithHeaders(t, env, http.MethodPut,
		fmt.Sprintf("/api/v1/payment-batches/%d/entries/%d", b.ID, e.Entry.ID), cookie, entryBody,
		map[string]string{"If-Match": fmt.Sprintf(`"%d"`, e.Entry.Version)})
	assert.Equal(t, http.StatusConflict, resp.StatusCode, "edit")

	resp = doWithHeaders(t, env, http.MethodDelete,
		fmt.Sprintf("/api/v1/payment-batches/%d/entries/%d", b.ID, e.Entry.ID), cookie, "",
		map[string]string{"If-Match": fmt.Sprintf(`"%d"`, e.Entry.Version)})
	assert.Equal(t, http.StatusConflict, resp.StatusCode, "remove")

	resp = doWithHeaders(t, env, http.MethodPatch,
		fmt.Sprintf("/api/v1/payment-batches/%d", b.ID), cookie, `{"label":"Renamed"}`,
		map[string]string{"If-Match": fmt.Sprintf(`"%d"`, abandoned.Version)})
	assert.Equal(t, http.StatusConflict, resp.StatusCode, "defaults")

	events := env.auditEvents(t, "payment.batch.abandon")
	require.Len(t, events, 1)
	assert.Equal(t, "success", events[0].Outcome)
	assert.Equal(t, "payment_batch", events[0].ResourceKind.String)
}

// TestBatchesDeniedToNonTreasurers proves a president can neither list, read,
// nor change batches — and in particular never receives row details.
func TestBatchesDeniedToNonTreasurers(t *testing.T) {
	env := setupAuthzTest(t, "president")
	cookie := env.signIn(t)
	m := seedMemberWithCoverage(t, env, "Hidden Subject", "")

	// Seed a batch with a row directly, since the president cannot create one.
	_, err := env.db.Exec(`
		INSERT INTO payment_batches (id, label, opened_by, opened_at)
		VALUES (1, 'Treasury only', 1, '2026-01-15T00:00:00.000Z')`)
	require.NoError(t, err)
	_, err = env.db.Exec(`
		INSERT INTO payment_batch_entries (batch_id, membership_id, sequence, amount_cents,
			method, reference, received_on, paid_through, treasurer_note)
		VALUES (1, ?, 1, 4000, 'check', '1042', '2026-01-15', '2026-12-31', 'Paid at the meeting')`, m)
	require.NoError(t, err)

	for _, tc := range []struct{ name, method, path, body string }{
		{"list", http.MethodGet, "/api/v1/payment-batches", ""},
		{"read", http.MethodGet, "/api/v1/payment-batches/1", ""},
		{"open", http.MethodPost, "/api/v1/payment-batches", `{"label":"Nope"}`},
		{"add row", http.MethodPost, "/api/v1/payment-batches/1/entries",
			fmt.Sprintf(`{"membership_id":%d,"amount_cents":100,"method":"cash","received_on":"2026-01-15","paid_through":"2026-12-31"}`, m)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := env.do(t, tc.method, tc.path, cookie, tc.body)
			require.Equal(t, http.StatusForbidden, resp.StatusCode)

			raw := readAll(t, resp)
			assert.NotContains(t, raw, "1042", "a denied response must leak no reference")
			assert.NotContains(t, raw, "Paid at the meeting", "nor any treasurer note")
			assert.NotContains(t, raw, "4000", "nor any amount")
		})
	}
}

func TestBatchEntryValidationOverHTTP(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	m := seedMemberWithCoverage(t, env, "Invalid Subject", "")
	b, _ := openBatchViaAPI(t, env, cookie, "Validation")
	path := fmt.Sprintf("/api/v1/payment-batches/%d/entries", b.ID)

	for _, tc := range []struct{ name, body string }{
		{"zero amount", fmt.Sprintf(`{"membership_id":%d,"amount_cents":0,"method":"cash","received_on":"2026-01-15","paid_through":"2026-12-31"}`, m)},
		{"unknown method", fmt.Sprintf(`{"membership_id":%d,"amount_cents":4000,"method":"venmo","received_on":"2026-01-15","paid_through":"2026-12-31"}`, m)},
		{"bad date", fmt.Sprintf(`{"membership_id":%d,"amount_cents":4000,"method":"cash","received_on":"01/15/2026","paid_through":"2026-12-31"}`, m)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := env.do(t, http.MethodPost, path, cookie, tc.body)
			assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
		})
	}

	t.Run("unknown membership", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, path, cookie,
			`{"membership_id":999,"amount_cents":4000,"method":"cash","received_on":"2026-01-15","paid_through":"2026-12-31"}`)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("an off-cycle paid-through is accepted", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, path, cookie,
			fmt.Sprintf(`{"membership_id":%d,"amount_cents":4000,"method":"cash","received_on":"2026-01-15","paid_through":"2026-06-30"}`, m))
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		var e entryResponse
		decodeBody(t, resp, &e)
		assert.Equal(t, "2026-06-30", e.Entry.PaidThrough)
	})
}
