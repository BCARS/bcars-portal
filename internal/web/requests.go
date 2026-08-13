package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bcars/bcars-portal/internal/db"
	"github.com/bcars/bcars-portal/internal/domain/changerequests"
	"github.com/bcars/bcars-portal/internal/domain/relationships"
)

// The officer request-review UI (bcars-portal-4ux.10).
//
// One queue holds every reviewed correction, whatever channel it arrived
// through: an officer taking a phone call at a meeting and a member typing into
// the member UI produce the same request and item rows and are reviewed the same
// way. There is no separate public queue and no anonymous intake, because there
// is no anonymous intake to have a queue for (ADR-0013); the vocabulary on these
// pages says "member" and "officer-entered" and never "public" or "anonymous".
//
// WHAT AN OFFICER MAY DO HERE, AND WHAT THEY MAY NOT
//
// Browsing, filtering, and linking a request to a person require
// change_request.manage. Deciding an item requires change_request.review, which
// is a separate capability precisely so a club can let someone triage the queue
// without letting them change canonical data. The two are never checked in a
// template; the route table states which one each page needs.
//
// LINKING IS NOT A LOOKUP ORACLE
//
// A member's suggestion about another person arrives as bounded text — a name,
// perhaps a call sign — and nothing was confirmed at submission. The officer
// resolves it here, against the club's records, which is the appropriate place
// for that: an officer already holds member.read. What must never happen is the
// reverse, a member learning from their own request whether the person is on
// file, and the member UI accordingly never renders the triage conclusion.
//
// RELATIONSHIP CONTEXT IS CONTEXT
//
// Once a request is linked, this page shows any recorded relationships for the
// target so an officer can see why the suggestion might be arriving from this
// person. It is displayed, labelled as informational, and used for nothing: it
// gates no control, enables no button, and is not consulted by any decision.

// Officer request routes.
const (
	RouteAdminRequests = "/admin/requests"
)

// RequestRoutes returns the officer review and access-management routes.
//
// Note the two capabilities. Everything that reads or triages the queue takes
// change_request.manage; only the decision takes change_request.review. A
// deployment that grants one without the other gets exactly the surface it
// asked for, without this file needing to know that happened.
func (h *Handler) RequestRoutes() []GuardedRoute {
	return []GuardedRoute{
		{Pattern: "GET " + RouteAdminRequests, Capability: "change_request.manage", ResourceKind: "change_request", handler: h.requestQueue},
		{Pattern: "GET " + RouteAdminRequests + "/{id}", Capability: "change_request.manage", ResourceKind: "change_request", handler: h.requestDetail},
		{Pattern: "POST " + RouteAdminRequests + "/{id}/target", Capability: "change_request.manage", AuditAction: "change_request.triage", ResourceKind: "change_request", handler: h.requestTriage},
		{Pattern: "POST " + RouteAdminRequests + "/{id}/items/{item_id}/decision", Capability: "change_request.review", AuditAction: "change_request.item.decide", ResourceKind: "change_request_item", handler: h.requestDecide},
	}
}

// --- Vocabulary ---

// sourceLabel names an intake channel for an officer.
//
// There is deliberately no case that produces "public" or "anonymous". The
// planned anonymous channel was withdrawn and removed from the database CHECK
// constraint (ADR-0013), so a request bearing one cannot exist; an unrecognised
// value renders as itself rather than being quietly relabelled, because a
// surprising source is something an officer should see rather than something
// this function should smooth over.
func sourceLabel(source string) string {
	switch source {
	case changerequests.SourceMember:
		return "Member"
	case changerequests.SourceOfficerPhone:
		return "Officer — phone"
	case changerequests.SourceOfficerEmail:
		return "Officer — email"
	case changerequests.SourceOfficerMail:
		return "Officer — post"
	case changerequests.SourceOfficerMeeting:
		return "Officer — meeting"
	default:
		return source
	}
}

// requestKind distinguishes a member correcting their own record from a member
// suggesting a change about somebody else.
//
// The signal is whether the request arrived WITH a target rather than acquiring
// one later: the member API attaches a target only for a record the submitter
// was granted, and triage is what fills one in afterwards. So a target present
// before any triage means the submitter was working on their own record, and
// that distinction survives an officer linking a cross-member hint later.
func requestKind(req changerequests.Request) string {
	if req.Source != changerequests.SourceMember {
		return "Officer-entered"
	}
	if req.TargetPersonID != 0 && req.TriagedAt == "" {
		return "Own record"
	}
	return "About another person"
}

