package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bcars/bcars-portal/internal/audit"
	"github.com/bcars/bcars-portal/internal/authn"
	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/domain/batches"
	"github.com/bcars/bcars-portal/internal/domain/dues"
	"github.com/bcars/bcars-portal/internal/domain/importd"
	"github.com/bcars/bcars-portal/internal/domain/members"
	"github.com/bcars/bcars-portal/internal/domain/treasury"
	"github.com/bcars/bcars-portal/internal/domain/worksheets"
	"github.com/bcars/bcars-portal/internal/mail"
)

// Handler serves the admin UI pages.
type Handler struct {
	render     *Renderer
	members    *members.Service
	imports    *importd.Service
	dues       *dues.Service
	batches    *batches.Service
	treasury   *treasury.Service
	worksheets *worksheets.Service
	queries    *sqlcgen.Queries
	db         *sql.DB
	log        *slog.Logger
	auth       *authn.AuthService
	sess       *authn.SessionStore
	emailLinks *authn.EmailLinkService
	audit      audit.Recorder

	// cookies is the shared source of session-cookie attributes, so login,
	// logout and the recovery/invitation flows cannot disagree about them.
	cookies authn.SessionCookieConfig

	// testMailer is set by tests that need to read what was sent. Nil in
	// production; the handler never reads it.
	testMailer *mail.FilelogSender
}

// mailerForTest exposes the sender a test injected.
func (h *Handler) mailerForTest() *mail.FilelogSender { return h.testMailer }

const sessionCookieName = "portal_session"

// Routes the emailed links point at. They are exported so the assembly can
// configure authn.EmailLinkConfig from the same constants RegisterRoutes uses,
// rather than the link generator hardcoding a path the router never served.
const (
	RouteLogin             = "/login"
	RouteForgotPassword    = "/forgot-password"
	RouteResetPassword     = "/reset-password"
	RouteInvitationConsume = "/auth/invitations/consume"
)

// HandlerConfig holds the web UI's dependencies. Mailer and BaseURL are
// required for the recovery and invitation flows: the previous constructor
// hardcoded a nil sender, so requesting a password reset panicked on a nil
// interface call.
type HandlerConfig struct {
	Logger       *slog.Logger
	Mailer       mail.Sender
	BaseURL      string
	SessionTTL   time.Duration
	EmailLinkTTL time.Duration

	// AllowInsecureCookies drops the Secure attribute from the session
	// cookie so the admin UI works over plaintext http://localhost.
	// Development only; the zero value keeps Secure on.
	AllowInsecureCookies bool

	// Pepper is the secret mixed into every password hash. It MUST match the
	// value the rest of the assembly uses: the admin UI verifies the same
	// stored hashes the API does, so a UI built without the pepper cannot
	// authenticate anyone in a deployment that has one. That was the state of
	// the shipped binary until the Phase 2 smoke test signed in through the
	// login form and got a 401 (bcars-portal-pma.12).
	Pepper []byte
}

func (c HandlerConfig) withDefaults() HandlerConfig {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.BaseURL == "" {
		c.BaseURL = "http://localhost:8080"
	}
	if c.SessionTTL == 0 {
		c.SessionTTL = 24 * time.Hour
	}
	if c.EmailLinkTTL == 0 {
		c.EmailLinkTTL = 24 * time.Hour
	}
	return c
}

