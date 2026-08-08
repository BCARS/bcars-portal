package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/authn"
	"github.com/bcars/bcars-portal/internal/db"
	"github.com/bcars/bcars-portal/internal/mail"
)

const testPassword = "correcthorsebatterystaple"

// prodEnv is the server the binary actually serves: buildHandler is the only
// wiring under test, so a regression in main's assembly (a missing
// authn.Middleware, an unsupplied EmailLinkService) fails here.
type prodEnv struct {
	ts      *httptest.Server
	db      *sql.DB
	mailDir string
}

func setupProdEnv(t *testing.T) *prodEnv {
	t.Helper()

	d, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, db.Migrate(d))

	mailDir := filepath.Join(t.TempDir(), "outbox")
	mailer, err := newMailSender(mailConfig{Transport: "filelog", FilelogDir: mailDir})
	require.NoError(t, err)

	handler, err := buildHandler(d, assemblyConfig{
		Version:      "test",
		CookieName:   "bcars_session",
		SessionTTL:   time.Hour,
		BaseURL:      "https://portal.example.org",
		EmailLinkTTL: time.Hour,
		Mailer:       mailer,
	})
	require.NoError(t, err)

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	return &prodEnv{ts: ts, db: d, mailDir: mailDir}
}

// seedUser inserts an active user with testPassword and grants it roleCode
// (empty roleCode means an authenticated user with no capabilities at all).
func (e *prodEnv) seedUser(t *testing.T, email, roleCode string) int64 {
	t.Helper()
	hash, err := authn.HashPassword(testPassword, nil, authn.DefaultParams())
	require.NoError(t, err)

	res, err := e.db.Exec(
		`INSERT INTO users (email, password_hash, is_active) VALUES (?, ?, 1)`, email, hash)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)

	if roleCode != "" {
		_, err = e.db.Exec(
			`INSERT INTO user_role_grants (user_id, role_code, granted_by, granted_at)
			 VALUES (?, ?, ?, datetime('now'))`, id, roleCode, id)
		require.NoError(t, err)
	}
	return id
}

func (e *prodEnv) signIn(t *testing.T, email string) *http.Cookie {
	t.Helper()
	body, err := json.Marshal(map[string]string{"email": email, "password": testPassword})
	require.NoError(t, err)

	resp, err := e.ts.Client().Post(e.ts.URL+"/api/v1/sessions", "application/json",
		strings.NewReader(string(body)))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "sign-in must succeed")

	for _, c := range resp.Cookies() {
		if c.Name == "bcars_session" {
			return c
		}
	}
	t.Fatalf("sign-in returned no bcars_session cookie")
	return nil
}

func (e *prodEnv) do(t *testing.T, method, path string, cookie *http.Cookie, body string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, e.ts.URL+path, reader)
	require.NoError(t, err)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := e.ts.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// TestProductionAssembly_SessionCookieResolvesPrincipal is the regression for
// bcars-portal-fmc.2: the shipped binary never wrapped the router in
// authn.Middleware, so PrincipalFrom returned nil and every authenticated call
// 401'd in production while the httpapi integration tests — which wrapped the
// handler themselves — stayed green.
func TestProductionAssembly_SessionCookieResolvesPrincipal(t *testing.T) {
	env := setupProdEnv(t)
	userID := env.seedUser(t, "admin@bcars.org", "administrator")

	cookie := env.signIn(t, "admin@bcars.org")
	require.True(t, cookie.HttpOnly, "session cookie must be HttpOnly")

	resp := env.do(t, http.MethodGet, "/api/v1/sessions/current", cookie, "")
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"the session cookie must resolve to a principal in the production assembly")

	var got struct {
		UserID       int64    `json:"user_id"`
		Email        string   `json:"email"`
		Capabilities []string `json:"capabilities"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, userID, got.UserID)
	assert.Equal(t, "admin@bcars.org", got.Email)
	assert.Contains(t, got.Capabilities, "audit.read",
		"administrator capabilities must be loaded onto the principal")
}

// TestProductionAssembly_CapabilityGuardedRead exercises a guarded endpoint
// with a privileged cookie and with an under-privileged one.
func TestProductionAssembly_CapabilityGuardedRead(t *testing.T) {
	env := setupProdEnv(t)
	env.seedUser(t, "admin@bcars.org", "administrator")
	env.seedUser(t, "member@bcars.org", "member")

	t.Run("allowed", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, "/api/v1/audit-events", env.signIn(t, "admin@bcars.org"), "")
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"administrator holds audit.read")
	})

	t.Run("denied for under-privileged principal", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, "/api/v1/audit-events", env.signIn(t, "member@bcars.org"), "")
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"member holds only session.self.read and must be denied audit.read")
	})

	t.Run("denied anonymous", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, "/api/v1/audit-events", nil, "")
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

// TestProductionAssembly_EmailLinkServiceWired proves Deps.EmailLinkService is
// supplied: without it the endpoint returns 501 from its nil-dependency guard
// and no mail is ever produced.
//
// The recovery flow beyond this point (purpose constant, consume route,
// Set-Cookie) is still broken — that is bcars-portal-fmc.4.
func TestProductionAssembly_EmailLinkServiceWired(t *testing.T) {
	env := setupProdEnv(t)
	env.seedUser(t, "admin@bcars.org", "administrator")

	resp := env.do(t, http.MethodPost, "/api/v1/auth/recovery/request", nil,
		`{"email":"admin@bcars.org"}`)
	require.NotEqual(t, http.StatusNotImplemented, resp.StatusCode,
		"EmailLinkService must be supplied in httpapi.Deps")
	require.Less(t, resp.StatusCode, 300, "recovery request must succeed")

	entries, err := os.ReadDir(env.mailDir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "the configured mail sender must have received the recovery message")

	data, err := os.ReadFile(filepath.Join(env.mailDir, entries[0].Name()))
	require.NoError(t, err)
	var logged struct {
		Message mail.Message `json:"message"`
	}
	require.NoError(t, json.Unmarshal(data, &logged))
	assert.Equal(t, "admin@bcars.org", logged.Message.To)
	assert.Contains(t, logged.Message.Payload["url"], "https://portal.example.org",
		"the configured BaseURL must be used to build the link")
}

// TestNewMailSender covers transport selection and its configuration errors.
func TestNewMailSender(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "outbox")
	s, err := newMailSender(mailConfig{Transport: "filelog", FilelogDir: dir})
	require.NoError(t, err)
	assert.IsType(t, &mail.FilelogSender{}, s)
	_, err = os.Stat(dir)
	assert.NoError(t, err, "filelog dir must be created")

	s, err = newMailSender(mailConfig{
		Transport: "smtp", SMTPHost: "mail.example.org", SMTPPort: 587, SMTPFrom: "portal@example.org",
	})
	require.NoError(t, err)
	assert.IsType(t, &mail.SMTPSender{}, s)

	_, err = newMailSender(mailConfig{Transport: "smtp", SMTPFrom: "portal@example.org"})
	assert.Error(t, err, "smtp without a host must be rejected")

	_, err = newMailSender(mailConfig{Transport: "carrier-pigeon"})
	assert.Error(t, err)
}
