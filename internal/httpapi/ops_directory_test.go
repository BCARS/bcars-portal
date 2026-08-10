package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The private member directory (bcars-portal-4ux.7).
//
// Two properties carry this feature, and both are negative: who may NOT browse,
// and which values must NOT appear. The tests below are written so that
// removing either guard turns them red.

type apiDirectory struct {
	Entries []struct {
		PersonID    int64  `json:"person_id"`
		DisplayName string `json:"display_name"`
		CallSign    string `json:"call_sign"`
		BaseType    string `json:"base_type"`
		Email       string `json:"email"`
		Phone       string `json:"phone"`
		EmailShared bool   `json:"email_shared"`
		PhoneShared bool   `json:"phone_shared"`
	} `json:"entries"`
	Total  int64 `json:"total"`
	Limit  int64 `json:"limit"`
	Offset int64 `json:"offset"`
}

// dirMember creates a person with a membership and optional contacts, and
// returns the person id.
func dirMember(t *testing.T, env *authzEnv, name, callSign, baseType, lifecycle string) int64 {
	t.Helper()
	res, err := env.db.Exec(
		`INSERT INTO persons (display_name, sort_name, call_sign) VALUES (?, ?, ?)`,
		name, name, nullIfEmpty(callSign))
	require.NoError(t, err)
	personID, err := res.LastInsertId()
	require.NoError(t, err)

	_, err = env.db.Exec(
		`INSERT INTO memberships (person_id, base_type, lifecycle) VALUES (?, ?, ?)`,
		personID, baseType, lifecycle)
	require.NoError(t, err)
	return personID
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// dirContact adds a contact and optionally a visibility decision. Pass an empty
// audience to leave the contact with no decision on file.
func dirContact(t *testing.T, env *authzEnv, personID int64, kind, value, audience string) int64 {
	t.Helper()
	res, err := env.db.Exec(`
		INSERT INTO contact_methods (person_id, kind, value_raw, value_norm)
		VALUES (?, ?, ?, ?)`, personID, kind, value, value)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)

	if audience != "" {
		_, err = env.db.Exec(`
			INSERT INTO contact_method_visibility_events
				(contact_method_id, audience, source, effective_at)
			VALUES (?, ?, 'officer', '2026-01-01T00:00:00.000Z')`, id, audience)
		require.NoError(t, err)
	}
	return id
}

// grantAccess gives the signed-in test user (id 1) access to a person.
func grantAccess(t *testing.T, env *authzEnv, personID int64) {
	t.Helper()
	_, err := env.db.Exec(`
		INSERT INTO member_access_grants (user_id, person_id, access_kind, granted_at)
		VALUES (1, ?, 'self', '2026-01-01T00:00:00.000Z')`, personID)
	require.NoError(t, err)
}

func readDirectory(t *testing.T, env *authzEnv, cookie *http.Cookie, query string) (*http.Response, apiDirectory) {
	t.Helper()
	resp := env.do(t, http.MethodGet, "/api/v1/directory"+query, cookie, "")
	var body apiDirectory
	if resp.StatusCode == http.StatusOK {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	}
	return resp, body
}

// TestDirectoryEligibilityMatrix is the allowed/denied matrix. Holding the
// capability is never enough; an active approved Full membership is.
func TestDirectoryEligibilityMatrix(t *testing.T) {
	cases := []struct {
		name      string
		baseType  string
		lifecycle string
		grant     bool
		allowed   bool
	}{
		{"active approved Full member", "full", "approved", true, true},
		{"Associate member", "associate", "approved", true, false},
		{"pending applicant", "full", "pending", true, false},
		{"inactive member", "full", "inactive", true, false},
		{"resigned member", "full", "resigned", true, false},
		{"deceased member", "full", "deceased", true, false},
		{"rejected applicant", "full", "rejected", true, false},
		{"Full member with no grant", "full", "approved", false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The 'member' role holds directory.read and nothing else.
			env := setupAuthzTest(t, "member")
			cookie := env.signIn(t)

			person := dirMember(t, env, "Caller Person", "W3CALL", tc.baseType, tc.lifecycle)
			if tc.grant {
				grantAccess(t, env, person)
			}
			dirMember(t, env, "Someone Else", "W3ELSE", "full", "approved")

			resp, body := readDirectory(t, env, cookie, "")
			if tc.allowed {
				assert.Equal(t, http.StatusOK, resp.StatusCode, readAll(t, resp))
				assert.NotEmpty(t, body.Entries)
				return
			}
			assert.Equal(t, http.StatusNotFound, resp.StatusCode,
				"an ineligible caller must not be able to browse")
		})
	}
}

