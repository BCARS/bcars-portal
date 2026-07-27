package web

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/domain/members"
)

// Handler serves the admin UI pages.
type Handler struct {
	render  *Renderer
	members *members.Service
	queries *sqlcgen.Queries
	db      *sql.DB
	log     *slog.Logger
}

// NewHandler creates a web handler with template rendering and domain services.
func NewHandler(database *sql.DB, logger *slog.Logger) (*Handler, error) {
	r, err := NewRenderer()
	if err != nil {
		return nil, fmt.Errorf("web: parse templates: %w", err)
	}

	if logger == nil {
		logger = slog.Default()
	}

	// Ensure bootstrap admin user exists (FK target for all officer actions).
	if err := ensureBootstrapUser(database); err != nil {
		return nil, fmt.Errorf("web: bootstrap user: %w", err)
	}

	return &Handler{
		render:  r,
		members: members.NewService(database),
		queries: sqlcgen.New(database),
		db:      database,
		log:     logger,
	}, nil
}

// ensureBootstrapUser creates user ID 1 if it doesn't already exist.
// This is the FK target for all officer actions in the Phase 1 single-user UI.
func ensureBootstrapUser(database *sql.DB) error {
	_, err := database.Exec(
		`INSERT OR IGNORE INTO users (id, email, is_active) VALUES (1, 'admin@portal.local', 1)`,
	)
	return err
}

// RegisterRoutes registers all admin UI routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("GET /admin/", h.logged(h.dashboard))
	mux.Handle("GET /admin/members", h.logged(h.memberList))
	mux.Handle("GET /admin/members/new", h.logged(h.memberNew))
	mux.Handle("POST /admin/members/new", h.logged(h.memberCreate))
	mux.Handle("GET /admin/members/{id}", h.logged(h.memberDetail))
	mux.Handle("GET /admin/members/{id}/edit", h.logged(h.memberEdit))
	mux.Handle("POST /admin/members/{id}/edit", h.logged(h.memberUpdate))
	mux.Handle("POST /admin/members/{id}/deactivate", h.logged(h.memberDeactivate))
	mux.Handle("POST /admin/members/{id}/reactivate", h.logged(h.memberReactivate))
	mux.Handle("POST /admin/members/{id}/memberships/{mid}/approve", h.logged(h.membershipApprove))
	mux.Handle("POST /admin/members/{id}/notes", h.logged(h.noteCreate))
	mux.Handle("GET /admin/members/{id}/contacts/new", h.logged(h.contactNew))
	mux.Handle("POST /admin/members/{id}/contacts/new", h.logged(h.contactCreate))
	mux.Handle("GET /admin/imports", h.logged(h.importList))
	mux.Handle("GET /admin/imports/{id}", h.logged(h.importDetail))
}

// logged wraps an http.HandlerFunc with request/response logging.
func (h *Handler) logged(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.log.Info("request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
		)
		next(w, r)
	})
}

// principal returns the admin principal. In Phase 1 the admin UI is
// single-user behind network access controls; WS4 session middleware
// will replace this with the authenticated user's principal.
func (h *Handler) principal() *authz.Principal {
	return &authz.Principal{
		UserID:       1,
		Capabilities: authz.Codes(),
	}
}

// --- Dashboard ---

type dashboardData struct {
	TotalPersons      int
	ActiveMemberships int
	PendingApprovals  int
	ImportRuns        int
	RecentAudit       []sqlcgen.AuditEvent
}

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data := dashboardData{}

	h.db.QueryRowContext(ctx, `SELECT count(*) FROM persons WHERE deactivated_at IS NULL`).Scan(&data.TotalPersons)
	h.db.QueryRowContext(ctx, `SELECT count(*) FROM memberships WHERE lifecycle = 'approved'`).Scan(&data.ActiveMemberships)
	h.db.QueryRowContext(ctx, `SELECT count(*) FROM memberships WHERE lifecycle = 'pending'`).Scan(&data.PendingApprovals)
	h.db.QueryRowContext(ctx, `SELECT count(*) FROM import_runs`).Scan(&data.ImportRuns)

	data.RecentAudit, _ = h.queries.ListAuditEvents(ctx, sqlcgen.ListAuditEventsParams{Limit: 10, Offset: 0})

	h.render.RenderHTTP(w, "dashboard.html", http.StatusOK, data)
}

