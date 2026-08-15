package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bcars/bcars-portal/internal/audit"
	"github.com/bcars/bcars-portal/internal/authn"
	"github.com/bcars/bcars-portal/internal/clientip"
	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/domain/batches"
	"github.com/bcars/bcars-portal/internal/domain/changerequests"
	"github.com/bcars/bcars-portal/internal/domain/directory"
	"github.com/bcars/bcars-portal/internal/domain/dues"
	"github.com/bcars/bcars-portal/internal/domain/importd"
	"github.com/bcars/bcars-portal/internal/domain/memberaccess"
	"github.com/bcars/bcars-portal/internal/domain/memberprofile"
	"github.com/bcars/bcars-portal/internal/domain/members"
	"github.com/bcars/bcars-portal/internal/domain/relationships"
	"github.com/bcars/bcars-portal/internal/domain/treasury"
	"github.com/bcars/bcars-portal/internal/domain/worksheets"
	"github.com/bcars/bcars-portal/internal/mail"
	"github.com/bcars/bcars-portal/internal/ratelimit"
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
	// memberProfiles and changeRequests back the member self-service UI. Both
	// are consulted on every request rather than trusting anything stored on
	// the session, so a revoked grant disappears from an existing session
	// (ADR-0010).
	memberProfiles *memberprofile.Service
	changeRequests *changerequests.Service
	// memberAccess and relationships back the officer review and access
	// surface (bcars-portal-4ux.10). They are separate services because the
	// facts they hold are separate: a grant is authority, a relationship is
	// context, and no page may turn one into the other.
	memberAccess  *memberaccess.Service
	relationships *relationships.Service
	directory     *directory.Service
	queries       *sqlcgen.Queries
	db            *sql.DB
	log           *slog.Logger
	auth          *authn.AuthService
	sess          *authn.SessionStore
	emailLinks    *authn.EmailLinkService
	audit         audit.Recorder

	// cookies is the shared source of session-cookie attributes, so login,
	// logout and the recovery/invitation flows cannot disagree about them.
	cookies authn.SessionCookieConfig

	// clientIP is the same hasher the API middleware uses, so a recovery or
	// sign-in initiated from the admin UI records the identical value the API
	// would record for that caller. Before this existed the UI passed an empty
	// hash and every UI-initiated recovery stored NULL (bcars-portal-fmc.21).
	clientIP clientip.Hasher

	// testMailer is set by tests that need to read what was sent. Nil in
	// production; the handler never reads it.
	testMailer *mail.FilelogSender
}

// mailerForTest exposes the sender a test injected.
func (h *Handler) mailerForTest() *mail.FilelogSender { return h.testMailer }

// The paths this package serves by name: the routes emailed links point at,
// and the landings a new session is sent to. They are exported so the assembly
// can configure authn.EmailLinkConfig from the same constants RegisterRoutes
// uses, rather than the link generator hardcoding a path the router never
// served.
const (
	RouteLogin = "/login"
	// RouteMemberHome is where a signed-in member lands. It exists because the
	// admin dashboard is not a member-safe destination: the member role holds
	// session.self.read, so before this route a provisioned member who signed
	// in was sent to /admin/ and shown club-wide counts and the last ten audit
	// events.
	RouteMemberHome        = "/member/"
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

	// SessionCookieName is the cookie both surfaces use. Empty means
	// authn.DefaultSessionCookieName, which is how the admin UI and the API
	// stay on one cookie without either restating the name.
	SessionCookieName string

	// ClientIP MUST match the API's configuration. The admin UI and the API
	// record client addresses into the same columns, and a limiter reading
	// them cannot group two different constructions. Leaving it zero disables
	// hashing, which stores NULL rather than a value that only looks usable.
	ClientIP clientip.Config
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

	cookies := authn.SessionCookieConfig{
		Name:          cfg.SessionCookieName,
		AllowInsecure: cfg.AllowInsecureCookies,
	}

	sessStore := authn.NewSessionStore(database, authn.SessionConfig{
		CookieName: cookies.CookieName(),
		TTL:        cfg.SessionTTL,
	})
	authSvc := authn.NewAuthService(database, sessStore, cfg.Pepper)

	emailLinks := authn.NewEmailLinkService(database, cfg.Mailer, authn.EmailLinkConfig{
		BaseURL:        cfg.BaseURL,
		TTL:            cfg.EmailLinkTTL,
		RecoveryPath:   RouteResetPassword,
		InvitationPath: RouteInvitationConsume,
		// Built here from the same database and the same secret the API uses,
		// so the two surfaces share one set of counts. Deriving it rather than
		// accepting it as config means the admin UI cannot be assembled without
		// a limiter while the API has one.
		Limiter: ratelimit.New(database, ratelimit.Config{HashKey: cfg.ClientIP.HashKey}),
	})

	return &Handler{
		render:         r,
		members:        members.NewService(database),
		dues:           dues.NewService(database),
		batches:        batches.NewService(database),
		treasury:       treasury.NewService(database),
		worksheets:     worksheets.NewService(database),
		memberProfiles: memberprofile.NewService(database),
		changeRequests: changerequests.NewService(database),
		memberAccess:   memberaccess.NewService(database),
		relationships:  relationships.NewService(database),
		directory:      directory.NewService(database),
		imports:        importd.NewService(database),
		queries:        sqlcgen.New(database),
		db:             database,
		log:            logger,
		auth:           authSvc,
		sess:           sessStore,
		emailLinks:     emailLinks,
		audit:          audit.NewSQLRecorder(database, logger),
		cookies:        cookies,
		clientIP:       clientip.NewHasher(cfg.ClientIP),
	}, nil
}

