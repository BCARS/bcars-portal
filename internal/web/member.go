package web

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bcars/bcars-portal/internal/domain/changerequests"
	"github.com/bcars/bcars-portal/internal/domain/memberprofile"
)

// The member self-service UI (bcars-portal-4ux.11).
//
// It renders what the member API already decides. Every read goes through
// memberprofile and every write through changerequests, so this file contains
// no authorization logic of its own — a page cannot show a record the domain
// service would refuse, because the page never receives one.
//
// TWO AUTHORITIES, VISIBLY SEPARATE
//
// A member may SEE only records an officer granted them, and may SUGGEST a
// correction about anyone. The UI keeps those apart on purpose: the suggestion
// form for someone else has no lookup, no autocomplete, no candidate list, and
// no confirmation that the person exists, and every page that offers it says
// in plain words that an officer must review the suggestion and that sending
// one gives the sender no access to that person's record.
//
// CSRF
//
// These forms carry no token, which matches the admin UI and the same reasoning
// (TestSessionCookieIsSameSiteLax): the session cookie is SameSite=Lax, so a
// browser does not attach it to a cross-site POST, and such a request arrives
// unauthenticated and is redirected to sign-in rather than acted on.

// Member routes. Exported for the same reason the emailed-link routes are:
// one place names them, so a template link and a mux pattern cannot drift.
const (
	RouteMemberRecords  = "/member/records/"
	RouteMemberSuggest  = "/member/suggest"
	RouteMemberRequests = "/member/requests"
)

// MemberRoutes returns every member self-service route with its capability
// requirement.
//
// It is a separate table from AdminRoutes so the two cannot be confused: an
// officer capability must never appear here, and a route added here reaches a
// caller holding only the member role.
//
// The profile pages require profile.self.read; the suggestion and request
// pages require change_request.submit.member, because suggesting a correction
// is not a read and must not require one. Where a suggestion page also shows a
// record, it loads it through memberprofile, which authorizes the read
// separately and answers "no such record" when the caller may not see it.
func (h *Handler) MemberRoutes() []GuardedRoute {
	return []GuardedRoute{
		{Pattern: "GET " + RouteMemberHome, Capability: "profile.self.read", ResourceKind: "member_access_grant", handler: h.memberHome},
		{Pattern: "GET " + RouteMemberRecords + "{person_id}", Capability: "profile.self.read", ResourceKind: "person", handler: h.memberRecord},

		{Pattern: "GET " + RouteMemberRecords + "{person_id}/suggest", Capability: "change_request.submit.member", ResourceKind: "change_request", handler: h.memberSuggestOwnForm},
		{Pattern: "POST " + RouteMemberRecords + "{person_id}/suggest", Capability: "change_request.submit.member", AuditAction: "change_request.submit", ResourceKind: "change_request", handler: h.memberSuggestOwnSubmit},

		{Pattern: "GET " + RouteMemberSuggest, Capability: "change_request.submit.member", ResourceKind: "change_request", handler: h.memberSuggestOtherForm},
		{Pattern: "POST " + RouteMemberSuggest, Capability: "change_request.submit.member", AuditAction: "change_request.submit", ResourceKind: "change_request", handler: h.memberSuggestOtherSubmit},

		{Pattern: "GET " + RouteMemberRequests, Capability: "change_request.submit.member", ResourceKind: "change_request", handler: h.memberRequests},
		{Pattern: "GET " + RouteMemberRequests + "/{id}", Capability: "change_request.submit.member", ResourceKind: "change_request", handler: h.memberRequestDetail},
		{Pattern: "POST " + RouteMemberRequests + "/{id}/withdrawal", Capability: "change_request.submit.member", AuditAction: "change_request.withdraw", ResourceKind: "change_request", handler: h.memberRequestWithdraw},
	}
}

// A report about a record the member cannot see carries NO structured change
// (ADR-0014.4, bcars-portal-ssz.4).
//
// It used to offer the same field list as the own-record form -- their name,
// their call sign, their contact details -- and produce an item with no target,
// because naming the target would mean looking the person up, which this form
// must never do. Nothing could ever apply such an item: an officer who linked
// the request was told to link the request (bcars-portal-3la).
//
// So it is a note. The member writes what they know, an officer reads it and
// edits the record directly, which is what they would do with the same sentence
// heard at a meeting. The submission boundary ADR-0013 protects is unchanged:
// no grant, no relationship, no lookup, and nothing learned by sending it.

