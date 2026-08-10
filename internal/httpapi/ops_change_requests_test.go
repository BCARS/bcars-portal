package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/httpapi"
)

// Officer-entered change-request intake (bcars-portal-4ux.2).
//
// The property that matters most here is a NEGATIVE one: recording a proposal
// changes nothing about the member. Most of these tests therefore assert what
// did NOT happen, through the shipped API.

type apiChangeRequest struct {
	ID                 int64  `json:"id"`
	Source             string `json:"source"`
	Status             string `json:"status"`
	TargetPersonID     int64  `json:"target_person_id"`
	TargetDisplayName  string `json:"target_display_name"`
	SuppliedName       string `json:"supplied_name"`
	SuppliedCallSign   string `json:"supplied_call_sign"`
	SuppliedContact    string `json:"supplied_contact"`
	StatedRelationship string `json:"stated_relationship"`
	Summary            string `json:"summary"`
	ReceivedByUserID   int64  `json:"received_by_user_id"`
	TriagedByUserID    int64  `json:"triaged_by_user_id"`
	TriagedAt          string `json:"triaged_at"`
	PendingItemsCount  int64  `json:"pending_items_count"`
	Version            int64  `json:"version"`
	Items              []struct {
		ID            int64  `json:"id"`
		Ordinal       int64  `json:"ordinal"`
		Operation     string `json:"operation"`
		ProposedValue string `json:"proposed_value"`
		TargetKind    string `json:"target_kind"`
		TargetID      int64  `json:"target_id"`
		Sensitivity   string `json:"sensitivity"`
		Status        string `json:"status"`
	} `json:"items"`
}

// canonicalSnapshot counts every table intake must not touch.
type canonicalSnapshot struct {
	persons, contacts, memberships, coverage, payments, prefs, visibility int
}

func snapshotCanonical(t *testing.T, env *authzEnv) canonicalSnapshot {
	t.Helper()
	var s canonicalSnapshot
	for _, q := range []struct {
		sql  string
		into *int
	}{
		{`SELECT count(*) FROM persons`, &s.persons},
		{`SELECT count(*) FROM contact_methods`, &s.contacts},
		{`SELECT count(*) FROM memberships`, &s.memberships},
		{`SELECT count(*) FROM coverage_events`, &s.coverage},
		{`SELECT count(*) FROM payments`, &s.payments},
		{`SELECT count(*) FROM acs_ares_sharing_events`, &s.prefs},
		{`SELECT count(*) FROM contact_method_visibility_events`, &s.visibility},
	} {
		require.NoError(t, env.db.QueryRow(q.sql).Scan(q.into))
	}
	return s
}

func createRequest(t *testing.T, env *authzEnv, cookie *http.Cookie, body, key string) *http.Response {
	t.Helper()
	return doWithHeaders(t, env, http.MethodPost, "/api/v1/change-requests", cookie, body,
		map[string]string{"Idempotency-Key": key})
}

func decodeRequest(t *testing.T, resp *http.Response) apiChangeRequest {
	t.Helper()
	var out apiChangeRequest
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}

