package httpapi_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type worksheetRunResponse struct {
	ID           int64  `json:"id"`
	AsOf         string `json:"as_of"`
	FilterKind   string `json:"filter_kind"`
	SortOrder    string `json:"sort_order"`
	IncludeEmail bool   `json:"include_email"`
	IncludePhone bool   `json:"include_phone"`
	GeneratedAt  string `json:"generated_at"`
	RowCount     int64  `json:"row_count"`
}

type worksheetRowResponse struct {
	Ordinal      int64  `json:"ordinal"`
	MembershipID int64  `json:"membership_id"`
	DisplayName  string `json:"display_name"`
	DuesStatus   string `json:"dues_status"`
	PaidThrough  string `json:"paid_through"`
	Email        string `json:"email"`
	EnteredSince bool   `json:"entered_since"`
}

// TestWorksheetLifecycleOverHTTP drives generate, read, follow-up, and batch
// linkage through the real router.
func TestWorksheetLifecycleOverHTTP(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	owing := seedMemberWithCoverage(t, env, "Owing Person", "2025-12-31")
	seedMemberWithCoverage(t, env, "Current Person", "2026-12-31")

	resp := env.do(t, http.MethodPost, "/api/v1/dues-worksheets", cookie,
		`{"label":"July chase","as_of":"2026-07-01","filter_kind":"owes","sort_order":"last_name"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var created struct {
		Run  worksheetRunResponse   `json:"run"`
		Rows []worksheetRowResponse `json:"rows"`
	}
	decodeBody(t, resp, &created)
	assert.Equal(t, "2026-07-01", created.Run.AsOf)
	assert.Equal(t, int64(1), created.Run.RowCount)
	require.Len(t, created.Rows, 1)
	assert.Equal(t, "Owing Person", created.Rows[0].DisplayName)
	assert.Equal(t, "expired", created.Rows[0].DuesStatus)
	assert.Equal(t, int64(1), created.Rows[0].Ordinal)
	assert.False(t, created.Rows[0].EnteredSince)

	runID := created.Run.ID

	t.Run("the run is listed and readable", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, "/api/v1/dues-worksheets", cookie, "")
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var page struct {
			Data []worksheetRunResponse `json:"data"`
		}
		decodeBody(t, resp, &page)
		require.Len(t, page.Data, 1)
		assert.Equal(t, runID, page.Data[0].ID)

		resp = env.do(t, http.MethodGet, fmt.Sprintf("/api/v1/dues-worksheets/%d", runID), cookie, "")
		require.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("a payment marks the line entered without rewriting the sheet", func(t *testing.T) {
		body := fmt.Sprintf(`{"membership_id":%d,"amount_cents":4000,"method":"cash",
			"received_on":"2026-07-05","paid_through":"2026-12-31"}`, owing)
		resp := doWithHeaders(t, env, http.MethodPost, "/api/v1/payments", cookie, body,
			map[string]string{"Idempotency-Key": "pay-1"})
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		resp = env.do(t, http.MethodGet,
			fmt.Sprintf("/api/v1/dues-worksheets/%d/rows", runID), cookie, "")
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var page struct {
			Data []worksheetRowResponse `json:"data"`
		}
		decodeBody(t, resp, &page)
		require.Len(t, page.Data, 1)
		assert.Equal(t, "expired", page.Data[0].DuesStatus, "the snapshot is unchanged")
		assert.Equal(t, "2025-12-31", page.Data[0].PaidThrough)
		assert.True(t, page.Data[0].EnteredSince, "but the reprint says the line is done")
	})

	t.Run("a follow-up sheet carries nobody forward", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, "/api/v1/dues-worksheets", cookie,
			fmt.Sprintf(`{"as_of":"2026-07-10","filter_kind":"unpaid_since_run",
				"source_run_id":%d,"sort_order":"last_name"}`, runID))
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		var followUp struct {
			Run worksheetRunResponse `json:"run"`
		}
		decodeBody(t, resp, &followUp)
		assert.Zero(t, followUp.Run.RowCount, "the only owing member has since paid")
	})

	t.Run("a batch can be linked to the run", func(t *testing.T) {
		b, _ := openBatchViaAPI(t, env, cookie, "From the sheet")
		resp := env.do(t, http.MethodPost,
			fmt.Sprintf("/api/v1/dues-worksheets/%d/batch", runID), cookie,
			fmt.Sprintf(`{"batch_id":%d}`, b.ID))
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var linked int64
		require.NoError(t, env.db.QueryRow(
			`SELECT worksheet_run_id FROM payment_batches WHERE id = ?`, b.ID).Scan(&linked))
		assert.Equal(t, runID, linked)

		events := env.auditEvents(t, "dues.worksheet.batch.link")
		assert.NotEmpty(t, events)
	})

	t.Run("generation is audited", func(t *testing.T) {
		events := env.auditEvents(t, "dues.worksheet.create")
		assert.NotEmpty(t, events)
		assert.Equal(t, "dues_worksheet_run", events[0].ResourceKind.String)
	})
}

func TestWorksheetValidationOverHTTP(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)

	for _, tc := range []struct{ name, body string }{
		{"unknown filter", `{"filter_kind":"everybody","sort_order":"last_name"}`},
		{"unknown sort", `{"filter_kind":"owes","sort_order":"by vibes"}`},
		{"follow-up with no source", `{"filter_kind":"unpaid_since_run","sort_order":"last_name"}`},
		{"bad as_of", `{"filter_kind":"owes","sort_order":"last_name","as_of":"07/01/2026"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := env.do(t, http.MethodPost, "/api/v1/dues-worksheets", cookie, tc.body)
			assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
		})
	}

	t.Run("unknown source run", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, "/api/v1/dues-worksheets", cookie,
			`{"filter_kind":"unpaid_since_run","source_run_id":999,"sort_order":"last_name"}`)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}

// TestWorksheetsDeniedToNonTreasurers proves the sheet is treasury-only and
// that a denial leaks no member data.
func TestWorksheetsDeniedToNonTreasurers(t *testing.T) {
	env := setupAuthzTest(t, "president")
	cookie := env.signIn(t)
	seedMemberWithCoverage(t, env, "Sheet Person", "2025-12-31")

	for _, tc := range []struct{ name, method, path, body string }{
		{"create", http.MethodPost, "/api/v1/dues-worksheets",
			`{"filter_kind":"owes","sort_order":"last_name"}`},
		{"list", http.MethodGet, "/api/v1/dues-worksheets", ""},
		{"read", http.MethodGet, "/api/v1/dues-worksheets/1", ""},
		{"rows", http.MethodGet, "/api/v1/dues-worksheets/1/rows", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := env.do(t, tc.method, tc.path, cookie, tc.body)
			require.Equal(t, http.StatusForbidden, resp.StatusCode)
			assert.NotContains(t, readAll(t, resp), "Sheet Person")
		})
	}

	var runs int
	require.NoError(t, env.db.QueryRow(`SELECT count(*) FROM dues_worksheet_runs`).Scan(&runs))
	assert.Zero(t, runs, "a denied request generates nothing")
}
