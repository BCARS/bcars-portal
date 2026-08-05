package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/db"
)

func setupHandler(t *testing.T) *Handler {
	t.Helper()
	d, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	require.NoError(t, db.Migrate(d))

	// Create a test user for the principal.
	_, err = d.Exec(`INSERT INTO users (email, is_active) VALUES ('admin@bcars.org', 1)`)
	require.NoError(t, err)

	h, err := NewHandler(d, nil)
	require.NoError(t, err)
	return h
}

func TestDashboard(t *testing.T) {
	h := setupHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/admin/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Dashboard")
	assert.Contains(t, w.Body.String(), "Total Members")
}

func TestMemberListEmpty(t *testing.T) {
	h := setupHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/admin/members", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "No members found")
}

func TestMemberCreateAndView(t *testing.T) {
	h := setupHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// GET new form.
	req := httptest.NewRequest("GET", "/admin/members/new", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Create Member")

	// POST create.
	form := url.Values{
		"display_name": {"Alice Test"},
		"sort_name":    {"Test, Alice"},
		"call_sign":    {"KA1AAA"},
		"base_type":    {"full"},
	}
	req = httptest.NewRequest("POST", "/admin/members/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Contains(t, w.Header().Get("Location"), "/admin/members/1")

	// GET detail.
	req = httptest.NewRequest("GET", "/admin/members/1", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Alice Test")
	assert.Contains(t, w.Body.String(), "KA1AAA")

	// GET list should show member.
	req = httptest.NewRequest("GET", "/admin/members", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Alice Test")
}

func TestMemberSearch(t *testing.T) {
	h := setupHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Create two members.
	for _, name := range []string{"Alice Test", "Bob Sample"} {
		form := url.Values{
			"display_name": {name},
			"sort_name":    {name},
			"base_type":    {"full"},
		}
		req := httptest.NewRequest("POST", "/admin/members/new", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		require.Equal(t, http.StatusSeeOther, w.Code)
	}

	// Search for Alice.
	req := httptest.NewRequest("GET", "/admin/members?q=Alice", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Alice Test")
	assert.NotContains(t, w.Body.String(), "Bob Sample")
}

func TestMemberEditAndUpdate(t *testing.T) {
	h := setupHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Create.
	form := url.Values{
		"display_name": {"Old Name"},
		"sort_name":    {"Name, Old"},
		"base_type":    {"full"},
	}
	req := httptest.NewRequest("POST", "/admin/members/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusSeeOther, w.Code)

	// GET edit form.
	req = httptest.NewRequest("GET", "/admin/members/1/edit", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Old Name")

	// POST update.
	form = url.Values{
		"display_name": {"New Name"},
		"sort_name":    {"Name, New"},
		"version":      {"1"},
	}
	req = httptest.NewRequest("POST", "/admin/members/1/edit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusSeeOther, w.Code)

	// Verify update.
	req = httptest.NewRequest("GET", "/admin/members/1", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Contains(t, w.Body.String(), "New Name")
}

func TestMemberDeactivateReactivate(t *testing.T) {
	h := setupHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Create.
	form := url.Values{"display_name": {"Test"}, "sort_name": {"Test"}, "base_type": {"full"}}
	req := httptest.NewRequest("POST", "/admin/members/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// Deactivate.
	form = url.Values{"version": {"1"}}
	req = httptest.NewRequest("POST", "/admin/members/1/deactivate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusSeeOther, w.Code)

	// Verify deactivated.
	req = httptest.NewRequest("GET", "/admin/members/1", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Contains(t, w.Body.String(), "Deactivated")

	// Reactivate.
	form = url.Values{"version": {"2"}}
	req = httptest.NewRequest("POST", "/admin/members/1/reactivate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusSeeOther, w.Code)
}

func TestMembershipApprove(t *testing.T) {
	h := setupHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Create member (creates pending membership).
	form := url.Values{"display_name": {"Test"}, "sort_name": {"Test"}, "base_type": {"full"}}
	req := httptest.NewRequest("POST", "/admin/members/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// View detail — should show pending.
	req = httptest.NewRequest("GET", "/admin/members/1", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Contains(t, w.Body.String(), "Pending")

	// Approve.
	form = url.Values{"version": {"1"}, "base_type": {"full"}}
	req = httptest.NewRequest("POST", "/admin/members/1/memberships/1/approve", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusSeeOther, w.Code)

	// View detail — should show approved.
	req = httptest.NewRequest("GET", "/admin/members/1", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Contains(t, w.Body.String(), "Approved")
}

func TestNoteCreate(t *testing.T) {
	h := setupHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Create member.
	form := url.Values{"display_name": {"Test"}, "sort_name": {"Test"}, "base_type": {"full"}}
	req := httptest.NewRequest("POST", "/admin/members/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// Add note.
	form = url.Values{"body": {"Test note content"}, "category": {"general"}}
	req = httptest.NewRequest("POST", "/admin/members/1/notes", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusSeeOther, w.Code)

	// View detail — should show note.
	req = httptest.NewRequest("GET", "/admin/members/1", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Contains(t, w.Body.String(), "Test note content")
}

func TestImportListEmpty(t *testing.T) {
	h := setupHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/admin/imports", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Import Review")
	assert.Contains(t, w.Body.String(), "No import runs yet")
}

func TestTemplateRendering(t *testing.T) {
	r, err := NewRenderer()
	require.NoError(t, err)

	tests := []struct {
		name string
		data any
	}{
		{"dashboard.html", dashboardData{}},
		{"members.html", memberListData{}},
		{"member_form.html", memberFormData{}},
		{"imports.html", struct{ Runs []interface{} }{}},
	}
	for _, tt := range tests {
		w := httptest.NewRecorder()
		r.RenderHTTP(w, tt.name, http.StatusOK, tt.data)
		assert.Equal(t, http.StatusOK, w.Code, "template %s should render", tt.name)
		assert.Contains(t, w.Body.String(), "BCARS Portal", "template %s should contain layout", tt.name)
	}
}