// RegisterRoutes registers all admin UI routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// Vendored assets. Public and unauthenticated: the sign-in page needs them
	// too, and they contain nothing a signed-out caller may not see.
	mux.Handle("GET "+RouteStatic+"{path...}", staticHandler())

	// The entry point. Without it the portal had no front door at all: "/"
	// returned net/http's plaintext "404 page not found", so reaching the
	// application meant knowing to type /login (bcars-portal-8yj).
	mux.Handle("GET /", h.logged(h.root))

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

	// Guarded routes — each declares the capability it requires and, for
	// mutations, the audit action it emits. GuardedRoutes is the single source
	// of truth; there is no way to register one without stating a capability,
	// which is what kept requireAuth-only routes from being noticed.
	for _, rt := range h.GuardedRoutes() {
		mux.Handle(rt.Pattern, h.requireCap(rt))
	}
}

// GuardedRoute binds a server-rendered route to the capability it requires.
// The same type covers the officer surface under /admin/ and the member surface
// under /member/, so neither can be registered without stating a capability.
type GuardedRoute struct {
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
func (h *Handler) AdminRoutes() []GuardedRoute {
	return []GuardedRoute{
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
		{Pattern: "GET /admin/members/{id}/address/new", Capability: "contact_method.write", ResourceKind: "contact_method", handler: h.addressNew},
		{Pattern: "POST /admin/members/{id}/address/new", Capability: "contact_method.write", AuditAction: "contact_method.create", ResourceKind: "contact_method", handler: h.addressCreate},

		{Pattern: "GET /admin/imports", Capability: "import.upload", ResourceKind: "import_run", handler: h.importList},
		{Pattern: "POST /admin/imports/upload", Capability: "import.upload", AuditAction: "import.upload", ResourceKind: "import_run", handler: h.importUpload},
		{Pattern: "GET /admin/imports/{id}", Capability: "import.upload", ResourceKind: "import_run", handler: h.importDetail},
		{Pattern: "POST /admin/imports/{id}/rows/{rowId}/decide", Capability: "import.upload", AuditAction: "import.row.decide", ResourceKind: "import_run", handler: h.importRowDecide},
		{Pattern: "POST /admin/imports/{id}/preview", Capability: "import.upload", AuditAction: "import.preview", ResourceKind: "import_run", handler: h.importPreview},
		{Pattern: "POST /admin/imports/{id}/commit", Capability: "import.commit", AuditAction: "import.commit", ResourceKind: "import_run", handler: h.importCommit},
		{Pattern: "POST /admin/imports/{id}/discard", Capability: "import.upload", AuditAction: "import.discard", ResourceKind: "import_run", handler: h.importDiscard},
	}
}

// GuardedRoutes returns every capability-guarded server-rendered route.
func (h *Handler) GuardedRoutes() []GuardedRoute {
	routes := append(h.AdminRoutes(), h.RequestRoutes()...)
	routes = append(routes, h.AccessRoutes()...)
	routes = append(routes, h.MemberRoutes()...)
	routes = append(routes, h.PreferenceRoutes()...)
	return append(routes, h.DirectoryRoutes()...)
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

// principalForSession resolves a principal from a session that was just
// created, before the browser has sent the cookie back. Sign-in, invitation
// acceptance and recovery all need the caller's capabilities to choose a
// landing, and re-reading the cookie they are about to set would find nothing.
//
// A nil result means "no capabilities", which landingFor treats as the officer
// default: exactly what a role-less account got before this existed.
func (h *Handler) principalForSession(sessionID string) *authz.Principal {
	sess, err := h.sess.Get(sessionID)
	if err != nil {
		return nil
	}
	caps, err := (&authn.SQLCapabilityLoader{DB: h.db}).EffectiveCapabilities(sess.UserID)
	if err != nil {
		h.log.Error("load capabilities failed", slog.Int64("user_id", sess.UserID), slog.String("error", err.Error()))
		return nil
	}
	return &authz.Principal{UserID: sess.UserID, Capabilities: caps}
}

// principalCtxKey keys the resolved principal in the request context.
type principalCtxKey struct{}

// requireCap wraps an admin route with logging, session authentication, and
// the capability check declared by the route. Unauthenticated callers are
// redirected to /login; authenticated callers missing the capability get 403.
//
// The resolved principal is placed in the request context so handlers reuse it
// instead of re-running the session and capability queries.
func (h *Handler) requireCap(rt GuardedRoute) http.Handler {
	return h.logged(func(w http.ResponseWriter, r *http.Request) {
		p := h.principalFromRequest(r)
		if p == nil {
			// No principal, so no roles: an unauthenticated denial records an
			// empty role list, which is itself the distinction an officer
			// reviewing denials is looking for.
			h.recordDenial(r, rt, 0, nil, audit.ReasonUnauthenticated)
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		// Loaded once per guarded request and used by whichever audit event
		// this request ends up producing. Roles are recorded, never consulted:
		// the capability check on the next line is the authorization.
		roles := h.rolesFor(p.UserID)

		if _, ok := p.Capabilities[rt.Capability]; !ok {
			h.recordDenial(r, rt, p.UserID, roles, audit.ReasonMissingCapability)
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
				Action:         rt.AuditAction,
				ActorUserID:    p.UserID,
				ActorRoleCodes: roles,
				ResourceKind:   kind,
				ResourceID:     id,
				Outcome:        audit.OutcomeForStatus(sw.status),
				DetailJSON:     detailJSON(r, rt),
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

// recordDenial audits a rejected admin request.
func (h *Handler) recordDenial(r *http.Request, rt GuardedRoute, userID int64, roles []string, reason string) {
	action := rt.AuditAction
	if action == "" {
		action = "authz.denied.web"
	}
	h.audit.Record(r.Context(), audit.Event{
		Action:         action,
		ActorUserID:    userID,
		ActorRoleCodes: roles,
		ResourceKind:   rt.ResourceKind,
		ResourceID:     pathID(r),
		Outcome:        audit.OutcomeDenied,
		ReasonCode:     reason,
		DetailJSON:     detailJSON(r, rt),
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
func detailJSON(r *http.Request, rt GuardedRoute) string {
	return fmt.Sprintf(`{"method":%q,"path":%q,"required_capability":%q,"surface":"web"}`,
		r.Method, r.URL.Path, rt.Capability)
}

// rolesFor loads the role codes a user currently holds, for the audit trail.
//
// It is a separate read from the capability load rather than a field on
// authz.Principal, because authz.Principal is the AUTHORIZATION type and roles
// decide nothing here: capabilities do, and they already contain the union of
// role-granted and directly-granted codes. Putting roles on that struct would
// invite a future check to read them.
//
// A failure yields no roles rather than refusing the request. One thinner audit
// column is a better outcome than an admin page that will not load.
func (h *Handler) rolesFor(userID int64) []string {
	roles, err := (&authn.SQLCapabilityLoader{DB: h.db}).EffectiveRoles(userID)
	if err != nil {
		h.log.Error("load roles failed", slog.Int64("user_id", userID), slog.String("error", err.Error()))
		return nil
	}
	return roles
}

// principalFromRequest resolves the authenticated principal from the session
// cookie. Returns nil if unauthenticated.
func (h *Handler) principalFromRequest(r *http.Request) *authz.Principal {
	cookie, err := r.Cookie(h.cookies.CookieName())
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
	h.renderPage(w, r, "login.html", http.StatusOK, loginData{})
}

func (h *Handler) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form data. Please check your input and try again.")
		return
	}

	email := r.FormValue("email")
	password := r.FormValue("password")

	sessionID, err := h.auth.SignIn(email, password, h.clientIP.HashRequest(r), r.UserAgent())
	if err != nil {
		h.log.Info("login failed", slog.String("email", email), slog.String("error", err.Error()))
		h.renderPage(w, r, "login.html", http.StatusUnauthorized, loginData{
			Email: email,
			Error: "Invalid email or password.",
		})
		return
	}

	h.setSessionCookie(w, sessionID)

	h.log.Info("login succeeded", slog.String("email", email))
	http.Redirect(w, r, landingFor(h.principalForSession(sessionID)), http.StatusSeeOther)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(h.cookies.CookieName())
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

	// Nav shows only the routes this caller may actually follow, so the
	// dashboard never advertises a page that answers 403.
	Nav navLinks
	// DuesOwed counts the memberships a treasurer would chase, so the
	// dashboard says whether the treasury needs attention rather than only
	// offering a link to go and look.
	DuesOwed int
}

// navLinks records which surfaces a principal may reach. Capabilities decide
// what is offered, not what the template happens to hard-code: the header has
// always shown Members and Imports to everyone, which sends officers to a
// permission error to discover what they cannot do.
type navLinks struct {
	// Member reports whether the caller has a member surface of their own to
	// visit. Officers are members (PLANNING.md), so for most officers this is
	// true and the two surfaces need a way across (bcars-portal-62d).
	Member     bool
	Members    bool
	Imports    bool
	Standing   bool
	Batches    bool
	Worksheets bool
	Payments   bool
	Audit      bool
}

// navFor reads the routes a principal may follow.
func navFor(p *authz.Principal) navLinks {
	return navLinks{
		Member:     hasCap(p, "profile.self.read"),
		Members:    hasCap(p, "member.read"),
		Imports:    hasCap(p, "import.upload"),
		Standing:   hasCap(p, "dues.read"),
		Batches:    hasCap(p, "payment.read"),
		Worksheets: hasCap(p, "dues.worksheet.manage"),
		Payments:   hasCap(p, "payment.post"),
		Audit:      hasCap(p, "audit.read"),
	}
}

// AnyTreasury reports whether the caller can reach any treasury surface.
func (n navLinks) AnyTreasury() bool {
	return n.Standing || n.Batches || n.Worksheets
}

// AnyOfficer reports whether the caller can reach any officer surface at all.
func (n navLinks) AnyOfficer() bool {
	return n.Members || n.Imports || n.Payments || n.Audit || n.AnyTreasury()
}

// landingFor picks where a signed-in principal belongs.
//
// Every surface that hands out a session goes through here, so sign-in, initial
// password setup and recovery cannot disagree about it. The member role holds
// session.self.read, which is enough to open the admin dashboard: sending a
// member there is not merely an unhelpful landing but a disclosure, since that
// page reports club-wide counts and recent audit events. A caller who can reach
// no officer surface and holds member self-service goes to the member landing
// instead.
// root is the portal's front door. It holds no content of its own: it sends the
// caller where they belong, which is the sign-in page when nobody is signed in
// and otherwise the same landing every other session-issuing surface uses.
//
// The redirect is deliberately not permanent. Where "/" leads depends on who is
// asking, so a 301 cached by the browser would send a signed-in officer to the
// sign-in page, or an anonymous visitor to a page they cannot see.
func (h *Handler) root(w http.ResponseWriter, r *http.Request) {
	// "GET /" is net/http's catch-all: it matches every path no other pattern
	// claims. Only the root itself is a front door. Anything else that lands
	// here is genuinely not found, and redirecting it would turn every typo and
	// stale link into a silent bounce to the dashboard.
	if r.URL.Path != "/" {
		h.notFound(w, r)
		return
	}

	p := h.principalFromRequest(r)
	if p == nil {
		http.Redirect(w, r, RouteLogin, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, landingFor(p), http.StatusSeeOther)
}

// notFound answers a path nothing serves, in the shape the caller asked for.
//
// The catch-all this sits behind receives unmatched API paths too, and an API
// client that gets an HTML page back cannot tell a missing endpoint from a
// broken deployment. Real API errors are RFC 7807 problem documents, so an
// unmatched one is too. This was worse than the plaintext 404 it replaced
// until it was noticed (introduced with the front door in bcars-portal-8yj).
func (h *Handler) notFound(w http.ResponseWriter, r *http.Request) {
	if wantsProblemJSON(r) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type":   "about:blank",
			"title":  "Not Found",
			"status": http.StatusNotFound,
			"detail": "No endpoint is served at " + r.URL.Path + ".",
		})
		return
	}
	h.renderError(w, r, http.StatusNotFound, "That page does not exist.")
}

// wantsProblemJSON reports whether the caller is an API client rather than a
// browser: either it asked below the API prefix, or it said it wants JSON and
// did not ask for HTML.
func wantsProblemJSON(r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		return true
	}
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "json") && !strings.Contains(accept, "text/html")
}

func landingFor(p *authz.Principal) string {
	if navFor(p).AnyOfficer() {
		return "/admin/"
	}
	if hasCap(p, "profile.self.read") {
		return RouteMemberHome
	}
	return "/admin/"
}

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := h.principalFromRequest(r)

	// "GET /admin/" is a net/http prefix pattern, so every unclaimed path under
	// it arrives here. Without this check /admin/nonexistent rendered the
	// dashboard with a 200, which made a mistyped or renamed route look like it
	// worked and quietly handed a real page to any script that navigated to a
	// wrong path (bcars-portal-i4a).
	if r.URL.Path != "/admin/" {
		h.renderError(w, r, http.StatusNotFound, "That page does not exist.")
		return
	}

	data := dashboardData{Nav: navFor(p)}

	// A member who can reach no officer surface is sent to their own landing
	// rather than shown an empty officer dashboard.
	if dest := landingFor(p); dest != "/admin/" {
		http.Redirect(w, r, dest, http.StatusSeeOther)
		return
	}

	if data.Nav.Standing {
		// Counted through the domain service so the dashboard cannot disagree
		// with the standing pages about who owes.
		if owing, err := h.dues.ListStanding(ctx, p, dues.StandingQuery{
			Status: dues.StatusExpired, Limit: statusCountsLimit,
		}); err == nil {
			data.DuesOwed = len(owing)
		}
	}

	// Each figure is gated by the capability that guards the page it summarises.
	// The dashboard is reachable with session.self.read alone, so counting
	// unconditionally published club-wide membership totals and the last ten
	// audit events to anyone holding a session. A summary is a read, not a
	// decoration.
	if data.Nav.Members {
		_ = h.db.QueryRowContext(ctx, `SELECT count(*) FROM persons WHERE deactivated_at IS NULL`).Scan(&data.TotalPersons)
		_ = h.db.QueryRowContext(ctx, `SELECT count(*) FROM memberships WHERE lifecycle = 'approved'`).Scan(&data.ActiveMemberships)
		_ = h.db.QueryRowContext(ctx, `SELECT count(*) FROM memberships WHERE lifecycle = 'pending'`).Scan(&data.PendingApprovals)
	}
	if data.Nav.Imports {
		_ = h.db.QueryRowContext(ctx, `SELECT count(*) FROM import_runs`).Scan(&data.ImportRuns)
	}
	if data.Nav.Audit {
		data.RecentAudit, _ = h.queries.ListAuditEvents(ctx, sqlcgen.ListAuditEventsParams{Limit: 10, Offset: 0})
	}

	h.renderPage(w, r, "dashboard.html", http.StatusOK, data)
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

	h.renderPage(w, r, "members.html", http.StatusOK, data)
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

	h.renderPage(w, r, "member_detail.html", http.StatusOK, data)
}

type memberFormData struct {
	IsEdit bool
	Person sqlcgen.Person
	Error  string
}

func (h *Handler) memberNew(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, r, "member_form.html", http.StatusOK, memberFormData{})
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
		h.renderPage(w, r, "member_form.html", http.StatusUnprocessableEntity, memberFormData{Error: err.Error()})
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

	h.renderPage(w, r, "member_form.html", http.StatusOK, memberFormData{IsEdit: true, Person: person})
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
		h.renderPage(w, r, "member_form.html", http.StatusUnprocessableEntity, memberFormData{
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

	h.renderPage(w, r, "contact_form.html", http.StatusOK, contactFormData{
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
		h.renderPage(w, r, "contact_form.html", http.StatusUnprocessableEntity, contactFormData{
			PersonID:   id,
			PersonName: person.DisplayName,
			Error:      err.Error(),
		})
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/members/%d?flash=Contact+added", id), http.StatusSeeOther)
}

// --- Mailing address ---

// addressForm is what the officer typed, kept so a rejected submission comes
// back with their words rather than an empty form.
type addressForm struct {
	Label      string
	Line1      string
	Line2      string
	City       string
	State      string
	PostalCode string
	Country    string
	IsPrimary  bool
}

type addressFormData struct {
	PersonID   int64
	PersonName string
	Error      string
	Submitted  addressForm
}

// defaultCountry is what the country box starts on.
//
// The club has had three out-of-country members ever and none currently, so an
// empty box would be retyped for every member while being wrong for none
// (bcars-portal-a9w). It is a default, not a constraint: the field is editable
// and stored as typed.
const defaultCountry = "United States"

func (h *Handler) addressNew(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := h.principal(r)
	id := parseID(r, "id")

	person, err := h.members.GetPerson(ctx, p, id)
	if err != nil {
		h.log.Error("get person for address form failed", slog.Int64("id", id), slog.String("error", err.Error()))
		h.renderError(w, r, http.StatusNotFound, "Member not found.")
		return
	}

	h.renderPage(w, r, "address_form.html", http.StatusOK, addressFormData{
		PersonID:   id,
		PersonName: person.DisplayName,
		Submitted:  addressForm{Label: "Home", Country: defaultCountry},
	})
}

func (h *Handler) addressCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := h.principal(r)
	id := parseID(r, "id")

	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form data. Please check your input and try again.")
		return
	}

	form := addressForm{
		Label:      strings.TrimSpace(r.FormValue("label")),
		Line1:      strings.TrimSpace(r.FormValue("line1")),
		Line2:      strings.TrimSpace(r.FormValue("line2")),
		City:       strings.TrimSpace(r.FormValue("city")),
		State:      strings.TrimSpace(r.FormValue("state")),
		PostalCode: strings.TrimSpace(r.FormValue("postal_code")),
		Country:    strings.TrimSpace(r.FormValue("country")),
		IsPrimary:  r.FormValue("is_primary") == "1",
	}

	render := func(msg string) {
		person, _ := h.members.GetPerson(ctx, p, id)
		h.renderPage(w, r, "address_form.html", http.StatusUnprocessableEntity, addressFormData{
			PersonID:   id,
			PersonName: person.DisplayName,
			Error:      msg,
			Submitted:  form,
		})
	}

	// Every part is optional, but an address that says nothing is not a record
	// of anything. A country alone is the case this catches: it arrives
	// pre-filled, so a form submitted untouched would otherwise store "United
	// States" as somebody's address.
	if form.Line1 == "" && form.Line2 == "" && form.City == "" &&
		form.State == "" && form.PostalCode == "" {
		render("Give at least a street, a city, a state or a postal code.")
		return
	}

	// value_raw carries the address on one line, because that is what every
	// existing surface renders and what search reads. The parts are the record;
	// this is the reading of it.
	oneLine := formatAddress(form)

	_, err := h.members.CreateContactMethod(ctx, p, members.CreateContactMethodParams{
		PersonID:         id,
		Kind:             "postal",
		Label:            form.Label,
		ValueRaw:         oneLine,
		ValueNorm:        oneLine,
		IsPrimary:        form.IsPrimary,
		PostalLine1:      form.Line1,
		PostalLine2:      form.Line2,
		PostalCity:       form.City,
		PostalState:      form.State,
		PostalPostalCode: form.PostalCode,
		PostalCountry:    form.Country,
	})
	if err != nil {
		h.log.Error("create address failed", slog.Int64("person_id", id), slog.String("error", err.Error()))
		render(err.Error())
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/members/%d?flash=Address+added", id), http.StatusSeeOther)
}

// formatAddress renders the parts as one line, skipping the ones left empty so
// a member with a town and no street does not read as ", Bedford, PA".
func formatAddress(f addressForm) string {
	parts := make([]string, 0, 6)
	for _, part := range []string{f.Line1, f.Line2, f.City, f.State, f.PostalCode} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	// The country is omitted from the one-line reading when it is the default:
	// a club roster of Pennsylvania members does not need every address to end
	// in "United States".
	if f.Country != "" && f.Country != defaultCountry {
		parts = append(parts, f.Country)
	}
	return strings.Join(parts, ", ")
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
	h.renderPage(w, r, "imports.html", http.StatusOK, data{
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

	h.renderPage(w, r, "import_detail.html", http.StatusOK, data)
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
	h.renderPage(w, r, "forgot_password.html", http.StatusOK, data{})
}

func (h *Handler) forgotPasswordSubmit(w http.ResponseWriter, r *http.Request) {
	type data struct {
		Error   string
		Success bool
	}
	if err := r.ParseForm(); err != nil {
		h.renderPage(w, r, "forgot_password.html", http.StatusBadRequest, data{Error: "Invalid form data."})
		return
	}

	email := r.FormValue("email")
	if email != "" && h.emailLinks != nil {
		err := h.emailLinks.RequestRecovery(r.Context(), email, h.clientIP.HashRequest(r))
		if errors.Is(err, authn.ErrRateLimited) {
			// The same refusal for every address. The limiter decided without
			// looking the address up, and this page must not undo that by
			// rendering one thing for a member and another for a stranger.
			h.audit.Record(r.Context(), audit.Event{
				Action:     "auth.recovery.request",
				Outcome:    audit.OutcomeDenied,
				ReasonCode: audit.ReasonRateLimited,
				DetailJSON: detailJSON(r, GuardedRoute{}),
			})
			h.renderPage(w, r, "forgot_password.html", http.StatusTooManyRequests, data{
				Error: "Too many recovery requests. Please wait a few minutes and try again.",
			})
			return
		}
	}

	// Always show success to prevent email enumeration.
	h.renderPage(w, r, "forgot_password.html", http.StatusOK, data{Success: true})
}

func (h *Handler) resetPasswordPage(w http.ResponseWriter, r *http.Request) {
	type data struct {
		Token string
		Error string
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		h.renderPage(w, r, "reset_password.html", http.StatusBadRequest, data{Error: "Missing recovery token. Please use the link from your email."})
		return
	}
	h.renderPage(w, r, "reset_password.html", http.StatusOK, data{Token: token})
}

func (h *Handler) resetPasswordSubmit(w http.ResponseWriter, r *http.Request) {
	type data struct {
		Token string
		Error string
	}
	if err := r.ParseForm(); err != nil {
		h.renderPage(w, r, "reset_password.html", http.StatusBadRequest, data{Error: "Invalid form data."})
		return
	}

	token := r.FormValue("token")
	password := r.FormValue("password")
	confirm := r.FormValue("confirm")

	if password != confirm {
		h.renderPage(w, r, "reset_password.html", http.StatusBadRequest, data{Token: token, Error: "Passwords do not match."})
		return
	}
	if len(password) < 12 {
		h.renderPage(w, r, "reset_password.html", http.StatusBadRequest, data{Token: token, Error: "Password must be at least 12 characters."})
		return
	}

	link, err := h.emailLinks.ConsumeLink(token)
	if err != nil {
		h.renderPage(w, r, "reset_password.html", http.StatusBadRequest, data{Error: "This recovery link is invalid or has expired. Please request a new one."})
		return
	}
	if link.Purpose != authn.PurposeRecovery || link.UserID == nil {
		h.renderPage(w, r, "reset_password.html", http.StatusBadRequest, data{Error: "Invalid recovery link."})
		return
	}

	if err := h.auth.SetPassword(r.Context(), *link.UserID, password); err != nil {
		h.log.Error("reset password", slog.String("error", err.Error()))
		h.renderPage(w, r, "reset_password.html", http.StatusInternalServerError, data{Error: "Failed to reset password. Please try again."})
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
	http.Redirect(w, r, landingFor(h.principalForSession(sessionID)), http.StatusSeeOther)
}

func (h *Handler) invitationPage(w http.ResponseWriter, r *http.Request) {
	type data struct {
		Token string
		Error string
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		h.renderPage(w, r, "accept_invitation.html", http.StatusBadRequest, data{Error: "Missing invitation token."})
		return
	}
	h.renderPage(w, r, "accept_invitation.html", http.StatusOK, data{Token: token})
}

func (h *Handler) invitationSubmit(w http.ResponseWriter, r *http.Request) {
	type data struct {
		Token string
		Error string
	}
	if err := r.ParseForm(); err != nil {
		h.renderPage(w, r, "accept_invitation.html", http.StatusBadRequest, data{Error: "Invalid form data."})
		return
	}

	token := r.FormValue("token")
	password := r.FormValue("password")
	confirm := r.FormValue("confirm")

	if password != confirm {
		h.renderPage(w, r, "accept_invitation.html", http.StatusBadRequest, data{Token: token, Error: "Passwords do not match."})
		return
	}
	if len(password) < 12 {
		h.renderPage(w, r, "accept_invitation.html", http.StatusBadRequest, data{Token: token, Error: "Password must be at least 12 characters."})
		return
	}

	link, err := h.emailLinks.ConsumeLink(token)
	if err != nil {
		h.renderPage(w, r, "accept_invitation.html", http.StatusBadRequest, data{Error: "This invitation link is invalid or has expired."})
		return
	}
	if link.Purpose != authn.PurposeInvitation {
		h.renderPage(w, r, "accept_invitation.html", http.StatusBadRequest, data{Error: "Invalid invitation link."})
		return
	}

	userID, err := h.auth.CreateUserFromInvitation(r.Context(), link, password)
	if err != nil {
		h.renderPage(w, r, "accept_invitation.html", http.StatusConflict, data{Error: "An account already exists for this email. Please sign in instead."})
		return
	}

	sessionID, err := h.auth.LoginByUserID(r.Context(), userID, h.sess)
	if err != nil {
		h.log.Error("auto-login after invitation", slog.String("error", err.Error()))
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	h.setSessionCookie(w, sessionID)
	http.Redirect(w, r, landingFor(h.principalForSession(sessionID)), http.StatusSeeOther)
}

type errorPageData struct {
	Code      int
	Title     string
	Message   string
	RequestID string
	// ActionHref and ActionLabel are the one way out this page offers. They are
	// resolved from the caller rather than fixed in the template, which used to
	// send everyone to /admin — a link a member is redirected away from and a
	// signed-out visitor cannot use at all.
	ActionHref  string
	ActionLabel string
}

// errorChrome picks the error page whose navigation suits the caller, and the
// destination its one button should offer.
//
// An error page dressed in the wrong chrome is its own small disclosure and a
// larger confusion: before this, a signed-out visitor who mistyped a URL was
// shown the officer header — Members, Treasury, Imports and a Sign Out button —
// for an application they were not signed in to (bcars-portal-i4a).
func errorChrome(p *authz.Principal) (template, href, label string) {
	switch {
	case p == nil:
		return "error_public.html", RouteLogin, "Go to sign in"
	case navFor(p).AnyOfficer():
		return "error.html", "/admin/", "Go to the dashboard"
	default:
		return "error_member.html", RouteMemberHome, "Go to your records"
	}
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

	page, href, label := errorChrome(h.principalFromRequest(r))
	data := errorPageData{
		Code:        code,
		Title:       title,
		Message:     message,
		ActionHref:  href,
		ActionLabel: label,
	}
	h.renderPage(w, r, page, code, data)
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
