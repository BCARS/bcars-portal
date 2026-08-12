package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/authn"
	"github.com/bcars/bcars-portal/internal/domain/authz"
)

// Member self-service: grant-bound profile reads and authenticated suggestions
// (bcars-portal-4ux.6).
//
// The property these tests exist to hold is a NEGATIVE one, and negatives are
// the ones that rot quietly: submitting a suggestion about another person must
// teach the submitter nothing about that person. Several tests below therefore
// assert on the exact bytes of a response rather than on a status code, because
// "did not leak" is not visible in a status code.

type apiMemberProfile struct {
	PersonID    int64  `json:"person_id"`
	DisplayName string `json:"display_name"`
	CallSign    string `json:"call_sign"`
	AccessKind  string `json:"access_kind"`
	BaseType    string `json:"base_type"`
	Lifecycle   string `json:"lifecycle"`
	Standing    *struct {
		Status      string `json:"status"`
		PaidThrough string `json:"paid_through"`
		AsOf        string `json:"as_of"`
	} `json:"dues_standing"`
	Contacts []struct {
		ID         int64  `json:"id"`
		Kind       string `json:"kind"`
		Label      string `json:"label"`
		Value      string `json:"value"`
		Primary    bool   `json:"primary"`
		SharedWith string `json:"shared_with"`
		Version    int64  `json:"version"`
	} `json:"contacts"`
}

type apiMemberRequest struct {
	ID            int64  `json:"id"`
	Status        string `json:"status"`
	AboutPersonID int64  `json:"about_person_id"`
	AboutName     string `json:"about_name"`
	AboutCallSign string `json:"about_call_sign"`
	Summary       string `json:"summary"`
	SubmittedAt   string `json:"submitted_at"`
	WithdrawnAt   string `json:"withdrawn_at"`
	Items         []struct {
		ID             int64  `json:"id"`
		Operation      string `json:"operation"`
		ProposedValue  string `json:"proposed_value"`
		Status         string `json:"status"`
		DecisionReason string `json:"decision_reason"`
	} `json:"items"`
	Version int64 `json:"version"`
}

// memberEnv is an officer plus two member accounts, built through the real
// provisioning API so the accounts under test are the ones officers create.
type memberEnv struct {
	*authzEnv
	officer *http.Cookie

	// full holds a grant to fullPerson; associate holds a grant to nothing at
	// all, which is the Associate case the corrected plan cares about.
	full          *http.Cookie
	fullUserID    int64
	fullPersonID  int64
	associate     *http.Cookie
	associateUser int64

	// strangerPersonID is a real person, granted to a THIRD account. It is the
	// control, and the reason it is granted to someone rather than to nobody:
	// a record nobody can see is also unreachable when the grant join is
	// broken, so an ungranted-by-anyone control cannot tell a working
	// authorization check from a missing one. Everything the full member and
	// the Associate can learn about it must be identical to what they can
	// learn about a person id that does not exist at all.
	strangerPersonID int64
	neighbour        *http.Cookie
	neighbourUserID  int64
}

const memberPassword = "membersownpassword12"

// addMember creates an account through the officer API, sets a password
// directly, and signs it in. Provisioning deliberately leaves no password
// (ADR-0012); onboarding through recovery is bcars-portal-4ux.5's subject, not
// this file's, so it is short-circuited here.
func (e *memberEnv) addMember(t *testing.T, email string) (*http.Cookie, int64) {
	t.Helper()

	resp := provision(t, e.authzEnv, e.officer, email)
	require.Equal(t, http.StatusOK, resp.StatusCode, readAll(t, resp))
	acct := decodeAccount(t, resp)

	hash, err := authn.HashPassword(memberPassword, nil, authn.DefaultParams())
	require.NoError(t, err)
	_, err = e.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, acct.UserID)
	require.NoError(t, err)

	body := fmt.Sprintf(`{"email":%q,"password":%q}`, email, memberPassword)
	signIn, err := e.ts.Client().Post(e.ts.URL+"/api/v1/sessions", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer signIn.Body.Close()
	require.Equal(t, http.StatusOK, signIn.StatusCode)

	for _, c := range signIn.Cookies() {
		if c.Name == "bcars_session" {
			return c, acct.UserID
		}
	}
	t.Fatalf("no session cookie for %s", email)
	return nil, 0
}