// NewHandler creates a web handler with template rendering and domain services.
func NewHandler(database *sql.DB, cfg HandlerConfig) (*Handler, error) {
	r, err := NewRenderer()
	if err != nil {
		return nil, fmt.Errorf("web: parse templates: %w", err)
	}

	cfg = cfg.withDefaults()
	logger := cfg.Logger

	sessStore := authn.NewSessionStore(database, authn.SessionConfig{
		CookieName: sessionCookieName,
		TTL:        cfg.SessionTTL,
	})
	authSvc := authn.NewAuthService(database, sessStore, cfg.Pepper)

	emailLinks := authn.NewEmailLinkService(database, cfg.Mailer, authn.EmailLinkConfig{
		BaseURL:        cfg.BaseURL,
		TTL:            cfg.EmailLinkTTL,
		RecoveryPath:   RouteResetPassword,
		InvitationPath: RouteInvitationConsume,
	})

	return &Handler{
		render:     r,
		members:    members.NewService(database),
		dues:       dues.NewService(database),
		batches:    batches.NewService(database),
		treasury:   treasury.NewService(database),
		worksheets: worksheets.NewService(database),
		imports:    importd.NewService(database),
		queries:    sqlcgen.New(database),
		db:         database,
		log:        logger,
		auth:       authSvc,
		sess:       sessStore,
		emailLinks: emailLinks,
		audit:      audit.NewSQLRecorder(database, logger),
		cookies: authn.SessionCookieConfig{
			Name:          sessionCookieName,
			AllowInsecure: cfg.AllowInsecureCookies,
		},
	}, nil
}

// RegisterRoutes registers all admin UI routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// Public: login/logout, recovery, invitation.
	mux.Handle("GET "+RouteLogin, h.logged(h.loginPage))
	mux.Handle("POST "+RouteLogin, h.logged(h.loginSubmit))
	mux.Handle("POST /logout", h.logged(h.logout))
	mux.Handle("GET "+RouteForgotPassword, h.logged(h.forgotPasswordPage))
	mux.Handle("POST "+RouteForgotPassword, h.logged(h.forgotPasswordSubmit))
	mux.Handle("GET "+RouteResetPassword, h.logged(h.resetPasswordPage))
	mux.Handle("POST "+RouteResetPassword, h.logged(h.resetPasswordSubmit))
	mux.Handle("GET "+RouteInvitationConsume, h.logged(h.invitationPage))
	mux.Handle("POST "+RouteInvitationConsume, h.logged(h.invitationSubmit))

	// Admin routes — each declares the capability it requires and, for
	// mutations, the audit action it emits. AdminRoutes is the single source
	// of truth; there is no way to register an admin route without stating a
	// capability, which is what kept requireAuth-only routes from being
	// noticed.
	for _, rt := range h.AdminRoutes() {
		mux.Handle(rt.Pattern, h.requireCap(rt))
	}
}

// AdminRoute binds an admin UI route to the capability it requires.
type AdminRoute struct {
	// Pattern is the net/http ServeMux pattern, including method.
	Pattern string
	// Capability is the authz capability code the caller must hold.
	Capability string
	// AuditAction is the audit event emitted on every call. Empty for
	// read-only routes, which are still audited when denied.
	AuditAction string
	// ResourceKind labels the audit event's subject.
	ResourceKind string

	handler http.HandlerFunc
}

