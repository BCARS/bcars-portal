package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Banner messages come from a fixed set (bcars-portal-9lx).

// flashKeyIn returns the message key a redirect carries.
//
// Assertions go through this rather than looking for words in the URL: the
// words are no longer there, and a test that searched for them would pass on
// any redirect at all once they were gone.
func flashKeyIn(t *testing.T, location string) string {
	t.Helper()
	u, err := url.Parse(location)
	require.NoError(t, err, "redirect location %q", location)
	return u.Query().Get(flashParam)
}

// assertFlash checks the redirect carries exactly this key.
func assertFlash(t *testing.T, w *httptest.ResponseRecorder, key string) {
	t.Helper()
	loc := w.Header().Get("Location")
	assert.Equal(t, key, flashKeyIn(t, loc), "redirect was %q", loc)
}

// assertFlashKind checks the redirect carries a key of this kind, for the tests
// that care only that the officer was told yes or told no.
func assertFlashKind(t *testing.T, w *httptest.ResponseRecorder, kind flashKind) {
	t.Helper()
	loc := w.Header().Get("Location")
	key := flashKeyIn(t, loc)
	entry, ok := flashCatalog[key]
	require.True(t, ok, "redirect %q carries no known message key", loc)
	assert.Equal(t, kind, entry.kind, "key %q", key)
}

func TestACraftedBannerSaysNothing(t *testing.T) {
	// The reported attack: a sentence in the URL, in the portal's chrome, to
	// an officer who followed a link from a club mailing list.
	crafted := []string{
		"/admin/members/10?flash=Your+account+was+deleted.+Call+814-555-0000+to+restore",
		"/admin/members/10?success=Your+account+was+deleted.+Call+814-555-0000",
		"/admin/members/10?error=Your+account+was+deleted.+Call+814-555-0000",
		"/admin/members/10?msg=Your+account+was+deleted.+Call+814-555-0000",
		"/admin/members/10?msg=member.created.evil",
		"/admin/members/10?msg=",
	}
	for _, target := range crafted {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		success, failure := flashBanner(r)
		assert.Empty(t, success, "target %q", target)
		assert.Empty(t, failure, "target %q", target)
	}
}

func TestAKnownKeyRendersItsOwnSentence(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, flashTarget("/admin/members/10", "member.created"), nil)
	success, failure := flashBanner(r)
	assert.Equal(t, "Member created", success)
	assert.Empty(t, failure)

	// A failure key reaches the failure banner, never the success one, so the
	// kind cannot be chosen by whoever writes the link.
	r = httptest.NewRequest(http.MethodGet, flashTarget("/admin/members/10", "form.invalid"), nil)
	success, failure = flashBanner(r)
	assert.Empty(t, success)
	assert.Equal(t, "Please check your entries and try again", failure)
}

func TestCountsAreRenderedNotPasted(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, flashTarget("/admin/imports/3", "import.committed", 20, 0, 1), nil)
	success, _ := flashBanner(r)
	assert.Equal(t, "Import committed: 20 created, 0 updated, 1 skipped", success)

	// A count that is not a number, or is out of range, cannot put text in the
	// sentence or make it absurd.
	r = httptest.NewRequest(http.MethodGet, "/admin/imports/3?msg=import.committed&n=lots", nil)
	success, failure := flashBanner(r)
	assert.Empty(t, success)
	assert.Empty(t, failure)

	r = httptest.NewRequest(http.MethodGet, "/admin/imports/3?msg=import.committed&n=-5&n=999999999", nil)
	success, _ = flashBanner(r)
	assert.Equal(t, "Import committed: 0 created, 99999 updated, 0 skipped", success)
}

func TestOnlyKnownReasonsAreAppended(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet,
		flashTargetWhy("/admin/requests/3", "request.applied_partial", []int{1}, []string{"stale", "self"}), nil)
	_, failure := flashBanner(r)
	assert.Contains(t, failure, "Applied 1 change.")
	assert.Contains(t, failure, "the record moved while you were reading it")
	assert.Contains(t, failure, "because you submitted it")

	// An invented reason is dropped rather than printed, and a repeated one is
	// said once.
	r = httptest.NewRequest(http.MethodGet,
		"/admin/requests/3?msg=request.applied_partial&n=1&why=Call+814-555-0000&why=stale&why=stale", nil)
	_, failure = flashBanner(r)
	assert.Equal(t, "Applied 1 change. One change was left alone because the record moved while you were reading it; reload and look again.", failure)
}

// Every key the handlers can emit has to exist here, and every entry has to be
// able to render. A typo in a key is otherwise a silent no-banner.
func TestEveryCatalogEntryRenders(t *testing.T) {
	for key, entry := range flashCatalog {
		r := httptest.NewRequest(http.MethodGet, flashTarget("/x", key, 1, 2, 3), nil)
		success, failure := flashBanner(r)
		assert.NotEmpty(t, success+failure, "key %q renders nothing", key)
		if entry.build == nil {
			assert.NotEmpty(t, entry.text, "key %q has neither text nor build", key)
		}
	}
	for key, text := range flashReasons {
		assert.NotEmpty(t, text, "reason %q has no text", key)
	}
}

// TestTheReportedLinkSaysNothingOnThePage is the walkthrough's finding, held at
// the page level. The unit test above proves flashBanner returns nothing; this
// proves the page an officer actually opens prints nothing, which is the claim
// the bead makes (bcars-portal-9lx).
func TestTheReportedLinkSaysNothingOnThePage(t *testing.T) {
	e := setupMemberEnv(t)
	officer := e.officerCookie(t)
	personID := e.grant(t, "Dale Rutherford")

	sentence := "Your account was deleted. Call 814-555-0000 to restore"
	for _, param := range []string{"flash", "success", "error", "msg"} {
		target := fmt.Sprintf("/admin/members/%d?%s=%s", personID, param, url.QueryEscape(sentence))
		w := e.getAs(t, target, officer)
		require.Equal(t, http.StatusOK, w.Code, target)
		assert.NotContains(t, w.Body.String(), "814-555-0000",
			"the portal repeated a stranger's sentence from %s", target)
		assert.NotContains(t, w.Body.String(), "Your account was deleted",
			"the portal repeated a stranger's sentence from %s", target)
	}

	// The same page still says what the portal itself did.
	w := e.getAs(t, flashTarget(fmt.Sprintf("/admin/members/%d", personID), "member.updated"), officer)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Member updated",
		"a real message must still reach the banner")
}