// statusLabel phrases the lifecycle for a reader.
func statusLabel(status string) string {
	switch status {
	case changerequests.StatusSubmitted:
		return "Submitted"
	case changerequests.StatusInReview:
		return "In review"
	case changerequests.StatusResolved:
		return "Resolved"
	case changerequests.StatusWithdrawn:
		return "Withdrawn"
	case changerequests.StatusDraft:
		return "Draft"
	default:
		return status
	}
}

func itemStatusLabel(status string) string {
	switch status {
	case changerequests.ItemPending:
		return "Awaiting decision"
	case changerequests.ItemApproved:
		return "Approved"
	case changerequests.ItemRejected:
		return "Rejected"
	case changerequests.ItemNeedsVerification:
		return "Needs verification"
	default:
		return status
	}
}

// --- Queue ---

type requestQueueData struct {
	Requests []officerRequestRow
	// Filters carries the current selection back to the form so the controls
	// still show what the officer chose after the page reloads.
	Filters  requestFilters
	Statuses []labelledValue
	Sources  []labelledValue
	Success  string
	Error    string
}

type labelledValue struct {
	Value string
	Label string
}

type requestFilters struct {
	Status       string
	Source       string
	UnlinkedOnly bool
}

type officerRequestRow struct {
	ID     int64
	Source string
	Kind   string
	Status string
	// About is who the request concerns: the linked person's name once an
	// officer has decided, and otherwise the submitter's own words. Supplied
	// reports which of the two this is, so the page can say so rather than
	// presenting a member's guess as a fact about the club's records.
	About    string
	Supplied bool
	Summary  string
	// Linked reports whether a canonical target has been decided.
	Linked       bool
	PendingItems int64
	SubmittedAt  string
}

func (h *Handler) requestQueue(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)

	filters := requestFilters{
		Status:       r.URL.Query().Get("status"),
		Source:       r.URL.Query().Get("source"),
		UnlinkedOnly: r.URL.Query().Get("unlinked") == "1",
	}

	list, err := h.changeRequests.List(r.Context(), p, changerequests.ListFilter{
		Status:               filters.Status,
		Source:               filters.Source,
		UnresolvedTargetOnly: filters.UnlinkedOnly,
		Limit:                changerequests.MaxLimit,
	})
	if err != nil {
		h.log.Error("request queue", slog.String("error", err.Error()))
		h.renderError(w, r, http.StatusInternalServerError, "The request queue could not be loaded. Please try again.")
		return
	}

	data := requestQueueData{
		Filters:  filters,
		Statuses: statusOptions(),
		Sources:  sourceOptions(),
		Success:  r.URL.Query().Get("success"),
		Error:    r.URL.Query().Get("error"),
	}
	for _, req := range list {
		data.Requests = append(data.Requests, officerRequestRowFrom(req))
	}
	h.renderPage(w, r, "requests.html", http.StatusOK, data)
}

func officerRequestRowFrom(req changerequests.Request) officerRequestRow {
	row := officerRequestRow{
		ID:           req.ID,
		Source:       sourceLabel(req.Source),
		Kind:         requestKind(req),
		Status:       statusLabel(req.Status),
		Summary:      req.Summary,
		Linked:       req.TargetPersonID != 0,
		PendingItems: req.PendingItems,
		SubmittedAt:  memberDate(req.SubmittedAt),
	}
	switch {
	case req.TargetDisplayName != "":
		row.About = req.TargetDisplayName
	default:
		row.About = suppliedDescription(req)
		row.Supplied = true
	}
	return row
}

// suppliedDescription is what the submitter said, assembled for display.
//
// It is never matched against the club's records here. An officer reads it and
// decides; this function's only job is to avoid showing an empty cell when the
// member gave a call sign but no name, or the reverse.
func suppliedDescription(req changerequests.Request) string {
	parts := make([]string, 0, 2)
	if req.SuppliedName != "" {
		parts = append(parts, req.SuppliedName)
	}
	if req.SuppliedCallSign != "" {
		parts = append(parts, req.SuppliedCallSign)
	}
	if len(parts) == 0 {
		return "Not described"
	}
	return strings.Join(parts, " · ")
}

func statusOptions() []labelledValue {
	return []labelledValue{
		{"", "Any status"},
		{changerequests.StatusSubmitted, "Submitted"},
		{changerequests.StatusInReview, "In review"},
		{changerequests.StatusResolved, "Resolved"},
		{changerequests.StatusWithdrawn, "Withdrawn"},
	}
}