// AdminRoutes returns every admin UI route with its capability requirement.
// Exported so tests can assert coverage across the whole table rather than
// route by route.
func (h *Handler) AdminRoutes() []AdminRoute {
	return []AdminRoute{
		{Pattern: "GET /admin/", Capability: "session.self.read", ResourceKind: "dashboard", handler: h.dashboard},

		{Pattern: "GET /admin/treasury", Capability: "dues.read", ResourceKind: "dues", handler: h.treasuryHome},
		{Pattern: "GET /admin/treasury/standing", Capability: "dues.read", ResourceKind: "dues", handler: h.treasuryStanding},
		{Pattern: "GET /admin/treasury/memberships/{id}/payment", Capability: "payment.post", ResourceKind: "payment", handler: h.treasuryPaymentForm},
		{Pattern: "POST /admin/treasury/memberships/{id}/payment", Capability: "payment.post", AuditAction: "payment.create", ResourceKind: "payment", handler: h.treasuryPaymentSubmit},

		{Pattern: "GET /admin/treasury/batches", Capability: "payment.read", ResourceKind: "payment_batch", handler: h.batchList},
		{Pattern: "POST /admin/treasury/batches", Capability: "payment.batch.manage", AuditAction: "payment.batch.open", ResourceKind: "payment_batch", handler: h.batchOpen},
		{Pattern: "GET /admin/treasury/batches/{id}", Capability: "payment.read", ResourceKind: "payment_batch", handler: h.batchDetail},
		{Pattern: "POST /admin/treasury/batches/{id}/defaults", Capability: "payment.batch.manage", AuditAction: "payment.batch.update", ResourceKind: "payment_batch", handler: h.batchUpdateDefaults},
		{Pattern: "POST /admin/treasury/batches/{id}/entries", Capability: "payment.batch.manage", AuditAction: "payment.batch.entry.create", ResourceKind: "payment_batch_entry", handler: h.batchAddEntry},
		{Pattern: "POST /admin/treasury/batches/{id}/entries/{entry_id}/delete", Capability: "payment.batch.manage", AuditAction: "payment.batch.entry.delete", ResourceKind: "payment_batch_entry", handler: h.batchDeleteEntry},
		{Pattern: "POST /admin/treasury/batches/{id}/post", Capability: "payment.post", AuditAction: "payment.batch.post", ResourceKind: "payment_batch", handler: h.batchPost},
		{Pattern: "POST /admin/treasury/batches/{id}/abandon", Capability: "payment.batch.manage", AuditAction: "payment.batch.abandon", ResourceKind: "payment_batch", handler: h.batchAbandon},
		{Pattern: "GET /admin/treasury/worksheets", Capability: "dues.worksheet.manage", ResourceKind: "dues_worksheet_run", handler: h.worksheetOptions},
		{Pattern: "POST /admin/treasury/worksheets", Capability: "dues.worksheet.manage", AuditAction: "dues.worksheet.create", ResourceKind: "dues_worksheet_run", handler: h.worksheetCreate},
		{Pattern: "GET /admin/treasury/worksheets/{id}", Capability: "dues.worksheet.manage", ResourceKind: "dues_worksheet_run", handler: h.worksheetSheet},
		{Pattern: "POST /admin/treasury/worksheets/{id}/batch", Capability: "payment.batch.manage", AuditAction: "dues.worksheet.batch.link", ResourceKind: "payment_batch", handler: h.worksheetBatch},

		{Pattern: "GET /admin/treasury/payments/{id}/receipt", Capability: "payment.read", ResourceKind: "payment", handler: h.receiptPage},
		{Pattern: "GET /admin/treasury/payments/{id}/correct", Capability: "payment.correct", ResourceKind: "payment", handler: h.correctionForm},
		{Pattern: "POST /admin/treasury/payments/{id}/correct", Capability: "payment.correct", AuditAction: "payment.correct", ResourceKind: "payment", handler: h.correctionSubmit},

		{Pattern: "GET /admin/members", Capability: "member.read", ResourceKind: "person", handler: h.memberList},
		{Pattern: "GET /admin/members/new", Capability: "member.create", ResourceKind: "person", handler: h.memberNew},
		{Pattern: "POST /admin/members/new", Capability: "member.create", AuditAction: "member.create", ResourceKind: "person", handler: h.memberCreate},
		{Pattern: "GET /admin/members/{id}", Capability: "member.read", ResourceKind: "person", handler: h.memberDetail},
		{Pattern: "GET /admin/members/{id}/edit", Capability: "member.update", ResourceKind: "person", handler: h.memberEdit},
		{Pattern: "POST /admin/members/{id}/edit", Capability: "member.update", AuditAction: "member.update", ResourceKind: "person", handler: h.memberUpdate},
		{Pattern: "POST /admin/members/{id}/deactivate", Capability: "member.deactivate", AuditAction: "member.deactivate", ResourceKind: "person", handler: h.memberDeactivate},
		{Pattern: "POST /admin/members/{id}/reactivate", Capability: "member.deactivate", AuditAction: "member.reactivate", ResourceKind: "person", handler: h.memberReactivate},

		{Pattern: "POST /admin/members/{id}/memberships/{mid}/approve", Capability: "membership.approve", AuditAction: "membership.approve", ResourceKind: "membership", handler: h.membershipApprove},
		{Pattern: "POST /admin/members/{id}/memberships/{mid}/reject", Capability: "membership.approve", AuditAction: "membership.reject", ResourceKind: "membership", handler: h.membershipReject},

		{Pattern: "POST /admin/members/{id}/notes", Capability: "notes.write.officer", AuditAction: "note.create", ResourceKind: "note", handler: h.noteCreate},

		{Pattern: "GET /admin/members/{id}/contacts/new", Capability: "contact_method.write", ResourceKind: "contact_method", handler: h.contactNew},
		{Pattern: "POST /admin/members/{id}/contacts/new", Capability: "contact_method.write", AuditAction: "contact_method.create", ResourceKind: "contact_method", handler: h.contactCreate},

		{Pattern: "GET /admin/imports", Capability: "import.upload", ResourceKind: "import_run", handler: h.importList},
		{Pattern: "POST /admin/imports/upload", Capability: "import.upload", AuditAction: "import.upload", ResourceKind: "import_run", handler: h.importUpload},
		{Pattern: "GET /admin/imports/{id}", Capability: "import.upload", ResourceKind: "import_run", handler: h.importDetail},
		{Pattern: "POST /admin/imports/{id}/rows/{rowId}/decide", Capability: "import.upload", AuditAction: "import.row.decide", ResourceKind: "import_run", handler: h.importRowDecide},
		{Pattern: "POST /admin/imports/{id}/preview", Capability: "import.upload", AuditAction: "import.preview", ResourceKind: "import_run", handler: h.importPreview},
		{Pattern: "POST /admin/imports/{id}/commit", Capability: "import.commit", AuditAction: "import.commit", ResourceKind: "import_run", handler: h.importCommit},
		{Pattern: "POST /admin/imports/{id}/discard", Capability: "import.upload", AuditAction: "import.discard", ResourceKind: "import_run", handler: h.importDiscard},
	}
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

// principalCtxKey keys the resolved principal in the request context.
type principalCtxKey struct{}

// requireCap wraps an admin route with logging, session authentication, and
// the capability check declared by the route. Unauthenticated callers are
// redirected to /login; authenticated callers missing the capability get 403.
//
// The resolved principal is placed in the request context so handlers reuse it
// instead of re-running the session and capability queries.
func (h *Handler) requireCap(rt AdminRoute) http.Handler {
	return h.logged(func(w http.ResponseWriter, r *http.Request) {
		p := h.principalFromRequest(r)
		if p == nil {
			h.recordDenial(r, rt, 0, audit.ReasonUnauthenticated)
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		if _, ok := p.Capabilities[rt.Capability]; !ok {
			h.recordDenial(r, rt, p.UserID, audit.ReasonMissingCapability)
			h.log.Warn("capability denied",
				slog.Int64("user_id", p.UserID),
				slog.String("capability", rt.Capability),
				slog.String("pattern", rt.Pattern),
			)
			h.renderError(w, r, http.StatusForbidden,
				"You do not have permission to perform this action.")
			return
		}

		ctx := context.WithValue(r.Context(), principalCtxKey{}, p)
		ctx = audit.WithResourceSlot(ctx)
		r = r.WithContext(ctx)

		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		rt.handler(sw, r)

		if rt.AuditAction != "" {
			kind, id := rt.ResourceKind, pathID(r)
			if k, i, ok := audit.StampedResource(r.Context()); ok {
				kind, id = k, i
			}
			h.audit.Record(r.Context(), audit.Event{
				Action:       rt.AuditAction,
				ActorUserID:  p.UserID,
				ResourceKind: kind,
				ResourceID:   id,
				Outcome:      outcomeForStatus(sw.status),
				DetailJSON:   detailJSON(r, rt),
			})
		}
	})
}

// statusWriter captures the response status so the audit outcome reflects what
// the handler actually did rather than assuming success.
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wroteHeader = true
	return w.ResponseWriter.Write(b)
}

// outcomeForStatus maps a response status to an audit outcome.
func outcomeForStatus(status int) string {
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return audit.OutcomeDenied
	case status >= 400:
		return audit.OutcomeFailure
	default:
		return audit.OutcomeSuccess
	}
}

