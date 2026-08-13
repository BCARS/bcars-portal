package web

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bcars/bcars-portal/internal/audit"
	"github.com/bcars/bcars-portal/internal/authn"
	"github.com/bcars/bcars-portal/internal/db"
	"github.com/bcars/bcars-portal/internal/domain/memberaccess"
)

// The officer access-management UI (bcars-portal-4ux.10).
//
// This page answers one question — which accounts may read this member's
// record — and offers four separate controls to change the answer. They are
// separate deliberately, because they are separate decisions with separate
// consequences:
//
//	provision   creates or reuses an account, and grants NOTHING;
//	grant       lets one existing account read one record;
//	revoke      takes that back, effective on the member's next request;
//	recovery    emails a sign-in link so the member can set a password.
//
// A single "add this member's spouse" button would have collapsed all four into
// one gesture whose blast radius nobody could describe afterwards. The audit
// trail is the other reason: four controls produce four distinguishable actions,
// so "who gave this account access to that record, and when" has an answer that
// does not require inferring intent from a compound event.
//
// The page also shows the record's informational relationships. They are
// rendered beneath the grants, labelled as context, and wired to nothing: no
// control on this page reads them, and adding a relationship through the
// officer API changes nothing about what appears in the access table. That
// separation is ADR-0010's, and it is asserted in the tests rather than left as
// a property of how the template happens to be laid out today.

// RouteAdminMemberAccess is the per-record access page.
const RouteAdminMemberAccess = "/admin/members/{id}/access"

// AccessRoutes returns the officer access-management routes.
func (h *Handler) AccessRoutes() []GuardedRoute {
	return []GuardedRoute{
		{Pattern: "GET " + RouteAdminMemberAccess, Capability: "member_access.manage", ResourceKind: "member_access_grant", handler: h.memberAccessPage},
		{Pattern: "POST /admin/members/{id}/access/accounts", Capability: "member_access.manage", AuditAction: "member_access.provision", ResourceKind: "user", handler: h.memberAccessProvision},
		{Pattern: "POST /admin/members/{id}/access/grants", Capability: "member_access.manage", AuditAction: "member_access.grant", ResourceKind: "member_access_grant", handler: h.memberAccessGrant},
		{Pattern: "POST /admin/members/{id}/access/revoke", Capability: "member_access.manage", AuditAction: "member_access.revoke", ResourceKind: "member_access_grant", handler: h.memberAccessRevoke},
		{Pattern: "POST /admin/members/{id}/access/recovery", Capability: "member_access.manage", AuditAction: "auth.recovery.request", ResourceKind: "user", handler: h.memberAccessRecovery},
	}
}

type memberAccessData struct {
	PersonID    int64
	DisplayName string
	CallSign    string
	// Grants covers every account that can or could reach this record,
	// revoked ones included, so "who could see this and when" stays
	// answerable after the fact.
	Grants []accessGrantRow
	// Relationships are informational context and nothing else. See the file
	// comment: no control on this page consults them.
	Relationships []relationshipContextRow
	Success       string
	Error         string
}

type accessGrantRow struct {
	UserID     int64
	Email      string
	AccessKind string
	Reason     string
	Active     bool
	GrantedAt  string
	RevokedAt  string
	// HasPassword reports whether the account can be signed into at all. A
	// provisioned account has none until its member sets one, and an officer
	// looking at "granted, but never signed in" needs to see which of the two
	// situations they are in.
	HasPassword bool
}

