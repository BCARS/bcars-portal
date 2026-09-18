package web

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tables stay inside the card that draws around them (bcars-portal-7jg).
//
// At the Larger text size the directory's columns needed more width than the
// card had, and the table drew straight past its right edge: the border
// stopped mid-row and the last column sat on the page background. `width:100%`
// is a preferred width, not a maximum, so a table whose minimum column widths
// exceed the container overflows it, and neither the table nor the card had
// anywhere to put that overflow.
//
// A Go test cannot measure a rendered layout, and this file does not pretend
// to. What it holds is the structural precondition for the fix: every table on
// a chrome-wrapped page sits inside a .table-wrap, and that class scrolls.
// Whether the result looks right at 22px was checked by running the demo
// portal and looking at it; this keeps the next screen from dropping the
// wrapper silently.

var tableTagRE = regexp.MustCompile(`(?s)<table[\s>]`)

// printOnlyTemplates render their own document for paper and carry their own
// stylesheet. Scrolling means nothing on a printed page.
var printOnlyTemplates = map[string]bool{
	"directory_print.html": true,
	"tokens.html":          true,
}

func TestEveryTableSitsInAScrollableWrapper(t *testing.T) {
	entries, err := templateFS.ReadDir("templates")
	require.NoError(t, err)

	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if printOnlyTemplates[name] {
			continue
		}
		body, err := templateFS.ReadFile("templates/" + name)
		require.NoError(t, err)
		src := string(body)
		if !tableTagRE.MatchString(src) {
			continue
		}

		tables := len(tableTagRE.FindAllString(src, -1))
		wrappers := strings.Count(src, `<div class="table-wrap">`)
		assert.GreaterOrEqualf(t, wrappers, tables,
			"%s has %d table(s) but %d .table-wrap wrapper(s); a table without one "+
				"draws past the card at the Larger text size", name, tables, wrappers)
		checked += tables
	}

	// A count, so this test cannot quietly pass by finding no templates at all
	// after a rename or a move.
	assert.Greater(t, checked, 15, "expected the portal's tabular screens to be found")
}

// The wrapper has to actually scroll, and must not clip a printed table, which
// would lose columns outright rather than merely hiding them behind a
// scrollbar.
func TestTheWrapperScrollsOnScreenAndNotOnPaper(t *testing.T) {
	body, err := templateFS.ReadFile("templates/tokens.html")
	require.NoError(t, err)
	css := string(body)

	assert.Contains(t, css, ".table-wrap { overflow-x: auto; }",
		"the wrapper is what gives the overflow somewhere to go")
	assert.Contains(t, css, "@media print { .table-wrap { overflow-x: visible; } }",
		"a printed table must not be clipped to the wrapper's width")
}

// The directory is the screen the bug was reported on, and the one the Larger
// setting exists for, so it is asserted by name as well as by the sweep above.
func TestTheDirectoryTableIsWrappedAtBothTextSizes(t *testing.T) {
	e := setupMemberEnv(t)
	cookie, _ := e.eligibleMember(t)
	other := e.dirPerson(t, "Verylongname Memberperson", "W3XYZ", "full")
	e.dirContactShared(t, other, "email", "a.very.long.email.address@example.invalid", "full_members", "")
	e.dirContactShared(t, other, "phone", "814-555-0199", "full_members", "")

	for _, size := range []string{"base", "large"} {
		_, err := e.h.db.Exec(`UPDATE users SET text_size = ? WHERE id = ?`, size, e.memberUserID)
		require.NoError(t, err)

		body := e.getAs(t, RouteMemberDirectory, cookie).Body.String()
		require.Contains(t, body, `data-text-size="`+size+`"`, "precondition: the size reached the page")

		// The table is inside the wrapper, which is inside the card.
		idx := strings.Index(body, "<table")
		require.NotEqual(t, -1, idx, "the directory should render a table at size %s", size)
		before := body[:idx]
		wrapAt := strings.LastIndex(before, `<div class="table-wrap">`)
		cardAt := strings.LastIndex(before, `<div class="card">`)
		assert.NotEqual(t, -1, wrapAt, "the directory table needs its scroll wrapper at size %s", size)
		assert.Greater(t, wrapAt, cardAt,
			"the wrapper belongs inside the card, so the overflow scrolls within the border")
	}
}