// recordDenial audits a rejected admin request.
func (h *Handler) recordDenial(r *http.Request, rt AdminRoute, userID int64, reason string) {
	action := rt.AuditAction
	if action == "" {
		action = "authz.denied.web"
	}
	h.audit.Record(r.Context(), audit.Event{
		Action:       action,
		ActorUserID:  userID,
		ResourceKind: rt.ResourceKind,
		ResourceID:   pathID(r),
		Outcome:      audit.OutcomeDenied,
		ReasonCode:   reason,
		DetailJSON:   detailJSON(r, rt),
	})
}

// pathID parses the {id} path parameter, returning 0 when absent or invalid.
func pathID(r *http.Request) int64 {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// detailJSON records non-PII request context on an audit event.
func detailJSON(r *http.Request, rt AdminRoute) string {
	return fmt.Sprintf(`{"method":%q,"path":%q,"required_capability":%q,"surface":"web"}`,
		r.Method, r.URL.Path, rt.Capability)
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
// within requireCap-protected handlers, which place it in the context; the
// fallback re-resolves it rather than returning nil to a handler that assumes
// it is present.
func (h *Handler) principal(r *http.Request) *authz.Principal {
	if p, ok := r.Context().Value(principalCtxKey{}).(*authz.Principal); ok {
		return p
	}
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
		h.renderError(w, r, http.StatusBadRequest, "Invalid form data. Please check your input and try again.")
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

	h.setSessionCookie(w, sessionID)

	h.log.Info("login succeeded", slog.String("email", email))
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err == nil {
		_ = h.auth.SignOut(cookie.Value)
	}

	http.SetCookie(w, h.cookies.Clear())

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
		h.renderDomainError(w, r, err)
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

type timelineItem struct {
	Kind       string
	Detail     string
	OccurredAt string
}

type memberDetailData struct {
	Person         sqlcgen.Person
	Memberships    []sqlcgen.Membership
	ContactMethods []sqlcgen.ContactMethod
	Notes          []sqlcgen.Note
	Timeline       []timelineItem
	Flash          string
}

func (h *Handler) memberDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := h.principal(r)
	id := parseID(r, "id")

	person, err := h.members.GetPerson(ctx, p, id)
	if err != nil {
		h.log.Error("get person failed", slog.Int64("id", id), slog.String("error", err.Error()))
		h.renderError(w, r, http.StatusNotFound, "Member not found.")
		return
	}

	data := memberDetailData{Person: person}
	data.Memberships, _ = h.members.ListMembershipsByPerson(ctx, p, id)
	data.ContactMethods, _ = h.members.ListContactMethods(ctx, p, id)
	data.Notes, _ = h.members.ListNotes(ctx, p, "person", id, 50, 0)
	data.Flash = r.URL.Query().Get("flash")

	// Load timeline.
	events, _ := h.members.Timeline(ctx, p, id, 20)
	for _, e := range events {
		data.Timeline = append(data.Timeline, timelineItem{
			Kind:       e.Kind,
			Detail:     e.Detail,
			OccurredAt: e.OccurredAt,
		})
	}

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
		h.renderError(w, r, http.StatusBadRequest, "Invalid form data. Please check your input and try again.")
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
		h.renderError(w, r, http.StatusNotFound, "Member not found.")
		return
	}

	h.render.RenderHTTP(w, "member_form.html", http.StatusOK, memberFormData{IsEdit: true, Person: person})
}

func (h *Handler) memberUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := h.principal(r)
	id := parseID(r, "id")

	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form data. Please check your input and try again.")
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
		h.renderError(w, r, http.StatusBadRequest, "Invalid form data. Please check your input and try again.")
		return
	}
	version, _ := strconv.ParseInt(r.FormValue("version"), 10, 64)

	if err := h.members.DeactivatePerson(ctx, p, id, version); err != nil {
		h.log.Error("deactivate person failed", slog.Int64("id", id), slog.String("error", err.Error()))
		h.renderDomainError(w, r, err)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/members/%d?flash=Member+deactivated", id), http.StatusSeeOther)
}

