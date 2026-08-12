package smoke

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Phase 3 completion smoke (bcars-portal-4ux.13).
//
// Everything here runs against the built binary started from a directory with
// no source, no go.mod, and no configuration (see start and requireOutsideRepo
// in harness_test.go). That matters more than usual for this phase: Phase 3
// added twelve templates, five of them in 4ux.10 and 4ux.12, and a template
// read from disk instead of the embedded filesystem would pass every package
// test and fail only here.
//
// The phase's whole subject is who may see and change what, so most of these
// assertions are about refusals. Four distinctions carry the phase:
//
//	a GRANT lets an account read one record;
//	a RELATIONSHIP is context and lets nobody read anything;
//	SUBMITTING a suggestion needs neither, only an authenticated member;
//	APPLYING one needs an officer capability and changes canonical data.
//
// Each is asserted against the running server rather than inferred from the
// packages that implement it.

const (
	p3MemberEmail = "phase3.member@bcars.test"
	p3MemberPass  = "phase3memberpassword1"
	p3AssocEmail  = "phase3.associate@bcars.test"
	p3AssocPass   = "phase3associatepass12"

	p3FullName   = "Dale Rutherford"
	p3SpouseName = "Marguerite Ashby"
	p3AssocName  = "Bob Associate"
	p3StrangerNm = "Never Granted Person"
)

