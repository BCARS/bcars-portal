package web

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/bcars/bcars-portal/internal/domain/directory"
)

// The member directory UI (bcars-portal-4ux.12).
//
// A plain sortable table with printing as a primary action, over the
// server-filtered directory service and nothing else. There is no second query
// path here, no per-row lookup, and no filtering in a template.
//
// THE VIEW MODEL CANNOT LEAK A HIDDEN VALUE
//
// This is the property the whole file is arranged around. A contact the caller
// may not see is never selected by the SQL, so it never reaches the service; and
// the row type below carries a rendered STRING rather than the contact list, so
// by the time a template runs there is nothing to accidentally render. "Not
// shared" is computed here, once, and a template that wanted to be indiscreet
// has no field to be indiscreet with.
//
// WITHHELD AND ABSENT READ THE SAME
//
// A member who shares no phone and a member who has no phone on file produce
// the identical cell. That is deliberate: a distinct "hidden" marker would tell
// the reader that a number exists and is being kept from them, which is a
// disclosure the member did not agree to. It also keeps every row the same
// shape, which is what makes the printed sheet readable.
//
// ELIGIBILITY IS NOT THE CAPABILITY
//
// directory.read gets a caller as far as this handler. Whether they may see a
// listing is a separate question the service answers per request, from an active
// grant to an active approved Full membership. An Associate holds the
// capability, uses their own profile, and is answered here exactly as if the
// page did not exist.

// Member directory routes.
const (
	RouteMemberDirectory      = "/member/directory"
	RouteMemberDirectoryPrint = "/member/directory/print"
)

// DirectoryRoutes returns the member directory routes.
func (h *Handler) DirectoryRoutes() []GuardedRoute {
	return []GuardedRoute{
		{Pattern: "GET " + RouteMemberDirectory, Capability: "directory.read", AuditAction: "directory.read", ResourceKind: "directory", handler: h.directoryPage},
		{Pattern: "GET " + RouteMemberDirectoryPrint, Capability: "directory.read", AuditAction: "directory.print", ResourceKind: "directory", handler: h.directoryPrint},
	}
}

// notSharedText is the one string both an absent and a withheld contact render
// as. It is a constant because the two must never drift apart into two
// distinguishable spellings.
const notSharedText = "Not shared"

type directoryData struct {
	Rows   []directoryRow
	Search string
	Sort   string
	// BaseType is the membership-type filter in effect, empty for both.
	BaseType string

	// Total counts every member in the directory, not every member whose
	// details this caller can see. It does not vary by viewer, which is what
	// keeps it from disclosing how many members withhold their details.
	Total  int64
	Limit  int64
	Offset int64

	// HasPrev and HasNext drive the paging controls, so a template does no
	// arithmetic about which pages exist.
	HasPrev bool
	HasNext bool
	PrevURL string
	NextURL string

	// PrintURL carries the same search, filter, and sort to the printable
	// view, so what prints is what the reader was looking at.
	PrintURL string

	// SortOptions and TypeOptions drive the controls.
	SortOptions []labelledValue
	TypeOptions []labelledValue
}

// directoryRow is one rendered line.
//
// Email and Phone are STRINGS, already reading "Not shared" where nothing is
// shared. There is no []Contact here and no "hidden" flag, because a field that
// does not exist cannot be rendered by mistake.
type directoryRow struct {
	PersonID    int64
	DisplayName string
	CallSign    string
	BaseType    string

	Email string
	Phone string
	// EmailShared and PhoneShared drive the muted styling only. They say
	// whether the cell holds a value, which the cell already shows; they carry
	// no value themselves.
	EmailShared bool
	PhoneShared bool
}

// printData is the printable roster.
//
// It carries the same rows and no extra fields. A print view that could show
// something the screen could not would be a second, laxer read path wearing a
// stylesheet.
type printData struct {
	Rows []directoryRow
	// Club identifies whose roster this is once it is off the screen and on
	// somebody's kitchen table.
	Club     string
	Search   string
	BaseType string
	Total    int64
	// Shown is how many rows are on the sheet, which can be fewer than Total
	// when a club outgrows the print bound.
	Shown int
	// Truncated says the sheet is short of the full roster, so it can say so
	// rather than quietly ending and reading as the whole club.
	Truncated bool
}

const clubName = "Bedford County Amateur Radio Society"

func (h *Handler) directoryPage(w http.ResponseWriter, r *http.Request) {
	page, q, ok := h.loadDirectory(w, r, false)
	if !ok {
		return
	}

	data := directoryData{
		Search:      q.Search,
		Sort:        q.Sort,
		BaseType:    q.BaseType,
		Total:       page.Total,
		Limit:       page.Limit,
		Offset:      page.Offset,
		SortOptions: directorySortOptions(),
		TypeOptions: directoryTypeOptions(),
		PrintURL:    RouteMemberDirectoryPrint + "?" + directoryQuery(q, 0),
	}
	for _, e := range page.Entries {
		data.Rows = append(data.Rows, directoryRowFrom(e))
	}

	if page.Offset > 0 {
		prev := page.Offset - page.Limit
		if prev < 0 {
			prev = 0
		}
		data.HasPrev = true
		data.PrevURL = RouteMemberDirectory + "?" + directoryQuery(q, prev)
	}
	if page.Offset+int64(len(page.Entries)) < page.Total {
		data.HasNext = true
		data.NextURL = RouteMemberDirectory + "?" + directoryQuery(q, page.Offset+page.Limit)
	}

	h.render.RenderHTTP(w, "directory.html", http.StatusOK, data)
}