// seedPersonForRequest creates a synthetic person and returns its id.
func seedPersonForRequest(t *testing.T, env *authzEnv, name string) int64 {
	t.Helper()
	res, err := env.db.Exec(
		`INSERT INTO persons (display_name, sort_name) VALUES (?, ?)`, name, name)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

// TestIntakeChangesNoCanonicalData is the bead's central acceptance criterion.
func TestIntakeChangesNoCanonicalData(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Canonical Member")

	before := snapshotCanonical(t, env)

	body := fmt.Sprintf(`{
		"source":"officer_phone",
		"target_person_id":%d,
		"summary":"Called to say their mobile number changed and to fix their call sign.",
		"items":[
			{"operation":"person.call_sign.set","proposed_value":"W3XYZ",
			 "target_kind":"person","target_id":%d},
			{"operation":"contact_method.add","proposed_value":"phone:814-555-0143"}
		]}`, person, person)
	resp := createRequest(t, env, cookie, body, "intake-1")
	require.Equal(t, http.StatusOK, resp.StatusCode, readAll(t, resp))

	after := snapshotCanonical(t, env)
	assert.Equal(t, before, after,
		"recording a proposal must not touch persons, contacts, memberships, coverage, payments, or preferences")

	// The person's own fields are untouched by name too, not just by count.
	var displayName string
	var callSign any
	require.NoError(t, env.db.QueryRow(
		`SELECT display_name, call_sign FROM persons WHERE id = ?`, person).
		Scan(&displayName, &callSign))
	assert.Equal(t, "Canonical Member", displayName)
	assert.Nil(t, callSign, "a proposed call sign must not become the member's call sign")
}

// TestIntakeStoresTheProposal proves the request is recorded faithfully.
func TestIntakeStoresTheProposal(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Ada Member")

	body := fmt.Sprintf(`{
		"source":"officer_meeting",
		"target_person_id":%d,
		"stated_relationship":"spouse",
		"summary":"Reported at the January meeting.",
		"items":[
			{"operation":"person.display_name.set","proposed_value":"Ada M. Member",
			 "target_kind":"person","target_id":%d,"sensitivity":"sensitive"}
		]}`, person, person)
	got := decodeRequest(t, createRequest(t, env, cookie, body, "intake-2"))

	assert.Equal(t, "officer_meeting", got.Source)
	assert.Equal(t, "submitted", got.Status)
	assert.Equal(t, person, got.TargetPersonID)
	assert.Equal(t, "Ada Member", got.TargetDisplayName)
	assert.Equal(t, "spouse", got.StatedRelationship)
	assert.Equal(t, int64(1), got.PendingItemsCount)
	assert.NotZero(t, got.ReceivedByUserID, "the recording officer must be captured")

	require.Len(t, got.Items, 1)
	assert.Equal(t, "person.display_name.set", got.Items[0].Operation)
	assert.Equal(t, "Ada M. Member", got.Items[0].ProposedValue)
	assert.Equal(t, "sensitive", got.Items[0].Sensitivity)
	assert.Equal(t, "pending", got.Items[0].Status)
	assert.Equal(t, int64(0), got.Items[0].Ordinal)
}

// TestIntakeIsIdempotent proves a retried submission does not become a second
// request.
func TestIntakeIsIdempotent(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Retry Member")

	body := fmt.Sprintf(`{"source":"officer_email","target_person_id":%d,
		"summary":"Emailed a new address.",
		"items":[{"operation":"contact_method.add","proposed_value":"email:new@example.test"}]}`, person)

	first := decodeRequest(t, createRequest(t, env, cookie, body, "retry-key"))
	second := decodeRequest(t, createRequest(t, env, cookie, body, "retry-key"))
	assert.Equal(t, first.ID, second.ID, "a retry must return the original request")

	var n int
	require.NoError(t, env.db.QueryRow(`SELECT count(*) FROM member_change_requests`).Scan(&n))
	assert.Equal(t, 1, n, "a retry must not record a second request")

	// A different body under the same key is refused rather than silently
	// replayed as the original.
	changed := fmt.Sprintf(`{"source":"officer_email","target_person_id":%d,
		"summary":"Actually it was their phone number.",
		"items":[{"operation":"contact_method.add","proposed_value":"phone:814-555-0100"}]}`, person)
	resp := createRequest(t, env, cookie, changed, "retry-key")
	assert.Equal(t, http.StatusConflict, resp.StatusCode,
		"reusing a key for different work must not replay the original")
}

// TestUnsupportedSuggestionStaysAReviewableNote proves the bead's rule that a
// financial or membership suggestion is captured but can never become an
// arbitrary field write.
func TestUnsupportedSuggestionStaysAReviewableNote(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Honorary Hopeful")

	body := fmt.Sprintf(`{"source":"officer_phone","target_person_id":%d,
		"summary":"Asked to be made an honorary member and to have their dues waived.",
		"items":[{"operation":"other","proposed_value":"Requests honorary status."}]}`, person)
	got := decodeRequest(t, createRequest(t, env, cookie, body, "other-1"))

	require.Len(t, got.Items, 1)
	assert.Equal(t, "other", got.Items[0].Operation)
	assert.Equal(t, "pending", got.Items[0].Status)

	// The database itself refuses to approve it, so no future service can
	// route it through a generic mutation path.
	_, err := env.db.Exec(`
		UPDATE member_change_request_items
		   SET status = 'approved', reviewed_by = 1, reviewed_at = '2026-08-09T12:00:00.000Z'
		 WHERE id = ?`, got.Items[0].ID)
	assert.Error(t, err, "an unsupported suggestion must never be approvable")
}

// TestIntakeRejectsUnknownOperations proves there is no arbitrary field path.
func TestIntakeRejectsUnknownOperations(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Target Member")

	for _, op := range []string{
		"membership.lifecycle.set",
		"payments.amount_cents.set",
		"persons.deceased_at",
		"", "*",
	} {
		body := fmt.Sprintf(`{"source":"officer_phone","target_person_id":%d,
			"summary":"Attempted a field write.",
			"items":[{"operation":%q,"proposed_value":"x"}]}`, person, op)
		resp := createRequest(t, env, cookie, body, "unknown-"+op)
		assert.GreaterOrEqual(t, resp.StatusCode, 400,
			"operation %q must be refused", op)
	}

	var n int
	require.NoError(t, env.db.QueryRow(`SELECT count(*) FROM member_change_request_items`).Scan(&n))
	assert.Zero(t, n, "no refused item may be stored")
}

// TestIntakeValidation covers the remaining refusals.
func TestIntakeValidation(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Validation Member")

	cases := []struct {
		name string
		body string
	}{
		{"a request needs a summary", fmt.Sprintf(
			`{"source":"officer_phone","target_person_id":%d,"summary":"   ",
			  "items":[{"operation":"other","proposed_value":"x"}]}`, person)},
		{"a request needs at least one item", fmt.Sprintf(
			`{"source":"officer_phone","target_person_id":%d,"summary":"Nothing proposed.","items":[]}`, person)},
		{"a request needs someone to be about", `{"source":"officer_phone",
			"summary":"Somebody said something.",
			"items":[{"operation":"other","proposed_value":"x"}]}`},
		{"a supported operation needs a value", fmt.Sprintf(
			`{"source":"officer_phone","target_person_id":%d,"summary":"No value.",
			  "items":[{"operation":"person.call_sign.set","proposed_value":""}]}`, person)},
		{"an item target needs both parts", fmt.Sprintf(
			`{"source":"officer_phone","target_person_id":%d,"summary":"Half a target.",
			  "items":[{"operation":"person.call_sign.set","proposed_value":"W3ABC","target_kind":"person"}]}`, person)},
		{"the target person must exist", `{"source":"officer_phone","target_person_id":99999,
			"summary":"Unknown member.",
			"items":[{"operation":"other","proposed_value":"x"}]}`},
		{"an unknown source is refused", fmt.Sprintf(
			`{"source":"carrier_pigeon","target_person_id":%d,"summary":"By pigeon.",
			  "items":[{"operation":"other","proposed_value":"x"}]}`, person)},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := createRequest(t, env, cookie, tc.body, fmt.Sprintf("invalid-%d", i))
			assert.GreaterOrEqual(t, resp.StatusCode, 400, readAll(t, resp))
		})
	}

	var n int
	require.NoError(t, env.db.QueryRow(`SELECT count(*) FROM member_change_requests`).Scan(&n))
	assert.Zero(t, n, "no refused request may be stored")
}

