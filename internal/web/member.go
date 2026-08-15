package web

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bcars/bcars-portal/internal/domain/changerequests"
	"github.com/bcars/bcars-portal/internal/domain/memberaccess"
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

// suggestionKinds is what a member may propose from the UI.
//
// It is a subset of changerequests.SupportedOperations, chosen because each one
// is something a member can state without knowing how the club stores it. The
// list is here rather than in a template so a template cannot invent an
// operation the review path has no adapter for.
var suggestionKinds = []struct {
	Operation string
	Label     string
	// ValueLabel prompts for the proposed value. Empty means the kind takes
	// none, which is only true of the catch-all.
	ValueLabel string
}{
	{"person.display_name.set", "My name is wrong", "What your name should be"},
	{"person.call_sign.set", "My call sign is wrong", "What your call sign should be"},
	{"contact_method.update", "One of my contact details is wrong", "What it should be"},
	{changerequests.OpOther, "Something else", "What should change"},
}

// otherSuggestionKinds is the same list phrased for a suggestion about someone
// else. Contact corrections are included, but without a target: the member
// describes the change and an officer decides which record and which value it
// applies to.
var otherSuggestionKinds = []struct {
	Operation  string
	Label      string
	ValueLabel string
}{
	{"person.display_name.set", "Their name is wrong", "What their name should be"},
	{"person.call_sign.set", "Their call sign is wrong", "What their call sign should be"},
	{"contact_method.update", "Their contact details are wrong", "What they should be"},
	{changerequests.OpOther, "Something else", "What should change"},
}

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