// memberDate turns a stored value into something a member reads.
//
// The database stores RFC 3339 because a machine sorts it; "2026-08-12T12:34:06.354Z"
// on a page asking someone to remember when they sent something is the storage
// format leaking into the product. An unparseable value falls back to the raw
// string rather than to an empty cell, because a wrong-looking date is easier
// to report than a missing one.
//
// IT DOES NOT CONVERT TO LOCAL TIME, and that is the point. "Paid through
// 2026-12-31" is a calendar date the club decided, not an instant: parsing it as
// UTC midnight and rendering it in a negative-offset zone moved it to 30
// December, so a member west of Greenwich was shown their dues expiring a day
// early. A date the club wrote down reads back as the date the club wrote down,
// wherever the reader happens to be.
func memberDate(stored string) string {
	// Coverage dates are stored date-only and timestamps in RFC 3339; both
	// reach this function, so both are tried.
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, isoStamp, "2006-01-02"} {
		if parsed, err := time.Parse(layout, stored); err == nil {
			return parsed.UTC().Format("2 January 2006")
		}
	}
	return stored
}

const isoStamp = "2006-01-02T15:04:05.000Z"

// membershipStanding describes the lifecycle in words, and says nothing at all
// for the ordinary case. "approved" is database vocabulary; a member reading
// their own record does not need to be told the normal state has a name.
func membershipStanding(lifecycle string) string {
	switch lifecycle {
	case "", "approved":
		return ""
	case "pending":
		return "Awaiting approval"
	case "rejected":
		return "Not approved"
	case "resigned":
		return "Resigned"
	case "deceased":
		return ""
	default:
		return lifecycle
	}
}

// kindLabel names an operation in a table column, where the possessive phrasing
// the forms use ("My name is wrong") would read wrongly — both for a delegate
// acting on someone's behalf and for a suggestion about another member.
func kindLabel(operation string) string {
	switch operation {
	case "person.display_name.set":
		return "Name"
	case "person.call_sign.set":
		return "Call sign"
	case "contact_method.update":
		return "Contact detail"
	default:
		return "Something else"
	}
}

// proposedValueLabel renders a proposed value the way a reader should see it.
//
// A contact value is stored as "kind:value" so the review path can tell an
// email from a phone (parseContactValue in the changerequests service). That
// encoding is for the applier, not for the member who typed the value or the
// officer deciding on it, so it is unwound here: "phone:814-555-0199" reads as
// "phone — 814-555-0199", and the kind is stated rather than dropped, because
// which detail is being corrected is part of what the officer is approving.
//
// Anything that does not carry the encoding is shown as written. Older rows
// predate it, and no display should turn into an error page over a value.
func proposedValueLabel(operation, raw string) string {
	if operation != "contact_method.update" && operation != "contact_method.create" {
		return raw
	}
	kind, value, found := strings.Cut(raw, ":")
	if !found || strings.TrimSpace(kind) == "" || strings.TrimSpace(value) == "" {
		return raw
	}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "email", "phone", "postal":
		return strings.TrimSpace(kind) + " — " + strings.TrimSpace(value)
	default:
		return raw
	}
}

// --- Landing ---

type memberHomeData struct {
	Email string
	// Records are the person records this caller may currently reach. An empty
	// list is a real state, not an error: an account exists before an officer
	// grants it anything, and a revoked grant leaves it empty again.
	Records []memberRecordRow
	// OpenRequests counts the caller's own suggestions still awaiting an
	// officer, so the landing says whether anything is outstanding rather than
	// only offering a link to go and look.
	OpenRequests int
	// DirectoryAvailable gates the directory link.
	//
	// Eligibility is asked of the service rather than inferred from the member
	// role or the directory.read capability, because it is neither: it is an
	// active grant to an active approved FULL membership. An Associate holds
	// the capability and is refused the listing, so a link shown to them would
	// lead to the not-found page the refusal renders (bcars-portal-4ux.12).
	DirectoryAvailable bool
}

type memberRecordRow struct {
	PersonID    int64
	DisplayName string
	CallSign    string
	AccessKind  string
	DuesStatus  string
	// PaidThrough is the coverage date in words. Empty when no coverage
	// decision has ever been recorded, which the page reports as "not
	// recorded" rather than inventing a date.
	PaidThrough string
}