// TestIntakeRequiresAnIdempotencyKey keeps retries safe by construction.
func TestIntakeRequiresAnIdempotencyKey(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Keyless Member")

	body := fmt.Sprintf(`{"source":"officer_phone","target_person_id":%d,"summary":"No key.",
		"items":[{"operation":"other","proposed_value":"x"}]}`, person)
	resp := doWithHeaders(t, env, http.MethodPost, "/api/v1/change-requests", cookie, body, nil)
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
}

// TestListIsDeterministicAndFiltered proves ordering, filtering, and paging.
func TestListIsDeterministicAndFiltered(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Listed Member")

	// Several requests recorded in the same millisecond, which is exactly the
	// case an id tie-breaker exists for.
	var ids []int64
	for i := 0; i < 5; i++ {
		body := fmt.Sprintf(`{"source":"officer_phone","target_person_id":%d,
			"summary":"Report %d.",
			"items":[{"operation":"other","proposed_value":"x"}]}`, person, i)
		ids = append(ids, decodeRequest(t, createRequest(t, env, cookie, body, fmt.Sprintf("list-%d", i))).ID)
	}

	list := func(t *testing.T, query string) []apiChangeRequest {
		t.Helper()
		resp := env.do(t, http.MethodGet, "/api/v1/change-requests"+query, cookie, "")
		require.Equal(t, http.StatusOK, resp.StatusCode, readAll(t, resp))
		var out []apiChangeRequest
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		return out
	}

	all := list(t, "")
	require.Len(t, all, 5)
	for i := 1; i < len(all); i++ {
		assert.Greater(t, all[i-1].ID, all[i].ID,
			"newest first with an id tie-breaker, or paging can hide a row")
	}

	// Paging covers every row exactly once.
	seen := map[int64]bool{}
	for offset := 0; offset < 5; offset += 2 {
		for _, r := range list(t, fmt.Sprintf("?limit=2&offset=%d", offset)) {
			assert.False(t, seen[r.ID], "request %d appeared on two pages", r.ID)
			seen[r.ID] = true
		}
	}
	assert.Len(t, seen, 5, "paging must cover every row")
	for _, id := range ids {
		assert.True(t, seen[id])
	}

	assert.Len(t, list(t, "?source=officer_phone"), 5)
	assert.Empty(t, list(t, "?source=officer_mail"), "a source filter must actually filter")
	assert.Len(t, list(t, "?status=submitted"), 5)
	assert.Empty(t, list(t, "?status=resolved"))
}