func (h *Handler) memberReactivate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := h.principal(r)
	id := parseID(r, "id")

	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form data. Please check your input and try again.")
		return
	}
	version, _ := strconv.ParseInt(r.FormValue("version"), 10, 64)

	if err := h.members.ReactivatePerson(ctx, p, id, version); err != nil {
		h.log.Error("reactivate person failed", slog.Int64("id", id), slog.String("error", err.Error()))
		h.renderDomainError(w, r, err)
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
		h.renderError(w, r, http.StatusBadRequest, "Invalid form data. Please check your input and try again.")
		return
	}
	version, _ := strconv.ParseInt(r.FormValue("version"), 10, 64)
	baseType := r.FormValue("base_type")

	_, err := h.members.ApproveMembership(ctx, p, mid, version, baseType, "Approved via admin UI")
	if err != nil {
		h.log.Error("approve membership failed", slog.Int64("person_id", id), slog.Int64("membership_id", mid), slog.String("error", err.Error()))
		h.renderDomainError(w, r, err)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/members/%d?flash=Membership+approved", id), http.StatusSeeOther)
}

func (h *Handler) membershipReject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := h.principal(r)
	id := parseID(r, "id")
	mid := parseID(r, "mid")

	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form data. Please check your input and try again.")
		return
	}
	version, _ := strconv.ParseInt(r.FormValue("version"), 10, 64)
	reason := r.FormValue("reason")
	if reason == "" {
		reason = "Rejected via admin UI"
	}

	_, err := h.members.RejectMembership(ctx, p, mid, version, reason)
	if err != nil {
		h.log.Error("reject membership failed", slog.Int64("person_id", id), slog.Int64("membership_id", mid), slog.String("error", err.Error()))
		h.renderDomainError(w, r, err)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/members/%d?flash=Membership+rejected", id), http.StatusSeeOther)
}

