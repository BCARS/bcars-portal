package httpapi_test

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLedgerAndExportsOverHTTP drives the treasurer's reporting surface through
// the real router.
func TestLedgerAndExportsOverHTTP(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	m := seedMemberWithCoverage(t, env, "Ledger Person", "")

	b, _ := openBatchViaAPI(t, env, cookie, "January")
	e := addEntryViaAPI(t, env, cookie, b.ID, m, 40000, "check")
	resp := postBatch(t, env, cookie, b.ID, e.Batch.Version, "post-1", true)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var posted postResultResponse
	decodeBody(t, resp, &posted)
	paymentID := posted.Payments[0].ID

	t.Run("ledger lists the entry", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, "/api/v1/ledger-entries", cookie, "")
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var page struct {
			Data []struct {
				PaymentID   int64  `json:"payment_id"`
				AmountCents int64  `json:"amount_cents"`
				DisplayName string `json:"display_name"`
				Superseded  bool   `json:"superseded"`
			} `json:"data"`
		}
		decodeBody(t, resp, &page)
		require.Len(t, page.Data, 1)
		assert.Equal(t, int64(40000), page.Data[0].AmountCents)
		assert.Equal(t, "Ledger Person", page.Data[0].DisplayName)
		assert.False(t, page.Data[0].Superseded)
	})

	t.Run("receipt is printable", func(t *testing.T) {
		resp := env.do(t, http.MethodGet,
			fmt.Sprintf("/api/v1/payments/%d/receipt", paymentID), cookie, "")
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var receipt struct {
			ReceiptCode string `json:"receipt_code"`
			AmountCents int64  `json:"amount_cents"`
			PaidThrough string `json:"paid_through"`
			Superseded  bool   `json:"superseded"`
		}
		decodeBody(t, resp, &receipt)
		assert.NotEmpty(t, receipt.ReceiptCode)
		assert.Equal(t, int64(40000), receipt.AmountCents)
		assert.Equal(t, "2026-12-31", receipt.PaidThrough)
		assert.False(t, receipt.Superseded)
	})

	t.Run("batch activity reads plainly", func(t *testing.T) {
		resp := env.do(t, http.MethodGet,
			fmt.Sprintf("/api/v1/payment-batches/%d/activity", b.ID), cookie, "")
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body struct {
			Activity []struct {
				Kind    string `json:"kind"`
				Summary string `json:"summary"`
			} `json:"activity"`
		}
		decodeBody(t, resp, &body)
		require.Len(t, body.Activity, 2)
		assert.Equal(t, "opened", body.Activity[0].Kind)
		assert.Equal(t, "posted", body.Activity[1].Kind)
		assert.Contains(t, body.Activity[1].Summary, "$400.00")
	})

	t.Run("export states its filters and is decodable CSV", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, "/api/v1/exports/treasury", cookie,
			`{"method":"check"}`)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var export struct {
			RowCount       int    `json:"row_count"`
			Format         string `json:"format"`
			Data           string `json:"data"`
			AppliedFilters []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"applied_filters"`
		}
		decodeBody(t, resp, &export)
		assert.Equal(t, 1, export.RowCount)
		assert.Equal(t, "csv", export.Format)

		raw, err := base64.StdEncoding.DecodeString(export.Data)
		require.NoError(t, err)
		csv := string(raw)
		assert.Contains(t, csv, "400.00")
		assert.Contains(t, csv, "# filter.method,check")

		var names []string
		for _, f := range export.AppliedFilters {
			names = append(names, f.Name)
		}
		assert.Contains(t, names, "method")
		assert.Contains(t, names, "view")

		events := env.auditEvents(t, "payment.export")
		assert.NotEmpty(t, events)
	})

	t.Run("batch export works", func(t *testing.T) {
		resp := env.do(t, http.MethodPost,
			fmt.Sprintf("/api/v1/payment-batches/%d/export", b.ID), cookie, "")
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var export struct {
			Filename string `json:"filename"`
			RowCount int    `json:"row_count"`
		}
		decodeBody(t, resp, &export)
		assert.Equal(t, 1, export.RowCount)
		assert.Contains(t, export.Filename, "bcars-batch-")
	})
}

// TestTreasuryDeniedToNonTreasurers proves an officer with member and dues
// access cannot infer amounts, references, receipts, or notes through any of
// the treasury surfaces, including through counts or error bodies.
func TestTreasuryDeniedToNonTreasurers(t *testing.T) {
	env := setupAuthzTest(t, "president")
	cookie := env.signIn(t)
	m := seedMemberWithCoverage(t, env, "Protected Person", "")

	_, err := env.db.Exec(`
		INSERT INTO payment_batches (id, label, state, opened_by, opened_at, posted_by, posted_at)
		VALUES (1, 'Treasury only', 'posted', 1, '2026-01-15T00:00:00.000Z', 1, '2026-01-15T00:00:00.000Z')`)
	require.NoError(t, err)
	_, err = env.db.Exec(`
		INSERT INTO payments (id, membership_id, batch_id, amount_cents, method, reference,
			received_on, entered_by, entered_at, receipt_code, entry_kind, treasurer_note)
		VALUES (1, ?, 1, 40000, 'check', '1042', '2026-01-15', 1,
			'2026-01-15T00:00:00.000Z', 'RCPT-000001-001', 'original', 'Paid at the meeting')`, m)
	require.NoError(t, err)

	for _, tc := range []struct{ name, method, path, body string }{
		{"ledger list", http.MethodGet, "/api/v1/ledger-entries", ""},
		{"ledger filtered by member", http.MethodGet,
			fmt.Sprintf("/api/v1/ledger-entries?membership_id=%d", m), ""},
		{"receipt", http.MethodGet, "/api/v1/payments/1/receipt", ""},
		{"batch activity", http.MethodGet, "/api/v1/payment-batches/1/activity", ""},
		{"treasury export", http.MethodPost, "/api/v1/exports/treasury", `{}`},
		{"batch export", http.MethodPost, "/api/v1/payment-batches/1/export", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := env.do(t, tc.method, tc.path, cookie, tc.body)
			require.Equal(t, http.StatusForbidden, resp.StatusCode)

			body := readAll(t, resp)
			for _, secret := range []string{
				"40000", "400.00", "1042", "RCPT-000001-001", "Paid at the meeting",
			} {
				assert.NotContains(t, body, secret,
					"a denied %s must not leak %q", tc.name, secret)
			}
			// A row count would let a caller infer how much activity exists.
			assert.NotContains(t, strings.ToLower(body), "row_count")
			assert.NotContains(t, strings.ToLower(body), "\"data\"")
		})
	}

	t.Run("but safe dues standing still works", func(t *testing.T) {
		assert.Equal(t, "unknown", standingOf(t, env, cookie, m))
	})
}