// sourceOptions offers the channels a request can actually arrive through.
// There is no public or anonymous entry because there is no such channel.
func sourceOptions() []labelledValue {
	return []labelledValue{
		{"", "Any source"},
		{changerequests.SourceMember, "Member"},
		{changerequests.SourceOfficerPhone, "Officer — phone"},
		{changerequests.SourceOfficerEmail, "Officer — email"},
		{changerequests.SourceOfficerMail, "Officer — post"},
		{changerequests.SourceOfficerMeeting, "Officer — meeting"},
	}
}

// --- Detail ---

type requestDetailData struct {
	Request officerRequestRow
	// Supplied is what the submitter wrote, kept next to what the officer
	// concluded rather than replaced by it.
	Supplied      suppliedSnapshot
	Items         []officerItemRow
	Relationships []relationshipContextRow
	// TargetPersonID is zero until an officer links the request.
	TargetPersonID int64
	Version        int64
	// CanReview reports whether this officer may decide items, so the page
	// offers decision controls only where they would work. The route table
	// still enforces it; this only avoids showing a button that 403s.
	CanReview bool
	// Reviewable reports whether the request is still open to decisions.
	Reviewable bool
	Success    string
	Error      string
}

type suppliedSnapshot struct {
	Name         string
	CallSign     string
	Contact      string
	Relationship string
	SubmittedBy  string
	SubmittedAt  string
}

type officerItemRow struct {
	ID            int64
	Label         string
	Operation     string
	ProposedValue string
	Status        string
	Sensitive     bool
	// Decidable reports whether this item is still pending.
	Decidable        bool
	DecisionReason   string
	VerificationNote string
	AppliedAt        string
	// Appliable reports whether an approval could ever change canonical data.
	// False for `other` and for the relationship operations, which have no
	// adapter yet; the page says so rather than offering an approval that
	// would fail.
	Appliable bool
}

// relationshipContextRow is informational and is treated as such.
//
// It carries no control, no link that grants anything, and no id an action
// could act on. An officer reads "spouse — Marguerite Ashby" and knows why this
// suggestion might be arriving from this person; nothing on the page changes
// because that line is there.
type relationshipContextRow struct {
	Kind        string
	Direction   string
	DisplayName string
	CallSign    string
	Context     string
}

func (h *Handler) requestDetail(w http.ResponseWriter, r *http.Request) {
	req, ok := h.loadRequest(w, r)
	if !ok {
		return
	}
	p := h.principalFromRequest(r)

	data := requestDetailData{
		Request: officerRequestRowFrom(req),
		Supplied: suppliedSnapshot{
			Name:         req.SuppliedName,
			CallSign:     req.SuppliedCallSign,
			Contact:      req.SuppliedContact,
			Relationship: req.StatedRelationship,
			SubmittedAt:  memberDate(req.SubmittedAt),
		},
		TargetPersonID: req.TargetPersonID,
		Version:        req.Version,
		CanReview:      hasCap(p, "change_request.review"),
		Reviewable: req.Status != changerequests.StatusResolved &&
			req.Status != changerequests.StatusWithdrawn,
		Success: r.URL.Query().Get("success"),
		Error:   r.URL.Query().Get("error"),
	}
	if req.RequesterUserID != 0 {
		_ = h.db.QueryRowContext(r.Context(),
			`SELECT email FROM users WHERE id = ?`, req.RequesterUserID).Scan(&data.Supplied.SubmittedBy)
	}
	for _, item := range req.Items {
		data.Items = append(data.Items, officerItemRowFrom(item))
	}
	data.Relationships = h.relationshipContext(r, req.TargetPersonID)

	h.renderPage(w, r, "request_detail.html", http.StatusOK, data)
}

func officerItemRowFrom(item changerequests.Item) officerItemRow {
	return officerItemRow{
		ID:               item.ID,
		Label:            kindLabel(item.Operation),
		Operation:        item.Operation,
		ProposedValue:    item.ProposedValue,
		Status:           itemStatusLabel(item.Status),
		Sensitive:        changerequests.EffectiveSensitivity(item.Operation, item.Sensitivity) == changerequests.SensitivitySensitive,
		Decidable:        item.Status == changerequests.ItemPending,
		DecisionReason:   item.DecisionReason,
		VerificationNote: item.VerificationNote,
		AppliedAt:        memberDate(item.AppliedAt),
		Appliable:        changerequests.Adapters[item.Operation] != changerequests.AdapterNone,
	}
}