// directoryPrint renders the whole filtered roster on one sheet.
//
// It is the same service call with the print bound raised, not a different
// query: the printed list is the list the reader filtered, and an ineligible
// caller is refused here exactly as on screen rather than finding printing to
// be the unguarded way in.
func (h *Handler) directoryPrint(w http.ResponseWriter, r *http.Request) {
	page, q, ok := h.loadDirectory(w, r, true)
	if !ok {
		return
	}

	data := printData{
		Club:     clubName,
		Search:   q.Search,
		BaseType: q.BaseType,
		Total:    page.Total,
		Shown:    len(page.Entries),
	}
	data.Truncated = int64(data.Shown) < page.Total
	for _, e := range page.Entries {
		data.Rows = append(data.Rows, directoryRowFrom(e))
	}
	h.render.RenderHTTP(w, "directory_print.html", http.StatusOK, data)
}

// loadDirectory reads one page for the caller, or renders the refusal.
//
// ErrNotEligible renders the SAME not-found page an unknown route would, and
// deliberately not a "you are not eligible" message: telling an Associate that
// the directory exists and that other members can read it is more than they
// need to know, and the service's own doc comment requires callers to translate
// the error this way.
func (h *Handler) loadDirectory(w http.ResponseWriter, r *http.Request, print bool) (directory.Page, directory.Query, bool) {
	p := h.principalFromRequest(r)
	q := directoryQueryFrom(r, print)

	page, err := h.directory.List(r.Context(), p, q)
	if errors.Is(err, directory.ErrNotEligible) {
		h.renderError(w, r, http.StatusNotFound, "No such page.")
		return directory.Page{}, q, false
	}
	if err != nil {
		h.log.Error("directory", slog.String("error", err.Error()))
		h.renderError(w, r, http.StatusInternalServerError, "The directory could not be loaded. Please try again.")
		return directory.Page{}, q, false
	}
	return page, q, true
}

// directoryQueryFrom reads the query string.
//
// An unknown sort or membership type falls back to the default rather than
// erroring. These arrive from a link or a typed URL and are display
// preferences; refusing the page over one would turn a stray character into a
// dead end.
func directoryQueryFrom(r *http.Request, print bool) directory.Query {
	q := directory.Query{
		Search:   strings.TrimSpace(r.URL.Query().Get("search")),
		BaseType: r.URL.Query().Get("type"),
		Sort:     r.URL.Query().Get("sort"),
		Print:    print,
	}
	if !directory.ValidFilter(q.BaseType) {
		q.BaseType = directory.FilterAll
	}
	if !directory.ValidSort(q.Sort) {
		q.Sort = directory.SortName
	}
	if !print {
		q.Offset, _ = strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
		if q.Offset < 0 {
			q.Offset = 0
		}
	}
	return q
}

// directoryQuery rebuilds the query string for a link, so paging and printing
// keep the reader's search, filter, and sort instead of silently resetting them.
func directoryQuery(q directory.Query, offset int64) string {
	values := make([]string, 0, 4)
	if q.Search != "" {
		values = append(values, "search="+url.QueryEscape(q.Search))
	}
	if q.BaseType != "" {
		values = append(values, "type="+url.QueryEscape(q.BaseType))
	}
	if q.Sort != "" {
		values = append(values, "sort="+url.QueryEscape(q.Sort))
	}
	if offset > 0 {
		values = append(values, "offset="+strconv.FormatInt(offset, 10))
	}
	return strings.Join(values, "&")
}

func directorySortOptions() []labelledValue {
	return []labelledValue{
		{directory.SortName, "Name"},
		{directory.SortCallSign, "Call sign"},
	}
}

func directoryTypeOptions() []labelledValue {
	return []labelledValue{
		{directory.FilterAll, "All members"},
		{directory.FilterFull, "Full members"},
		{directory.FilterAssociate, "Associates"},
	}
}

// directoryRowFrom renders one entry.
//
// This is where a contact list becomes text, and it is the only place that
// happens. Everything below the return value is a string a template prints.
func directoryRowFrom(e directory.Entry) directoryRow {
	return directoryRow{
		PersonID:    e.PersonID,
		DisplayName: e.DisplayName,
		CallSign:    e.CallSign,
		BaseType:    e.BaseType,
		Email:       contactText(e.Emails),
		Phone:       contactText(e.Phones),
		EmailShared: e.EmailShared(),
		PhoneShared: e.PhoneShared(),
	}
}

// contactText joins every shared value of one kind, or says nothing is shared.
//
// A member may share more than one number and both can matter, so all of them
// are listed rather than the first. A label is included when there is one and
// there is more than one value: "home" beside a lone number is noise, but with
// two numbers it is the whole reason the reader can pick the right one.
func contactText(contacts []directory.Contact) string {
	if len(contacts) == 0 {
		return notSharedText
	}
	parts := make([]string, 0, len(contacts))
	for _, c := range contacts {
		if c.Label != "" && len(contacts) > 1 {
			parts = append(parts, c.Label+": "+c.Value)
			continue
		}
		parts = append(parts, c.Value)
	}
	return strings.Join(parts, ", ")
}
