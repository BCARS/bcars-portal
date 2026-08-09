package web

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEveryEmbeddedTemplateIsRegistered is the regression for the defect that
// made every recovery and invitation page fail at runtime: the templates were
// embedded but a hand-maintained list forgot to parse them.
func TestEveryEmbeddedTemplateIsRegistered(t *testing.T) {
	r, err := NewRenderer()
	require.NoError(t, err)

	embedded, err := fs.Glob(templateFS, "templates/*.html")
	require.NoError(t, err)
	require.NotEmpty(t, embedded)

	registered := make(map[string]bool)
	for _, name := range r.TemplateNames() {
		registered[name] = true
	}

	for _, full := range embedded {
		name := path.Base(full)
		if name == layoutTemplate {
			continue
		}
		assert.True(t, registered[name],
			"template %s is embedded but not registered — it would fail at runtime", name)
	}
}

func TestRenderUnknownTemplateNamesIt(t *testing.T) {
	r, err := NewRenderer()
	require.NoError(t, err)

	err = r.Render(&strings.Builder{}, "does_not_exist.html", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does_not_exist.html",
		"the error must name the template, or a render failure is undiagnosable")
}

// TestRecoveryLinkResolvesToARealRoute closes the loop on the emailed URL: the
// generated link previously pointed at /auth/recovery/consume, which no router
// ever served.
func TestRecoveryLinkResolvesToARealRoute(t *testing.T) {
	e := setupHandlerWithRoles(t, "member")

	// The handler's own link service, so the test exercises the paths the
	// running server is configured with rather than paths the test chose.
	require.NoError(t, e.h.emailLinks.RequestRecovery(context.Background(), "test@test.local", ""))

	sent, err := e.h.mailerForTest().ReadAll()
	require.NoError(t, err)
	require.Len(t, sent, 1)

	raw := sent[0].Message.Payload["url"]
	require.NotEmpty(t, raw)
	parsed, err := url.Parse(raw)
	require.NoError(t, err)

	// The emailed path must be served by the mux, not merely well-formed.
	req := httptest.NewRequest("GET", parsed.Path+"?"+parsed.RawQuery, nil)
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	assert.NotEqual(t, http.StatusNotFound, w.Code,
		"the emailed recovery URL %s must resolve to a registered route", parsed.Path)
}

// TestRecoveryRoundTrip exercises the whole journey: request a reset, follow
// the emailed link, set a new password, and sign in with it. Every one of the
// five defects in bcars-portal-fmc.4 breaks this test.
func TestRecoveryRoundTrip(t *testing.T) {
	e := setupHandlerWithRoles(t, "member")

	// Request recovery through the UI.
	form := url.Values{"email": {"test@test.local"}}
	req := httptest.NewRequest("POST", RouteForgotPassword, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "the forgot-password page must render")

	sent, err := e.h.mailerForTest().ReadAll()
	require.NoError(t, err)
	require.Len(t, sent, 1, "a recovery mail must actually be sent")
	token := sent[0].Message.Payload["token"]
	require.NotEmpty(t, token)

	// Follow the link.
	req = httptest.NewRequest("GET", RouteResetPassword+"?token="+url.QueryEscape(token), nil)
	w = httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "the reset-password page must render")

	// Set a new password.
	form = url.Values{
		"token":    {token},
		"password": {"brandnewpassword123"},
		"confirm":  {"brandnewpassword123"},
	}
	req = httptest.NewRequest("POST", RouteResetPassword, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	require.Contains(t, []int{http.StatusOK, http.StatusSeeOther}, w.Code,
		"setting the new password must succeed, got %d", w.Code)

	// The new password works.
	form = url.Values{"email": {"test@test.local"}, "password": {"brandnewpassword123"}}
	req = httptest.NewRequest("POST", RouteLogin, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusSeeOther, w.Code, "the new password must authenticate")
}