func (h *Handler) memberAccessPage(w http.ResponseWriter, r *http.Request) {
	person, ok := h.loadAccessPerson(w, r)
	if !ok {
		return
	}
	p := h.principalFromRequest(r)

	data := memberAccessData{
		PersonID:    person.ID,
		DisplayName: person.DisplayName,
		CallSign:    person.CallSign.String,
		Success:     r.URL.Query().Get("success"),
		Error:       r.URL.Query().Get("error"),
	}

	grants, err := h.memberAccess.ListGrantsForPerson(r.Context(), p, person.ID)
	if err != nil {
		h.log.Error("person access", slog.String("error", err.Error()))
		h.renderError(w, r, http.StatusInternalServerError, "Record access could not be loaded. Please try again.")
		return
	}
	for _, g := range grants {
		row := accessGrantRow{
			UserID:     g.UserID,
			AccessKind: g.AccessKind,
			Reason:     g.Reason,
			Active:     g.Active(),
			GrantedAt:  memberDate(g.GrantedAt),
		}
		if g.RevokedAt != "" {
			row.RevokedAt = memberDate(g.RevokedAt)
		}
		var passwordHash any
		_ = h.db.QueryRowContext(r.Context(),
			`SELECT email, password_hash FROM users WHERE id = ?`, g.UserID).
			Scan(&row.Email, &passwordHash)
		row.HasPassword = passwordHash != nil
		data.Grants = append(data.Grants, row)
	}

	data.Relationships = h.relationshipContext(r, person.ID)

	h.renderPage(w, r, "member_access.html", http.StatusOK, data)
}

// loadAccessPerson reads the record this page is about.
func (h *Handler) loadAccessPerson(w http.ResponseWriter, r *http.Request) (personRecord, bool) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	row, err := h.queries.GetPerson(r.Context(), id)
	if err != nil {
		h.renderError(w, r, http.StatusNotFound, "No such member record.")
		return personRecord{}, false
	}
	return personRecord{ID: row.ID, DisplayName: row.DisplayName, CallSign: row.CallSign}, true
}

// memberAccessProvision creates or reuses an account and grants it nothing.
//
// The separation is the point. An officer who provisions has created a login
// for a mailbox; they have not decided that this login may read this record,
// and the page makes them say so separately. Provisioning never sets a
// password: an officer must not choose someone else's credential, so the new
// account cannot be signed into until its member sets one through recovery
// (ADR-0012).
func (h *Handler) memberAccessProvision(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	target := accessPath(id)

	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, target+"?error=Please+check+your+entries+and+try+again", http.StatusSeeOther)
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	if email == "" {
		http.Redirect(w, r, target+"?error=Give+the+email+address+for+the+account", http.StatusSeeOther)
		return
	}

	acct, err := h.memberAccess.Provision(r.Context(), p, memberaccess.ProvisionParams{Email: email}, time.Now())
	if err != nil {
		h.log.Error("member account provision", slog.String("error", err.Error()))
		http.Redirect(w, r, target+"?error="+urlMessage(accessErrorMessage(err)), http.StatusSeeOther)
		return
	}
	audit.StampResource(r.Context(), "user", acct.UserID)

	msg := "Account+created.+It+has+no+password+and+no+access+until+you+grant+one"
	if !acct.Created {
		msg = "That+address+already+had+an+account,+so+it+was+reused"
	}
	http.Redirect(w, r, target+"?success="+msg, http.StatusSeeOther)
}