// memberHome is where a signed-in member lands.
//
// It reads the caller's active grants on every request through the domain
// service. Nothing about which records appear here comes from the session, the
// URL, or a matching contact address, so revoking a grant removes the record
// from the very next page load of a session that is already open (ADR-0010).
func (h *Handler) memberHome(w http.ResponseWriter, r *http.Request) {
	// RouteMemberHome is a prefix pattern, so an unclaimed path under /member/
	// lands here. Rendering the member landing for it would report a wrong URL
	// as a working page, the same way /admin/ did (bcars-portal-i4a).
	if r.URL.Path != RouteMemberHome {
		h.renderError(w, r, http.StatusNotFound, "That page does not exist.")
		return
	}

	ctx := r.Context()
	p := h.principalFromRequest(r)

	data := memberHomeData{}
	_ = h.db.QueryRowContext(ctx, `SELECT email FROM users WHERE id = ?`, p.UserID).Scan(&data.Email)

	profiles, err := h.memberProfiles.List(ctx, p)
	if err != nil {
		h.log.Error("member records", slog.String("error", err.Error()))
		h.renderError(w, r, http.StatusInternalServerError, "Your records could not be loaded. Please try again.")
		return
	}
	for _, profile := range profiles {
		data.Records = append(data.Records, memberRecordRowFrom(profile))
	}

	// A caller who may not submit still gets the landing; they simply see no
	// request section. The capability, not the template, decides.
	if hasCap(p, "change_request.submit.member") {
		own, err := h.changeRequests.List(ctx, p, changerequests.ListFilter{
			RequesterUserID: p.UserID, Limit: changerequests.MaxLimit,
		})
		if err == nil {
			for _, req := range own {
				if req.Status != changerequests.StatusResolved &&
					req.Status != changerequests.StatusWithdrawn {
					data.OpenRequests++
				}
			}
		}
	}

	if hasCap(p, "directory.read") {
		eligible, err := h.directory.Eligible(ctx, p)
		if err != nil {
			// A failed probe hides the link rather than showing one that might
			// not work. The directory is a convenience; the records below it
			// are the reason the member signed in.
			h.log.Error("directory eligibility", slog.String("error", err.Error()))
		}
		data.DirectoryAvailable = eligible
	}

	h.renderPage(w, r, "member_home.html", http.StatusOK, data)
}

func memberRecordRowFrom(profile memberprofile.Profile) memberRecordRow {
	row := memberRecordRow{
		PersonID:    profile.PersonID,
		DisplayName: profile.DisplayName,
		CallSign:    profile.CallSign,
		AccessKind:  profile.AccessKind,
	}
	if profile.Standing != nil {
		row.DuesStatus = profile.Standing.Status
		if profile.Standing.PaidThrough != "" {
			row.PaidThrough = memberDate(profile.Standing.PaidThrough)
		}
	}
	return row
}

// --- One record ---

type memberRecordData struct {
	Record   memberRecordRow
	Contacts []memberContactRow
	// BaseType is the underlying right, which an honorary dues waiver never
	// changes. Lifecycle is already phrased for a reader, and is empty for the
	// ordinary approved case.
	BaseType  string
	Lifecycle string
	Success   string
}

type memberContactRow struct {
	ID         int64
	Kind       string
	Label      string
	Value      string
	Primary    bool
	SharedWith string
	Version    int64
}

func (h *Handler) memberRecord(w http.ResponseWriter, r *http.Request) {
	profile, ok := h.loadMemberRecord(w, r)
	if !ok {
		return
	}

	data := memberRecordData{
		Record:    memberRecordRowFrom(profile),
		BaseType:  profile.BaseType,
		Lifecycle: membershipStanding(profile.Lifecycle),
		Success:   r.URL.Query().Get("success"),
	}
	for _, c := range profile.Contacts {
		data.Contacts = append(data.Contacts, memberContactRow{
			ID: c.ID, Kind: c.Kind, Label: c.Label, Value: c.Value,
			Primary: c.Primary, SharedWith: c.SharedWith, Version: c.Version,
		})
	}

	h.renderPage(w, r, "member_record.html", http.StatusOK, data)
}

