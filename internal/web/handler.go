package web

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bcars/bcars-portal/internal/authn"
	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/domain/importd"
	"github.com/bcars/bcars-portal/internal/domain/members"
)

// Handler serves the admin UI pages.
type Handler struct {
	render  *Renderer
	members *members.Service
	imports *importd.Service
	queries *sqlcgen.Queries
	db      *sql.DB
	log     *slog.Logger
	auth    *authn.AuthService
	sess    *authn.SessionStore
}

const sessionCookieName = "portal_session"

// NewHandler creates a web handler with template rendering and domain services.
func NewHandler(database *sql.DB, logger *slog.Logger) (*Handler, error) {
	r, err := NewRenderer()
	if err != nil {
		return nil, fmt.Errorf("web: parse templates: %w", err)
	}

	if logger == nil {
		logger = slog.Default()
	}

	sessStore := authn.NewSessionStore(database, authn.SessionConfig{
		CookieName: sessionCookieName,
		TTL:        24 * time.Hour,
	})
	authSvc := authn.NewAuthService(database, sessStore, nil)

	return &Handler{
		render:  r,
		members: members.NewService(database),
		imports: importd.NewService(database),
		queries: sqlcgen.New(database),
		db:      database,
		log:     logger,
		auth:    authSvc,
		sess:    sessStore,
	}, nil
}

// RegisterRoutes registers all admin UI routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// Public: login/logout.
	mux.Handle("GET /login", h.logged(h.loginPage))
	mux.Handle("POST /login", h.logged(h.loginSubmit))
	mux.Handle("POST /logout", h.logged(h.logout))

	// Admin routes — require authenticated session.
	mux.Handle("GET /admin/", h.requireAuth(h.dashboard))
	mux.Handle("GET /admin/members", h.requireAuth(h.memberList))
	mux.Handle("GET /admin/members/new", h.requireAuth(h.memberNew))
	mux.Handle("POST /admin/members/new", h.requireAuth(h.memberCreate))
	mux.Handle("GET /admin/members/{id}", h.requireAuth(h.memberDetail))
	mux.Handle("GET /admin/members/{id}/edit", h.requireAuth(h.memberEdit))
	mux.Handle("POST /admin/members/{id}/edit", h.requireAuth(h.memberUpdate))
	mux.Handle("POST /admin/members/{id}/deactivate", h.requireAuth(h.memberDeactivate))
	mux.Handle("POST /admin/members/{id}/reactivate", h.requireAuth(h.memberReactivate))
	mux.Handle("POST /admin/members/{id}/memberships/{mid}/approve", h.requireAuth(h.membershipApprove))
	mux.Handle("POST /admin/members/{id}/notes", h.requireAuth(h.noteCreate))
	mux.Handle("GET /admin/members/{id}/contacts/new", h.requireAuth(h.contactNew))
	mux.Handle("POST /admin/members/{id}/contacts/new", h.requireAuth(h.contactCreate))
	mux.Handle("GET /admin/imports", h.requireAuth(h.importList))
	mux.Handle("POST /admin/imports/upload", h.requireAuth(h.importUpload))
	mux.Handle("GET /admin/imports/{id}", h.requireAuth(h.importDetail))
	mux.Handle("POST /admin/imports/{id}/preview", h.requireAuth(h.importPreview))
	mux.Handle("POST /admin/imports/{id}/commit", h.requireAuth(h.importCommit))
	mux.Handle("POST /admin/imports/{id}/discard", h.requireAuth(h.importDiscard))
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

// requireAuth wraps a handler with logging + session authentication.
// Redirects to /login if no valid session exists.
func (h *Handler) requireAuth(next http.HandlerFunc) http.Handler {
	return h.logged(func(w http.ResponseWriter, r *http.Request) {
		p := h.principalFromRequest(r)
		if p == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	})
}

// principalFromRequest resolves the authenticated principal from the session
// cookie. Returns nil if unauthenticated.
func (h *Handler) principalFromRequest(r *http.Request) *authz.Principal {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil
	}

	sess, err := h.sess.Get(cookie.Value)
	if err != nil {
		return nil
	}

	_ = h.sess.Touch(sess.ID)

	// Load effective capabilities from DB.
	capLoader := &authn.SQLCapabilityLoader{DB: h.db}
	caps, err := capLoader.EffectiveCapabilities(sess.UserID)
	if err != nil {
		h.log.Error("load capabilities failed", slog.Int64("user_id", sess.UserID), slog.String("error", err.Error()))
		return nil
	}

	return &authz.Principal{
		UserID:       sess.UserID,
		Capabilities: caps,
	}
}

// principal extracts the principal from the request. Must only be called
// within requireAuth-protected handlers.
func (h *Handler) principal(r *http.Request) *authz.Principal {
	return h.principalFromRequest(r)
}

// --- Login / Logout ---

type loginData struct {
	Email string
	Error string
}

func (h *Handler) loginPage(w http.ResponseWriter, r *http.Request) {
	h.render.RenderHTTP(w, "login.html", http.StatusOK, loginData{})
}