// kindsFor picks the phrasing that fits who the record belongs to. A delegate
// granted access on someone's behalf is not correcting their OWN name, and a
// form that said so would be asking them to confirm something untrue.
func kindsFor(accessKind string) []suggestionKind {
	if accessKind == memberaccess.AccessSelf {
		return ownKinds()
	}
	return otherKinds()
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

type memberSuggestData struct {
	// About names the record this concerns, and is empty for a suggestion
	// about someone else. That emptiness is the whole difference between the
	// two forms.
	About    memberRecordRow
	Contacts []memberContactRow
	Kinds    []suggestionKind
	// Targets is the own-record form's single question: name, call sign, each
	// contact detail, or something else.
	Targets []suggestTarget
	// Submitted holds what the member typed, so a rejected submission comes
	// back with their words rather than an empty form.
	Submitted memberSuggestForm
	Error     string
}

// suggestionKind is the template-facing shape of the allowlist.
type suggestionKind struct {
	Operation  string
	Label      string
	ValueLabel string
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

func ownKinds() []suggestionKind {
	out := make([]suggestionKind, 0, len(suggestionKinds))
	for _, k := range suggestionKinds {
		out = append(out, suggestionKind(k))
	}
	return out
}

func otherKinds() []suggestionKind {
	out := make([]suggestionKind, 0, len(otherSuggestionKinds))
	for _, k := range otherSuggestionKinds {
		out = append(out, suggestionKind(k))
	}
	return out
}

func (h *Handler) memberSuggestOwnForm(w http.ResponseWriter, r *http.Request) {
	profile, ok := h.loadMemberRecord(w, r)
	if !ok {
		return
	}
	contacts := contactRows(profile)
	kinds := kindsFor(profile.AccessKind)
	h.renderPage(w, r, "member_suggest_own.html", http.StatusOK, memberSuggestData{
		About:    memberRecordRowFrom(profile),
		Contacts: contacts,
		Kinds:    kinds,
		Targets:  ownTargets(kinds, contacts),
	})
}

// suggestTarget is one thing a member can say is wrong about their own record.
//
// Contacts are entries in this list rather than a separate dropdown. The form
// previously offered radios for what needs correcting AND an always-visible
// "which contact detail?" chooser that no radio governed, so a member choosing
// "My name is wrong" was shown a live list of their telephone numbers to pick
// from (bcars-portal-245). One question with one answer is the whole fix.
type suggestTarget struct {
	// Value is what the radio posts. "contact:<id>" carries which detail; the
	// rest are operation codes. The server maps it back, so a form cannot name
	// an operation the review path has no adapter for.
	Value      string
	Label      string
	ValueLabel string
}

// ownTargets lists the member's name, call sign, each contact detail they hold,
// and the catch-all.
func ownTargets(kinds []suggestionKind, contacts []memberContactRow) []suggestTarget {
	out := make([]suggestTarget, 0, len(kinds)+len(contacts))
	for _, k := range kinds {
		if k.Operation == "contact_method.update" {
			// Replaced below by one entry per contact the member actually has.
			for _, c := range contacts {
				label := c.Kind
				if c.Label != "" {
					label += " (" + c.Label + ")"
				}
				out = append(out, suggestTarget{
					Value:      "contact:" + strconv.FormatInt(c.ID, 10),
					Label:      "My " + label + " is wrong — " + c.Value,
					ValueLabel: "What it should be",
				})
			}
			continue
		}
		out = append(out, suggestTarget{Value: k.Operation, Label: k.Label, ValueLabel: k.ValueLabel})
	}
	return out
}

// targetToKind maps a posted target back to an operation and, for a contact, to
// which one. ok is false for anything not offered.
func targetToKind(target string, contacts []memberContactRow) (kind string, contactID int64, ok bool) {
	if id, found := strings.CutPrefix(target, "contact:"); found {
		parsed, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return "", 0, false
		}
		for _, c := range contacts {
			if c.ID == parsed {
				return "contact_method.update", parsed, true
			}
		}
		// A contact the member does not hold is not theirs to correct here.
		return "", 0, false
	}
	for _, k := range suggestionKinds {
		if k.Operation == target && k.Operation != "contact_method.update" {
			return target, 0, true
		}
	}
	return "", 0, false
}

func contactRows(profile memberprofile.Profile) []memberContactRow {
	out := make([]memberContactRow, 0, len(profile.Contacts))
	for _, c := range profile.Contacts {
		out = append(out, memberContactRow{
			ID: c.ID, Kind: c.Kind, Label: c.Label, Value: c.Value,
			Primary: c.Primary, SharedWith: c.SharedWith, Version: c.Version,
		})
	}
	return out
}

// summaryFor is what an officer reads first when the member wrote no note.
//
// Every change request carries a plain-language summary, because that is the
// line an officer triages from (changerequests.ErrSummaryRequired). Requiring
// the MEMBER to write it turned "my call sign should be W3XYZ" into a short
// essay before the form would accept it, so the form composes one from the
// choice they made instead of demanding prose (bcars-portal-245). A member who
// does write a note keeps their own words.
func summaryFor(form memberSuggestForm, targets []suggestTarget) string {
	if form.Summary != "" {
		return form.Summary
	}
	for _, t := range targets {
		if t.Value != form.Target {
			continue
		}
		// The target's own label already names the thing in the member's terms
		// ("My phone (Mobile) is wrong — 814-555-0113"); the part before the
		// dash is the subject, which is what an officer needs on one line.
		subject := t.Label
		if i := strings.Index(subject, " — "); i > 0 {
			subject = subject[:i]
		}
		return subject + ". Should be: " + form.ProposedValue
	}
	return form.ProposedValue
}

func (h *Handler) memberSuggestOwnSubmit(w http.ResponseWriter, r *http.Request) {
	profile, ok := h.loadMemberRecord(w, r)
	if !ok {
		return
	}

	ownContacts := contactRows(profile)
	ownKindList := kindsFor(profile.AccessKind)
	form, problem := readSuggestForm(r, true, ownContacts)
	render := func(msg string) {
		contacts := ownContacts
		kinds := ownKindList
		h.renderPage(w, r, "member_suggest_own.html", http.StatusBadRequest, memberSuggestData{
			About:     memberRecordRowFrom(profile),
			Contacts:  contacts,
			Kinds:     kinds,
			Targets:   ownTargets(kinds, contacts),
			Submitted: form,
			Error:     msg,
		})
	}
	if problem != "" {
		render(problem)
		return
	}

	item := changerequests.ItemInput{
		Operation:     form.Kind,
		ProposedValue: form.ProposedValue,
	}
	switch form.Kind {
	case "person.display_name.set", "person.call_sign.set":
		item.TargetKind = "person"
		item.TargetID = profile.PersonID
	case "contact_method.update":
		// The contact must be one this record actually holds. The form offers
		// only those, and this re-checks it rather than trusting the posted id:
		// a hand-built form could otherwise name a stranger's contact row.
		contact, found := findContact(profile, form.ContactID)
		if !found {
			render("Choose which contact detail is wrong.")
			return
		}
		item.TargetKind = "contact_method"
		item.TargetID = contact.ID
		item.TargetVersion = contact.Version
	}

	p := h.principalFromRequest(r)
	created, err := h.changeRequests.Create(r.Context(), p, changerequests.CreateParams{
		Source:          changerequests.SourceMember,
		RequesterUserID: p.UserID,
		TargetPersonID:  profile.PersonID,
		Summary:         summaryFor(form, ownTargets(ownKindList, ownContacts)),
		SourceIPHash:    h.clientIP.HashRequest(r),
		Items:           []changerequests.ItemInput{item},
	}, idempotencyKeyFor(r), time.Now())
	if err != nil {
		h.log.Error("member suggestion", slog.String("error", err.Error()))
		render(friendlyError(err))
		return
	}

	http.Redirect(w, r, RouteMemberRequests+"/"+strconv.FormatInt(created.ID, 10)+"?success=Your+suggestion+has+been+sent+to+the+officers",
		http.StatusSeeOther)
}

func findContact(profile memberprofile.Profile, id int64) (memberprofile.Contact, bool) {
	for _, c := range profile.Contacts {
		if c.ID == id {
			return c, true
		}
	}
	return memberprofile.Contact{}, false
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
		Kinds: otherKinds(),
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
	form, problem := readSuggestForm(r, false, nil)
	render := func(msg string, status int) {
		h.renderPage(w, r, "member_suggest_other.html", status, memberSuggestData{
			Kinds:     otherKinds(),
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
		Items: []changerequests.ItemInput{{
			Operation:     form.Kind,
			ProposedValue: form.ProposedValue,
		}},
	}, idempotencyKeyFor(r), time.Now())
	if err != nil {
		h.log.Error("member suggestion about another", slog.String("error", err.Error()))
		render(friendlyError(err), http.StatusBadRequest)
		return
	}

	http.Redirect(w, r, RouteMemberRequests+"/"+strconv.FormatInt(created.ID, 10)+"?success=Your+suggestion+has+been+sent+to+the+officers",
		http.StatusSeeOther)
}

// readSuggestForm parses and bounds what a member typed. It returns the parsed
// form and a message to show the member, empty when the form is usable.
//
// The message is a string rather than an error because it is copy addressed to
// a person, not a condition another function handles: an error value here would
// invite a caller to wrap it, log it, or compare it, none of which is what a
// sentence in a form belongs to.
//
// The operation is matched against the allowlist rather than passed through, so
// a hand-built form cannot propose an operation the review path has no adapter
// for — and cannot smuggle one the UI deliberately does not offer.
func readSuggestForm(r *http.Request, own bool, contacts []memberContactRow) (memberSuggestForm, string) {
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

	if own {
		// The own-record form asks one question, and each answer carries which
		// contact detail it means. Nothing is read from a separate chooser.
		kind, contactID, ok := targetToKind(form.Target, contacts)
		if !ok {
			return form, "Choose what needs correcting."
		}
		form.Kind = kind
		form.ContactID = contactID
		form.ContactSelected = strconv.FormatInt(contactID, 10)
	} else {
		known := false
		for _, k := range suggestionKinds {
			if k.Operation == form.Kind {
				known = true
			}
		}
		if !known {
			return form, "Choose what needs correcting."
		}
	}

	// The note is optional. It was required, which turned an ordinary
	// correction — "my call sign should be W3XYZ" — into a short essay before
	// the form would accept it (bcars-portal-245).
	//
	// What a submission may never be is empty of content. A specific kind
	// carries its value; the catch-all carries only the note, so there the note
	// is the whole of it.
	if form.Kind == changerequests.OpOther {
		if form.Summary == "" {
			return form, "Tell the officers what should change."
		}
		return form, ""
	}
	if form.ProposedValue == "" {
		return form, "Give the corrected value."
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
	Success string
	Error   string
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
			ProposedValue:  item.ProposedValue,
			Status:         item.Status,
			DecisionReason: item.DecisionReason,
		})
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