// TestTriageLinksWithoutRewritingWhatWasSupplied proves the triage contract.
func TestTriageLinksWithoutRewritingWhatWasSupplied(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Kilo Member")

	// An unresolved submission: a hint but no canonical target.
	body := `{"source":"officer_mail","supplied_name":"K. Member","supplied_call_sign":"W3ABC",
		"summary":"Letter said the roster phone number is wrong.",
		"items":[{"operation":"contact_method.update","proposed_value":"phone:814-555-0199"}]}`
	created := decodeRequest(t, createRequest(t, env, cookie, body, "triage-1"))
	require.Zero(t, created.TargetPersonID)

	// It shows up in the triage queue.
	resp := env.do(t, http.MethodGet, "/api/v1/change-requests?unresolved_target_only=true", cookie, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var queue []apiChangeRequest
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&queue))
	require.Len(t, queue, 1)
	assert.Equal(t, created.ID, queue[0].ID)

	// Link it.
	linked := decodeRequest(t, doWithHeaders(t, env, http.MethodPost,
		fmt.Sprintf("/api/v1/change-requests/%d/target", created.ID), cookie,
		fmt.Sprintf(`{"target_person_id":%d}`, person),
		map[string]string{"If-Match": fmt.Sprintf(`"%d"`, created.Version)}))

	assert.Equal(t, person, linked.TargetPersonID)
	assert.Equal(t, "Kilo Member", linked.TargetDisplayName)
	assert.NotZero(t, linked.TriagedByUserID)
	assert.NotEmpty(t, linked.TriagedAt)
	assert.Equal(t, "K. Member", linked.SuppliedName,
		"triage records a conclusion; it must not rewrite what the submitter said")
	assert.Equal(t, "W3ABC", linked.SuppliedCallSign)

	// And it leaves the triage queue.
	resp = env.do(t, http.MethodGet, "/api/v1/change-requests?unresolved_target_only=true", cookie, "")
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&queue))
	assert.Empty(t, queue)
}

// TestTriageIsVersionGuarded proves two officers cannot silently overwrite one
// another on the same submission.
func TestTriageIsVersionGuarded(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	first := seedPersonForRequest(t, env, "First Guess")
	second := seedPersonForRequest(t, env, "Second Guess")

	created := decodeRequest(t, createRequest(t, env, cookie,
		`{"source":"officer_mail","supplied_name":"Ambiguous","summary":"Who is this?",
		  "items":[{"operation":"other","proposed_value":"x"}]}`, "triage-2"))

	path := fmt.Sprintf("/api/v1/change-requests/%d/target", created.ID)

	t.Run("If-Match is required", func(t *testing.T) {
		resp := doWithHeaders(t, env, http.MethodPost, path, cookie,
			fmt.Sprintf(`{"target_person_id":%d}`, first), nil)
		assert.Equal(t, http.StatusPreconditionRequired, resp.StatusCode)
	})

	// One officer wins.
	require.Equal(t, http.StatusOK, doWithHeaders(t, env, http.MethodPost, path, cookie,
		fmt.Sprintf(`{"target_person_id":%d}`, first),
		map[string]string{"If-Match": fmt.Sprintf(`"%d"`, created.Version)}).StatusCode)

	// The second, working from the version they read, is refused.
	resp := doWithHeaders(t, env, http.MethodPost, path, cookie,
		fmt.Sprintf(`{"target_person_id":%d}`, second),
		map[string]string{"If-Match": fmt.Sprintf(`"%d"`, created.Version)})
	assert.Equal(t, http.StatusPreconditionFailed, resp.StatusCode,
		"a stale triage must be refused, not silently applied")

	var target int64
	require.NoError(t, env.db.QueryRow(
		`SELECT target_person_id FROM member_change_requests WHERE id = ?`, created.ID).Scan(&target))
	assert.Equal(t, first, target, "the first officer's conclusion must stand")
}

