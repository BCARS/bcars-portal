package web

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/domain/memberaccess"
)

// The member directory UI (bcars-portal-4ux.12).
//
// Two properties carry most of these tests. The first is that a withheld
// contact and an absent one are indistinguishable, on screen and on paper. The
// second is that eligibility is decided per request by the service, so no
// template change and no hand-typed URL can turn an Associate into a reader.

// dirPerson inserts an approved member of the given type.
func (e *memberTestEnv) dirPerson(t *testing.T, name, callSign, baseType string) int64 {
	t.Helper()
	res, err := e.h.db.Exec(
		`INSERT INTO persons (display_name, sort_name, call_sign) VALUES (?, ?, ?)`,
		name, strings.ToLower(name), nullIfBlank(callSign))
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	_, err = e.h.db.Exec(
		`INSERT INTO memberships (person_id, base_type, lifecycle) VALUES (?, ?, 'approved')`,
		id, baseType)
	require.NoError(t, err)
	return id
}

func nullIfBlank(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// dirContactShared adds a contact and records a visibility decision for it.
func (e *memberTestEnv) dirContactShared(t *testing.T, personID int64, kind, value, audience, label string) {
	t.Helper()
	res, err := e.h.db.Exec(`
		INSERT INTO contact_methods (person_id, kind, label, value_raw, value_norm, is_primary)
		VALUES (?, ?, ?, ?, ?, 0)`, personID, kind, nullIfBlank(label), value, value)
	require.NoError(t, err)
	contactID, err := res.LastInsertId()
	require.NoError(t, err)
	if audience != "" {
		_, err = e.h.db.Exec(`
			INSERT INTO contact_method_visibility_events
				(contact_method_id, audience, effective_at, actor_user_id, source)
			VALUES (?, ?, '2026-01-01T00:00:00.000Z', 1, 'member_request')`,
			contactID, audience)
		require.NoError(t, err)
	}
}

// eligibleMember provisions an account, grants it an approved FULL record, and
// signs it in — which is precisely what directory eligibility requires.
func (e *memberTestEnv) eligibleMember(t *testing.T) (*http.Cookie, int64) {
	t.Helper()
	personID := e.dirPerson(t, "Dale Rutherford", "W3DLR", "full")
	_, err := e.access.GrantAccess(context.Background(),
		&authz.Principal{UserID: e.officerUserID}, e.memberUserID,
		memberaccess.GrantParams{PersonID: personID, AccessKind: memberaccess.AccessSelf,
			Reason: "test setup"}, time.Now().UTC())
	require.NoError(t, err)
	return e.signInMember(t), personID
}

// TestDirectoryListsSharedContactsOnly is the read model working: shared values
// appear, and everything else is one indistinguishable cell.
func TestDirectoryListsSharedContactsOnly(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, _ := e.eligibleMember(t)

	shares := e.dirPerson(t, "Alice Shares", "W3ALS", "full")
	e.dirContactShared(t, shares, "email", "alice@example.test", "full_members", "")
	e.dirContactShared(t, shares, "phone", "540-555-0101", "full_members", "")

	withholds := e.dirPerson(t, "Bob Withholds", "W3BOB", "full")
	e.dirContactShared(t, withholds, "email", "bob-private@example.test", "officers", "")

	e.dirPerson(t, "Carol Hasnone", "W3CAR", "full")

	w := e.getAs(t, RouteMemberDirectory, cookie)
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	assert.Contains(t, body, "alice@example.test")
	assert.Contains(t, body, "540-555-0101")
	assert.NotContains(t, body, "bob-private@example.test",
		"a contact kept from full members must never reach the page")
	// Bob's withheld email and absent phone, Carol's two absent cells, and the
	// reader's own two: six cells, one spelling.
	assert.Equal(t, 6, strings.Count(body, notSharedText),
		"a withheld contact and an absent one produce the same cell")
}

// TestWithheldAndAbsentAreIndistinguishable states the privacy property
// directly: the two rows must be byte-identical in their contact cells.
func TestWithheldAndAbsentAreIndistinguishable(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, _ := e.eligibleMember(t)

	withholds := e.dirPerson(t, "Bob Withholds", "W3BOB", "full")
	e.dirContactShared(t, withholds, "phone", "540-555-0199", "officers", "")
	e.dirPerson(t, "Carol Hasnone", "W3CAR", "full")

	body := e.getAs(t, RouteMemberDirectory, cookie).Body.String()

	bobRow := rowFor(t, body, "Bob Withholds")
	carolRow := rowFor(t, body, "Carol Hasnone")

	// Only the identifying cells may differ. Everything downstream of them --
	// which is to say every contact cell -- must be byte-identical.
	normalise := func(row, name, callSign string) string {
		row = strings.ReplaceAll(row, name, "NAME")
		return strings.ReplaceAll(row, callSign, "CALLSIGN")
	}
	assert.Equal(t,
		normalise(bobRow, "Bob Withholds", "W3BOB"),
		normalise(carolRow, "Carol Hasnone", "W3CAR"),
		"a member who withholds a number and one who has none must render identically")
	assert.NotContains(t, bobRow, "540-555-0199")
	assert.NotContains(t, strings.ToLower(bobRow), "hidden",
		"the page must not mark a value as withheld, which would disclose that it exists")
}

// rowFor extracts the table row containing name, for comparing two rows'
// rendered shape.
func rowFor(t *testing.T, body, name string) string {
	t.Helper()
	idx := strings.Index(body, name)
	require.GreaterOrEqual(t, idx, 0, "%s is not on the page", name)
	start := strings.LastIndex(body[:idx], "<tr>")
	require.GreaterOrEqual(t, start, 0)
	end := strings.Index(body[start:], "</tr>")
	require.GreaterOrEqual(t, end, 0)
	return body[start : start+end]
}

// TestDirectorySortsByNameAndCallSign covers the sortable table, including that
// sorting happens across the whole listing rather than within a page.
func TestDirectorySortsByNameAndCallSign(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, _ := e.eligibleMember(t)

	e.dirPerson(t, "Zoe Anders", "A1AAA", "full")
	e.dirPerson(t, "Adam Zircon", "Z9ZZZ", "full")
	e.dirPerson(t, "Nora Nocall", "", "full")

	byName := e.getAs(t, RouteMemberDirectory, cookie).Body.String()
	assert.Less(t, strings.Index(byName, "Adam Zircon"), strings.Index(byName, "Zoe Anders"),
		"the default order is by name")

	byCall := e.getAs(t, RouteMemberDirectory+"?sort=call_sign", cookie).Body.String()
	assert.Less(t, strings.Index(byCall, "Zoe Anders"), strings.Index(byCall, "Adam Zircon"),
		"A1AAA sorts before Z9ZZZ")
	assert.Greater(t, strings.Index(byCall, "Nora Nocall"), strings.Index(byCall, "Adam Zircon"),
		"a member with no call sign sorts last, not first where SQLite puts NULL")
}

// TestUnknownSortFallsBackRatherThanFailing keeps a stray query string from
// becoming a dead end.
func TestUnknownSortFallsBackRatherThanFailing(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, _ := e.eligibleMember(t)
	e.dirPerson(t, "Alice Shares", "W3ALS", "full")

	w := e.getAs(t, RouteMemberDirectory+"?sort=salary&type=nonsense", cookie)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Alice Shares")
}

// TestDirectorySearchAndFilterNarrowTheList covers the two controls.
func TestDirectorySearchAndFilterNarrowTheList(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, _ := e.eligibleMember(t)

	e.dirPerson(t, "Alice Shares", "W3ALS", "full")
	e.dirPerson(t, "Bob Associate", "W3BOB", "associate")

	found := e.getAs(t, RouteMemberDirectory+"?search=Alice", cookie).Body.String()
	assert.Contains(t, found, "Alice Shares")
	assert.NotContains(t, found, "Bob Associate")

	byCall := e.getAs(t, RouteMemberDirectory+"?search=W3BOB", cookie).Body.String()
	assert.Contains(t, byCall, "Bob Associate", "search matches a call sign too")

	fullOnly := e.getAs(t, RouteMemberDirectory+"?type=full", cookie).Body.String()
	assert.Contains(t, fullOnly, "Alice Shares")
	assert.NotContains(t, fullOnly, "Bob Associate")
}

// TestPrintShowsTheSameFilteredListAndNamesTheClub is the print acceptance:
// same filter, same fields, and a sheet that says whose roster it is.
func TestPrintShowsTheSameFilteredListAndNamesTheClub(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, _ := e.eligibleMember(t)

	shares := e.dirPerson(t, "Alice Shares", "W3ALS", "full")
	e.dirContactShared(t, shares, "email", "alice@example.test", "full_members", "")
	withholds := e.dirPerson(t, "Bob Withholds", "W3BOB", "full")
	e.dirContactShared(t, withholds, "email", "bob-private@example.test", "officers", "")

	w := e.getAs(t, RouteMemberDirectoryPrint, cookie)
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	assert.Contains(t, body, "Bedford County Amateur Radio Society",
		"the sheet must identify the club once it is off the screen")
	assert.Contains(t, body, "@page")
	assert.Contains(t, body, "letter portrait")

	assert.Contains(t, body, "alice@example.test")
	assert.NotContains(t, body, "bob-private@example.test",
		"printing must not be the laxer read path")
	assert.Contains(t, body, notSharedText)

	// The same four columns as the screen, and no more.
	for _, header := range []string{"Name", "Call sign", "Email", "Phone"} {
		assert.Contains(t, body, ">"+header+"<")
	}
	for _, absent := range []string{"Dues", "Paid", "Address", "Membership type", "Notes"} {
		assert.NotContains(t, body, absent,
			"print must not add a field the screen does not show")
	}

	// A filter carries through to the sheet.
	filtered := e.getAs(t, RouteMemberDirectoryPrint+"?search=Alice", cookie).Body.String()
	assert.Contains(t, filtered, "Alice Shares")
	assert.NotContains(t, filtered, "Bob Withholds")
}

// TestPrintLinkCarriesTheCurrentView proves what prints is what was on screen.
func TestPrintLinkCarriesTheCurrentView(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, _ := e.eligibleMember(t)
	e.dirPerson(t, "Alice Shares", "W3ALS", "full")

	body := e.getAs(t, RouteMemberDirectory+"?search=Alice&sort=call_sign&type=full", cookie).Body.String()
	assert.Contains(t, body, "/member/directory/print?search=Alice&amp;type=full&amp;sort=call_sign",
		"the print link must keep the reader's search, filter, and sort")
}

// TestIneligibleMembersReachNeitherScreenNorPrint is the authorization rule.
// An Associate holds directory.read and is still answered as if the page did
// not exist.
func TestIneligibleMembersReachNeitherScreenNorPrint(t *testing.T) {
	e := setupMemberEnv(t)

	// Granted an ASSOCIATE record, so the capability is held and eligibility is
	// not met.
	assoc := e.dirPerson(t, "Bob Associate", "W3BOB", "associate")
	_, err := e.access.GrantAccess(context.Background(),
		&authz.Principal{UserID: e.officerUserID}, e.memberUserID,
		memberaccess.GrantParams{PersonID: assoc, AccessKind: memberaccess.AccessSelf,
			Reason: "test setup"}, time.Now().UTC())
	require.NoError(t, err)
	cookie := e.signInMember(t)

	e.dirPerson(t, "Alice Shares", "W3ALS", "full")

	for _, path := range []string{RouteMemberDirectory, RouteMemberDirectoryPrint} {
		w := e.getAs(t, path, cookie)
		assert.Equal(t, http.StatusNotFound, w.Code,
			"%s must refuse an ineligible member", path)
		assert.NotContains(t, w.Body.String(), "Alice Shares",
			"%s must render no listing at all", path)
		assert.NotContains(t, strings.ToLower(w.Body.String()), "not eligible",
			"the refusal must not confirm that a directory exists")
	}
}

// TestDirectoryRefusesStrangers covers the anonymous and capability-less cases.
func TestDirectoryRefusesStrangers(t *testing.T) {
	e := setupMemberEnv(t)

	for _, path := range []string{RouteMemberDirectory, RouteMemberDirectoryPrint} {
		anon := e.getAs(t, path, nil)
		assert.Equal(t, http.StatusSeeOther, anon.Code, "%s must redirect an anonymous caller", path)
		assert.Equal(t, RouteLogin, anon.Header().Get("Location"))
	}

	// An officer without directory.read is refused by the capability check,
	// before eligibility is ever consulted.
	officer := e.officerCookie(t)
	w := e.getAs(t, RouteMemberDirectory, officer)
	assert.Equal(t, http.StatusForbidden, w.Code,
		"the secretary role holds no directory.read and must be refused")
}

// TestLandingOffersTheDirectoryOnlyWhenReachable holds the rule the member
// layout already follows: a page must not advertise a link that fails.
func TestLandingOffersTheDirectoryOnlyWhenReachable(t *testing.T) {
	e := setupMemberEnv(t)

	assoc := e.dirPerson(t, "Bob Associate", "W3BOB", "associate")
	_, err := e.access.GrantAccess(context.Background(),
		&authz.Principal{UserID: e.officerUserID}, e.memberUserID,
		memberaccess.GrantParams{PersonID: assoc, AccessKind: memberaccess.AccessSelf,
			Reason: "test setup"}, time.Now().UTC())
	require.NoError(t, err)
	cookie := e.signInMember(t)

	body := e.getAs(t, RouteMemberHome, cookie).Body.String()
	assert.NotContains(t, body, RouteMemberDirectory,
		"an ineligible member must not be offered a link that answers 404")

	// Grant the same account a FULL record and the link appears.
	full := e.dirPerson(t, "Dale Rutherford", "W3DLR", "full")
	_, err = e.access.GrantAccess(context.Background(),
		&authz.Principal{UserID: e.officerUserID}, e.memberUserID,
		memberaccess.GrantParams{PersonID: full, AccessKind: memberaccess.AccessSelf,
			Reason: "test setup"}, time.Now().UTC())
	require.NoError(t, err)

	body = e.getAs(t, RouteMemberHome, cookie).Body.String()
	require.Contains(t, body, RouteMemberDirectory)
	assert.Equal(t, http.StatusOK, e.getAs(t, RouteMemberDirectory, cookie).Code,
		"the offered link must work in the same session that was just granted")
}

// TestDirectoryLinksToTheCorrectionWorkflow is the acceptance criterion that
// the page connects to the reviewed-correction flow rather than dead-ending.
func TestDirectoryLinksToTheCorrectionWorkflow(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, _ := e.eligibleMember(t)
	e.dirPerson(t, "Alice Shares", "W3ALS", "full")

	body := e.getAs(t, RouteMemberDirectory, cookie).Body.String()
	require.Contains(t, body, `href="`+RouteMemberSuggest+`"`,
		"the directory must offer the correction flow for somebody else's details")
	assert.Equal(t, http.StatusOK, e.getAs(t, RouteMemberSuggest, cookie).Code,
		"and that link must work for this caller")
}

// TestDirectoryEscapesMemberText keeps a name from being rendered as markup.
func TestDirectoryEscapesMemberText(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, _ := e.eligibleMember(t)
	e.dirPerson(t, `<script>alert('x')</script>`, "W3XSS", "full")

	for _, path := range []string{RouteMemberDirectory, RouteMemberDirectoryPrint} {
		body := e.getAs(t, path, cookie).Body.String()
		assert.NotContains(t, body, "<script>alert",
			"%s must render a stored name as text", path)
		assert.Contains(t, body, "&lt;script&gt;")
	}
}

// TestDirectoryTotalDoesNotVaryByViewer keeps the count from disclosing how
// many members withhold their details.
func TestDirectoryTotalDoesNotVaryByViewer(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, _ := e.eligibleMember(t)

	shares := e.dirPerson(t, "Alice Shares", "W3ALS", "full")
	e.dirContactShared(t, shares, "email", "alice@example.test", "full_members", "")
	withholds := e.dirPerson(t, "Bob Withholds", "W3BOB", "full")
	e.dirContactShared(t, withholds, "email", "bob-private@example.test", "officers", "")

	body := e.getAs(t, RouteMemberDirectory, cookie).Body.String()
	assert.Contains(t, body, "of 3 members",
		"the total counts every member, including those whose details are withheld")
}

// TestDirectoryPagingKeepsTheReadersView covers the pager and that it carries
// search and sort along.
func TestDirectoryPagingKeepsTheReadersView(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, _ := e.eligibleMember(t)
	for i := 0; i < 60; i++ {
		e.dirPerson(t, fmt.Sprintf("Member %02d", i), fmt.Sprintf("W3%03d", i), "full")
	}

	body := e.getAs(t, RouteMemberDirectory+"?sort=call_sign", cookie).Body.String()
	require.Contains(t, body, "Next")
	assert.Contains(t, body, "sort=call_sign&amp;offset=50",
		"the next page must keep the chosen sort")

	second := e.getAs(t, RouteMemberDirectory+"?sort=call_sign&offset=50", cookie)
	require.Equal(t, http.StatusOK, second.Code)
	assert.Contains(t, second.Body.String(), "Previous")
}

// Officers are members: the club elects them from its membership, and the
// portal takes that as the rule (bcars-portal-j10). An officer refused the
// directory is therefore not a member being told no — it is an account nobody
// linked to a member record, and it must be told so. The bootstrap
// administrator is the standing example, created before any person exists.

// officerWithCap signs in an account holding the given roles and returns its
// cookie. It links nothing, so the account is exactly the unlinked officer the
// refusal below is about.
func (e *memberTestEnv) unlinkedOfficerCookie(t *testing.T, role string) *http.Cookie {
	t.Helper()
	_, err := e.h.db.Exec(
		`INSERT INTO user_role_grants (user_id, role_code, granted_by, granted_at, reason)
		 VALUES (?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'), 'test setup')`,
		e.officerUserID, role, e.officerUserID)
	require.NoError(t, err)
	return e.officerCookie(t)
}

func TestAnUnlinkedOfficerIsToldWhyTheDirectoryRefusesThem(t *testing.T) {
	e := setupMemberEnv(t)
	cookie := e.unlinkedOfficerCookie(t, "administrator")

	for _, path := range []string{RouteMemberDirectory, RouteMemberDirectoryPrint} {
		w := e.getAs(t, path, cookie)

		require.Equalf(t, http.StatusForbidden, w.Code,
			"%s must refuse an unlinked officer as a refusal, not as a missing page", path)

		body := w.Body.String()
		assert.Containsf(t, body, "not linked to a member record",
			"%s must say what is actually wrong", path)
		assert.NotContainsf(t, body, "No such page",
			"%s told an administrator the page does not exist", path)
	}
}

// TestAnIneligibleMemberStillLearnsNothing is the other half, and the reason
// the refusal above is conditional. An Associate holds directory.read and is
// refused; telling them the directory exists and that other members read it is
// more than they need to know (bcars-portal-4ux.12).
func TestAnIneligibleMemberStillLearnsNothing(t *testing.T) {
	e := setupMemberEnv(t)

	assoc := e.dirPerson(t, "Bob Associate", "W3BOB", "associate")
	_, err := e.access.GrantAccess(context.Background(),
		&authz.Principal{UserID: e.officerUserID}, e.memberUserID,
		memberaccess.GrantParams{PersonID: assoc, AccessKind: memberaccess.AccessSelf,
			Reason: "test setup"}, time.Now().UTC())
	require.NoError(t, err)
	cookie := e.signInMember(t)

	w := e.getAs(t, RouteMemberDirectory, cookie)
	require.Equal(t, http.StatusNotFound, w.Code,
		"an Associate must still be answered as though the page were not there")

	body := w.Body.String()
	assert.NotContains(t, body, "not linked to a member record",
		"the officer explanation leaked to a member who may not read the directory")
	assert.NotContains(t, body, "directory",
		"the refusal must not disclose that a directory exists")
}

// TestALinkedOfficerReadsTheDirectory is what the seeded fixture now produces
// and what the club actually needs: the officer who hands the roster out at a
// meeting can open and print it.
func TestALinkedOfficerReadsTheDirectory(t *testing.T) {
	e := setupMemberEnv(t)
	cookie := e.unlinkedOfficerCookie(t, "administrator")

	// Link the officer's account to their own approved full membership, the
	// way seed-demo and a real installation do.
	personID := e.dirPerson(t, "Dana Whitfield", "W3XAB", "full")
	_, err := e.access.GrantAccess(context.Background(),
		&authz.Principal{UserID: e.officerUserID}, e.officerUserID,
		memberaccess.GrantParams{PersonID: personID, AccessKind: memberaccess.AccessSelf,
			Reason: "test setup"}, time.Now().UTC())
	require.NoError(t, err)

	for _, path := range []string{RouteMemberDirectory, RouteMemberDirectoryPrint} {
		w := e.getAs(t, path, cookie)
		assert.Equalf(t, http.StatusOK, w.Code,
			"%s must open for an officer who is also a member", path)
	}
}

// A directory row used to be a dead end (bcars-portal-tsj). A member who spotted
// a wrong call sign while reading the roster had to leave the page, find the
// correction form, and identify the person again by typing their name from
// memory — with the roster no longer in front of them.

func TestEachDirectoryRowOffersACorrection(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, _ := e.eligibleMember(t)
	e.dirPerson(t, "Alice Shares", "W3ALS", "full")

	body := e.getAs(t, RouteMemberDirectory, cookie).Body.String()

	require.Contains(t, body, "Suggest a correction",
		"every row should offer the correction the page's own footer describes")

	// Parse the link rather than matching the raw markup: the query string is
	// HTML-escaped in the attribute, so a literal comparison would be asserting
	// on Go's escaping rather than on what the browser will request.
	var found url.Values
	for _, m := range regexp.MustCompile(`href="([^"]*suggest[^"]*)"`).FindAllStringSubmatch(body, -1) {
		u, err := url.Parse(html.UnescapeString(m[1]))
		require.NoError(t, err)
		if u.Query().Get("about_name") == "Alice Shares" {
			found = u.Query()
		}
	}
	require.NotNil(t, found, "no row linked to the correction form naming Alice Shares")
	assert.Equal(t, "W3ALS", found.Get("about_call_sign"),
		"the link should carry the call sign as well as the name")
}

// TestTheCorrectionFormArrivesKnowingWhoItIsAbout follows the link, which is the
// half that matters: a link carrying a name means nothing if the form drops it.
func TestTheCorrectionFormArrivesKnowingWhoItIsAbout(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, _ := e.eligibleMember(t)

	w := e.getAs(t, RouteMemberSuggest+"?about_name=Alice+Shares&about_call_sign=W3ALS", cookie)
	require.Equal(t, http.StatusOK, w.Code)

	body := w.Body.String()
	assert.Contains(t, body, `value="Alice Shares"`,
		"the form should arrive with the member already named")
	assert.Contains(t, body, `value="W3ALS"`)
}

// TestTheSubjectIsThesendersToChange keeps the model honest. This form consults
// no record — an officer decides during triage which member a suggestion is
// about — so a prefilled name is a starting point, not an identification the
// portal has made, and the sender may correct it.
func TestTheSubjectIsTheSendersToChange(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, _ := e.eligibleMember(t)

	w := e.post(t, RouteMemberSuggest, url.Values{
		"kind":            {"person.call_sign.set"},
		"proposed_value":  {"W3NEW"},
		"about_name":      {"Someone The Directory Did Not Name"},
		"about_call_sign": {"W3XXX"},
		"summary":         {"I typed this myself."},
	}, cookie)
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	var about string
	require.NoError(t, e.h.db.QueryRow(
		`SELECT COALESCE(supplied_name, '') FROM member_change_requests ORDER BY id DESC LIMIT 1`).
		Scan(&about))
	assert.Equal(t, "Someone The Directory Did Not Name", about,
		"what the sender typed is what is stored, prefill or not")
}
