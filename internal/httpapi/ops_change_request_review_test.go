package httpapi_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/domain/changerequests"
	"github.com/bcars/bcars-portal/internal/httpapi"
)

// Per-item review and canonical apply (bcars-portal-4ux.3).
//
// The claims worth testing here are all about what happens at the seam between
// a decision and a canonical write: they must move together or not at all.

type apiDecision struct {
	Request apiChangeRequest `json:"request"`
	Item    struct {
		ID                     int64  `json:"id"`
		Operation              string `json:"operation"`
		Status                 string `json:"status"`
		Sensitivity            string `json:"sensitivity"`
		DecisionReason         string `json:"decision_reason"`
		VerificationNote       string `json:"verification_note"`
		AppliedAt              string `json:"applied_at"`
		AppliedResourceKind    string `json:"applied_resource_kind"`
		AppliedResourceID      int64  `json:"applied_resource_id"`
		AppliedResourceVersion int64  `json:"applied_resource_version"`
		ReviewedByUserID       int64  `json:"reviewed_by_user_id"`
	} `json:"item"`
	Applied bool `json:"applied"`
	Replay  bool `json:"replay"`
}

func decidePath(requestID, itemID int64) string {
	return fmt.Sprintf("/api/v1/change-requests/%d/items/%d/decision", requestID, itemID)
}

func decide(t *testing.T, env *authzEnv, cookie *http.Cookie, requestID, itemID int64, body string) *http.Response {
	t.Helper()
	return doWithHeaders(t, env, http.MethodPost, decidePath(requestID, itemID), cookie, body, nil)
}

func decodeDecision(t *testing.T, resp *http.Response) apiDecision {
	t.Helper()
	var out apiDecision
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}

// seedContact adds a contact method and returns its id and version.
func seedContact(t *testing.T, env *authzEnv, personID int64, kind, value string) (int64, int64) {
	t.Helper()
	res, err := env.db.Exec(`
		INSERT INTO contact_methods (person_id, kind, value_raw, value_norm)
		VALUES (?, ?, ?, ?)`, personID, kind, value, value)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	var version int64
	require.NoError(t, env.db.QueryRow(`SELECT version FROM contact_methods WHERE id = ?`, id).Scan(&version))
	return id, version
}

// requestWithItem creates a one-item request and returns the request and item ids.
func requestWithItem(t *testing.T, env *authzEnv, cookie *http.Cookie, personID int64, itemJSON, key string) (int64, int64) {
	t.Helper()
	body := fmt.Sprintf(`{"source":"officer_phone","target_person_id":%d,
		"summary":"Reported by telephone.","items":[%s]}`, personID, itemJSON)
	created := decodeRequest(t, createRequest(t, env, cookie, body, key))
	require.Len(t, created.Items, 1)
	return created.ID, created.Items[0].ID
}

// TestApprovalAppliesThroughTheDomainAdapter proves an approved item actually
// changes canonical data, through the service that owns the field.
func TestApprovalAppliesThroughTheDomainAdapter(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Ada Member")

	reqID, itemID := requestWithItem(t, env, cookie, person, fmt.Sprintf(
		`{"operation":"person.display_name.set","proposed_value":"Ada M. Member",
		  "target_kind":"person","target_id":%d}`, person), "apply-1")

	got := decodeDecision(t, decide(t, env, cookie, reqID, itemID, `{"decision":"approved"}`))
	assert.True(t, got.Applied)
	assert.Equal(t, "approved", got.Item.Status)
	assert.Equal(t, "person", got.Item.AppliedResourceKind)
	assert.Equal(t, person, got.Item.AppliedResourceID)
	assert.NotZero(t, got.Item.AppliedResourceVersion, "the resulting version must be recorded")
	assert.NotEmpty(t, got.Item.AppliedAt)
	assert.NotZero(t, got.Item.ReviewedByUserID)

	var name string
	require.NoError(t, env.db.QueryRow(
		`SELECT display_name FROM persons WHERE id = ?`, person).Scan(&name))
	assert.Equal(t, "Ada M. Member", name, "the approval must reach canonical data")

	// Every item terminal, so the request resolves.
	assert.Equal(t, int64(0), got.Request.PendingItemsCount)
}