// --- Members ---

type memberListData struct {
	Members    []memberRow
	Query      string
	HasMore    bool
	NextOffset int64
}

type memberRow struct {
	ID            int64
	DisplayName   string
	SortName      string
	CallSign      sql.NullString
	BaseType      string
	DeactivatedAt sql.NullString
	DeceasedAt    sql.NullString
}

func (h *Handler) memberList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := h.principal()
	query := r.URL.Query().Get("q")
	offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	const limit = 50

	persons, err := h.members.ListPersons(ctx, p, members.ListPersonsParams{
		Query:  query,
		Limit:  limit + 1,
		Offset: offset,
	})
	if err != nil {
		h.log.Error("list persons failed", slog.String("error", err.Error()))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := memberListData{Query: query, NextOffset: offset + limit}

	hasMore := len(persons) > int(limit)
	if hasMore {
		persons = persons[:limit]
	}
	data.HasMore = hasMore

	for _, ps := range persons {
		data.Members = append(data.Members, memberRow{
			ID:            ps.ID,
			DisplayName:   ps.DisplayName,
			SortName:      ps.SortName,
			CallSign:      ps.CallSign,
			DeactivatedAt: ps.DeactivatedAt,
			DeceasedAt:    ps.DeceasedAt,
		})
	}

	h.render.RenderHTTP(w, "members.html", http.StatusOK, data)
}

type memberDetailData struct {
	Person         sqlcgen.Person
	Memberships    []sqlcgen.Membership
	ContactMethods []sqlcgen.ContactMethod
	Notes          []sqlcgen.Note
	Flash          string
}

func (h *Handler) memberDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := h.principal()
	id := parseID(r, "id")

	person, err := h.members.GetPerson(ctx, p, id)
	if err != nil {
		h.log.Error("get person failed", slog.Int64("id", id), slog.String("error", err.Error()))
		http.Error(w, "Member not found", http.StatusNotFound)
		return
	}

	data := memberDetailData{Person: person}
	data.Memberships, _ = h.members.ListMembershipsByPerson(ctx, p, id)
	data.ContactMethods, _ = h.members.ListContactMethods(ctx, p, id)
	data.Notes, _ = h.members.ListNotes(ctx, p, "person", id, 50, 0)
	data.Flash = r.URL.Query().Get("flash")

	h.render.RenderHTTP(w, "member_detail.html", http.StatusOK, data)
}

type memberFormData struct {
	IsEdit bool
	Person sqlcgen.Person
	Error  string
}

func (h *Handler) memberNew(w http.ResponseWriter, r *http.Request) {
	h.render.RenderHTTP(w, "member_form.html", http.StatusOK, memberFormData{})
}

func (h *Handler) memberCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := h.principal()

	r.ParseForm()
	person, err := h.members.CreatePerson(ctx, p, members.CreatePersonParams{
		DisplayName: r.FormValue("display_name"),
		SortName:    r.FormValue("sort_name"),
		CallSign:    strings.ToUpper(strings.TrimSpace(r.FormValue("call_sign"))),
		BaseType:    r.FormValue("base_type"),
	})
	if err != nil {
		h.log.Error("create person failed", slog.String("error", err.Error()))
		h.render.RenderHTTP(w, "member_form.html", http.StatusUnprocessableEntity, memberFormData{Error: err.Error()})
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/members/%d?flash=Member+created", person.ID), http.StatusSeeOther)
}

func (h *Handler) memberEdit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := h.principal()
	id := parseID(r, "id")

	person, err := h.members.GetPerson(ctx, p, id)
	if err != nil {
		h.log.Error("get person for edit failed", slog.Int64("id", id), slog.String("error", err.Error()))
		http.Error(w, "Member not found", http.StatusNotFound)
		return
	}

	h.render.RenderHTTP(w, "member_form.html", http.StatusOK, memberFormData{IsEdit: true, Person: person})
}