// loadMemberRecord reads the record named in the path for this caller.
//
// A record the caller was never granted and a record that does not exist both
// render the same "not found" page, because a page that said "you may not see
// that" would confirm the record exists.
func (h *Handler) loadMemberRecord(w http.ResponseWriter, r *http.Request) (memberprofile.Profile, bool) {
	p := h.principalFromRequest(r)
	personID, _ := strconv.ParseInt(r.PathValue("person_id"), 10, 64)

	profile, err := h.memberProfiles.Get(r.Context(), p, personID)
	if errors.Is(err, memberprofile.ErrNotFound) {
		h.renderError(w, r, http.StatusNotFound, "No such record.")
		return memberprofile.Profile{}, false
	}
	if err != nil {
		h.log.Error("member record", slog.String("error", err.Error()))
		h.renderError(w, r, http.StatusInternalServerError, "That record could not be loaded. Please try again.")
		return memberprofile.Profile{}, false
	}
	return profile, true
}

// --- Suggesting a correction ---

// memberSuggestData is the note form about somebody else. It names no record
// and offers no field list, because a note proposes nothing (ADR-0014.4).
type memberSuggestData struct {
	// Submitted holds what the member typed, so a rejected submission comes
	// back with their words rather than an empty form.
	Submitted memberSuggestForm
	Error     string
}

type memberSuggestForm struct {
	// Target is what the radio posted, kept so a rejected form comes back with
	// the member's choice still selected.
	Target          string
	Kind            string
	ProposedValue   string
	ContactID       int64
	AboutName       string
	AboutCallSign   string
	Relationship    string
	Summary         string
	ContactSelected string
}

// --- The member's edit form (bcars-portal-ssz.2, ADR-0014) ---

// memberEditData is the record as an editable form.
//
// It carries the CURRENT value of every field, because the form is a mirror of
// the record rather than a question about it. A member who has moved house
// corrects the address and the telephone number in one submission; the single
// question this replaced made that two submissions, or a note.
type memberEditData struct {
	About    memberRecordRow
	Contacts []memberEditContact
	// PersonVersion is the version of the record the member is looking at. It
	// rides the form so review can tell whether an officer changed the name or
	// call sign in the meantime.
	PersonVersion int64
	// Submitted holds what the member typed on a submission that came back with
	// a problem, so their words survive the round trip.
	Submitted memberEditForm
	Error     string
}

// memberEditContact is one contact detail as an editable row.
type memberEditContact struct {
	ID int64
	// Label is what the field is called on screen: "Email (Home)".
	Label string
	// Field is the form input name, "contact_12". Contacts are rows, not
	// columns, so each needs its own name rather than an index a reordering
	// could shift.
	Field   string
	Value   string
	Version int64
}

// memberEditForm is what the member posted.
type memberEditForm struct {
	DisplayName string
	CallSign    string
	// Contacts maps a contact id to the value the member typed for it.
	Contacts map[int64]string
	Note     string
}

func (h *Handler) memberSuggestOwnForm(w http.ResponseWriter, r *http.Request) {
	profile, ok := h.loadMemberRecord(w, r)
	if !ok {
		return
	}
	h.renderPage(w, r, "member_suggest_own.html", http.StatusOK, memberEditDataFor(profile, memberEditForm{}, ""))
}

// memberEditDataFor builds the form, filled with the record's current values or
// with what the member typed when a submission is being handed back.
func memberEditDataFor(profile memberprofile.Profile, submitted memberEditForm, problem string) memberEditData {
	data := memberEditData{
		About:         memberRecordRowFrom(profile),
		PersonVersion: profile.PersonVersion,
		Error:         problem,
	}

	filled := problem != ""
	data.Submitted = submitted
	if !filled {
		data.Submitted = memberEditForm{
			DisplayName: profile.DisplayName,
			CallSign:    profile.CallSign,
			Contacts:    map[int64]string{},
			Note:        "",
		}
	}

	for _, c := range profile.Contacts {
		label := contactFieldLabel(c)
		value := c.Value
		if filled {
			if typed, ok := submitted.Contacts[c.ID]; ok {
				value = typed
			}
		}
		data.Contacts = append(data.Contacts, memberEditContact{
			ID:      c.ID,
			Label:   label,
			Field:   contactFieldName(c.ID),
			Value:   value,
			Version: c.Version,
		})
	}
	return data
}

// contactFieldLabel names a contact detail the way its owner would: the kind,
// and the label the club stored for it when there is one.
func contactFieldLabel(c memberprofile.Contact) string {
	label := strings.ToUpper(c.Kind[:1]) + c.Kind[1:]
	if c.Label != "" {
		label += " (" + c.Label + ")"
	}
	return label
}

func contactFieldName(id int64) string {
	return "contact_" + strconv.FormatInt(id, 10)
}