// TestApprovalCarriesUnrelatedFieldsForward proves applying one field does not
// blank another nobody proposed changing.
func TestApprovalCarriesUnrelatedFieldsForward(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Keep Fields")
	_, err := env.db.Exec(`UPDATE persons SET call_sign = 'W3KEEP' WHERE id = ?`, person)
	require.NoError(t, err)

	reqID, itemID := requestWithItem(t, env, cookie, person, fmt.Sprintf(
		`{"operation":"person.display_name.set","proposed_value":"Kept Fields",
		  "target_kind":"person","target_id":%d}`, person), "apply-keep")
	require.True(t, decodeDecision(t, decide(t, env, cookie, reqID, itemID, `{"decision":"approved"}`)).Applied)

	var name, callSign string
	require.NoError(t, env.db.QueryRow(
		`SELECT display_name, call_sign FROM persons WHERE id = ?`, person).Scan(&name, &callSign))
	assert.Equal(t, "Kept Fields", name)
	assert.Equal(t, "W3KEEP", callSign, "an unrelated field must survive the approval")
}

// TestRejectionChangesNothing proves the negative case.
func TestRejectionChangesNothing(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Rejected Member")

	before := snapshotCanonical(t, env)
	reqID, itemID := requestWithItem(t, env, cookie, person, fmt.Sprintf(
		`{"operation":"person.display_name.set","proposed_value":"Never Applied",
		  "target_kind":"person","target_id":%d}`, person), "reject-1")

	got := decodeDecision(t, decide(t, env, cookie, reqID, itemID,
		`{"decision":"rejected","reason":"Could not verify with the member."}`))
	assert.False(t, got.Applied)
	assert.Equal(t, "rejected", got.Item.Status)
	assert.Equal(t, "Could not verify with the member.", got.Item.DecisionReason)
	assert.Empty(t, got.Item.AppliedAt, "a rejected item is never applied")

	assert.Equal(t, before, snapshotCanonical(t, env))
	var name string
	require.NoError(t, env.db.QueryRow(`SELECT display_name FROM persons WHERE id = ?`, person).Scan(&name))
	assert.Equal(t, "Rejected Member", name)
}

// TestRejectionRequiresAReason keeps a member entitled to know why.
func TestRejectionRequiresAReason(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Reasonless")

	reqID, itemID := requestWithItem(t, env, cookie, person,
		`{"operation":"other","proposed_value":"Something"}`, "reason-1")

	resp := decide(t, env, cookie, reqID, itemID, `{"decision":"rejected","reason":"  "}`)
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
}