func (e *memberEnv) createPerson(t *testing.T, name, callSign, baseType string) int64 {
	t.Helper()
	resp := e.do(t, http.MethodPost, "/api/v1/members", e.officer,
		fmt.Sprintf(`{"display_name":%q,"sort_name":%q,"call_sign":%q,"base_type":%q}`,
			name, name, callSign, baseType))
	require.Equal(t, http.StatusCreated, resp.StatusCode, readAll(t, resp))

	var body struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.NotZero(t, body.ID)
	return body.ID
}

func setupMemberSelfService(t *testing.T) *memberEnv {
	t.Helper()
	base := setupAuthzTest(t, "secretary")
	e := &memberEnv{authzEnv: base}
	e.officer = base.signIn(t)

	e.fullPersonID = e.createPerson(t, "Dale Rutherford", "W3DLR", "full")
	e.strangerPersonID = e.createPerson(t, "Marguerite Ashby", "W3MGA", "full")

	e.full, e.fullUserID = e.addMember(t, "dale@bcars.example")
	e.associate, e.associateUser = e.addMember(t, "associate@bcars.example")
	e.neighbour, e.neighbourUserID = e.addMember(t, "marguerite@bcars.example")

	resp := grantRecord(t, base, e.officer, e.fullUserID, e.fullPersonID, "self")
	require.Equal(t, http.StatusOK, resp.StatusCode, readAll(t, resp))

	// The stranger record belongs to somebody, just not to the two accounts
	// under test.
	resp = grantRecord(t, base, e.officer, e.neighbourUserID, e.strangerPersonID, "self")
	require.Equal(t, http.StatusOK, resp.StatusCode, readAll(t, resp))

	return e
}

func (e *memberEnv) submit(t *testing.T, cookie *http.Cookie, key, body string) *http.Response {
	t.Helper()
	return doWithHeaders(t, e.authzEnv, http.MethodPost, "/api/v1/me/change-requests", cookie, body,
		map[string]string{"Idempotency-Key": key})
}

func decodeMemberRequest(t *testing.T, resp *http.Response) apiMemberRequest {
	t.Helper()
	var r apiMemberRequest
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&r))
	return r
}

// TestMemberReadsOnlyGrantedRecords is the read half of the bead: profile
// access is the grant and nothing else.
func TestMemberReadsOnlyGrantedRecords(t *testing.T) {
	e := setupMemberSelfService(t)

	resp := e.do(t, http.MethodGet, "/api/v1/me/records", e.full, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, readAll(t, resp))

	var listed []apiMemberProfile
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&listed))
	require.Len(t, listed, 1, "exactly the one record this account was granted")
	assert.Equal(t, e.fullPersonID, listed[0].PersonID)
	assert.Equal(t, "Dale Rutherford", listed[0].DisplayName)

	// The Associate holds the capability and was granted nothing, so they see
	// nothing. An empty list, not an error: having no records is a real state.
	resp = e.do(t, http.MethodGet, "/api/v1/me/records", e.associate, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var none []apiMemberProfile
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&none))
	assert.Empty(t, none)

	// A record granted to SOMEBODY ELSE and a record that does not exist are
	// the same answer, byte for byte. Anything else makes the id parameter a
	// membership oracle — and a record granted to nobody would not test the
	// grant join at all, since an empty join and a broken one look alike.
	ungranted := e.do(t, http.MethodGet,
		fmt.Sprintf("/api/v1/me/records/%d", e.strangerPersonID), e.full, "")
	missing := e.do(t, http.MethodGet, "/api/v1/me/records/999999", e.full, "")
	assert.Equal(t, http.StatusNotFound, ungranted.StatusCode,
		"one member's grant must not become another member's read")
	assert.Equal(t, http.StatusNotFound, missing.StatusCode)
	assert.Equal(t, readAll(t, missing), readAll(t, ungranted),
		"an existing record you cannot see must be indistinguishable from one that does not exist")

	// The account that WAS granted it reads it, so the refusals above are
	// authorization working rather than the record being unreachable.
	granted := e.do(t, http.MethodGet,
		fmt.Sprintf("/api/v1/me/records/%d", e.strangerPersonID), e.neighbour, "")
	require.Equal(t, http.StatusOK, granted.StatusCode, readAll(t, granted))
	assert.Contains(t, readAll(t, granted), "Marguerite Ashby")
}

