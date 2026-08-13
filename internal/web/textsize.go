package web

import (
	"context"
	"log/slog"
	"net/http"

	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/authz"
)

// Text sizes the portal renders at. These are the stored values as well as the
// data-text-size attribute the stylesheet keys off, so adding a size means
// adding a rule in the token block and a value to the CHECK constraint in
// 0014_user_text_size.sql.
const (
	textSizeBase  = "base"
	textSizeLarge = "large"
)

// validTextSize reports whether v is a size the portal renders, so a hand-made
// form post cannot store a value the stylesheet has no rule for. The database
// CHECK constraint refuses the same set; this exists so the caller gets a 422
// rather than a failed write.
func validTextSize(v string) bool {
	return v == textSizeBase || v == textSizeLarge
}

// textSizeFor returns the signed-in user's stored text size, falling back to
// the base size for a signed-out caller or an unreadable preference.
//
// A failure here must not cost the caller their page: the preference is
// presentation, and rendering at the base size is a worse experience than the
// user asked for but a far better one than an error. The failure is logged so
// a persistently unreadable column is visible in the logs rather than silently
// pinning every officer to the base size.
func (h *Handler) textSizeFor(r *http.Request) string {
	p := h.principal(r)
	if p == nil || p.UserID == 0 {
		return textSizeBase
	}
	size, err := h.queries.GetUserTextSize(r.Context(), p.UserID)
	if err != nil {
		h.log.Warn("read text size preference failed",
			slog.Int64("user_id", p.UserID),
			slog.String("error", err.Error()))
		return textSizeBase
	}
	if !validTextSize(size) {
		return textSizeBase
	}
	return size
}

// renderPage renders a page at the caller's stored text size. Every handler
// goes through here rather than calling the renderer directly, so a new page
// inherits the preference without its author having to remember it.
func (h *Handler) renderPage(w http.ResponseWriter, r *http.Request, name string, status int, data any) {
	h.render.RenderPage(w, name, status, data, h.textSizeFor(r))
}

// setTextSize stores the caller's text size preference.
func (h *Handler) setTextSize(ctx context.Context, userID int64, size string) error {
	return h.queries.SetUserTextSize(ctx, sqlcgen.SetUserTextSizeParams{
		TextSize: size,
		ID:       userID,
	})
}

// RoutePreferences is the prefix for per-user display preferences. It is a
// third guarded surface alongside /admin/ and /member/, and deliberately so:
// what lives here is scoped to the caller's own account and is neither officer
// authority nor member record access, so hanging it off either surface would
// mean picking one and refusing the other half of the portal.
const RoutePreferences = "/preferences/"

// RouteTextSize is the text size preference page, linked from both shells.
const RouteTextSize = RoutePreferences + "text-size"

// PreferenceRoutes returns the routes for per-user display preferences.
//
// session.self.read is the capability every signed-in role holds, officer and
// member alike, which is what this page needs: type size is an accessibility
// setting and refusing it to the roles that most need it would be perverse.
//
// The POST carries no AuditAction. The audit log exists to answer who saw or
// changed a member's data; how large one officer renders their own type is not
// such a question, and writing an event per toggle would bury the ones that
// are.
func (h *Handler) PreferenceRoutes() []GuardedRoute {
	return []GuardedRoute{
		{Pattern: "GET " + RouteTextSize, Capability: "session.self.read", ResourceKind: "user_preference", handler: h.textSizePage},
		{Pattern: "POST " + RouteTextSize, Capability: "session.self.read", ResourceKind: "user_preference", handler: h.textSizeSubmit},
	}
}

// textSizeData backs the standalone preference page. It carries its own
// TextSize because the page is not chrome-wrapped: it embeds no base layout,
// so nothing else would supply the value its <html> element needs.
type textSizeData struct {
	TextSize  string
	BackHref  string
	BackLabel string
	Saved     bool
	Error     string
}

// backFor resolves where this caller returns to. landingFor already encodes
// which surface a principal belongs on, so an officer is sent to the dashboard
// and a member to their own landing rather than to a page they are refused.
func backFor(p *authz.Principal) (href, label string) {
	if dest := landingFor(p); dest == RouteMemberHome {
		return dest, "your records"
	}
	return "/admin/", "the dashboard"
}

// renderTextSize renders the preference page at the size it is reporting.
//
// It goes through RenderPage rather than RenderHTTP even though the page is
// standalone today: if it ever grows a base layout, the renderer wraps it and
// this keeps working, where a direct RenderHTTP would quietly hand the layout a
// struct with no .Page and fail at execution.
func (h *Handler) renderTextSize(w http.ResponseWriter, status int, data textSizeData) {
	h.render.RenderPage(w, "text_size.html", status, data, data.TextSize)
}

func (h *Handler) textSizePage(w http.ResponseWriter, r *http.Request) {
	href, label := backFor(h.principal(r))
	h.renderTextSize(w, http.StatusOK, textSizeData{
		TextSize:  h.textSizeFor(r),
		BackHref:  href,
		BackLabel: label,
	})
}

func (h *Handler) textSizeSubmit(w http.ResponseWriter, r *http.Request) {
	p := h.principal(r)
	href, label := backFor(p)

	size := r.FormValue("text_size")
	if !validTextSize(size) {
		// The stored preference is left alone: a form post naming a size the
		// portal does not render is not a reason to reset the one the caller
		// already chose.
		h.renderTextSize(w, http.StatusUnprocessableEntity, textSizeData{
			TextSize:  h.textSizeFor(r),
			BackHref:  href,
			BackLabel: label,
			Error:     "Choose either Normal or Larger.",
		})
		return
	}

	if err := h.setTextSize(r.Context(), p.UserID, size); err != nil {
		h.log.Error("save text size preference failed",
			slog.Int64("user_id", p.UserID),
			slog.String("error", err.Error()))
		h.renderTextSize(w, http.StatusInternalServerError, textSizeData{
			TextSize:  h.textSizeFor(r),
			BackHref:  href,
			BackLabel: label,
			Error:     "That could not be saved. Please try again.",
		})
		return
	}

	h.renderTextSize(w, http.StatusOK, textSizeData{
		TextSize:  size,
		BackHref:  href,
		BackLabel: label,
		Saved:     true,
	})
}