// relationshipContext loads the target's recorded relationships, if any.
//
// A failure here is logged and swallowed: context that cannot be loaded is a
// missing paragraph, not a reason to refuse an officer the review page. The
// request itself, the supplied snapshot, and every decision control are
// unaffected by whether this returns anything — which is the same statement as
// "relationships are not authorization", made operationally.
func (h *Handler) relationshipContext(r *http.Request, personID int64) []relationshipContextRow {
	if personID == 0 || h.relationships == nil {
		return nil
	}
	p := h.principalFromRequest(r)
	if !hasCap(p, "relationship.manage") {
		return nil
	}
	rels, err := h.relationships.ListForPerson(r.Context(), p, personID)
	if err != nil {
		h.log.Error("relationship context", slog.String("error", err.Error()))
		return nil
	}
	out := make([]relationshipContextRow, 0, len(rels))
	for _, rel := range rels {
		out = append(out, relationshipContextRow{
			Kind:        relationshipKindLabel(rel.Kind, rel.Direction),
			Direction:   rel.Direction,
			DisplayName: rel.OtherDisplayName,
			CallSign:    rel.OtherCallSign,
			Context:     rel.Context,
		})
	}
	return out
}

// relationshipKindLabel phrases a link from the subject's side.
//
// Direction matters to a reader in a way it does not to the database: "parent
// of Dale" and "child of Dale" are one row read from two ends, and an officer
// shown the wrong one would misread the household.
func relationshipKindLabel(kind, direction string) string {
	outgoing := direction != relationships.DirectionIncoming
	switch kind {
	case relationships.KindSpousePartner:
		return "Spouse or partner"
	case relationships.KindParentGuardian:
		if outgoing {
			return "Parent or guardian of"
		}
		return "Child or dependent of"
	case relationships.KindChildDependent:
		if outgoing {
			return "Child or dependent of"
		}
		return "Parent or guardian of"
	case relationships.KindHousehold:
		return "Same household as"
	default:
		return "Related to"
	}
}

// loadRequest reads one request for an officer.
func (h *Handler) loadRequest(w http.ResponseWriter, r *http.Request) (changerequests.Request, bool) {
	p := h.principalFromRequest(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)

	req, err := h.changeRequests.Get(r.Context(), p, id)
	if errors.Is(err, changerequests.ErrNotFound) {
		h.renderError(w, r, http.StatusNotFound, "No such request.")
		return changerequests.Request{}, false
	}
	if err != nil {
		h.log.Error("officer request", slog.String("error", err.Error()))
		h.renderError(w, r, http.StatusInternalServerError, "That request could not be loaded. Please try again.")
		return changerequests.Request{}, false
	}
	return req, true
}

// --- Triage ---

// requestTriage links a request to the person an officer decided it concerns.
//
// It never rewrites the supplied snapshot; the domain service enforces that,
// and the detail page goes on showing what the submitter wrote beside what the
// officer concluded. The write carries the version the officer's page was
// rendered from, so two officers triaging the same hint cannot silently
// overwrite one another — the second is told to reload.
func (h *Handler) requestTriage(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	target := RouteAdminRequests + "/" + strconv.FormatInt(id, 10)

	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, target+"?error=Please+check+your+entries+and+try+again", http.StatusSeeOther)
		return
	}
	personID, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("target_person_id")), 10, 64)
	version, _ := strconv.ParseInt(r.FormValue("version"), 10, 64)
	if personID == 0 {
		http.Redirect(w, r, target+"?error=Give+the+member+record+this+request+concerns", http.StatusSeeOther)
		return
	}

	_, err := h.changeRequests.Triage(r.Context(), p, id, changerequests.TriageParams{
		TargetPersonID:  personID,
		ExpectedVersion: version,
	}, time.Now())
	switch {
	case err == nil:
		http.Redirect(w, r, target+"?success=Linked+to+the+member+record", http.StatusSeeOther)
	case errors.Is(err, changerequests.ErrNotFound):
		h.renderError(w, r, http.StatusNotFound, "No such request.")
	case errors.Is(err, changerequests.ErrUnknownPerson):
		http.Redirect(w, r, target+"?error=No+member+record+with+that+number", http.StatusSeeOther)
	case errors.Is(err, changerequests.ErrAlreadyResolved):
		http.Redirect(w, r, target+"?error=This+request+is+already+closed", http.StatusSeeOther)
	case errors.Is(err, db.ErrStale):
		http.Redirect(w, r,
			target+"?error=Another+officer+changed+this+request+while+you+were+reading+it.+Reload+and+try+again",
			http.StatusSeeOther)
	default:
		h.log.Error("request triage", slog.String("error", err.Error()))
		http.Redirect(w, r, target+"?error=That+link+could+not+be+saved.+Please+try+again", http.StatusSeeOther)
	}
}