// TestMemberProfileCarriesNoAdministrativeData checks what the safe read model
// leaves out. The member holds neither member.read nor dues.read, and the
// payload must not hand them the effect of either.
func TestMemberProfileCarriesNoAdministrativeData(t *testing.T) {
	e := setupMemberSelfService(t)

	// Give the record something an officer would see: a treasury payment and
	// an officer note.
	_, err := e.db.Exec(
		`INSERT INTO notes (subject_kind, subject_id, category, visibility, body, author_id, source)
		 VALUES ('person', ?, 'general', 'officer', 'Officer only: chased at the meeting', 1, 'manual')`,
		e.fullPersonID)
	require.NoError(t, err)

	resp := e.do(t, http.MethodGet,
		fmt.Sprintf("/api/v1/me/records/%d", e.fullPersonID), e.full, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	raw := readAll(t, resp)

	var profile apiMemberProfile
	require.NoError(t, json.Unmarshal([]byte(raw), &profile))
	assert.Equal(t, "Dale Rutherford", profile.DisplayName)
	assert.Equal(t, "self", profile.AccessKind)

	for _, leak := range []string{
		"Officer only", "amount_cents", "treasurer", "audit", "payment", "note",
	} {
		assert.NotContains(t, strings.ToLower(raw), strings.ToLower(leak),
			"the member profile must not carry %q", leak)
	}
}

// TestAssociateSuggestsAboutAnotherPersonAndLearnsNothing is the central
// non-disclosure test. An Associate with no grant to anyone may file a
// suggestion about another member, and the act of doing so must not confirm
// that the member exists.
func TestAssociateSuggestsAboutAnotherPersonAndLearnsNothing(t *testing.T) {
	e := setupMemberSelfService(t)

	resp := e.submit(t, e.associate, "assoc-1", `{
		"about_name": "Marguerite Ashby",
		"about_call_sign": "W3MGA",
		"stated_relationship": "We operate the same net",
		"summary": "Her call sign is printed wrong in the newsletter.",
		"items": [{"operation": "other"}]
	}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode, readAll(t, resp))
	raw := readAll(t, resp)

	var created apiMemberRequest
	require.NoError(t, json.Unmarshal([]byte(raw), &created))
	assert.Equal(t, "submitted", created.Status)
	assert.Equal(t, "Marguerite Ashby", created.AboutName,
		"the submitter's own words are echoed back to them")
	assert.Zero(t, created.AboutPersonID,
		"no canonical id: learning the club's id for a person is learning they are on file")

	// Nothing in the response confirms the person, or offers a candidate.
	for _, leak := range []string{
		fmt.Sprintf(`"person_id":%d`, e.strangerPersonID),
		"target_person_id", "match", "candidate", "display_name", "existing",
	} {
		assert.NotContains(t, raw, leak, "submission must not return %q", leak)
	}

	// And it grants nothing. The Associate still cannot read that record.
	resp = e.do(t, http.MethodGet,
		fmt.Sprintf("/api/v1/me/records/%d", e.strangerPersonID), e.associate, "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode,
		"submitting a suggestion about someone must not grant access to them")

	resp = e.do(t, http.MethodGet, "/api/v1/me/records", e.associate, "")
	assert.Equal(t, "[]", strings.TrimSpace(readAll(t, resp)),
		"and must not add them to the submitter's own records")
}

// TestSuggestingAboutAnUngrantedRecordRevealsNothing closes the other door: a
// caller who guesses an id must not be able to tell a real one from a fiction.
func TestSuggestingAboutAnUngrantedRecordRevealsNothing(t *testing.T) {
	e := setupMemberSelfService(t)

	body := func(personID int64) string {
		return fmt.Sprintf(`{
			"about_person_id": %d,
			"summary": "Please fix the call sign.",
			"items": [{"operation": "other"}]
		}`, personID)
	}

	real := e.submit(t, e.associate, "probe-real", body(e.strangerPersonID))
	fake := e.submit(t, e.associate, "probe-fake", body(999999))

	require.Equal(t, http.StatusUnprocessableEntity, real.StatusCode)
	require.Equal(t, http.StatusUnprocessableEntity, fake.StatusCode)
	assert.Equal(t, readAll(t, fake), readAll(t, real),
		"a real record you cannot see and an id that never existed must answer identically")
}

// TestFullMemberSubmitsAboutOwnRecord covers the ordinary case, including the
// one place a member may name a resource: their own contact method.
func TestFullMemberSubmitsAboutOwnRecord(t *testing.T) {
	e := setupMemberSelfService(t)

	resp := e.do(t, http.MethodPost,
		fmt.Sprintf("/api/v1/members/%d/contact-methods", e.fullPersonID), e.officer,
		`{"kind":"email","value_raw":"dale.old@example.test","label":"home"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode, readAll(t, resp))

	resp = e.do(t, http.MethodGet,
		fmt.Sprintf("/api/v1/me/records/%d", e.fullPersonID), e.full, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var profile apiMemberProfile
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&profile))
	require.Len(t, profile.Contacts, 1, "a member must see their own contact methods to correct them")
	contact := profile.Contacts[0]
	assert.Equal(t, "dale.old@example.test", contact.Value)

	resp = e.submit(t, e.full, "self-1", fmt.Sprintf(`{
		"about_person_id": %d,
		"summary": "My email address changed.",
		"items": [{
			"operation": "contact_method.update",
			"proposed_value": "dale.new@example.test",
			"target_kind": "contact_method",
			"target_id": %d,
			"target_version": %d
		}]
	}`, e.fullPersonID, contact.ID, contact.Version))
	require.Equal(t, http.StatusCreated, resp.StatusCode, readAll(t, resp))

	created := decodeMemberRequest(t, resp)
	assert.Equal(t, e.fullPersonID, created.AboutPersonID,
		"a record the caller may see is echoed back with its id")
	require.Len(t, created.Items, 1)
	assert.Equal(t, "pending", created.Items[0].Status)

	// The canonical record has not moved. That is the whole premise of the
	// request model: nothing changes until an officer approves it.
	var stored string
	require.NoError(t, e.db.QueryRow(
		`SELECT value_raw FROM contact_methods WHERE id = ?`, contact.ID).Scan(&stored))
	assert.Equal(t, "dale.old@example.test", stored,
		"submitting a correction must not change canonical data")
}

// TestMemberCannotAimAnItemAtAnotherRecord stops the obvious escalation: a
// member attaching a guessed contact-method id to a suggestion about someone
// else.
func TestMemberCannotAimAnItemAtAnotherRecord(t *testing.T) {
	e := setupMemberSelfService(t)

	resp := e.do(t, http.MethodPost,
		fmt.Sprintf("/api/v1/members/%d/contact-methods", e.strangerPersonID), e.officer,
		`{"kind":"phone","value_raw":"+15555550101"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode, readAll(t, resp))
	var contact struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&contact))

	// Pointed at a stranger's contact method while describing them by name.
	resp = e.submit(t, e.associate, "aim-1", fmt.Sprintf(`{
		"about_name": "Marguerite Ashby",
		"summary": "Her number is wrong.",
		"items": [{
			"operation": "contact_method.update",
			"proposed_value": "+15555550199",
			"target_kind": "contact_method",
			"target_id": %d
		}]
	}`, contact.ID))
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode,
		"a suggestion about someone else must not name a target resource")

	// And pointed at a stranger's contact method while claiming it is on the
	// caller's own record.
	resp = e.submit(t, e.full, "aim-2", fmt.Sprintf(`{
		"about_person_id": %d,
		"summary": "Fix this.",
		"items": [{
			"operation": "contact_method.update",
			"proposed_value": "+15555550199",
			"target_kind": "contact_method",
			"target_id": %d
		}]
	}`, e.fullPersonID, contact.ID))
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode,
		"an item may only name a resource that belongs to the record it is about")
}

// TestMemberTracksAndWithdrawsOnlyTheirOwnRequests covers the tracking half.
func TestMemberTracksAndWithdrawsOnlyTheirOwnRequests(t *testing.T) {
	e := setupMemberSelfService(t)

	resp := e.submit(t, e.full, "mine-1", fmt.Sprintf(`{
		"about_person_id": %d,
		"summary": "My name is spelled wrong.",
		"items": [{"operation": "person.display_name.set", "proposed_value": "Dale Rutherforde"}]
	}`, e.fullPersonID))
	require.Equal(t, http.StatusCreated, resp.StatusCode, readAll(t, resp))
	mine := decodeMemberRequest(t, resp)

	resp = e.submit(t, e.associate, "theirs-1", `{
		"about_name": "Someone Else",
		"summary": "Their address is out of date.",
		"items": [{"operation": "other"}]
	}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode, readAll(t, resp))
	theirs := decodeMemberRequest(t, resp)

	// Each member's list holds exactly their own.
	resp = e.do(t, http.MethodGet, "/api/v1/me/change-requests", e.full, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var listed []apiMemberRequest
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&listed))
	require.Len(t, listed, 1)
	assert.Equal(t, mine.ID, listed[0].ID)

	// Another member's request is not merely hidden from the list; it is not
	// readable by id, and it answers exactly as a request that never existed.
	notYours := e.do(t, http.MethodGet,
		fmt.Sprintf("/api/v1/me/change-requests/%d", theirs.ID), e.full, "")
	neverExisted := e.do(t, http.MethodGet, "/api/v1/me/change-requests/999999", e.full, "")
	assert.Equal(t, http.StatusNotFound, notYours.StatusCode)
	assert.Equal(t, http.StatusNotFound, neverExisted.StatusCode)
	assert.Equal(t, readAll(t, neverExisted), readAll(t, notYours),
		"another member's request must be indistinguishable from one that does not exist")

	// Withdrawing someone else's is equally refused.
	resp = e.do(t, http.MethodPost,
		fmt.Sprintf("/api/v1/me/change-requests/%d/withdrawal", theirs.ID), e.full, "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// Withdrawing your own works, and keeps the record rather than deleting it.
	resp = e.do(t, http.MethodPost,
		fmt.Sprintf("/api/v1/me/change-requests/%d/withdrawal", mine.ID), e.full, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, readAll(t, resp))
	withdrawn := decodeMemberRequest(t, resp)
	assert.Equal(t, "withdrawn", withdrawn.Status)
	assert.NotEmpty(t, withdrawn.WithdrawnAt)
	require.Len(t, withdrawn.Items, 1, "withdrawal retracts, it does not delete what was asked for")

	var count int
	require.NoError(t, e.db.QueryRow(
		`SELECT count(*) FROM member_change_request_items WHERE request_id = ?`, mine.ID).Scan(&count))
	assert.Equal(t, 1, count)
}

// TestWithdrawalIsRefusedOnceAnOfficerHasDecided keeps a member from erasing
// the stated reason for a change an officer already made.
func TestWithdrawalIsRefusedOnceAnOfficerHasDecided(t *testing.T) {
	e := setupMemberSelfService(t)

	resp := e.submit(t, e.full, "decided-1", fmt.Sprintf(`{
		"about_person_id": %d,
		"summary": "My name is spelled wrong.",
		"items": [{"operation": "person.display_name.set", "proposed_value": "Dale Rutherforde"}]
	}`, e.fullPersonID))
	require.Equal(t, http.StatusCreated, resp.StatusCode, readAll(t, resp))
	mine := decodeMemberRequest(t, resp)
	require.Len(t, mine.Items, 1)

	resp = e.do(t, http.MethodPost,
		fmt.Sprintf("/api/v1/change-requests/%d/items/%d/decision", mine.ID, mine.Items[0].ID),
		e.officer, `{"decision":"rejected","reason":"Spelling confirmed correct at the meeting."}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, readAll(t, resp))

	resp = e.do(t, http.MethodPost,
		fmt.Sprintf("/api/v1/me/change-requests/%d/withdrawal", mine.ID), e.full, "")
	assert.Equal(t, http.StatusConflict, resp.StatusCode,
		"a request an officer has begun deciding cannot be retracted")

	// The member can still see the decision and the reason for it.
	resp = e.do(t, http.MethodGet,
		fmt.Sprintf("/api/v1/me/change-requests/%d", mine.ID), e.full, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	read := decodeMemberRequest(t, resp)
	require.Len(t, read.Items, 1)
	assert.Equal(t, "rejected", read.Items[0].Status)
	assert.Contains(t, read.Items[0].DecisionReason, "Spelling confirmed",
		"a member is entitled to know why their suggestion was refused")
}

// TestRevokingAccessEndsProfileReadsButKeepsTheRequestTrail separates the two
// lifetimes the bead names: access is current state, a request is history.
func TestRevokingAccessEndsProfileReadsButKeepsTheRequestTrail(t *testing.T) {
	e := setupMemberSelfService(t)

	resp := e.submit(t, e.full, "trail-1", fmt.Sprintf(`{
		"about_person_id": %d,
		"summary": "My call sign changed.",
		"items": [{"operation": "person.call_sign.set", "proposed_value": "W3NEW"}]
	}`, e.fullPersonID))
	require.Equal(t, http.StatusCreated, resp.StatusCode, readAll(t, resp))
	filed := decodeMemberRequest(t, resp)

	resp = revokeRecord(t, e.authzEnv, e.officer, e.fullUserID, e.fullPersonID, "left the household")
	require.Equal(t, http.StatusOK, resp.StatusCode, readAll(t, resp))

	// The read ends at once, inside the session that is already open.
	resp = e.do(t, http.MethodGet,
		fmt.Sprintf("/api/v1/me/records/%d", e.fullPersonID), e.full, "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode,
		"revocation must take effect in an open session, not at next sign-in")

	// The request they filed survives, and is still theirs to read.
	resp = e.do(t, http.MethodGet,
		fmt.Sprintf("/api/v1/me/change-requests/%d", filed.ID), e.full, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, readAll(t, resp))
	kept := decodeMemberRequest(t, resp)
	assert.Equal(t, filed.ID, kept.ID)
	require.Len(t, kept.Items, 1, "revoking access must not erase what was asked for")

	// But the canonical link is no longer echoed, because they may no longer
	// see that record.
	assert.Zero(t, kept.AboutPersonID,
		"a record the caller may no longer see must stop being named back to them")

	var rows int
	require.NoError(t, e.db.QueryRow(
		`SELECT count(*) FROM member_change_requests WHERE id = ?`, filed.ID).Scan(&rows))
	assert.Equal(t, 1, rows, "the audit trail of the request is not deleted by revocation")
}

// TestTriageDoesNotLeakTheTargetBackToTheSubmitter closes the path a member
// suggestion actually acquires a canonical target through: an officer links it
// during triage.
//
// Submission itself cannot attach a target the caller may not see, so this is
// the only way the two can come apart — and it is the one that matters, because
// the officer's conclusion about WHO the suggestion concerned is exactly the
// fact the submitter must not learn.
func TestTriageDoesNotLeakTheTargetBackToTheSubmitter(t *testing.T) {
	e := setupMemberSelfService(t)

	resp := e.submit(t, e.associate, "triage-1", `{
		"about_name": "Marguerite Ashby",
		"summary": "Her call sign is printed wrong.",
		"items": [{"operation": "other"}]
	}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode, readAll(t, resp))
	filed := decodeMemberRequest(t, resp)
	require.Zero(t, filed.AboutPersonID)

	// The officer works out who it was about and links it.
	resp = doWithHeaders(t, e.authzEnv, http.MethodPost,
		fmt.Sprintf("/api/v1/change-requests/%d/target", filed.ID), e.officer,
		fmt.Sprintf(`{"target_person_id":%d}`, e.strangerPersonID),
		map[string]string{"If-Match": fmt.Sprintf("%q", fmt.Sprint(filed.Version))})
	require.Equal(t, http.StatusOK, resp.StatusCode, readAll(t, resp))

	// The submitter reads their own request back and still learns nothing.
	resp = e.do(t, http.MethodGet,
		fmt.Sprintf("/api/v1/me/change-requests/%d", filed.ID), e.associate, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	raw := readAll(t, resp)

	var read apiMemberRequest
	require.NoError(t, json.Unmarshal([]byte(raw), &read))
	assert.Zero(t, read.AboutPersonID,
		"the officer's triage conclusion must not be echoed to a submitter who cannot see that record")
	assert.NotContains(t, raw, "about_person_id",
		"the field is omitted entirely rather than sent as a zero the client might ignore")
	assert.Equal(t, "Marguerite Ashby", read.AboutName,
		"what the submitter typed is still their own to read")

	// The same is true of the list.
	resp = e.do(t, http.MethodGet, "/api/v1/me/change-requests", e.associate, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotContains(t, readAll(t, resp), fmt.Sprintf(`"about_person_id":%d`, e.strangerPersonID))

	// The officer, who may see it, does see the link.
	resp = e.do(t, http.MethodGet,
		fmt.Sprintf("/api/v1/change-requests/%d", filed.ID), e.officer, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, readAll(t, resp), fmt.Sprintf(`"target_person_id":%d`, e.strangerPersonID),
		"triage is not hidden from officers; it is hidden from the submitter")
}

// TestMemberSelfServiceRefusesAnonymousAndUnderprivileged covers the two
// negative authorization cases for every route in this surface.
func TestMemberSelfServiceRefusesAnonymousAndUnderprivileged(t *testing.T) {
	e := setupMemberSelfService(t)

	routes := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/v1/me/records", ""},
		{http.MethodGet, "/api/v1/me/records/1", ""},
		{http.MethodPost, "/api/v1/me/change-requests",
			`{"about_name":"Someone","summary":"x","items":[{"operation":"other"}]}`},
		{http.MethodGet, "/api/v1/me/change-requests", ""},
		{http.MethodGet, "/api/v1/me/change-requests/1", ""},
		{http.MethodPost, "/api/v1/me/change-requests/1/withdrawal", ""},
	}

	for _, rt := range routes {
		t.Run("anonymous "+rt.path, func(t *testing.T) {
			resp := e.do(t, rt.method, rt.path, nil, rt.body)
			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
				"an anonymous caller must not reach %s; there is no unauthenticated intake", rt.path)
		})
	}

	// An officer identity that was never provisioned as a member holds neither
	// member capability, and is refused rather than quietly served an empty
	// list.
	for _, rt := range routes {
		t.Run("officer without member role "+rt.path, func(t *testing.T) {
			resp := e.do(t, rt.method, rt.path, e.officer, rt.body)
			assert.Equal(t, http.StatusForbidden, resp.StatusCode,
				"%s must require a member capability", rt.path)
		})
	}
}

// TestNoAnonymousCorrectionIntakeIsRegistered holds the line the corrected plan
// drew: the public form is gone from the API, not merely undocumented.
func TestNoAnonymousCorrectionIntakeIsRegistered(t *testing.T) {
	e := setupMemberSelfService(t)

	// Every path a public form has ever been proposed at.
	for _, path := range []string{
		"/api/v1/corrections",
		"/api/v1/public/corrections",
		"/api/v1/public/change-requests",
		"/api/v1/change-requests/public",
	} {
		resp := e.do(t, http.MethodPost, path, nil,
			`{"summary":"anonymous","items":[{"operation":"other"}]}`)
		assert.Contains(t, []int{http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusUnauthorized},
			resp.StatusCode, "%s must not be an anonymous intake route", path)
	}

	// And the officer intake endpoint refuses the legacy source outright, so
	// the value surviving in the 0009 CHECK constraint stays unreachable.
	resp := doWithHeaders(t, e.authzEnv, http.MethodPost, "/api/v1/change-requests", e.officer,
		`{"source":"public","supplied_name":"Anon","summary":"x","items":[{"operation":"other"}]}`,
		map[string]string{"Idempotency-Key": "public-1"})
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode,
		"'public' is an inert legacy value, not an intake channel")

	var rows int
	require.NoError(t, e.db.QueryRow(
		`SELECT count(*) FROM member_change_requests WHERE source = 'public'`).Scan(&rows))
	assert.Zero(t, rows, "no route may create a public-source request")
}

// TestMemberCapabilityIsSubmitMember locks the rename in at the catalog, so a
// role seeded with the old code cannot silently hold nothing.
func TestMemberCapabilityIsSubmitMember(t *testing.T) {
	e := setupMemberSelfService(t)

	var codes []string
	rows, err := e.db.Query(
		`SELECT capability_code FROM role_capabilities WHERE role_code = 'member' ORDER BY capability_code`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var c string
		require.NoError(t, rows.Scan(&c))
		codes = append(codes, c)
	}
	require.NoError(t, rows.Err())

	assert.Contains(t, codes, "change_request.submit.member")
	assert.NotContains(t, codes, "change_request.submit.self",
		"the renamed capability must not linger alongside its replacement")

	_, known := authz.ByCode("change_request.submit.member")
	assert.True(t, known, "the capability catalog must name the code the routes require")
}