// memberSuggestOwnSubmit turns the edited form into one request carrying one
// item per field the member actually changed.
//
// UNCHANGED FIELDS PRODUCE NO ITEM. The form posts every field, including the
// ones the member never touched, so a submission that proposed all of them
// would ask an officer to approve the record's own current values back onto
// itself -- and, worse, would carry a stale version for fields nobody meant to
// touch, turning an unrelated officer edit into a conflict.
//
// Nothing here writes canonical data. Every field becomes a proposal.
func (h *Handler) memberSuggestOwnSubmit(w http.ResponseWriter, r *http.Request) {
	profile, ok := h.loadMemberRecord(w, r)
	if !ok {
		return
	}

	form, problem := readMemberEditForm(r, profile)
	render := func(msg string) {
		h.renderPage(w, r, "member_suggest_own.html", http.StatusBadRequest,
			memberEditDataFor(profile, form, msg))
	}
	if problem != "" {
		render(problem)
		return
	}

	items, changed := memberEditItems(profile, form)
	if len(items) == 0 {
		if form.Note == "" {
			render("Change something on the form, or write a note telling the officers what needs to happen.")
			return
		}
		// A note with no field edits is the "add my new work number" case: the
		// form deliberately cannot create a contact row, so the member says so
		// in words and an officer does it. It is still a reviewable item, so it
		// still reaches the queue and still resolves.
		items = []changerequests.ItemInput{{Operation: changerequests.OpOther}}
	}

	p := h.principalFromRequest(r)
	created, err := h.changeRequests.Create(r.Context(), p, changerequests.CreateParams{
		Source:          changerequests.SourceMember,
		RequesterUserID: p.UserID,
		TargetPersonID:  profile.PersonID,
		Summary:         memberEditSummary(form, changed),
		SourceIPHash:    h.clientIP.HashRequest(r),
		Items:           items,
	}, memberEditIdempotencyKey(r, profile.PersonID), time.Now())
	if err != nil {
		h.log.Error("member suggestion", slog.String("error", err.Error()))
		render(friendlyError(err))
		return
	}

	http.Redirect(w, r, RouteMemberRequests+"/"+strconv.FormatInt(created.ID, 10)+"?success=Your+suggestion+has+been+sent+to+the+officers",
		http.StatusSeeOther)
}

// readMemberEditForm reads only the fields the record actually has.
//
// A contact input is read by looking up each contact the RECORD holds, never by
// walking the posted form: a hand-built POST naming contact_99 gets no more
// attention than a browser sending nothing at all.
func readMemberEditForm(r *http.Request, profile memberprofile.Profile) (memberEditForm, string) {
	if err := r.ParseForm(); err != nil {
		return memberEditForm{}, "Please check your entries and try again."
	}

	form := memberEditForm{
		DisplayName: strings.TrimSpace(r.FormValue("display_name")),
		CallSign:    strings.TrimSpace(r.FormValue("call_sign")),
		Note:        strings.TrimSpace(r.FormValue("note")),
		Contacts:    make(map[int64]string, len(profile.Contacts)),
	}
	for _, c := range profile.Contacts {
		form.Contacts[c.ID] = strings.TrimSpace(r.FormValue(contactFieldName(c.ID)))
	}

	// A field cleared to blank is a REMOVAL, and this form does not do
	// removals (ADR-0014.3). Refusing it here, in the member's terms, is the
	// difference between "a name cannot be blank" and the domain's answer to
	// the same mistake: "changerequests: this operation needs a proposed
	// value: person.call_sign.set".
	//
	// A record that never had a call sign is not affected: blank stays blank
	// and proposes nothing.
	if form.DisplayName == "" {
		return form, "A name cannot be blank. To have something removed, ask in the note instead."
	}
	if form.CallSign == "" && profile.CallSign != "" {
		return form, "A call sign cannot be blank. To have it removed, ask in the note instead."
	}
	for _, c := range profile.Contacts {
		if form.Contacts[c.ID] == "" {
			return form, "A contact detail cannot be blank. To have one removed, ask in the note instead."
		}
	}
	return form, ""
}