// TestChangeRequestsDenyUnauthorizedCallers proves the capability guard, using
// a role that holds member.read but no request capability.
func TestChangeRequestsDenyUnauthorizedCallers(t *testing.T) {
	env := setupAuthzTest(t, "acs_coordinator")
	cookie := env.signIn(t)

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/change-requests", `{"source":"officer_phone","supplied_name":"X","summary":"y","items":[{"operation":"other","proposed_value":"z"}]}`},
		{http.MethodGet, "/api/v1/change-requests", ""},
		{http.MethodGet, "/api/v1/change-requests/1", ""},
		{http.MethodPost, "/api/v1/change-requests/1/target", `{"target_person_id":1}`},
	} {
		resp := doWithHeaders(t, env, tc.method, tc.path, cookie, tc.body,
			map[string]string{"Idempotency-Key": "denied", "If-Match": `"1"`})
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"%s %s must be denied without change_request.manage", tc.method, tc.path)
	}

	resp := doWithHeaders(t, env, http.MethodGet, "/api/v1/change-requests", nil, "", nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"an anonymous caller must be refused")
}

// TestIntakeIsAudited proves capture and triage leave a record.
func TestIntakeIsAudited(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Audited Member")

	created := decodeRequest(t, createRequest(t, env, cookie, fmt.Sprintf(
		`{"source":"officer_phone","target_person_id":%d,"summary":"Audited.",
		  "items":[{"operation":"other","proposed_value":"x"}]}`, person), "audit-1"))

	events := env.auditEvents(t, "change_request.create")
	require.NotEmpty(t, events, "intake must be audited")
	assert.Equal(t, "success", events[0].Outcome)
	assert.Equal(t, created.ID, events[0].ResourceID.Int64,
		"the audit event must name the request it recorded")
}

// TestGetReturnsAnETagThatTriageAccepts closes the read/write loop a client
// actually follows.
func TestGetReturnsAnETagThatTriageAccepts(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Etag Member")

	created := decodeRequest(t, createRequest(t, env, cookie,
		`{"source":"officer_mail","supplied_name":"Etag","summary":"Read then write.",
		  "items":[{"operation":"other","proposed_value":"x"}]}`, "etag-1"))

	resp := env.do(t, http.MethodGet, fmt.Sprintf("/api/v1/change-requests/%d", created.ID), cookie, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	etag := resp.Header.Get("ETag")
	require.NotEmpty(t, etag, "a mutable resource must publish an ETag")

	linked := doWithHeaders(t, env, http.MethodPost,
		fmt.Sprintf("/api/v1/change-requests/%d/target", created.ID), cookie,
		fmt.Sprintf(`{"target_person_id":%d}`, person),
		map[string]string{"If-Match": etag})
	assert.Equal(t, http.StatusOK, linked.StatusCode,
		"the ETag a read returns must be accepted by the matching write")
}

// TestUnknownRequestIs404 keeps a missing row from reading as a server error.
func TestUnknownRequestIs404(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodGet, "/api/v1/change-requests/99999", cookie, "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	resp = doWithHeaders(t, env, http.MethodPost, "/api/v1/change-requests/99999/target", cookie,
		`{"target_person_id":1}`, map[string]string{"If-Match": `"1"`})
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestChangeRequestOperationsDeclareMetadata guards the generic invariants for
// the new surface: a real capability and an enforceable confirmation level.
func TestChangeRequestOperationsDeclareMetadata(t *testing.T) {
	meta := httpapi.AllMeta()
	for _, opID := range []string{
		"change-request-create", "change-request-list",
		"change-request-get", "change-request-triage",
	} {
		m, ok := meta[opID]
		require.True(t, ok, "%s must be registered through httpapi.Register", opID)
		assert.Equal(t, "change_request.manage", m.RequiredCapability)
		assert.NotEmpty(t, m.AuditAction, "%s must declare an audit action", opID)
		assert.Contains(t, []string{httpapi.ConfirmNone, httpapi.ConfirmExplicit}, m.ConfirmationLevel)
	}
}