func TestPhase3ReviewedCorrectionsSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("smoke test builds binaries and starts a server; skipped in -short")
	}

	e := start(t)
	e.requireReady()

	admin := e.consumeInvitation(e.bootstrapAdmin(), adminEmail, adminPass)

	// --- 1. The club's people, approved the way an officer approves them ---
	//
	// A new membership is 'pending'; directory eligibility requires an active
	// approved FULL membership, so approval runs through the real endpoint
	// rather than being written into the database.
	dale := e.createPerson(admin, p3FullName, "full")
	spouse := e.createPerson(admin, p3SpouseName, "full")
	assoc := e.createPerson(admin, p3AssocName, "associate")
	stranger := e.createPerson(admin, p3StrangerNm, "full")

	e.approveMembership(admin, dale, "full")
	e.approveMembership(admin, spouse, "full")
	e.approveMembership(admin, assoc, "associate")
	e.approveMembership(admin, stranger, "full")

	// --- 2. A relationship confers nothing ---
	//
	// Recorded BEFORE any grant, so what follows cannot be explained by the
	// relationship having been added late.
	relID := e.createRelationship(admin, dale, spouse, "spouse_partner", "Married thirty years.")

	// --- 3. Two member accounts: one Full, one Associate ---
	memberUser := e.provisionMember(admin, p3MemberEmail)
	assocUser := e.provisionMember(admin, p3AssocEmail)
	e.grantRecord(admin, memberUser, dale, "self")
	e.grantRecord(admin, assocUser, assoc, "self")

	member := e.onboardAndSignIn(p3MemberEmail, p3MemberPass)
	associate := e.onboardAndSignIn(p3AssocEmail, p3AssocPass)

	// --- 4. A grant reads one record; a relationship opens none ---
	resp := e.do(http.MethodGet, fmt.Sprintf("/api/v1/me/records/%d", dale), member, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, "the granted record must be readable")
	assert.Contains(t, readBody(resp), p3FullName)

	spouseRead := e.do(http.MethodGet, fmt.Sprintf("/api/v1/me/records/%d", spouse), member, "")
	missingRead := e.do(http.MethodGet, "/api/v1/me/records/999999", member, "")
	assert.Equal(t, http.StatusNotFound, spouseRead.StatusCode,
		"a recorded spouse must not open that spouse's record")
	assert.Equal(t, readBody(missingRead), readBody(spouseRead),
		"an ungranted record must be indistinguishable from one that does not exist")

	// The relationship is still on file; it simply bought nothing.
	require.NotZero(t, relID)
	grantRows := e.queryStrings(fmt.Sprintf(
		`SELECT id FROM member_access_grants WHERE person_id = %d AND revoked_at IS NULL`, spouse))
	assert.Empty(t, grantRows, "recording a relationship must create no access grant")

	// --- 5. Directory: Full members browse, Associates do not ---
	dir := e.do(http.MethodGet, "/member/directory", member, "")
	require.Equal(t, http.StatusOK, dir.StatusCode, "an eligible Full member must reach the directory")
	dirBody := readBody(dir)
	assert.Contains(t, dirBody, p3SpouseName, "the directory lists other members")
	assert.Contains(t, dirBody, "Not shared",
		"members sharing no contact details render the same as those withholding them")

	print := e.do(http.MethodGet, "/member/directory/print", member, "")
	require.Equal(t, http.StatusOK, print.StatusCode)
	assert.Contains(t, readBody(print), "Bedford County Amateur Radio Society",
		"the printed roster must identify the club")

	for _, path := range []string{"/member/directory", "/member/directory/print", "/api/v1/directory"} {
		refused := e.do(http.MethodGet, path, associate, "")
		assert.Equal(t, http.StatusNotFound, refused.StatusCode,
			"%s must refuse an Associate as though it did not exist", path)
		assert.NotContains(t, readBody(refused), p3SpouseName,
			"%s must render no listing to an Associate", path)
	}

	// --- 6. Both member kinds may suggest a correction about someone else ---
	//
	// The Associate cannot browse the directory and holds no grant on the
	// stranger's record, and still may say something is wrong with it. That is
	// the phase's deliberate asymmetry: suggesting is not reading.
	fullReq := e.submitSuggestion(member, "smoke-p3-full",
		`{"about_name":"Someone From The Tuesday Net","summary":"Their call sign is printed wrong.",`+
			`"items":[{"operation":"other"}]}`)
	assocReq := e.submitSuggestion(associate, "smoke-p3-assoc",
		`{"about_name":"Another Person Entirely","summary":"Their address changed.",`+
			`"items":[{"operation":"other"}]}`)
	require.NotZero(t, fullReq)
	require.NotZero(t, assocReq)

	// Neither submission touched canonical data or created a grant.
	assert.Empty(t, e.queryStrings(
		`SELECT id FROM member_access_grants WHERE reason LIKE '%suggest%'`),
		"submitting a suggestion must never create access")
	assert.Equal(t, []string{p3StrangerNm}, e.queryStrings(fmt.Sprintf(
		`SELECT display_name FROM persons WHERE id = %d`, stranger)),
		"a suggestion about somebody must not alter anybody")

	// --- 7. There is no anonymous intake, on either transport ---
	anon := e.doWithKey(http.MethodPost, "/api/v1/me/change-requests", nil,
		`{"about_name":"Anonymous Tip","summary":"x","items":[{"operation":"other"}]}`,
		"smoke-p3-anon")
	assert.Equal(t, http.StatusUnauthorized, anon.StatusCode,
		"an unauthenticated correction submission must be refused")

	for _, path := range []string{
		"/api/v1/public/change-requests", "/api/v1/corrections", "/corrections", "/suggest",
	} {
		gone := e.do(http.MethodGet, path, nil, "")
		assert.Equal(t, http.StatusNotFound, gone.StatusCode,
			"%s must not exist: the anonymous channel was withdrawn (ADR-0013)", path)
	}

	form := e.postForm("/member/suggest", nil, url.Values{
		"kind": {"other"}, "about_name": {"Anonymous Tip"}, "summary": {"x"}})
	assert.Equal(t, http.StatusSeeOther, form.StatusCode)
	assert.Equal(t, "/login", form.Header.Get("Location"),
		"the member suggestion form must send an anonymous browser to sign in")

	// --- 8. An officer links an unresolved hint and reviews it ---
	//
	// The member described somebody in their own words and nothing was
	// confirmed at submission; resolving who they meant is the officer's job,
	// and doing it must not rewrite what the member wrote.
	queue := e.do(http.MethodGet, "/admin/requests?unlinked=1", admin, "")
	require.Equal(t, http.StatusOK, queue.StatusCode, "the officer queue must render")
	assert.Contains(t, readBody(queue), "Someone From The Tuesday Net",
		"an unlinked member hint must appear in the triage queue")

	version := e.requestVersion(admin, fullReq)
	linked := e.postForm(fmt.Sprintf("/admin/requests/%d/target", fullReq), admin, url.Values{
		"target_person_id": {fmt.Sprint(stranger)},
		"version":          {fmt.Sprint(version)},
	})
	require.Equal(t, http.StatusSeeOther, linked.StatusCode, "linking must succeed")

	detail := e.do(http.MethodGet, fmt.Sprintf("/admin/requests/%d", fullReq), admin, "")
	require.Equal(t, http.StatusOK, detail.StatusCode)
	detailBody := readBody(detail)
	assert.Contains(t, detailBody, p3StrangerNm, "the linked record is named")
	assert.Contains(t, detailBody, "Someone From The Tuesday Net",
		"what the member supplied must survive the officer's conclusion")

	// --- 9. Officer capture, per-field review, and apply ---
	//
	// The canonical change happens here and nowhere else in this test.
	before := e.queryStrings(fmt.Sprintf(`SELECT display_name FROM persons WHERE id = %d`, spouse))
	require.Equal(t, []string{p3SpouseName}, before)

	captured := e.captureOfficerRequest(admin, spouse, "Marguerite Ashby-Rutherford")
	itemID := e.firstItemID(admin, captured)

	decided := e.do(http.MethodPost,
		fmt.Sprintf("/api/v1/change-requests/%d/items/%d/decision", captured, itemID), admin,
		`{"decision":"approved"}`)
	require.Equal(t, http.StatusOK, decided.StatusCode, "apply: %s", readBody(decided))

	after := e.queryStrings(fmt.Sprintf(`SELECT display_name FROM persons WHERE id = %d`, spouse))
	assert.Equal(t, []string{"Marguerite Ashby-Rutherford"}, after,
		"an approved correction must reach the canonical record")

	// A member cannot reach the review path that did it.
	for _, path := range []string{
		fmt.Sprintf("/api/v1/change-requests/%d", captured),
		"/admin/requests",
	} {
		refused := e.do(http.MethodGet, path, member, "")
		assert.Equal(t, http.StatusForbidden, refused.StatusCode,
			"%s must refuse a member", path)
	}

	// --- 10. The trail says who did it ---
	actions := e.queryStrings(`
		SELECT DISTINCT action FROM audit_events
		 WHERE outcome = 'success' AND action LIKE 'change_request%'
		 ORDER BY action`)
	for _, want := range []string{"change_request.item.decide", "change_request.triage"} {
		assert.Contains(t, actions, want, "the review trail must record %s", want)
	}

	applied := e.queryStrings(fmt.Sprintf(`
		SELECT count(*) FROM member_change_request_items
		 WHERE request_id = %d AND status = 'approved' AND applied_at IS NOT NULL`, captured))
	assert.Equal(t, []string{"1"}, applied, "the applied item records when it was applied")

	// --- 11. Access management remains a separate, revocable decision ---
	e.revokeRecord(admin, memberUser, dale)

	gone := e.do(http.MethodGet, fmt.Sprintf("/api/v1/me/records/%d", dale), member, "")
	assert.Equal(t, http.StatusNotFound, gone.StatusCode,
		"revocation must take effect inside a session that is already open")

	stillRelated := e.queryStrings(fmt.Sprintf(
		`SELECT count(*) FROM person_relationships WHERE id = %d AND archived_at IS NULL`, relID))
	assert.Equal(t, []string{"1"}, stillRelated,
		"revoking access must not disturb the relationship, which was never access")

	// And with the grant gone, so is directory eligibility.
	afterRevoke := e.do(http.MethodGet, "/member/directory", member, "")
	assert.Equal(t, http.StatusNotFound, afterRevoke.StatusCode,
		"directory eligibility is recomputed per request, not cached at sign-in")
}