func (h *Handler) noteCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := h.principal(r)
	id := parseID(r, "id")

	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form data. Please check your input and try again.")
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
		h.renderDomainError(w, r, err)
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
		h.renderError(w, r, http.StatusNotFound, "Member not found.")
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
		h.renderError(w, r, http.StatusBadRequest, "Invalid form data. Please check your input and try again.")
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
		Runs    []sqlcgen.ImportRun
		Error   string
		Success string
	}
	h.render.RenderHTTP(w, "imports.html", http.StatusOK, data{
		Runs:    runs,
		Error:   r.URL.Query().Get("error"),
		Success: r.URL.Query().Get("success"),
	})
}

type importDetailRow struct {
	ID             int64
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
	Error       string
	Success     string
}

func (h *Handler) importDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := parseID(r, "id")

	run, err := h.queries.GetImportRun(ctx, id)
	if err != nil {
		h.renderError(w, r, http.StatusNotFound, "Import run not found.")
		return
	}

	rows, _ := h.queries.ListStagedRows(ctx, sqlcgen.ListStagedRowsParams{
		ImportRunID: id, Limit: 1000, Offset: 0,
	})

	data := importDetailData{
		Run:       run,
		TotalRows: len(rows),
		Error:     r.URL.Query().Get("error"),
		Success:   r.URL.Query().Get("success"),
	}

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
		ID:             row.ID,
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

