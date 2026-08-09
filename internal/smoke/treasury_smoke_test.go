package smoke

// The Phase 2 completion proof.
//
// Every assertion below runs against the shipped binaries over HTTP. That
// matters more here than anywhere else in the repository: Phase 1 reached a
// state where all seven gates were green while the production API could not
// resolve a signed-in principal, because every test built its own router. A
// treasury that passes its unit tests and cannot take money in the deployed
// artifact would be the same failure with worse consequences.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	treasurerEmail = "treasurer@bcars.example"
	treasurerPass  = "treasurerpassword12345"
	presidentEmail = "president@bcars.example"
	presidentPass  = "presidentpassword12345"
)

// TestTreasurySmoke drives the Phase 2 outcome end to end against the running
// server: draft isolation, exactly-once posting, immutable correction, the safe
// standing versus restricted detail boundary, and worksheet-to-batch ordering.
func TestTreasurySmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("smoke test builds binaries and starts a server; skipped in -short")
	}

	e := start(t)
	e.requireReady()

	adminCookie := e.consumeInvitation(e.bootstrapAdmin(), adminEmail, adminPass)

	// Two principals with genuinely different authority, both onboarded the
	// way a real club would: invited, then granted a role.
	treasurer := e.officerWithRole(adminCookie, treasurerEmail, treasurerPass, "treasurer")
	president := e.officerWithRole(adminCookie, presidentEmail, presidentPass, "president")

	// Two synthetic members. Nothing here reads real member data.
	alpha := e.createMember(adminCookie, "Alpha Smoketest", "Smoketest, Alpha", "W3AAA")
	bravo := e.createMember(adminCookie, "Bravo Smoketest", "Smoketest, Bravo", "W3BBB")

	// --- 1. A draft batch changes nothing ---

	batchID, _ := e.openBatch(treasurer, "Smoke meeting")
	e.addEntry(treasurer, batchID, alpha, 40000, "check", "2026-12-31")
	// Every entry mutation moves the batch version, so the version to post with
	// is the one the last row returned.
	batchVersion := e.addEntry(treasurer, batchID, bravo, 4000, "cash", "2026-12-31")

	assert.Equal(t, "unknown", e.standingStatus(treasurer, alpha),
		"a draft batch must not change anyone's dues standing")
	assert.Equal(t, "unknown", e.standingStatus(treasurer, bravo))

	// --- 2. A stale post writes nothing ---

	resp := e.postBatch(treasurer, batchID, batchVersion+99, "stale-key", true)
	require.Equal(t, http.StatusPreconditionFailed, resp.StatusCode,
		"a stale batch version must be refused: %s", readBody(resp))
	assert.Equal(t, "unknown", e.standingStatus(treasurer, alpha),
		"a refused post must leave standing untouched")

	// An unconfirmed post is refused too.
	resp = e.postBatch(treasurer, batchID, batchVersion, "unconfirmed-key", false)
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode,
		"posting without confirmation must be refused")

	// --- 3. Posting moves every intended standing exactly once ---

	resp = e.postBatch(treasurer, batchID, batchVersion, "post-key", true)

	var posted struct {
		Batch struct {
			State  string `json:"state"`
			Totals struct {
				NetTotalCents int64 `json:"net_total_cents"`
			} `json:"totals"`
		} `json:"batch"`
		Payments []struct {
			ID          int64  `json:"id"`
			AmountCents int64  `json:"amount_cents"`
			ReceiptCode string `json:"receipt_code"`
		} `json:"payments"`
	}
	e.decodeJSON(resp, http.StatusOK, &posted, "post batch")
	require.Equal(t, "posted", posted.Batch.State)
	require.Len(t, posted.Payments, 2)
	require.Equal(t, int64(44000), posted.Batch.Totals.NetTotalCents)

	assert.Equal(t, "current", e.standingStatus(treasurer, alpha))
	assert.Equal(t, "current", e.standingStatus(treasurer, bravo))

	// Retrying the identical request returns the original and posts nothing new.
	resp = e.postBatch(treasurer, batchID, batchVersion, "post-key", true)
	e.decodeJSON(resp, http.StatusOK, nil, "idempotent retry")
	assert.Equal(t, 2, e.countLedgerEntries(treasurer),
		"an idempotent retry must not post the money twice")

	// --- 4. Correcting $400 to $40 preserves the original ---

	var originalID int64
	for _, p := range posted.Payments {
		if p.AmountCents == 40000 {
			originalID = p.ID
		}
	}
	require.NotZero(t, originalID, "the $400 payment must be findable")

	resp = e.do(http.MethodGet, fmt.Sprintf("/api/v1/payments/%d", originalID), treasurer, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	revision := resp.Header.Get("ETag")
	require.Equal(t, `"0"`, revision, "an uncorrected chain is revision 0")

	resp = e.doWithHeaders(http.MethodPost,
		fmt.Sprintf("/api/v1/payments/%d/corrections", originalID), treasurer,
		`{"amount_cents":4000,"method":"check","received_on":"2026-07-05",
		  "paid_through":"2026-12-31","reason":"Typed 400 instead of 40","confirm":true}`,
		map[string]string{"If-Match": `"0"`, "Idempotency-Key": "correct-key"})

	var corrected struct {
		Effective struct {
			AmountCents int64 `json:"amount_cents"`
		} `json:"effective_payment"`
		Chain []struct {
			AmountCents int64  `json:"amount_cents"`
			EntryKind   string `json:"entry_kind"`
		} `json:"chain"`
		LedgerTotals struct {
			NetTotalCents int64 `json:"net_total_cents"`
		} `json:"ledger_totals"`
		PaidThrough string `json:"paid_through"`
	}
	e.decodeJSON(resp, http.StatusCreated, &corrected, "correction")

	assert.Equal(t, int64(4000), corrected.Effective.AmountCents)
	assert.Equal(t, int64(8000), corrected.LedgerTotals.NetTotalCents,
		"a $440 batch corrected from $400 to $40 nets $80")
	require.Len(t, corrected.Chain, 3, "original, reversal, and replacement all survive")
	assert.Equal(t, int64(40000), corrected.Chain[0].AmountCents, "the original is untouched")
	assert.Equal(t, int64(-40000), corrected.Chain[1].AmountCents)
	assert.Equal(t, "reversal", corrected.Chain[1].EntryKind)

	// An amount-only correction leaves the coverage decision alone.
	assert.Equal(t, "2026-12-31", corrected.PaidThrough)
	assert.Equal(t, "current", e.standingStatus(treasurer, alpha))
	assert.Equal(t, 1, e.countCoverageEvents(alpha),
		"correcting only the money must not add a coverage decision")

	// --- 5. A treasurer can export the books ---

	resp = e.do(http.MethodPost, "/api/v1/exports/treasury", treasurer, `{}`)

	var export struct {
		RowCount int    `json:"row_count"`
		Data     string `json:"data"`
	}
	e.decodeJSON(resp, http.StatusOK, &export, "treasury export")
	require.Positive(t, export.RowCount)

	raw, err := base64.StdEncoding.DecodeString(export.Data)
	require.NoError(t, err, "the export must be decodable CSV")
	csv := string(raw)
	assert.Contains(t, csv, "Alpha Smoketest")
	assert.Contains(t, csv, "400.00", "the original amount is still in the books")
	assert.Contains(t, csv, "40.00", "alongside what it became")
	assert.Contains(t, csv, "# generated_at", "the export states when it was made")

	// --- 6. The safe standing versus restricted detail boundary ---

	assert.Equal(t, "current", e.standingStatus(president, alpha),
		"an executive officer may read safe dues standing")

	for _, path := range []string{
		fmt.Sprintf("/api/v1/payments/%d", originalID),
		"/api/v1/ledger-entries",
		fmt.Sprintf("/api/v1/payment-batches/%d", batchID),
		fmt.Sprintf("/api/v1/memberships/%d/coverage-events", alpha),
	} {
		resp := e.do(http.MethodGet, path, president, "")
		require.Equal(t, http.StatusForbidden, resp.StatusCode,
			"an executive officer must be denied %s", path)
		body := readBody(resp)
		for _, secret := range []string{"40000", "400.00", "RCPT-"} {
			assert.NotContains(t, body, secret,
				"a denied response must not leak %q from %s", secret, path)
		}
	}

	resp = e.do(http.MethodPost, "/api/v1/exports/treasury", president, `{}`)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"an executive officer must not export the books")

	// --- 7. Worksheet order seeds a batch ---

	resp = e.do(http.MethodPost, "/api/v1/dues-worksheets", treasurer,
		`{"label":"Smoke sheet","filter_kind":"active","sort_order":"last_name"}`)

	var sheet struct {
		Run struct {
			ID       int64 `json:"id"`
			RowCount int64 `json:"row_count"`
		} `json:"run"`
		Rows []struct {
			Ordinal     int64  `json:"ordinal"`
			DisplayName string `json:"display_name"`
		} `json:"rows"`
	}
	e.decodeJSON(resp, http.StatusCreated, &sheet, "worksheet")
	require.Equal(t, int64(2), sheet.Run.RowCount)
	require.Len(t, sheet.Rows, 2)
	assert.Equal(t, "Alpha Smoketest", sheet.Rows[0].DisplayName, "sorted by last name, then first")
	assert.Equal(t, int64(1), sheet.Rows[0].Ordinal)

	nextBatch, _ := e.openBatch(treasurer, "From the smoke sheet")
	resp = e.do(http.MethodPost,
		fmt.Sprintf("/api/v1/dues-worksheets/%d/batch", sheet.Run.ID), treasurer,
		fmt.Sprintf(`{"batch_id":%d}`, nextBatch))
	e.decodeJSON(resp, http.StatusOK, nil, "worksheet to batch")

	// Read the order from the batch consumer, not from the worksheet. Asserting
	// the worksheet rows are ordered and, separately, that the batch is empty
	// proves both pieces and not the property a treasurer depends on.
	resp = e.do(http.MethodGet, fmt.Sprintf("/api/v1/payment-batches/%d", nextBatch), treasurer, "")
	var linked struct {
		WorksheetRunID int64 `json:"worksheet_run_id"`
		Totals         struct {
			EntryCount int64 `json:"entry_count"`
		} `json:"totals"`
	}
	e.decodeJSON(resp, http.StatusOK, &linked, "linked batch")
	assert.Equal(t, sheet.Run.ID, linked.WorksheetRunID,
		"the batch must identify the sheet it was opened from")
	assert.Zero(t, linked.Totals.EntryCount,
		"linking a worksheet must not invent rows; the treasurer types what is on the paper")

	// --- 8. The admin UI serves the treasurer's pages from the same binary ---
	//
	// The admin UI carries its own session cookie, distinct from the API's, so
	// this signs in again through the UI's own login form. That split is a
	// pre-existing assembly inconsistency rather than a Phase 2 gap, tracked as
	// bcars-portal-6q6.3.

	uiCookie := e.webLogin(treasurerEmail, treasurerPass)

	for _, page := range []string{
		"/admin/treasury",
		"/admin/treasury/batches",
		"/admin/treasury/worksheets",
		fmt.Sprintf("/admin/treasury/worksheets/%d", sheet.Run.ID),
	} {
		resp := e.do(http.MethodGet, page, uiCookie, "")
		require.Equal(t, http.StatusOK, resp.StatusCode, "the deployed binary must serve %s", page)
	}

	// The shipped batch page presents the sheet's members in saved order.
	resp = e.do(http.MethodGet, fmt.Sprintf("/admin/treasury/batches/%d", nextBatch), uiCookie, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	batchHTML := readBody(resp)
	assert.Contains(t, batchHTML, fmt.Sprintf("Working down sheet %d", sheet.Run.ID))
	assert.Contains(t, batchHTML, "0 of 2 entered")
	alphaAt := strings.Index(batchHTML, "Alpha Smoketest")
	bravoAt := strings.Index(batchHTML, "Bravo Smoketest")
	require.Positive(t, alphaAt, "the linked batch must list the sheet's members")
	assert.Less(t, alphaAt, bravoAt, "in the order the sheet was printed in")

	// The printed sheet carries the club identity and the year-end rule.
	resp = e.do(http.MethodGet,
		fmt.Sprintf("/admin/treasury/worksheets/%d", sheet.Run.ID), uiCookie, "")
	sheetHTML := readBody(resp)
	assert.Contains(t, sheetHTML, "Bedford County Amateur Radio Society")
	assert.Contains(t, sheetHTML, "letter portrait")
	assert.NotContains(t, sheetHTML, "Butler County",
		"the county identity correction must hold in the shipped binary")
}