// --- Deciding an item ---

// requestDecide records one per-item decision and, for a supported approval,
// applies it.
//
// Everything consequential belongs to the domain service and stays there: the
// self-review refusal, the verification note a sensitive approval requires, the
// rejection reason, and the single transaction that makes a failed apply roll
// back its own decision. This handler translates a form into those parameters
// and an error into a sentence, which is the whole of its authority.
func (h *Handler) requestDecide(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	itemID, _ := strconv.ParseInt(r.PathValue("item_id"), 10, 64)
	target := RouteAdminRequests + "/" + strconv.FormatInt(id, 10)

	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, target+"?error=Please+check+your+entries+and+try+again", http.StatusSeeOther)
		return
	}

	decision, err := h.changeRequests.DecideItem(r.Context(), p, h.members, id, itemID,
		changerequests.DecideParams{
			Decision:         r.FormValue("decision"),
			Reason:           strings.TrimSpace(r.FormValue("reason")),
			VerificationNote: strings.TrimSpace(r.FormValue("verification_note")),
		}, time.Now())

	switch {
	case err == nil:
		http.Redirect(w, r, target+"?success="+decisionMessage(decision), http.StatusSeeOther)
	case errors.Is(err, changerequests.ErrNotFound), errors.Is(err, changerequests.ErrItemNotInRequest):
		h.renderError(w, r, http.StatusNotFound, "No such request item.")
	case errors.Is(err, changerequests.ErrItemDecided):
		http.Redirect(w, r,
			target+"?error=Another+officer+has+already+decided+this+item.+Reload+to+see+their+decision",
			http.StatusSeeOther)
	case errors.Is(err, changerequests.ErrSelfReview):
		http.Redirect(w, r,
			target+"?error=You+submitted+this+request,+so+another+officer+must+approve+this+item",
			http.StatusSeeOther)
	case errors.Is(err, changerequests.ErrVerificationNoteRequired):
		http.Redirect(w, r,
			target+"?error=Say+how+you+verified+this+before+approving+it", http.StatusSeeOther)
	case errors.Is(err, changerequests.ErrReasonRequired):
		http.Redirect(w, r,
			target+"?error=Give+a+reason+for+the+rejection", http.StatusSeeOther)
	case errors.Is(err, changerequests.ErrNoAdapter):
		http.Redirect(w, r,
			target+"?error=This+suggestion+cannot+be+applied+automatically.+Use+the+usual+workflow,+then+reject+or+hold+it+here",
			http.StatusSeeOther)
	case errors.Is(err, changerequests.ErrUnknownDecision):
		http.Redirect(w, r, target+"?error=Choose+approve,+reject,+or+needs+verification", http.StatusSeeOther)
	case errors.Is(err, db.ErrStale):
		http.Redirect(w, r,
			target+"?error=The+record+changed+while+you+were+reading+it,+so+nothing+was+applied.+Reload+and+try+again",
			http.StatusSeeOther)
	case errors.Is(err, changerequests.ErrTargetRequired):
		http.Redirect(w, r,
			target+"?error=Link+this+request+to+a+member+record+before+approving+it", http.StatusSeeOther)
	case errors.Is(err, changerequests.ErrBadValue):
		http.Redirect(w, r,
			target+"?error=The+suggested+value+is+not+valid+for+this+kind+of+change", http.StatusSeeOther)
	default:
		h.log.Error("request decision", slog.String("error", err.Error()))
		http.Redirect(w, r, target+"?error=That+decision+could+not+be+saved.+Please+try+again", http.StatusSeeOther)
	}
}

// decisionMessage says what actually happened, including the case where the
// decision was recorded but nothing was applied — a distinction an officer
// needs, since "approved" without "applied" means the change is still theirs to
// make by hand.
func decisionMessage(d changerequests.Decision) string {
	switch {
	case d.Replay:
		return "That+item+was+already+decided+that+way"
	case d.Applied:
		return "Approved+and+applied+to+the+member+record"
	case d.Item.Status == changerequests.ItemApproved:
		return "Approved.+Apply+the+change+with+the+usual+workflow"
	case d.Item.Status == changerequests.ItemRejected:
		return "Rejected"
	default:
		return "Held+for+verification"
	}
}