// TestUnsupportedItemCannotBeApprovedThroughTheAPI proves the escape hatch
// stays closed at the transport too, not only in the database.
func TestUnsupportedItemCannotBeApprovedThroughTheAPI(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Honorary Hopeful")

	before := snapshotCanonical(t, env)
	reqID, itemID := requestWithItem(t, env, cookie, person,
		`{"operation":"other","proposed_value":"Please waive my dues."}`, "other-approve")

	resp := decide(t, env, cookie, reqID, itemID, `{"decision":"approved"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode,
		"an unsupported suggestion has no adapter and must not be approvable")

	// Nothing recorded, nothing applied.
	var status string
	require.NoError(t, env.db.QueryRow(
		`SELECT status FROM member_change_request_items WHERE id = ?`, itemID).Scan(&status))
	assert.Equal(t, "pending", status, "a refused approval must not record a decision")
	assert.Equal(t, before, snapshotCanonical(t, env))

	// It can still be rejected, which is how an officer clears the queue.
	resp = decide(t, env, cookie, reqID, itemID,
		`{"decision":"rejected","reason":"Honorary status is a board decision."}`)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestStaleTargetIsAConflictWithNoPartialApplication is the transactional
// claim: when the resource moved since the request was recorded, the approval
// is refused and NOTHING is written — not the decision, not the change.
func TestStaleTargetIsAConflictWithNoPartialApplication(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Moving Target")

	// The submitter saw version 1.
	reqID, itemID := requestWithItem(t, env, cookie, person, fmt.Sprintf(
		`{"operation":"person.display_name.set","proposed_value":"Stale Proposal",
		  "target_kind":"person","target_id":%d,"target_version":1}`, person), "stale-1")

	// An officer changed the person in the meantime.
	_, err := env.db.Exec(
		`UPDATE persons SET display_name = 'Changed Meanwhile', version = version + 1 WHERE id = ?`, person)
	require.NoError(t, err)

	resp := decide(t, env, cookie, reqID, itemID, `{"decision":"approved"}`)
	assert.Equal(t, http.StatusPreconditionFailed, resp.StatusCode,
		"a stale target must return the standard conflict")

	var status string
	var appliedAt sql.NullString
	require.NoError(t, env.db.QueryRow(
		`SELECT status, applied_at FROM member_change_request_items WHERE id = ?`, itemID).
		Scan(&status, &appliedAt))
	assert.Equal(t, "pending", status, "the decision must have rolled back with the apply")
	assert.False(t, appliedAt.Valid)

	var name string
	require.NoError(t, env.db.QueryRow(`SELECT display_name FROM persons WHERE id = ?`, person).Scan(&name))
	assert.Equal(t, "Changed Meanwhile", name, "the officer's change must survive")
}

// TestApprovalReplaysIdempotently proves a retried approval returns the
// recorded outcome instead of applying twice.
func TestApprovalReplaysIdempotently(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Replay Member")

	reqID, itemID := requestWithItem(t, env, cookie, person, fmt.Sprintf(
		`{"operation":"contact_method.add","proposed_value":"email:new@example.test",
		  "target_kind":"person","target_id":%d}`, person), "replay-1")

	first := decodeDecision(t, decide(t, env, cookie, reqID, itemID, `{"decision":"approved"}`))
	require.True(t, first.Applied)

	second := decodeDecision(t, decide(t, env, cookie, reqID, itemID, `{"decision":"approved"}`))
	assert.True(t, second.Replay, "a repeated approval must report a replay")
	assert.False(t, second.Applied, "a replay must not apply again")
	assert.Equal(t, first.Item.AppliedResourceID, second.Item.AppliedResourceID)

	var contacts int
	require.NoError(t, env.db.QueryRow(
		`SELECT count(*) FROM contact_methods WHERE person_id = ?`, person).Scan(&contacts))
	assert.Equal(t, 1, contacts, "a replayed approval must not create a second contact method")
}

// TestChangingADecisionIsRefused keeps the record honest.
func TestChangingADecisionIsRefused(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Decided Once")

	reqID, itemID := requestWithItem(t, env, cookie, person,
		`{"operation":"other","proposed_value":"x"}`, "decided-1")

	require.Equal(t, http.StatusOK,
		decide(t, env, cookie, reqID, itemID, `{"decision":"rejected","reason":"No."}`).StatusCode)

	resp := decide(t, env, cookie, reqID, itemID, `{"decision":"needs_verification"}`)
	assert.Equal(t, http.StatusConflict, resp.StatusCode,
		"a decided item must not be quietly re-decided")
}

// TestSensitiveApprovalRequiresAVerificationNote enforces the policy table.
func TestSensitiveApprovalRequiresAVerificationNote(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Call Sign Member")

	// person.call_sign.set is sensitive by policy, even though the submitter
	// declared nothing.
	reqID, itemID := requestWithItem(t, env, cookie, person, fmt.Sprintf(
		`{"operation":"person.call_sign.set","proposed_value":"W3NEW",
		  "target_kind":"person","target_id":%d}`, person), "sensitive-1")

	resp := decide(t, env, cookie, reqID, itemID, `{"decision":"approved"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode,
		"a sensitive approval must state how it was verified")

	var callSign sql.NullString
	require.NoError(t, env.db.QueryRow(`SELECT call_sign FROM persons WHERE id = ?`, person).Scan(&callSign))
	assert.False(t, callSign.Valid, "the refused approval must not have applied")

	got := decodeDecision(t, decide(t, env, cookie, reqID, itemID,
		`{"decision":"approved","verification_note":"Called the published number back."}`))
	assert.True(t, got.Applied)
	assert.Equal(t, "Called the published number back.", got.Item.VerificationNote)

	require.NoError(t, env.db.QueryRow(`SELECT call_sign FROM persons WHERE id = ?`, person).Scan(&callSign))
	assert.Equal(t, "W3NEW", callSign.String)
}

// TestSubmitterCannotDowngradeSensitivity proves the policy floor holds: a
// submitter may raise a class but never lower it.
func TestSubmitterCannotDowngradeSensitivity(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Downgrade Attempt")

	reqID, itemID := requestWithItem(t, env, cookie, person, fmt.Sprintf(
		`{"operation":"person.call_sign.set","proposed_value":"W3DOWN",
		  "target_kind":"person","target_id":%d,"sensitivity":"ordinary"}`, person), "downgrade-1")

	resp := decide(t, env, cookie, reqID, itemID, `{"decision":"approved"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode,
		"declaring an item ordinary must not bypass the policy floor")
}

// TestSelfReviewOfASensitiveItemIsRefused proves the requester cannot approve
// their own sensitive change.
func TestSelfReviewOfASensitiveItemIsRefused(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Self Reviewer")

	// A member-submitted request whose requester is the signed-in user.
	res, err := env.db.Exec(`
		INSERT INTO member_change_requests (source, status, requester_user_id, target_person_id,
			summary, submitted_at)
		VALUES ('member', 'submitted', 1, ?, 'I changed my call sign.', '2026-08-09T12:00:00.000Z')`, person)
	require.NoError(t, err)
	reqID, err := res.LastInsertId()
	require.NoError(t, err)

	res, err = env.db.Exec(`
		INSERT INTO member_change_request_items (request_id, ordinal, operation, proposed_value,
			target_kind, target_id, sensitivity)
		VALUES (?, 0, 'person.call_sign.set', 'W3SELF', 'person', ?, 'ordinary')`, reqID, person)
	require.NoError(t, err)
	itemID, err := res.LastInsertId()
	require.NoError(t, err)

	resp := decide(t, env, cookie, reqID, itemID,
		`{"decision":"approved","verification_note":"I am sure."}`)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"the requester must not approve their own sensitive item")

	var callSign sql.NullString
	require.NoError(t, env.db.QueryRow(`SELECT call_sign FROM persons WHERE id = ?`, person).Scan(&callSign))
	assert.False(t, callSign.Valid)
}

// TestPreferenceApprovalAppendsWithMemberRequestSource proves preference
// history records how the decision arrived, and appends rather than rewrites.
func TestPreferenceApprovalAppendsWithMemberRequestSource(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Sharing Member")

	// An existing officer-sourced preference, which must survive.
	_, err := env.db.Exec(`
		INSERT INTO acs_ares_sharing_events (person_id, participates, source, effective_at)
		VALUES (?, 0, 'officer', '2026-01-01T00:00:00.000Z')`, person)
	require.NoError(t, err)

	reqID, itemID := requestWithItem(t, env, cookie, person, fmt.Sprintf(
		`{"operation":"sharing_pref.acs_ares.set","proposed_value":"true",
		  "target_kind":"person","target_id":%d}`, person), "pref-1")

	got := decodeDecision(t, decide(t, env, cookie, reqID, itemID,
		`{"decision":"approved","verification_note":"Confirmed with the member at the meeting."}`))
	require.True(t, got.Applied)

	rows, err := env.db.Query(
		`SELECT participates, source FROM acs_ares_sharing_events WHERE person_id = ? ORDER BY id`, person)
	require.NoError(t, err)
	defer rows.Close()

	type ev struct {
		participates int
		source       string
	}
	var events []ev
	for rows.Next() {
		var e ev
		require.NoError(t, rows.Scan(&e.participates, &e.source))
		events = append(events, e)
	}
	require.NoError(t, rows.Err())

	require.Len(t, events, 2, "an approval must append, not rewrite the history")
	assert.Equal(t, ev{0, "officer"}, events[0], "the earlier decision must survive intact")
	assert.Equal(t, ev{1, "member_request"}, events[1],
		"the applied preference must record that it came from a reviewed request")
}

// TestMixedDecisionsResolveOnlyWhenEveryItemIsTerminal proves per-item review.
func TestMixedDecisionsResolveOnlyWhenEveryItemIsTerminal(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Mixed Member")

	body := fmt.Sprintf(`{"source":"officer_phone","target_person_id":%d,
		"summary":"Two corrections and one question.",
		"items":[
			{"operation":"person.display_name.set","proposed_value":"Mixed M. Member",
			 "target_kind":"person","target_id":%d},
			{"operation":"contact_method.add","proposed_value":"phone:814-555-0143"},
			{"operation":"other","proposed_value":"Also asked about dues."}
		]}`, person, person)
	created := decodeRequest(t, createRequest(t, env, cookie, body, "mixed-1"))
	require.Len(t, created.Items, 3)

	// Approve the first.
	got := decodeDecision(t, decide(t, env, cookie, created.ID, created.Items[0].ID, `{"decision":"approved"}`))
	assert.True(t, got.Applied)
	assert.Equal(t, int64(2), got.Request.PendingItemsCount)
	assert.Equal(t, "in_review", got.Request.Status)

	// Hold the second.
	got = decodeDecision(t, decide(t, env, cookie, created.ID, created.Items[1].ID,
		`{"decision":"needs_verification","reason":"Ask the member to confirm the number."}`))
	assert.False(t, got.Applied)
	assert.Equal(t, int64(1), got.Request.PendingItemsCount)

	// Reject the third.
	got = decodeDecision(t, decide(t, env, cookie, created.ID, created.Items[2].ID,
		`{"decision":"rejected","reason":"Dues questions go to the treasurer."}`))
	assert.Equal(t, int64(0), got.Request.PendingItemsCount)
	assert.Equal(t, "resolved", got.Request.Status)

	// The approved one applied; the other two did not.
	var name string
	require.NoError(t, env.db.QueryRow(`SELECT display_name FROM persons WHERE id = ?`, person).Scan(&name))
	assert.Equal(t, "Mixed M. Member", name)

	var contacts int
	require.NoError(t, env.db.QueryRow(
		`SELECT count(*) FROM contact_methods WHERE person_id = ?`, person).Scan(&contacts))
	assert.Zero(t, contacts, "a held item must not have been applied")

	// The decisions on the other two survive the third being decided.
	var held, rejected string
	require.NoError(t, env.db.QueryRow(
		`SELECT status FROM member_change_request_items WHERE id = ?`, created.Items[1].ID).Scan(&held))
	require.NoError(t, env.db.QueryRow(
		`SELECT status FROM member_change_request_items WHERE id = ?`, created.Items[2].ID).Scan(&rejected))
	assert.Equal(t, "needs_verification", held)
	assert.Equal(t, "rejected", rejected)
}

// TestContactUpdateAppliesInPlace proves the most common correction of all
// edits the existing contact rather than replacing it.
func TestContactUpdateAppliesInPlace(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Phone Member")
	contactID, contactVersion := seedContact(t, env, person, "phone", "814-555-0100")

	reqID, itemID := requestWithItem(t, env, cookie, person, fmt.Sprintf(
		`{"operation":"contact_method.update","proposed_value":"phone:814-555-0199",
		  "target_kind":"contact_method","target_id":%d,"target_version":%d}`,
		contactID, contactVersion), "update-1")

	got := decodeDecision(t, decide(t, env, cookie, reqID, itemID, `{"decision":"approved"}`))
	require.True(t, got.Applied)
	assert.Equal(t, contactID, got.Item.AppliedResourceID, "the same contact row must be edited")

	var value, norm string
	var count int
	require.NoError(t, env.db.QueryRow(
		`SELECT value_raw, value_norm FROM contact_methods WHERE id = ?`, contactID).Scan(&value, &norm))
	require.NoError(t, env.db.QueryRow(
		`SELECT count(*) FROM contact_methods WHERE person_id = ?`, person).Scan(&count))

	assert.Equal(t, "814-555-0199", value)
	assert.Equal(t, "8145550199", norm, "the comparison form must be normalized")
	assert.Equal(t, 1, count, "an update must not leave a second contact behind")
}

// TestApprovalRequiresConfirmation proves the review endpoint uses the generic
// control from bcars-portal-6q6.1 rather than inventing its own.
func TestApprovalRequiresConfirmation(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Unconfirmed Member")

	reqID, itemID := requestWithItem(t, env, cookie, person, fmt.Sprintf(
		`{"operation":"person.display_name.set","proposed_value":"Should Not Apply",
		  "target_kind":"person","target_id":%d}`, person), "confirm-1")

	resp := doWithHeaders(t, env, http.MethodPost, decidePath(reqID, itemID), cookie,
		`{"decision":"approved"}`, map[string]string{httpapi.ConfirmHeader: "false"})
	assert.Equal(t, http.StatusPreconditionRequired, resp.StatusCode,
		"applying a reviewed item is consequential and must be confirmed")

	var name string
	require.NoError(t, env.db.QueryRow(`SELECT display_name FROM persons WHERE id = ?`, person).Scan(&name))
	assert.Equal(t, "Unconfirmed Member", name)
}

// TestReviewDeniedWithoutTheReviewCapability proves capture and review are
// separate authorities.
func TestReviewDeniedWithoutTheReviewCapability(t *testing.T) {
	env := setupAuthzTest(t, "acs_coordinator")
	cookie := env.signIn(t)

	resp := decide(t, env, cookie, 1, 1, `{"decision":"approved"}`)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// TestDecisionIsAudited proves the event names the item and the outcome.
func TestDecisionIsAudited(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Audited Decision")

	reqID, itemID := requestWithItem(t, env, cookie, person, fmt.Sprintf(
		`{"operation":"person.display_name.set","proposed_value":"Audited Applied",
		  "target_kind":"person","target_id":%d}`, person), "audit-decide")
	require.True(t, decodeDecision(t, decide(t, env, cookie, reqID, itemID, `{"decision":"approved"}`)).Applied)

	events := env.auditEvents(t, "change_request.item.decide")
	require.NotEmpty(t, events)
	assert.Equal(t, "success", events[0].Outcome)
	assert.Equal(t, "change_request_item", events[0].ResourceKind.String)
	assert.Equal(t, itemID, events[0].ResourceID.Int64)

	// The proposed value must not be copied into the audit trail.
	for _, e := range events {
		assert.NotContains(t, e.Detail.String, "Audited Applied",
			"an audit event must not copy the private value it decided")
	}
}

// TestItemMustBelongToTheRequestInThePath keeps ids from being interchangeable.
func TestItemMustBelongToTheRequestInThePath(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)
	person := seedPersonForRequest(t, env, "Path Member")

	reqA, _ := requestWithItem(t, env, cookie, person, `{"operation":"other","proposed_value":"a"}`, "path-a")
	_, itemB := requestWithItem(t, env, cookie, person, `{"operation":"other","proposed_value":"b"}`, "path-b")

	resp := decide(t, env, cookie, reqA, itemB, `{"decision":"rejected","reason":"No."}`)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestPolicyTablesCoverEveryOperation is the checked-in-artifact guard: every
// supported operation must have both a sensitivity floor and an adapter
// decision, so adding an operation without deciding either fails here rather
// than at runtime.
func TestPolicyTablesCoverEveryOperation(t *testing.T) {
	for _, op := range changerequests.SupportedOperations {
		_, hasSensitivity := changerequests.MinimumSensitivity[op]
		assert.True(t, hasSensitivity, "operation %s has no sensitivity floor", op)

		_, hasAdapter := changerequests.Adapters[op]
		assert.True(t, hasAdapter, "operation %s has no adapter decision", op)
	}

	assert.False(t, changerequests.CanApply(changerequests.OpOther),
		"the unsupported escape hatch must never be appliable")
	assert.True(t, changerequests.CanApply("person.call_sign.set"))
}

// TestFailedApplyRollsBackTheDecision proves step 4 failing undoes step 3: the
// adapter runs, refuses, and the decision recorded moments earlier is gone.
//
// A duplicate call sign is the reachable way to make an adapter fail after the
// decision is written — persons.call_sign carries a unique index, so the
// domain's own constraint does the refusing.
func TestFailedApplyRollsBackTheDecision(t *testing.T) {
	env := setupAuthzTest(t, "secretary")
	cookie := env.signIn(t)

	holder := seedPersonForRequest(t, env, "Call Sign Holder")
	_, err := env.db.Exec(`UPDATE persons SET call_sign = 'W3TAKEN' WHERE id = ?`, holder)
	require.NoError(t, err)
	claimant := seedPersonForRequest(t, env, "Call Sign Claimant")

	reqID, itemID := requestWithItem(t, env, cookie, claimant, fmt.Sprintf(
		`{"operation":"person.call_sign.set","proposed_value":"W3TAKEN",
		  "target_kind":"person","target_id":%d}`, claimant), "dup-callsign")

	resp := decide(t, env, cookie, reqID, itemID,
		`{"decision":"approved","verification_note":"Member insists it is theirs."}`)
	assert.GreaterOrEqual(t, resp.StatusCode, 400, "a duplicate call sign must be refused")

	var status string
	var reviewedBy sql.NullInt64
	require.NoError(t, env.db.QueryRow(
		`SELECT status, reviewed_by FROM member_change_request_items WHERE id = ?`, itemID).
		Scan(&status, &reviewedBy))
	assert.Equal(t, "pending", status,
		"the decision must roll back with the failed apply, not survive it")
	assert.False(t, reviewedBy.Valid, "no reviewer may be recorded for a decision that rolled back")

	// And neither person's call sign moved.
	var claimantSign sql.NullString
	var holderSign string
	require.NoError(t, env.db.QueryRow(`SELECT call_sign FROM persons WHERE id = ?`, claimant).Scan(&claimantSign))
	require.NoError(t, env.db.QueryRow(`SELECT call_sign FROM persons WHERE id = ?`, holder).Scan(&holderSign))
	assert.False(t, claimantSign.Valid)
	assert.Equal(t, "W3TAKEN", holderSign)
}