// TestIneligibleAndUnknownAreIndistinguishable proves the refusal discloses
// nothing about whether a directory exists for someone else.
func TestIneligibleAndUnknownAreIndistinguishable(t *testing.T) {
	env := setupAuthzTest(t, "member")
	cookie := env.signIn(t)

	person := dirMember(t, env, "Associate Caller", "W3ASSC", "associate", "approved")
	grantAccess(t, env, person)
	dirMember(t, env, "Full Member", "W3FULL", "full", "approved")

	ineligible, _ := readDirectory(t, env, cookie, "")
	unknown := env.do(t, http.MethodGet, "/api/v1/directory-does-not-exist", cookie, "")

	assert.Equal(t, http.StatusNotFound, ineligible.StatusCode)
	assert.Equal(t, unknown.StatusCode, ineligible.StatusCode,
		"an ineligible caller and a nonexistent path must look the same")
}

// TestDirectoryDeniesAnonymousAndUncapableCallers covers the outer guard.
func TestDirectoryDeniesAnonymousAndUncapableCallers(t *testing.T) {
	env := setupAuthzTest(t, "member")

	anon := env.do(t, http.MethodGet, "/api/v1/directory", nil, "")
	assert.Equal(t, http.StatusUnauthorized, anon.StatusCode)

	// acs_coordinator holds member.read but not directory.read.
	other := setupAuthzTest(t, "acs_coordinator")
	cookie := other.signIn(t)
	person := dirMember(t, other, "Coordinator", "W3COOR", "full", "approved")
	grantAccess(t, other, person)

	resp, _ := readDirectory(t, other, cookie, "")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"the capability guard must refuse before eligibility is considered")
}

// eligibleEnv returns an env whose signed-in user is an active Full member.
func eligibleEnv(t *testing.T) (*authzEnv, *http.Cookie) {
	t.Helper()
	env := setupAuthzTest(t, "member")
	cookie := env.signIn(t)
	caller := dirMember(t, env, "Aaa Caller", "W3AAA", "full", "approved")
	grantAccess(t, env, caller)
	return env, cookie
}

// TestContactVisibilityFiltering is the field-level non-disclosure matrix.
func TestContactVisibilityFiltering(t *testing.T) {
	env, cookie := eligibleEnv(t)

	// A Full member who shares.
	shares := dirMember(t, env, "Bbb Shares", "W3SHAR", "full", "approved")
	dirContact(t, env, shares, "email", "shares@example.test", "full_members")
	dirContact(t, env, shares, "phone", "814-555-0101", "full_members")

	// A Full member who hid both.
	hides := dirMember(t, env, "Ccc Hides", "W3HIDE", "full", "approved")
	dirContact(t, env, hides, "email", "hides@example.test", "hidden")
	dirContact(t, env, hides, "phone", "814-555-0102", "hidden")

	// A Full member who restricted to officers only.
	officers := dirMember(t, env, "Ddd Officers", "W3OFFR", "full", "approved")
	dirContact(t, env, officers, "email", "officers@example.test", "officers_only")

	// A Full member with contacts but no decision on file: the Phase 1
	// default shares a Full member's contact with Full members.
	defaulted := dirMember(t, env, "Eee Default", "W3DFLT", "full", "approved")
	dirContact(t, env, defaulted, "email", "default@example.test", "")

	// An Associate with no decision on file: the default is restricted.
	assoc := dirMember(t, env, "Fff Associate", "W3ASSO", "associate", "approved")
	dirContact(t, env, assoc, "email", "assoc@example.test", "")

	// A Full member with nothing on file at all.
	none := dirMember(t, env, "Ggg Nothing", "W3NONE", "full", "approved")

	resp, body := readDirectory(t, env, cookie, "?limit=100")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	byID := map[int64]struct {
		email, phone             string
		emailShared, phoneShared bool
	}{}
	for _, e := range body.Entries {
		byID[e.PersonID] = struct {
			email, phone             string
			emailShared, phoneShared bool
		}{e.Email, e.Phone, e.EmailShared, e.PhoneShared}
	}

	t.Run("a shared contact appears", func(t *testing.T) {
		got := byID[shares]
		assert.Equal(t, "shares@example.test", got.email)
		assert.Equal(t, "814-555-0101", got.phone)
		assert.True(t, got.emailShared)
		assert.True(t, got.phoneShared)
	})

	t.Run("a hidden contact never appears", func(t *testing.T) {
		got := byID[hides]
		assert.Empty(t, got.email)
		assert.Empty(t, got.phone)
		assert.False(t, got.emailShared)
	})

	t.Run("officers_only is not full_members", func(t *testing.T) {
		assert.Empty(t, byID[officers].email,
			"a contact restricted to officers must not reach the member directory")
	})

	t.Run("a Full member with no decision defaults to shared", func(t *testing.T) {
		assert.Equal(t, "default@example.test", byID[defaulted].email)
	})

	t.Run("an Associate with no decision defaults to restricted", func(t *testing.T) {
		assert.Empty(t, byID[assoc].email,
			"an Associate's contact default must stay restricted")
	})

	t.Run("hidden and absent are indistinguishable", func(t *testing.T) {
		assert.Equal(t, byID[hides].email, byID[none].email)
		assert.Equal(t, byID[hides].emailShared, byID[none].emailShared)
	})

	t.Run("no withheld value appears anywhere in the response", func(t *testing.T) {
		raw := marshal(t, body)
		for _, secret := range []string{
			"hides@example.test", "814-555-0102",
			"officers@example.test", "assoc@example.test",
		} {
			assert.NotContains(t, raw, secret,
				"a withheld value must not appear anywhere in the payload")
		}
	})
}

