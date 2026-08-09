package httpapi_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedMemberWithCoverage creates a person, an approved membership, and one
// coverage decision, returning the membership id.
func seedMemberWithCoverage(t *testing.T, env *authzEnv, name, paidThrough string) int64 {
	t.Helper()
	res, err := env.db.Exec(`INSERT INTO persons (display_name, sort_name) VALUES (?, ?)`, name, name)
	require.NoError(t, err)
	personID, err := res.LastInsertId()
	require.NoError(t, err)

	res, err = env.db.Exec(
		`INSERT INTO memberships (person_id, base_type, lifecycle) VALUES (?, 'full', 'approved')`,
		personID)
	require.NoError(t, err)
	membershipID, err := res.LastInsertId()
	require.NoError(t, err)

	if paidThrough != "" {
		_, err = env.db.Exec(`
			INSERT INTO coverage_events (membership_id, paid_through, reason_kind, decided_at)
			VALUES (?, ?, 'adjustment', '2026-01-01T00:00:00.000Z')`, membershipID, paidThrough)
		require.NoError(t, err)
	}
	return membershipID
}

// doWithHeaders is do() plus extra request headers, which the If-Match paths
// need. It lives here rather than in the shared helper so the existing tests
// keep their simpler signature.
func doWithHeaders(t *testing.T, env *authzEnv, method, path string, cookie *http.Cookie, body string, headers map[string]string) *http.Response {
	t.Helper()
	var r *http.Request
	var err error
	if body != "" {
		r, err = http.NewRequest(method, env.ts.URL+path, strings.NewReader(body))
		require.NoError(t, err)
		r.Header.Set("Content-Type", "application/json")
	} else {
		r, err = http.NewRequest(method, env.ts.URL+path, nil)
		require.NoError(t, err)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	resp, err := env.ts.Client().Do(r)
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func decodeBody(t *testing.T, resp *http.Response, into any) {
	t.Helper()
	require.NoError(t, json.NewDecoder(resp.Body).Decode(into))
}

// TestDuesStandingIsSafeForExecutiveOfficers proves the president can see
// standing and that the response carries no restricted payment detail. This is
// the boundary the design draws between safe standing and treasury data.
func TestDuesStandingIsSafeForExecutiveOfficers(t *testing.T) {
	env := setupAuthzTest(t, "president")
	cookie := env.signIn(t)
	id := seedMemberWithCoverage(t, env, "Ada Lovelace", "2026-12-31")

	resp := env.do(t, http.MethodGet,
		fmt.Sprintf("/api/v1/memberships/%d/dues-standing?as_of=2026-07-01", id), cookie, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))
	assert.Equal(t, "current", body["status"])
	assert.Equal(t, "2026-12-31", body["paid_through"])
	assert.Equal(t, "2026-07-01", body["as_of"])
	assert.Equal(t, "full", body["base_type"], "underlying membership rights are still reported")

	for _, restricted := range []string{
		"amount_cents", "method", "reference", "receipt_code", "treasurer_note",
	} {
		assert.NotContains(t, body, restricted,
			"safe standing must never carry %s", restricted)
	}
}

func TestDuesStandingListFiltersByStatus(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	seedMemberWithCoverage(t, env, "Current Person", "2026-12-31")
	seedMemberWithCoverage(t, env, "Expired Person", "2024-12-31")
	seedMemberWithCoverage(t, env, "Unrecorded Person", "")

	resp := env.do(t, http.MethodGet,
		"/api/v1/dues-standing?as_of=2026-07-01&status=expired", cookie, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var page struct {
		Data []struct {
			DisplayName string `json:"display_name"`
			Status      string `json:"status"`
		} `json:"data"`
	}
	decodeBody(t, resp, &page)
	require.Len(t, page.Data, 1)
	assert.Equal(t, "Expired Person", page.Data[0].DisplayName)
	assert.Equal(t, "expired", page.Data[0].Status)
}

func TestDuesStandingRejectsBadAsOf(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodGet, "/api/v1/dues-standing?as_of=07%2F01%2F2026", cookie, "")
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
}

// TestDuesStandingRequiresCapability proves a role with member access but no
// dues.read cannot read standing at all.
func TestDuesStandingRequiresCapability(t *testing.T) {
	env := setupAuthzTest(t, "acs_coordinator")
	cookie := env.signIn(t)
	id := seedMemberWithCoverage(t, env, "Guarded Person", "2026-12-31")

	resp := env.do(t, http.MethodGet, "/api/v1/dues-standing", cookie, "")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	resp = env.do(t, http.MethodGet,
		fmt.Sprintf("/api/v1/memberships/%d/dues-standing", id), cookie, "")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// TestCoverageDetailDeniedToExecutiveOfficers proves dues.read does not open
// the coverage history or the adjustment endpoint.
func TestCoverageDetailDeniedToExecutiveOfficers(t *testing.T) {
	env := setupAuthzTest(t, "president")
	cookie := env.signIn(t)
	id := seedMemberWithCoverage(t, env, "Coverage Person", "2026-12-31")

	resp := env.do(t, http.MethodGet,
		fmt.Sprintf("/api/v1/memberships/%d/coverage-events", id), cookie, "")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"reading standing does not imply reading the coverage history")

	resp = env.do(t, http.MethodPost,
		fmt.Sprintf("/api/v1/memberships/%d/coverage-events", id), cookie,
		`{"paid_through":"2027-12-31","reason":"Should not be allowed"}`)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// TestCoverageAdjustmentAppendsAndAudits drives the treasurer's adjustment path
// end to end and proves the prior decision survives.
func TestCoverageAdjustmentAppendsAndAudits(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	id := seedMemberWithCoverage(t, env, "Adjusted Person", "2025-12-31")

	resp := env.do(t, http.MethodPost,
		fmt.Sprintf("/api/v1/memberships/%d/coverage-events", id), cookie,
		`{"paid_through":"2026-12-31","reason":"Waived the lapse by board decision"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var created struct {
		ID                int64  `json:"id"`
		ReasonKind        string `json:"reason_kind"`
		SupersedesEventID int64  `json:"supersedes_event_id"`
		DecidedByUserID   int64  `json:"decided_by_user_id"`
	}
	decodeBody(t, resp, &created)
	assert.Equal(t, "adjustment", created.ReasonKind)
	assert.NotZero(t, created.SupersedesEventID)
	assert.NotZero(t, created.DecidedByUserID)

	resp = env.do(t, http.MethodGet,
		fmt.Sprintf("/api/v1/memberships/%d/coverage-events", id), cookie, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var page struct {
		Data []struct {
			ID          int64  `json:"id"`
			PaidThrough string `json:"paid_through"`
		} `json:"data"`
	}
	decodeBody(t, resp, &page)
	require.Len(t, page.Data, 2, "the superseded decision is still readable")

	resp = env.do(t, http.MethodGet,
		fmt.Sprintf("/api/v1/memberships/%d/dues-standing?as_of=2026-07-01", id), cookie, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var standing struct {
		Status      string `json:"status"`
		PaidThrough string `json:"paid_through"`
	}
	decodeBody(t, resp, &standing)
	assert.Equal(t, "current", standing.Status)
	assert.Equal(t, "2026-12-31", standing.PaidThrough)

	events := env.auditEvents(t, "coverage.adjust")
	require.Len(t, events, 1)
	assert.Equal(t, "success", events[0].Outcome)
	assert.Equal(t, "coverage_event", events[0].ResourceKind.String)
	assert.Equal(t, created.ID, events[0].ResourceID.Int64)
}

// TestCoverageAdjustmentAcceptsOffCycleDate pins the owner's decision that the
// server records what happened rather than enforcing the club year-end.
func TestCoverageAdjustmentAcceptsOffCycleDate(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	id := seedMemberWithCoverage(t, env, "Off Cycle Person", "")

	resp := env.do(t, http.MethodPost,
		fmt.Sprintf("/api/v1/memberships/%d/coverage-events", id), cookie,
		`{"paid_through":"2026-06-30","reason":"Prorated by agreement"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var created struct {
		PaidThrough string `json:"paid_through"`
	}
	decodeBody(t, resp, &created)
	assert.Equal(t, "2026-06-30", created.PaidThrough)
}

func TestCoverageAdjustmentRequiresReason(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	id := seedMemberWithCoverage(t, env, "Reasonless Person", "")

	resp := env.do(t, http.MethodPost,
		fmt.Sprintf("/api/v1/memberships/%d/coverage-events", id), cookie,
		`{"paid_through":"2026-12-31","reason":""}`)
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
}

func TestCoverageAdjustmentUnknownMembership(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodPost, "/api/v1/memberships/999/coverage-events", cookie,
		`{"paid_through":"2026-12-31","reason":"No such member"}`)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestDuesRateLifecycle covers create, revise with If-Match, the stale write,
// and the duplicate create.
func TestDuesRateLifecycle(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodPut, "/api/v1/dues-rates/2026", cookie,
		`{"amount_cents":4000,"note":"Board approved"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	etag := resp.Header.Get("ETag")
	assert.Equal(t, `"1"`, etag)

	var rate struct {
		Year        int64 `json:"year"`
		AmountCents int64 `json:"amount_cents"`
		Version     int64 `json:"version"`
	}
	decodeBody(t, resp, &rate)
	assert.Equal(t, int64(2026), rate.Year)
	assert.Equal(t, int64(4000), rate.AmountCents)

	t.Run("creating the same year twice conflicts", func(t *testing.T) {
		resp := env.do(t, http.MethodPut, "/api/v1/dues-rates/2026", cookie,
			`{"amount_cents":5000}`)
		assert.Equal(t, http.StatusConflict, resp.StatusCode)
	})

	t.Run("a stale If-Match is refused", func(t *testing.T) {
		resp := doWithHeaders(t, env, http.MethodPut, "/api/v1/dues-rates/2026", cookie,
			`{"amount_cents":5000}`, map[string]string{"If-Match": `"99"`})
		assert.Equal(t, http.StatusPreconditionFailed, resp.StatusCode)
	})

	t.Run("the current If-Match wins", func(t *testing.T) {
		resp := doWithHeaders(t, env, http.MethodPut, "/api/v1/dues-rates/2026", cookie,
			`{"amount_cents":5000,"note":"Raised"}`, map[string]string{"If-Match": etag})
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, `"2"`, resp.Header.Get("ETag"))

		var revised struct {
			AmountCents int64 `json:"amount_cents"`
			Version     int64 `json:"version"`
		}
		decodeBody(t, resp, &revised)
		assert.Equal(t, int64(5000), revised.AmountCents)
		assert.Equal(t, int64(2), revised.Version)
	})

	t.Run("the rate is audited", func(t *testing.T) {
		events := env.auditEvents(t, "dues.rate.set")
		assert.NotEmpty(t, events)
	})
}

func TestDuesRateRequiresManageCapability(t *testing.T) {
	env := setupAuthzTest(t, "president")
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodPut, "/api/v1/dues-rates/2026", cookie,
		`{"amount_cents":4000}`)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"an executive officer reads standing but does not set the rate")

	resp = env.do(t, http.MethodGet, "/api/v1/dues-rates", cookie, "")
	assert.Equal(t, http.StatusOK, resp.StatusCode, "but may read the rates")
}

// TestDuesSuggestionsAreNonBinding proves the endpoint announces itself as a
// hint and writes nothing.
func TestDuesSuggestionsAreNonBinding(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodPut, "/api/v1/dues-rates/2026", cookie, `{"amount_cents":4000}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp = env.do(t, http.MethodGet, "/api/v1/dues-suggestions?as_of=2026-07-01", cookie, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		AsOf    string `json:"as_of"`
		Binding bool   `json:"binding"`
		Choices []struct {
			PaidThrough string `json:"paid_through"`
			AmountCents int64  `json:"amount_cents"`
			RateKnown   bool   `json:"rate_known"`
			Explanation string `json:"explanation"`
		} `json:"choices"`
	}
	decodeBody(t, resp, &body)
	assert.Equal(t, "2026-07-01", body.AsOf)
	assert.False(t, body.Binding)
	require.Len(t, body.Choices, 3)
	assert.Equal(t, "2026-12-31", body.Choices[0].PaidThrough)
	assert.Equal(t, int64(4000), body.Choices[0].AmountCents)
	assert.True(t, body.Choices[0].RateKnown)
	assert.False(t, body.Choices[1].RateKnown, "2027 has no rate on file")
	for _, c := range body.Choices {
		assert.NotEmpty(t, c.Explanation)
	}

	var events int
	require.NoError(t, env.db.QueryRow(`SELECT count(*) FROM coverage_events`).Scan(&events))
	assert.Zero(t, events, "asking for a suggestion must not change any member's coverage")
}