// memberAccessGrant gives one existing account access to this record.
//
// It takes an email rather than a user id so an officer works in the terms they
// have — they know the mailbox, not the row number — and it refuses an unknown
// address rather than quietly provisioning one, because creating an account is
// the other control and must stay a deliberate act.
func (h *Handler) memberAccessGrant(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	target := accessPath(id)

	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, target+"?error=Please+check+your+entries+and+try+again", http.StatusSeeOther)
		return
	}
	email := memberaccess.NormalizeEmail(r.FormValue("email"))
	kind := r.FormValue("access_kind")
	if kind == "" {
		kind = memberaccess.AccessSelf
	}
	if email == "" {
		http.Redirect(w, r, target+"?error=Give+the+email+address+of+the+account+to+grant", http.StatusSeeOther)
		return
	}

	var userID int64
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM users WHERE email = ?`, email).Scan(&userID); err != nil {
		http.Redirect(w, r,
			target+"?error=No+account+for+that+address.+Create+the+account+first", http.StatusSeeOther)
		return
	}

	grant, err := h.memberAccess.GrantAccess(r.Context(), p, userID, memberaccess.GrantParams{
		PersonID:   id,
		AccessKind: kind,
		Reason:     strings.TrimSpace(r.FormValue("reason")),
	}, time.Now())
	if err != nil {
		http.Redirect(w, r, target+"?error="+urlMessage(accessErrorMessage(err)), http.StatusSeeOther)
		return
	}
	audit.StampResource(r.Context(), "member_access_grant", grant.ID)
	http.Redirect(w, r, target+"?success=Access+granted", http.StatusSeeOther)
}

// memberAccessRevoke ends one account's access to this record.
//
// It takes effect on that member's NEXT request, inside a session they already
// have open, because authorization reloads active grants per request rather
// than caching them at sign-in. The page says so, because an officer revoking
// access during a difficult conversation needs to know whether they have to
// wait for something.
func (h *Handler) memberAccessRevoke(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	target := accessPath(id)

	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, target+"?error=Please+check+your+entries+and+try+again", http.StatusSeeOther)
		return
	}
	userID, _ := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
	if userID == 0 {
		http.Redirect(w, r, target+"?error=Choose+which+account+to+revoke", http.StatusSeeOther)
		return
	}

	grant, err := h.memberAccess.RevokeAccess(r.Context(), p, userID, memberaccess.RevokeParams{
		PersonID: id,
		Reason:   strings.TrimSpace(r.FormValue("reason")),
	}, time.Now())
	if err != nil {
		http.Redirect(w, r, target+"?error="+urlMessage(accessErrorMessage(err)), http.StatusSeeOther)
		return
	}
	audit.StampResource(r.Context(), "member_access_grant", grant.ID)
	http.Redirect(w, r,
		target+"?success=Access+revoked.+It+ends+on+their+next+page+load,+including+a+session+already+open",
		http.StatusSeeOther)
}

// memberAccessRecovery emails a sign-in link so a member can set a password.
//
// This is how a provisioned account becomes usable, and it is the same
// enumeration-safe flow the public forgot-password page uses: the officer is
// told the message was sent whether or not the address has an account, because
// this page must not become a way to test which addresses exist.
func (h *Handler) memberAccessRecovery(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	target := accessPath(id)

	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, target+"?error=Please+check+your+entries+and+try+again", http.StatusSeeOther)
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	if email == "" {
		http.Redirect(w, r, target+"?error=Give+the+email+address+to+send+to", http.StatusSeeOther)
		return
	}

	if h.emailLinks != nil {
		err := h.emailLinks.RequestRecovery(r.Context(), email, h.clientIP.HashRequest(r))
		if errors.Is(err, authn.ErrRateLimited) {
			http.Redirect(w, r,
				target+"?error=Too+many+recent+requests+for+that+address.+Wait+a+few+minutes+and+try+again",
				http.StatusSeeOther)
			return
		}
		if err != nil {
			// Logged, not shown. Reporting which addresses fail would answer
			// the question this flow exists to refuse.
			h.log.Error("officer-initiated recovery", slog.String("error", err.Error()))
		}
	}

	http.Redirect(w, r,
		target+"?success=If+that+address+has+an+account,+a+sign-in+link+is+on+its+way",
		http.StatusSeeOther)
}

// --- internals ---

// personRecord is the small slice of a person this page needs.
type personRecord struct {
	ID          int64
	DisplayName string
	CallSign    sql.NullString
}

func accessPath(personID int64) string {
	return "/admin/members/" + strconv.FormatInt(personID, 10) + "/access"
}

// accessErrorMessage phrases a domain refusal for an officer.
func accessErrorMessage(err error) string {
	switch {
	case errors.Is(err, memberaccess.ErrEmailRequired):
		return "Give a usable email address"
	case errors.Is(err, memberaccess.ErrUnknownUser):
		return "No account for that address"
	case errors.Is(err, memberaccess.ErrUnknownPerson):
		return "No such member record"
	case errors.Is(err, memberaccess.ErrAlreadyGranted):
		return "That account already reaches this record"
	case errors.Is(err, memberaccess.ErrGrantNotFound):
		return "That account does not currently reach this record"
	case errors.Is(err, memberaccess.ErrUnknownAccessKind):
		return "Choose whether this is the member's own record or a delegate"
	case errors.Is(err, db.ErrStale):
		return "Another officer changed this while you were reading it. Reload and try again"
	default:
		return "That change could not be saved. Please try again"
	}
}

// urlMessage encodes a sentence for the redirect query string, matching the
// form the hand-written messages elsewhere in this package already use.
func urlMessage(msg string) string {
	return strings.ReplaceAll(msg, " ", "+")
}