func (h *Handler) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form data", http.StatusBadRequest)
		return
	}

	email := r.FormValue("email")
	password := r.FormValue("password")

	sessionID, err := h.auth.SignIn(email, password, "", r.UserAgent())
	if err != nil {
		h.log.Info("login failed", slog.String("email", email), slog.String("error", err.Error()))
		h.render.RenderHTTP(w, "login.html", http.StatusUnauthorized, loginData{
			Email: email,
			Error: "Invalid email or password.",
		})
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	h.log.Info("login succeeded", slog.String("email", email))
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err == nil {
		_ = h.auth.SignOut(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		MaxAge:   -1,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	http.Redirect(w, r, "/login", http.StatusSeeOther)
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

	_ = h.db.QueryRowContext(ctx, `SELECT count(*) FROM persons WHERE deactivated_at IS NULL`).Scan(&data.TotalPersons)
	_ = h.db.QueryRowContext(ctx, `SELECT count(*) FROM memberships WHERE lifecycle = 'approved'`).Scan(&data.ActiveMemberships)
	_ = h.db.QueryRowContext(ctx, `SELECT count(*) FROM memberships WHERE lifecycle = 'pending'`).Scan(&data.PendingApprovals)
	_ = h.db.QueryRowContext(ctx, `SELECT count(*) FROM import_runs`).Scan(&data.ImportRuns)

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
	p := h.principal(r)
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
	p := h.principal(r)
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
	p := h.principal(r)

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form data", http.StatusBadRequest)
		return
	}
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
	p := h.principal(r)
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
	p := h.principal(r)
	id := parseID(r, "id")

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form data", http.StatusBadRequest)
		return
	}
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
	p := h.principal(r)
	id := parseID(r, "id")

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form data", http.StatusBadRequest)
		return
	}
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
	p := h.principal(r)
	id := parseID(r, "id")

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form data", http.StatusBadRequest)
		return
	}
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
	p := h.principal(r)
	id := parseID(r, "id")
	mid := parseID(r, "mid")

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form data", http.StatusBadRequest)
		return
	}
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
	p := h.principal(r)
	id := parseID(r, "id")

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form data", http.StatusBadRequest)
		return
	}
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
	p := h.principal(r)
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
	p := h.principal(r)
	id := parseID(r, "id")

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form data", http.StatusBadRequest)
		return
	}
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

// maxImportUploadSize limits import file uploads to 10 MB.
const maxImportUploadSize = 10 << 20

func (h *Handler) importUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	principal := h.principal(r)
	if principal == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Parse multipart form with size limit.
	if err := r.ParseMultipartForm(maxImportUploadSize); err != nil {
		h.log.Error("import upload: parse form", slog.String("error", err.Error()))
		http.Redirect(w, r, "/admin/imports?error=File+too+large+or+invalid+form", http.StatusSeeOther)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Redirect(w, r, "/admin/imports?error=No+file+selected", http.StatusSeeOther)
		return
	}
	defer file.Close()

	filename := header.Filename
	sourceKind := "csv"
	if strings.HasSuffix(strings.ToLower(filename), ".json") {
		sourceKind = "json"
	}

	// Generate an idempotency key from filename + timestamp.
	idemKey := fmt.Sprintf("ui-%s-%d", filename, time.Now().UnixNano())

	result, err := h.imports.Upload(ctx, file, sourceKind, filename, principal.UserID, idemKey)
	if err != nil {
		h.log.Error("import upload", slog.String("error", err.Error()))
		http.Redirect(w, r, "/admin/imports?error="+friendlyError(err), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/imports/%d", result.RunID), http.StatusSeeOther)
}

func (h *Handler) importPreview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := parseID(r, "id")

	_, err := h.imports.Preview(ctx, id)
	if err != nil {
		h.log.Error("import preview", slog.String("error", err.Error()))
		http.Redirect(w, r, fmt.Sprintf("/admin/imports/%d?error=%s", id, friendlyError(err)), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/imports/%d", id), http.StatusSeeOther)
}

func (h *Handler) importCommit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	principal := h.principal(r)
	if principal == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	id := parseID(r, "id")

	// Auto-preview before commit to ensure state transition.
	_, _ = h.imports.Preview(ctx, id)

	result, err := h.imports.Commit(ctx, id, principal.UserID)
	if err != nil {
		h.log.Error("import commit", slog.String("error", err.Error()))
		http.Redirect(w, r, fmt.Sprintf("/admin/imports/%d?error=%s", id, friendlyError(err)), http.StatusSeeOther)
		return
	}

	msg := fmt.Sprintf("Import+committed:+%d+created,+%d+updated,+%d+skipped",
		result.Created, result.Updated, result.Skipped)
	http.Redirect(w, r, fmt.Sprintf("/admin/imports/%d?success=%s", id, msg), http.StatusSeeOther)
}

func (h *Handler) importDiscard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := parseID(r, "id")

	if err := h.imports.Discard(ctx, id); err != nil {
		h.log.Error("import discard", slog.String("error", err.Error()))
		http.Redirect(w, r, fmt.Sprintf("/admin/imports/%d?error=%s", id, friendlyError(err)), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/imports?success=Import+discarded", http.StatusSeeOther)
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