// webLogin signs in through the admin UI's own login form and returns its
// session cookie. Both surfaces now issue one cookie (bcars-portal-6q6.3), so
// this cookie also authenticates API requests; it is obtained through the form
// here because that is the path an officer actually takes.
func (e *env) webLogin(email, password string) *http.Cookie {
	e.t.Helper()

	form := fmt.Sprintf("email=%s&password=%s", email, password)
	req, err := http.NewRequest(http.MethodPost, e.baseURL+"/login", strings.NewReader(form))
	require.NoError(e.t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := e.client.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	require.Equal(e.t, http.StatusSeeOther, resp.StatusCode, "admin UI login must succeed")

	c := sessionCookie(resp)
	if c == nil {
		e.t.Fatal("the admin UI login returned no session cookie")
	}
	return c
}

// decodeJSON asserts the status and decodes the body.
//
// The body is read exactly once. Passing readBody(resp) as an assertion message
// consumes the stream even when the assertion passes, which is how the first
// draft of this test decoded EOF from a successful response.
func (e *env) decodeJSON(resp *http.Response, want int, v any, label string) {
	e.t.Helper()
	body := readBody(resp)
	require.Equal(e.t, want, resp.StatusCode, "%s: %s", label, body)
	if v != nil {
		require.NoError(e.t, json.Unmarshal([]byte(body), v), "%s: decoding %s", label, body)
	}
}

// --- helpers ---

// officerWithRole invites a user, consumes the invitation, and grants a role.
func (e *env) officerWithRole(adminCookie *http.Cookie, email, password, role string) *http.Cookie {
	e.t.Helper()

	// Consume the invitation so the account exists and has a password, then
	// grant the role and sign in again: a session issued before the grant would
	// not carry the new capabilities.
	e.consumeInvitation(e.inviteWithoutRole(adminCookie, email), email, password)

	userID := e.userIDFor(email)
	resp := e.do(http.MethodPost, fmt.Sprintf("/api/v1/users/%d/role-grants", userID), adminCookie,
		fmt.Sprintf(`{"role_code":%q}`, role))
	e.decodeJSON(resp, http.StatusCreated, nil, "granting "+role)

	resp = e.do(http.MethodPost, "/api/v1/sessions", nil,
		fmt.Sprintf(`{"email":%q,"password":%q}`, email, password))
	require.Equal(e.t, http.StatusOK, resp.StatusCode)
	fresh := sessionCookie(resp)
	require.NotNil(e.t, fresh)
	return fresh
}

func (e *env) userIDFor(email string) int64 {
	e.t.Helper()
	ids := e.queryStrings(fmt.Sprintf(
		`SELECT CAST(id AS TEXT) FROM users WHERE email = '%s'`, email))
	require.Len(e.t, ids, 1, "user %s must exist", email)
	var id int64
	_, err := fmt.Sscanf(ids[0], "%d", &id)
	require.NoError(e.t, err)
	return id
}

// createMember creates a person and approves a membership, returning the
// membership id.
func (e *env) createMember(adminCookie *http.Cookie, display, sort, callSign string) int64 {
	e.t.Helper()

	resp := e.do(http.MethodPost, "/api/v1/members", adminCookie, fmt.Sprintf(
		`{"display_name":%q,"sort_name":%q,"call_sign":%q,"base_type":"full"}`,
		display, sort, callSign))
	e.decodeJSON(resp, http.StatusCreated, nil, "create member")

	ids := e.queryStrings(fmt.Sprintf(`
		SELECT CAST(m.id AS TEXT) FROM memberships m
		  JOIN persons p ON p.id = m.person_id
		 WHERE p.display_name = '%s'`, display))
	require.Len(e.t, ids, 1)
	var id int64
	_, err := fmt.Sscanf(ids[0], "%d", &id)
	require.NoError(e.t, err)
	return id
}

// openBatch opens a draft batch and returns its id and version.
func (e *env) openBatch(cookie *http.Cookie, label string) (int64, int64) {
	e.t.Helper()
	resp := e.do(http.MethodPost, "/api/v1/payment-batches", cookie,
		fmt.Sprintf(`{"label":%q}`, label))

	var body struct {
		ID      int64 `json:"id"`
		Version int64 `json:"version"`
	}
	e.decodeJSON(resp, http.StatusCreated, &body, "open batch")
	return body.ID, body.Version
}

// addEntry adds one draft row and returns the batch's new version.
func (e *env) addEntry(cookie *http.Cookie, batchID, membershipID, cents int64, method, paidThrough string) int64 {
	e.t.Helper()
	resp := e.do(http.MethodPost, fmt.Sprintf("/api/v1/payment-batches/%d/entries", batchID), cookie,
		fmt.Sprintf(`{"membership_id":%d,"amount_cents":%d,"method":%q,
			"received_on":"2026-07-05","paid_through":%q}`,
			membershipID, cents, method, paidThrough))

	var body struct {
		Batch struct {
			Version int64 `json:"version"`
		} `json:"batch"`
	}
	e.decodeJSON(resp, http.StatusCreated, &body, "add entry")
	return body.Batch.Version
}

func (e *env) postBatch(cookie *http.Cookie, batchID, version int64, key string, confirm bool) *http.Response {
	e.t.Helper()
	return e.doWithHeaders(http.MethodPost,
		fmt.Sprintf("/api/v1/payment-batches/%d/post", batchID), cookie,
		fmt.Sprintf(`{"confirm":%t}`, confirm),
		map[string]string{
			"If-Match":        fmt.Sprintf(`"%d"`, version),
			"Idempotency-Key": key,
		})
}

// standingStatus reads a membership's safe derived standing.
func (e *env) standingStatus(cookie *http.Cookie, membershipID int64) string {
	e.t.Helper()
	resp := e.do(http.MethodGet,
		fmt.Sprintf("/api/v1/memberships/%d/dues-standing?as_of=2026-07-10", membershipID), cookie, "")

	var body struct {
		Status string `json:"status"`
	}
	e.decodeJSON(resp, http.StatusOK, &body, "standing")
	return body.Status
}

func (e *env) countLedgerEntries(cookie *http.Cookie) int {
	e.t.Helper()
	resp := e.do(http.MethodGet, "/api/v1/ledger-entries?effective_only=true", cookie, "")

	var page struct {
		Data []json.RawMessage `json:"data"`
	}
	e.decodeJSON(resp, http.StatusOK, &page, "ledger entries")
	return len(page.Data)
}

func (e *env) countCoverageEvents(membershipID int64) int {
	e.t.Helper()
	rows := e.queryStrings(fmt.Sprintf(
		`SELECT CAST(count(*) AS TEXT) FROM coverage_events WHERE membership_id = %d`, membershipID))
	require.Len(e.t, rows, 1)
	return int(rows[0][0] - '0')
}

// doWithHeaders issues a request carrying extra headers, which the ETag and
// idempotency contracts require.
func (e *env) doWithHeaders(method, path string, cookie *http.Cookie, body string, headers map[string]string) *http.Response {
	e.t.Helper()

	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	req, err := http.NewRequest(method, e.baseURL+path, reader)
	require.NoError(e.t, err)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := e.client.Do(req)
	require.NoError(e.t, err)
	e.t.Cleanup(func() { resp.Body.Close() })
	return resp
}