// TestLatestVisibilityDecisionWins proves the history is read, not just any row.
func TestLatestVisibilityDecisionWins(t *testing.T) {
	env, cookie := eligibleEnv(t)
	person := dirMember(t, env, "Bbb Changed", "W3CHNG", "full", "approved")
	contact := dirContact(t, env, person, "email", "changed@example.test", "full_members")

	// The member later hid it.
	_, err := env.db.Exec(`
		INSERT INTO contact_method_visibility_events
			(contact_method_id, audience, source, effective_at)
		VALUES (?, 'hidden', 'officer', '2026-06-01T00:00:00.000Z')`, contact)
	require.NoError(t, err)

	_, body := readDirectory(t, env, cookie, "?limit=100")
	assert.NotContains(t, marshal(t, body), "changed@example.test",
		"the most recent decision must win over an earlier one")
}

// TestImportDefaultDecisionIsHonoured proves an imported decision is a decision.
func TestImportDefaultDecisionIsHonoured(t *testing.T) {
	env, cookie := eligibleEnv(t)
	person := dirMember(t, env, "Bbb Imported", "W3IMPT", "full", "approved")
	id := dirContact(t, env, person, "email", "imported@example.test", "")

	_, err := env.db.Exec(`
		INSERT INTO contact_method_visibility_events
			(contact_method_id, audience, source, effective_at)
		VALUES (?, 'hidden', 'import_default', '2026-01-01T00:00:00.000Z')`, id)
	require.NoError(t, err)

	_, body := readDirectory(t, env, cookie, "?limit=100")
	assert.NotContains(t, marshal(t, body), "imported@example.test",
		"an imported visibility decision keeps its result until superseded")
}

// TestDirectoryPopulation proves who is listed and who is not.
func TestDirectoryPopulation(t *testing.T) {
	env, cookie := eligibleEnv(t)

	full := dirMember(t, env, "Bbb Full", "W3FUL2", "full", "approved")
	assoc := dirMember(t, env, "Ccc Assoc", "W3ASC2", "associate", "approved")
	pending := dirMember(t, env, "Ddd Pending", "W3PEND", "full", "pending")
	resigned := dirMember(t, env, "Eee Resigned", "W3RESG", "full", "resigned")

	_, body := readDirectory(t, env, cookie, "?limit=100")

	listed := map[int64]bool{}
	for _, e := range body.Entries {
		listed[e.PersonID] = true
	}
	assert.True(t, listed[full], "an active Full member is listed")
	assert.True(t, listed[assoc], "an active Associate is listed")
	assert.False(t, listed[pending], "a pending applicant is not listed")
	assert.False(t, listed[resigned], "a resigned member is not listed")
}