// --- flow steps ---

// onboardAndSignIn sets a first password through the recovery flow and signs
// in, returning the member's session cookie.
func (e *env) onboardAndSignIn(email, password string) *http.Cookie {
	e.t.Helper()

	before := e.mailCount()
	resp := e.postForm("/forgot-password", nil, url.Values{"email": {email}})
	require.Equal(e.t, http.StatusOK, resp.StatusCode)
	token := e.waitForMailToken(before, "password_recovery")

	resp = e.postForm("/reset-password", nil, url.Values{
		"token": {token}, "password": {password}, "confirm": {password}})
	require.Equal(e.t, http.StatusSeeOther, resp.StatusCode, "set first password: %s", readBody(resp))

	resp = e.postForm("/login", nil, url.Values{"email": {email}, "password": {password}})
	require.Equal(e.t, http.StatusSeeOther, resp.StatusCode, "sign in %s", email)
	cookie := sessionCookie(resp)
	require.NotNil(e.t, cookie, "sign-in must hand back a session for %s", email)
	return cookie
}

// approveMembership approves the person's pending membership through the real
// endpoint, because directory eligibility depends on an approved one.
//
// The membership id and version come from the database rather than a read
// endpoint because none exposes them: the officer UI reads them through sqlc.
// Only this setup step reads the file directly; every assertion below goes
// through the running server.
func (e *env) approveMembership(admin *http.Cookie, personID int64, baseType string) {
	e.t.Helper()

	rows := e.queryStrings(fmt.Sprintf(
		`SELECT id || ':' || version FROM memberships WHERE person_id = %d ORDER BY id LIMIT 1`,
		personID))
	require.NotEmpty(e.t, rows, "person %d has no membership to approve", personID)
	parts := strings.SplitN(rows[0], ":", 2)
	require.Len(e.t, parts, 2)

	resp := e.do(http.MethodPost, fmt.Sprintf("/api/v1/memberships/%s/approve", parts[0]), admin,
		fmt.Sprintf(`{"base_type":%q,"version":%s,"reason":"smoke test"}`, baseType, parts[1]))
	require.Equal(e.t, http.StatusOK, resp.StatusCode, "approve membership: %s", readBody(resp))
}