// memberEditItems is the diff: one item per field whose value the member
// changed, and changed names them for the summary line.
func memberEditItems(profile memberprofile.Profile, form memberEditForm) (items []changerequests.ItemInput, changed []string) {
	if form.DisplayName != profile.DisplayName {
		items = append(items, changerequests.ItemInput{
			Operation:     "person.display_name.set",
			ProposedValue: form.DisplayName,
			TargetKind:    "person",
			TargetID:      profile.PersonID,
			TargetVersion: profile.PersonVersion,
		})
		changed = append(changed, "name")
	}
	// A call sign is compared case-insensitively: it is stored upper-case, and
	// a member who retypes their own in lower case has not proposed anything.
	if !strings.EqualFold(form.CallSign, profile.CallSign) {
		items = append(items, changerequests.ItemInput{
			Operation:     "person.call_sign.set",
			ProposedValue: form.CallSign,
			TargetKind:    "person",
			TargetID:      profile.PersonID,
			TargetVersion: profile.PersonVersion,
		})
		changed = append(changed, "call sign")
	}
	for _, c := range profile.Contacts {
		typed, ok := form.Contacts[c.ID]
		if !ok || typed == c.Value {
			continue
		}
		items = append(items, changerequests.ItemInput{
			Operation: "contact_method.update",
			// The review path reads a contact value as "kind:value" so an
			// approval cannot turn an email into a phone (bcars-portal-b4d).
			ProposedValue: c.Kind + ":" + typed,
			TargetKind:    "contact_method",
			TargetID:      c.ID,
			TargetVersion: c.Version,
		})
		changed = append(changed, strings.ToLower(contactFieldLabel(c)))
	}
	return items, changed
}

// memberEditSummary is the line an officer triages from.
//
// The member's own words win when they wrote any. Otherwise the form composes
// one naming what changed, because every request must carry a summary
// (changerequests.ErrSummaryRequired) and requiring the member to write prose
// before the form would accept a corrected digit is what bcars-portal-245
// removed.
func memberEditSummary(form memberEditForm, changed []string) string {
	if form.Note != "" {
		return form.Note
	}
	switch len(changed) {
	case 0:
		return "Asked the officers to look at this record."
	case 1:
		return "Correction to " + changed[0] + "."
	default:
		return "Corrections to " + strings.Join(changed[:len(changed)-1], ", ") +
			" and " + changed[len(changed)-1] + "."
	}
}

// memberEditIdempotencyKey derives the key a resubmitted form reuses, so the
// ordinary browser back-and-send-again files one suggestion rather than two.
//
// It is the posted values, not a fresh value per request, for the same reason
// the single-field form's key was: two identical submissions are one intent.
func memberEditIdempotencyKey(r *http.Request, personID int64) string {
	var b strings.Builder
	b.WriteString("edit-")
	b.WriteString(strconv.FormatInt(personID, 10))
	for _, field := range []string{"display_name", "call_sign", "note"} {
		b.WriteString("|")
		b.WriteString(r.FormValue(field))
	}
	// Contact inputs are named per row, so they are collected and sorted rather
	// than read from a fixed list: an unsorted map walk would produce a
	// different key for the same form on the next request.
	keys := make([]string, 0, len(r.PostForm))
	for name := range r.PostForm {
		if strings.HasPrefix(name, "contact_") {
			keys = append(keys, name)
		}
	}
	sort.Strings(keys)
	for _, name := range keys {
		b.WriteString("|")
		b.WriteString(name)
		b.WriteString("=")
		b.WriteString(r.FormValue(name))
	}
	return b.String()
}

func (h *Handler) memberSuggestOtherForm(w http.ResponseWriter, r *http.Request) {
	// The subject can arrive from the directory, so a member who spots a wrong
	// call sign while reading the roster does not have to leave, find this
	// page, and type the person's name again from memory (bcars-portal-tsj).
	//
	// These prefill the member's own words and nothing else. No lookup happens
	// here and none may: this form deliberately consults no record, so what
	// arrives is a starting point the sender can correct, not an identification
	// the portal has made.
	h.renderPage(w, r, "member_suggest_other.html", http.StatusOK, memberSuggestData{
		Submitted: memberSuggestForm{
			AboutName:     strings.TrimSpace(r.URL.Query().Get("about_name")),
			AboutCallSign: strings.TrimSpace(r.URL.Query().Get("about_call_sign")),
		},
	})
}