func (h *Handler) importRowDecide(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	principal := h.principal(r)
	if principal == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	id := parseID(r, "id")
	rowID := parseID(r, "rowId")
	action := r.FormValue("action")

	if action == "" {
		action = "skip" // default
	}

	_, err := h.imports.RecordDecision(ctx, id, importd.DecisionInput{
		RowID:     rowID,
		DecidedBy: principal.UserID,
		Action:    action,
	})
	if err != nil {
		h.log.Error("import row decision", slog.String("error", err.Error()))
		http.Redirect(w, r, fmt.Sprintf("/admin/imports/%d?error=%s", id, friendlyError(err)), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/imports/%d?success=Decision+recorded", id), http.StatusSeeOther)
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

// --- Recovery/invitation handlers ---

// setSessionCookie writes the session cookie from the handler's shared
// configuration. Every place the admin UI hands out a session goes through
// here so none of them can drop an attribute.
func (h *Handler) setSessionCookie(w http.ResponseWriter, sessionID string) {
	http.SetCookie(w, h.cookies.Set(sessionID, time.Time{}))
}

func (h *Handler) forgotPasswordPage(w http.ResponseWriter, r *http.Request) {
	type data struct {
		Error   string
		Success bool
	}
	h.render.RenderHTTP(w, "forgot_password.html", http.StatusOK, data{})
}

func (h *Handler) forgotPasswordSubmit(w http.ResponseWriter, r *http.Request) {
	type data struct {
		Error   string
		Success bool
	}
	if err := r.ParseForm(); err != nil {
		h.render.RenderHTTP(w, "forgot_password.html", http.StatusBadRequest, data{Error: "Invalid form data."})
		return
	}

	email := r.FormValue("email")
	if email != "" && h.emailLinks != nil {
		_ = h.emailLinks.RequestRecovery(r.Context(), email, "")
	}

	// Always show success to prevent email enumeration.
	h.render.RenderHTTP(w, "forgot_password.html", http.StatusOK, data{Success: true})
}

func (h *Handler) resetPasswordPage(w http.ResponseWriter, r *http.Request) {
	type data struct {
		Token string
		Error string
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		h.render.RenderHTTP(w, "reset_password.html", http.StatusBadRequest, data{Error: "Missing recovery token. Please use the link from your email."})
		return
	}
	h.render.RenderHTTP(w, "reset_password.html", http.StatusOK, data{Token: token})
}

func (h *Handler) resetPasswordSubmit(w http.ResponseWriter, r *http.Request) {
	type data struct {
		Token string
		Error string
	}
	if err := r.ParseForm(); err != nil {
		h.render.RenderHTTP(w, "reset_password.html", http.StatusBadRequest, data{Error: "Invalid form data."})
		return
	}

	token := r.FormValue("token")
	password := r.FormValue("password")
	confirm := r.FormValue("confirm")

	if password != confirm {
		h.render.RenderHTTP(w, "reset_password.html", http.StatusBadRequest, data{Token: token, Error: "Passwords do not match."})
		return
	}
	if len(password) < 12 {
		h.render.RenderHTTP(w, "reset_password.html", http.StatusBadRequest, data{Token: token, Error: "Password must be at least 12 characters."})
		return
	}

	link, err := h.emailLinks.ConsumeLink(token)
	if err != nil {
		h.render.RenderHTTP(w, "reset_password.html", http.StatusBadRequest, data{Error: "This recovery link is invalid or has expired. Please request a new one."})
		return
	}
	if link.Purpose != authn.PurposeRecovery || link.UserID == nil {
		h.render.RenderHTTP(w, "reset_password.html", http.StatusBadRequest, data{Error: "Invalid recovery link."})
		return
	}

	if err := h.auth.SetPassword(r.Context(), *link.UserID, password); err != nil {
		h.log.Error("reset password", slog.String("error", err.Error()))
		h.render.RenderHTTP(w, "reset_password.html", http.StatusInternalServerError, data{Error: "Failed to reset password. Please try again."})
		return
	}

	// Sign in automatically.
	sessionID, err := h.auth.LoginByUserID(r.Context(), *link.UserID, h.sess)
	if err != nil {
		h.log.Error("auto-login after reset", slog.String("error", err.Error()))
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	h.setSessionCookie(w, sessionID)
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (h *Handler) invitationPage(w http.ResponseWriter, r *http.Request) {
	type data struct {
		Token string
		Error string
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		h.render.RenderHTTP(w, "accept_invitation.html", http.StatusBadRequest, data{Error: "Missing invitation token."})
		return
	}
	h.render.RenderHTTP(w, "accept_invitation.html", http.StatusOK, data{Token: token})
}

func (h *Handler) invitationSubmit(w http.ResponseWriter, r *http.Request) {
	type data struct {
		Token string
		Error string
	}
	if err := r.ParseForm(); err != nil {
		h.render.RenderHTTP(w, "accept_invitation.html", http.StatusBadRequest, data{Error: "Invalid form data."})
		return
	}

	token := r.FormValue("token")
	password := r.FormValue("password")
	confirm := r.FormValue("confirm")

	if password != confirm {
		h.render.RenderHTTP(w, "accept_invitation.html", http.StatusBadRequest, data{Token: token, Error: "Passwords do not match."})
		return
	}
	if len(password) < 12 {
		h.render.RenderHTTP(w, "accept_invitation.html", http.StatusBadRequest, data{Token: token, Error: "Password must be at least 12 characters."})
		return
	}

	link, err := h.emailLinks.ConsumeLink(token)
	if err != nil {
		h.render.RenderHTTP(w, "accept_invitation.html", http.StatusBadRequest, data{Error: "This invitation link is invalid or has expired."})
		return
	}
	if link.Purpose != authn.PurposeInvitation {
		h.render.RenderHTTP(w, "accept_invitation.html", http.StatusBadRequest, data{Error: "Invalid invitation link."})
		return
	}

	userID, err := h.auth.CreateUserFromInvitation(r.Context(), link, password)
	if err != nil {
		h.render.RenderHTTP(w, "accept_invitation.html", http.StatusConflict, data{Error: "An account already exists for this email. Please sign in instead."})
		return
	}

	sessionID, err := h.auth.LoginByUserID(r.Context(), userID, h.sess)
	if err != nil {
		h.log.Error("auto-login after invitation", slog.String("error", err.Error()))
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	h.setSessionCookie(w, sessionID)
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

type errorPageData struct {
	Code      int
	Title     string
	Message   string
	RequestID string
}

// renderError renders a user-friendly error page.
// For HTMX requests, it returns a small HTML fragment instead.
func (h *Handler) renderError(w http.ResponseWriter, r *http.Request, code int, message string) {
	title := "Error"
	switch code {
	case http.StatusBadRequest:
		title = "Bad Request"
	case http.StatusForbidden:
		title = "Forbidden"
	case http.StatusNotFound:
		title = "Not Found"
	case http.StatusConflict:
		title = "Conflict"
	case http.StatusInternalServerError:
		title = "Server Error"
		// Don't leak internal details on 500.
		message = "An unexpected error occurred. Please try again or contact an administrator."
	}

	// For HTMX requests, return a fragment.
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(code)
		fmt.Fprintf(w, `<div class="alert alert-error">%s: %s</div>`, title, message)
		return
	}

	data := errorPageData{
		Code:    code,
		Title:   title,
		Message: message,
	}
	h.render.RenderHTTP(w, "error.html", code, data)
}

// renderDomainError maps common domain errors to appropriate HTTP responses.
func (h *Handler) renderDomainError(w http.ResponseWriter, r *http.Request, err error) {
	msg := friendlyError(err)
	switch {
	case strings.Contains(msg, "not found") || strings.Contains(err.Error(), "no rows"):
		h.renderError(w, r, http.StatusNotFound, msg)
	case strings.Contains(msg, "stale version") || strings.Contains(err.Error(), "conflict"):
		h.renderError(w, r, http.StatusConflict, msg)
	case strings.Contains(err.Error(), "not authorized") || strings.Contains(err.Error(), "forbidden"):
		h.renderError(w, r, http.StatusForbidden, "You do not have permission to perform this action.")
	default:
		h.log.Error("unhandled error", slog.String("error", err.Error()))
		h.renderError(w, r, http.StatusInternalServerError, "")
	}
}