func (h *Handler) memberUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := h.principal()
	id := parseID(r, "id")

	r.ParseForm()
	version, _ := strconv.ParseInt(r.FormValue("version"), 10, 64)

	_, err := h.members.UpdatePerson(ctx, p, members.UpdatePersonParams{
		ID:          id,
		DisplayName: r.FormValue("display_name"),
		SortName:    r.FormValue("sort_name"),
		CallSign:    strings.ToUpper(strings.TrimSpace(r.FormValue("call_sign"))),
		Version:     version,
	})
	if err != nil {
		h.log.Error("update person failed", slog.Int64("id", id), slog.String("error", err.Error()))
		person, _ := h.members.GetPerson(ctx, p, id)
		h.render.RenderHTTP(w, "member_form.html", http.StatusUnprocessableEntity, memberFormData{
			IsEdit: true,
			Person: person,
			Error:  friendlyError(err),
		})
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/members/%d?flash=Member+updated", id), http.StatusSeeOther)
}

func (h *Handler) memberDeactivate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := h.principal()
	id := parseID(r, "id")

	r.ParseForm()
	version, _ := strconv.ParseInt(r.FormValue("version"), 10, 64)

	if err := h.members.DeactivatePerson(ctx, p, id, version); err != nil {
		h.log.Error("deactivate person failed", slog.Int64("id", id), slog.String("error", err.Error()))
		http.Error(w, friendlyError(err), http.StatusConflict)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/members/%d?flash=Member+deactivated", id), http.StatusSeeOther)
}

func (h *Handler) memberReactivate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := h.principal()
	id := parseID(r, "id")

	r.ParseForm()
	version, _ := strconv.ParseInt(r.FormValue("version"), 10, 64)

	if err := h.members.ReactivatePerson(ctx, p, id, version); err != nil {
		h.log.Error("reactivate person failed", slog.Int64("id", id), slog.String("error", err.Error()))
		http.Error(w, friendlyError(err), http.StatusConflict)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/members/%d?flash=Member+reactivated", id), http.StatusSeeOther)
}

func (h *Handler) membershipApprove(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := h.principal()
	id := parseID(r, "id")
	mid := parseID(r, "mid")

	r.ParseForm()
	version, _ := strconv.ParseInt(r.FormValue("version"), 10, 64)
	baseType := r.FormValue("base_type")

	_, err := h.members.ApproveMembership(ctx, p, mid, version, baseType, "Approved via admin UI")
	if err != nil {
		h.log.Error("approve membership failed", slog.Int64("person_id", id), slog.Int64("membership_id", mid), slog.String("error", err.Error()))
		http.Error(w, friendlyError(err), http.StatusConflict)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/members/%d?flash=Membership+approved", id), http.StatusSeeOther)
}

func (h *Handler) noteCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := h.principal()
	id := parseID(r, "id")

	r.ParseForm()
	_, err := h.members.CreateNote(ctx, p, members.CreateNoteParams{
		SubjectKind: "person",
		SubjectID:   id,
		Category:    r.FormValue("category"),
		Visibility:  "officer",
		Body:        r.FormValue("body"),
	})
	if err != nil {
		h.log.Error("create note failed", slog.Int64("person_id", id), slog.String("error", err.Error()))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/members/%d?flash=Note+added", id), http.StatusSeeOther)
}

// --- Contacts ---

type contactFormData struct {
	PersonID   int64
	PersonName string
	Error      string
}

func (h *Handler) contactNew(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := h.principal()
	id := parseID(r, "id")

	person, err := h.members.GetPerson(ctx, p, id)
	if err != nil {
		h.log.Error("get person for contact form failed", slog.Int64("id", id), slog.String("error", err.Error()))
		http.Error(w, "Member not found", http.StatusNotFound)
		return
	}

	h.render.RenderHTTP(w, "contact_form.html", http.StatusOK, contactFormData{
		PersonID:   person.ID,
		PersonName: person.DisplayName,
	})
}

func (h *Handler) contactCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := h.principal()
	id := parseID(r, "id")

	r.ParseForm()
	valueRaw := strings.TrimSpace(r.FormValue("value"))
	isPrimary := r.FormValue("is_primary") == "1"

	_, err := h.members.CreateContactMethod(ctx, p, members.CreateContactMethodParams{
		PersonID:  id,
		Kind:      r.FormValue("kind"),
		Label:     r.FormValue("label"),
		ValueRaw:  valueRaw,
		ValueNorm: valueRaw,
		IsPrimary: isPrimary,
	})
	if err != nil {
		h.log.Error("create contact method failed", slog.Int64("person_id", id), slog.String("error", err.Error()))
		person, _ := h.members.GetPerson(ctx, p, id)
		h.render.RenderHTTP(w, "contact_form.html", http.StatusUnprocessableEntity, contactFormData{
			PersonID:   id,
			PersonName: person.DisplayName,
			Error:      err.Error(),
		})
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/members/%d?flash=Contact+added", id), http.StatusSeeOther)
}

// --- Imports ---

func (h *Handler) importList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	runs, _ := h.queries.ListImportRuns(ctx, sqlcgen.ListImportRunsParams{Limit: 50, Offset: 0})

	type data struct {
		Runs []sqlcgen.ImportRun
	}
	h.render.RenderHTTP(w, "imports.html", http.StatusOK, data{Runs: runs})
}