// memberSuggestOtherSubmit files a suggestion about a person the caller may not
// be able to see.
//
// It performs NO lookup. It does not check whether the described person exists,
// does not attach a canonical target, and reports the same outcome either way.
// An officer links the request to a record during triage; until then the club's
// records are not consulted at all, so there is nothing here that could answer
// "is this person a member".
func (h *Handler) memberSuggestOtherSubmit(w http.ResponseWriter, r *http.Request) {
	form, problem := readSuggestForm(r)
	render := func(msg string, status int) {
		h.renderPage(w, r, "member_suggest_other.html", status, memberSuggestData{
			Submitted: form,
			Error:     msg,
		})
	}
	if problem != "" {
		render(problem, http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(form.AboutName) == "" && strings.TrimSpace(form.AboutCallSign) == "" {
		render("Give the person's name or call sign, so an officer knows who this is about.", http.StatusBadRequest)
		return
	}

	p := h.principalFromRequest(r)
	created, err := h.changeRequests.Create(r.Context(), p, changerequests.CreateParams{
		Source:          changerequests.SourceMember,
		RequesterUserID: p.UserID,
		// No TargetPersonID and no item target. Both would require knowing
		// which record this concerns, and finding that out is exactly what this
		// form must not do.
		SuppliedName:       form.AboutName,
		SuppliedCallSign:   form.AboutCallSign,
		StatedRelationship: form.Relationship,
		Summary:            form.Summary,
		SourceIPHash:       h.clientIP.HashRequest(r),
		// One item, and it is the note itself. `other` can never be approved,
		// which is right: there is nothing here to apply. An officer reads the
		// summary, edits the record, and marks the request done.
		Items: []changerequests.ItemInput{{Operation: changerequests.OpOther}},
	}, idempotencyKeyFor(r), time.Now())
	if err != nil {
		h.log.Error("member note about another", slog.String("error", err.Error()))
		render(friendlyError(err), http.StatusBadRequest)
		return
	}

	http.Redirect(w, r, RouteMemberRequests+"/"+strconv.FormatInt(created.ID, 10)+"?success=Your+note+has+been+sent+to+the+officers",
		http.StatusSeeOther)
}

// readSuggestForm reads the form about SOMEBODY ELSE.
//
// The own-record form is an edit form now (ADR-0014) and is read by
// readMemberEditForm, so this no longer has an own-record branch and no longer
// takes the caller's contacts: this form shows nobody's details and proposes
// against nobody's row.
func readSuggestForm(r *http.Request) (memberSuggestForm, string) {
	if err := r.ParseForm(); err != nil {
		return memberSuggestForm{}, "Please check your entries and try again."
	}

	form := memberSuggestForm{
		Target:        r.FormValue("target"),
		Kind:          r.FormValue("kind"),
		ProposedValue: strings.TrimSpace(r.FormValue("proposed_value")),
		AboutName:     strings.TrimSpace(r.FormValue("about_name")),
		AboutCallSign: strings.TrimSpace(r.FormValue("about_call_sign")),
		Relationship:  strings.TrimSpace(r.FormValue("relationship")),
		Summary:       strings.TrimSpace(r.FormValue("summary")),
	}

	// The note IS the submission (ADR-0014.4). There is no field to choose and
	// no value to propose, so the only thing that can be missing is the words.
	if form.Summary == "" {
		return form, "Tell the officers what they should know."
	}
	return form, ""
}

// idempotencyKeyFor derives the key a resubmitted form reuses.
//
// A browser back-and-resubmit is the ordinary way a member files the same
// suggestion twice, so the key is the form the member posted rather than a
// fresh value per request: two identical submissions collapse into one request
// instead of giving officers the same correction to review twice.
func idempotencyKeyFor(r *http.Request) string {
	var b strings.Builder
	for _, field := range []string{"kind", "proposed_value", "contact_id", "about_name", "about_call_sign", "summary"} {
		b.WriteString(r.FormValue(field))
		b.WriteByte('\n')
	}
	b.WriteString(r.URL.Path)
	sum := sha256.Sum256([]byte(b.String()))
	return "web-" + hex.EncodeToString(sum[:])
}

// --- Own requests ---

type memberRequestsData struct {
	Requests []memberRequestRow
	Success  string
}

type memberRequestRow struct {
	ID          int64
	Status      string
	AboutName   string
	Summary     string
	SubmittedAt string
	// Withdrawable reports whether the member may still retract it, so the
	// list offers the button only where it would work.
	Withdrawable bool
}

type memberRequestDetailData struct {
	Request memberRequestRow
	Items   []memberRequestItemRow
	// HasProposals reports that this carries something an officer decides,
	// rather than being a note. A note's words are already on the page under
	// what the member asked for; repeating them in a table headed "the changes
	// you proposed" tells them they proposed a change they did not.
	HasProposals bool
	Success      string
	Error        string
}

type memberRequestItemRow struct {
	Label          string
	ProposedValue  string
	Status         string
	DecisionReason string
}

func (h *Handler) memberRequests(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)

	// RequesterUserID comes from the session, never from input, so there is no
	// value a caller could send that widens the list.
	own, err := h.changeRequests.List(r.Context(), p, changerequests.ListFilter{
		RequesterUserID: p.UserID,
		Limit:           changerequests.MaxLimit,
	})
	if err != nil {
		h.log.Error("member requests", slog.String("error", err.Error()))
		h.renderError(w, r, http.StatusInternalServerError, "Your suggestions could not be loaded. Please try again.")
		return
	}

	data := memberRequestsData{Success: r.URL.Query().Get("success")}
	for _, req := range own {
		data.Requests = append(data.Requests, memberRequestRowFrom(req))
	}
	h.renderPage(w, r, "member_requests.html", http.StatusOK, data)
}

func memberRequestRowFrom(req changerequests.Request) memberRequestRow {
	row := memberRequestRow{
		ID:          req.ID,
		Status:      req.Status,
		AboutName:   req.SuppliedName,
		Summary:     req.Summary,
		SubmittedAt: memberDate(req.SubmittedAt),
	}
	row.Withdrawable = req.Status != changerequests.StatusResolved &&
		req.Status != changerequests.StatusWithdrawn
	for _, item := range req.Items {
		if item.Status != changerequests.ItemPending {
			row.Withdrawable = false
		}
	}
	return row
}

func (h *Handler) memberRequestDetail(w http.ResponseWriter, r *http.Request) {
	req, ok := h.loadOwnRequest(w, r)
	if !ok {
		return
	}

	data := memberRequestDetailData{
		Request: memberRequestRowFrom(req),
		Success: r.URL.Query().Get("success"),
		Error:   r.URL.Query().Get("error"),
	}
	for _, item := range req.Items {
		data.Items = append(data.Items, memberRequestItemRow{
			Label:          kindLabel(item.Operation),
			ProposedValue:  proposedValueLabel(item.Operation, item.ProposedValue),
			Status:         item.Status,
			DecisionReason: item.DecisionReason,
		})
		if item.Operation != changerequests.OpOther {
			data.HasProposals = true
		}
	}
	h.renderPage(w, r, "member_request_detail.html", http.StatusOK, data)
}

// loadOwnRequest reads a request the caller submitted.
//
// A request submitted by somebody else renders the same "not found" page as one
// that never existed, so a member cannot count the officer queue by walking ids.
// The officer's triage conclusion is never rendered here at all, which is why
// this page shows no target person: learning who the club decided a suggestion
// concerned is learning that the person is on file.
func (h *Handler) loadOwnRequest(w http.ResponseWriter, r *http.Request) (changerequests.Request, bool) {
	p := h.principalFromRequest(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)

	req, err := h.changeRequests.GetForRequester(r.Context(), p, id)
	if errors.Is(err, changerequests.ErrNotYours) || errors.Is(err, changerequests.ErrNotFound) {
		h.renderError(w, r, http.StatusNotFound, "No such suggestion.")
		return changerequests.Request{}, false
	}
	if err != nil {
		h.log.Error("member request", slog.String("error", err.Error()))
		h.renderError(w, r, http.StatusInternalServerError, "That suggestion could not be loaded. Please try again.")
		return changerequests.Request{}, false
	}
	return req, true
}

func (h *Handler) memberRequestWithdraw(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	target := RouteMemberRequests + "/" + strconv.FormatInt(id, 10)

	_, err := h.changeRequests.Withdraw(r.Context(), p, id, time.Now())
	switch {
	case err == nil:
		http.Redirect(w, r, target+"?success=Your+suggestion+has+been+withdrawn", http.StatusSeeOther)
	case errors.Is(err, changerequests.ErrNotYours), errors.Is(err, changerequests.ErrNotFound):
		h.renderError(w, r, http.StatusNotFound, "No such suggestion.")
	case errors.Is(err, changerequests.ErrDecidedItems), errors.Is(err, changerequests.ErrAlreadyResolved):
		http.Redirect(w, r,
			target+"?error=An+officer+has+already+started+reviewing+this,+so+it+can+no+longer+be+withdrawn",
			http.StatusSeeOther)
	default:
		h.log.Error("member withdrawal", slog.String("error", err.Error()))
		h.renderError(w, r, http.StatusInternalServerError, "That suggestion could not be withdrawn. Please try again.")
	}
}