// createRelationship records an informational relationship and returns its id.
func (e *env) createRelationship(admin *http.Cookie, from, to int64, kind, context string) int64 {
	e.t.Helper()
	resp := e.do(http.MethodPost, "/api/v1/relationships", admin,
		fmt.Sprintf(`{"from_person_id":%d,"to_person_id":%d,"kind":%q,"context":%q}`,
			from, to, kind, context))
	require.Equal(e.t, http.StatusOK, resp.StatusCode, "create relationship: %s", readBody(resp))

	var body struct {
		ID int64 `json:"id"`
	}
	require.NoError(e.t, json.NewDecoder(resp.Body).Decode(&body))
	require.NotZero(e.t, body.ID)
	return body.ID
}

// submitSuggestion files a member suggestion and returns its request id.
func (e *env) submitSuggestion(member *http.Cookie, key, body string) int64 {
	e.t.Helper()
	resp := e.doWithKey(http.MethodPost, "/api/v1/me/change-requests", member, body, key)
	require.Equal(e.t, http.StatusCreated, resp.StatusCode, "submit suggestion: %s", readBody(resp))

	var out struct {
		ID int64 `json:"id"`
	}
	require.NoError(e.t, json.NewDecoder(resp.Body).Decode(&out))
	require.NotZero(e.t, out.ID)
	return out.ID
}

// captureOfficerRequest records an officer-entered correction naming a target,
// which is the shape an officer produces when a member telephones.
func (e *env) captureOfficerRequest(admin *http.Cookie, personID int64, newName string) int64 {
	e.t.Helper()
	resp := e.doWithKey(http.MethodPost, "/api/v1/change-requests", admin,
		fmt.Sprintf(`{"source":"officer_phone","target_person_id":%d,`+
			`"summary":"Called in a name change.",`+
			`"items":[{"operation":"person.display_name.set","proposed_value":%q,`+
			`"target_kind":"person","target_id":%d}]}`, personID, newName, personID),
		"smoke-p3-officer-capture")
	require.Equal(e.t, http.StatusOK, resp.StatusCode, "officer capture: %s", readBody(resp))

	var out struct {
		ID int64 `json:"id"`
	}
	require.NoError(e.t, json.NewDecoder(resp.Body).Decode(&out))
	require.NotZero(e.t, out.ID)
	return out.ID
}

// requestVersion reads the version an officer's page would have been rendered
// from, which triage requires so two officers cannot overwrite each other.
func (e *env) requestVersion(admin *http.Cookie, requestID int64) int64 {
	e.t.Helper()
	var out struct {
		Version int64 `json:"version"`
	}
	e.decodeRequest(admin, requestID, &out)
	return out.Version
}

// firstItemID returns the id of a request's first item.
func (e *env) firstItemID(admin *http.Cookie, requestID int64) int64 {
	e.t.Helper()
	var out struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	e.decodeRequest(admin, requestID, &out)
	require.NotEmpty(e.t, out.Items, "request %d has no items", requestID)
	return out.Items[0].ID
}

func (e *env) decodeRequest(admin *http.Cookie, requestID int64, into any) {
	e.t.Helper()
	resp := e.do(http.MethodGet, fmt.Sprintf("/api/v1/change-requests/%d", requestID), admin, "")
	require.Equal(e.t, http.StatusOK, resp.StatusCode, "read request: %s", readBody(resp))
	require.NoError(e.t, json.NewDecoder(resp.Body).Decode(into))
}

// TestPhase3TemplatesAreEmbedded is the narrow version of what the whole smoke
// harness proves, stated so a failure names the cause.
//
// Every page 4ux.10 and 4ux.11 and 4ux.12 added is requested from a server
// running in a directory with no templates on disk. A template read from the
// filesystem rather than the embedded FS passes every package test, because
// those run inside the repository, and fails only here.
func TestPhase3TemplatesAreEmbedded(t *testing.T) {
	if testing.Short() {
		t.Skip("smoke test builds binaries and starts a server; skipped in -short")
	}

	e := start(t)
	e.requireReady()
	admin := e.consumeInvitation(e.bootstrapAdmin(), adminEmail, adminPass)

	person := e.createPerson(admin, "Template Check", "full")
	e.approveMembership(admin, person, "full")

	for _, page := range []string{
		"/admin/requests",
		fmt.Sprintf("/admin/members/%d/access", person),
		"/admin/members",
		"/login",
		"/forgot-password",
	} {
		resp := e.do(http.MethodGet, page, admin, "")
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s must render: %s", page, readBody(resp))
		body := readBody(resp)
		require.NotEmpty(t, strings.TrimSpace(body), "%s rendered an empty page", page)
		assert.NotContains(t, body, "template error",
			"%s failed to execute its template from the embedded filesystem", page)
	}
}