type importDetailRow struct {
	SourceRowIndex int64
	DisplayName    string
	CallSign       string
	ProposedAction string
	MatchMethod    string
	RequiresManual bool
	ManualReason   string
}

type importDetailData struct {
	Run         sqlcgen.ImportRun
	TotalRows   int
	AutoRows    int
	ManualRows  int
	ManualItems []importDetailRow
	AllItems    []importDetailRow
}

func (h *Handler) importDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := parseID(r, "id")

	run, err := h.queries.GetImportRun(ctx, id)
	if err != nil {
		http.Error(w, "Import run not found", http.StatusNotFound)
		return
	}

	rows, _ := h.queries.ListStagedRows(ctx, sqlcgen.ListStagedRowsParams{
		ImportRunID: id, Limit: 1000, Offset: 0,
	})

	data := importDetailData{Run: run, TotalRows: len(rows)}

	for _, row := range rows {
		item := stagedToRow(row)
		data.AllItems = append(data.AllItems, item)
		if row.RequiresManual == 1 {
			data.ManualRows++
			data.ManualItems = append(data.ManualItems, item)
		} else {
			data.AutoRows++
		}
	}

	h.render.RenderHTTP(w, "import_detail.html", http.StatusOK, data)
}

func stagedToRow(row sqlcgen.StagedImportRow) importDetailRow {
	item := importDetailRow{
		SourceRowIndex: row.SourceRowIndex,
		ProposedAction: row.ProposedAction,
		RequiresManual: row.RequiresManual == 1,
	}
	if row.MatchMethod.Valid {
		item.MatchMethod = row.MatchMethod.String
	}
	if row.ManualReason.Valid {
		item.ManualReason = row.ManualReason.String
	}

	// Extract name and call sign from normalized JSON.
	var norm struct {
		DisplayName string `json:"DisplayName"`
		CallSign    string `json:"CallSign"`
	}
	if err := json.Unmarshal([]byte(row.NormalizedJson), &norm); err == nil {
		item.DisplayName = norm.DisplayName
		item.CallSign = norm.CallSign
	}
	return item
}

// --- Helpers ---

func parseID(r *http.Request, param string) int64 {
	id, _ := strconv.ParseInt(r.PathValue(param), 10, 64)
	return id
}

func friendlyError(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "stale version") {
		return "This record was modified by another user. Please reload and try again."
	}
	return msg
}