// TestDirectoryOrderingAndPaging proves stability.
func TestDirectoryOrderingAndPaging(t *testing.T) {
	env, cookie := eligibleEnv(t)

	// Same display name, so only the id tie-breaker orders them.
	for i := 0; i < 5; i++ {
		dirMember(t, env, "Zzz Same Name", "", "full", "approved")
	}

	_, all := readDirectory(t, env, cookie, "?limit=100")
	require.GreaterOrEqual(t, len(all.Entries), 6)
	for i := 1; i < len(all.Entries); i++ {
		if all.Entries[i-1].DisplayName == all.Entries[i].DisplayName {
			assert.Less(t, all.Entries[i-1].PersonID, all.Entries[i].PersonID,
				"equal names must be ordered by id so paging is stable")
		}
	}

	seen := map[int64]bool{}
	for offset := int64(0); offset < all.Total; offset += 2 {
		_, page := readDirectory(t, env, cookie, fmt.Sprintf("?limit=2&offset=%d", offset))
		for _, e := range page.Entries {
			assert.False(t, seen[e.PersonID], "person %d appeared on two pages", e.PersonID)
			seen[e.PersonID] = true
		}
	}
	assert.Equal(t, int(all.Total), len(seen), "paging must cover every member exactly once")
}

// TestDirectorySearch proves search covers name and call sign.
func TestDirectorySearch(t *testing.T) {
	env, cookie := eligibleEnv(t)
	dirMember(t, env, "Bbb Searchable", "W3SRCH", "full", "approved")
	dirMember(t, env, "Ccc Other", "W3OTHR", "full", "approved")

	_, byName := readDirectory(t, env, cookie, "?search=Searchable")
	require.Len(t, byName.Entries, 1)
	assert.Equal(t, "Bbb Searchable", byName.Entries[0].DisplayName)

	_, byCall := readDirectory(t, env, cookie, "?search=W3SRCH")
	require.Len(t, byCall.Entries, 1)
	assert.Equal(t, "W3SRCH", byCall.Entries[0].CallSign)

	_, none := readDirectory(t, env, cookie, "?search=NoSuchMember")
	assert.Empty(t, none.Entries)
}

// TestTotalDoesNotVaryByViewer proves the count discloses nothing about how
// many members hide their details.
func TestTotalDoesNotVaryByViewer(t *testing.T) {
	env, cookie := eligibleEnv(t)

	visible := dirMember(t, env, "Bbb Visible", "W3VIS", "full", "approved")
	dirContact(t, env, visible, "email", "visible@example.test", "full_members")
	hidden := dirMember(t, env, "Ccc Hidden", "W3HID", "full", "approved")
	dirContact(t, env, hidden, "email", "secret@example.test", "hidden")

	_, body := readDirectory(t, env, cookie, "?limit=100")
	assert.Equal(t, int64(3), body.Total,
		"the total counts members, not visible contact values")
}

// TestPrintUsesTheSameFilteredResult proves print is not a laxer second path.
func TestPrintUsesTheSameFilteredResult(t *testing.T) {
	env, cookie := eligibleEnv(t)
	person := dirMember(t, env, "Bbb Printed", "W3PRNT", "full", "approved")
	dirContact(t, env, person, "email", "printed-secret@example.test", "hidden")

	resp, body := readDirectory(t, env, cookie, "?print=true")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotContains(t, marshal(t, body), "printed-secret@example.test",
		"printing must not bypass the contact filtering")
	assert.Greater(t, body.Limit, int64(50), "print raises the page bound")
}

// TestDirectoryExposesNoAdministrativeFields keeps the payload minimal.
func TestDirectoryExposesNoAdministrativeFields(t *testing.T) {
	env, cookie := eligibleEnv(t)
	person := dirMember(t, env, "Bbb Minimal", "W3MIN", "full", "approved")
	dirContact(t, env, person, "postal", "1 Main Street", "full_members")

	_, err := env.db.Exec(`
		INSERT INTO notes (subject_kind, subject_id, category, visibility, body, author_id, source)
		VALUES ('person', ?, 'general', 'officer', 'Officer only note', 1, 'officer')`, person)
	require.NoError(t, err)

	resp := env.do(t, http.MethodGet, "/api/v1/directory?limit=100", cookie, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	raw := readAll(t, resp)

	for _, forbidden := range []string{
		"1 Main Street", "postal", "Officer only note",
		"paid_through", "amount_cents", "deceased_at", "note",
	} {
		assert.NotContains(t, raw, forbidden,
			"the directory payload must not carry %q", forbidden)
	}
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
