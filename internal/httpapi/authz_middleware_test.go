package httpapi_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/authn"
	"github.com/bcars/bcars-portal/internal/httpapi"
	"github.com/bcars/bcars-portal/internal/mail"
)

// authzEnv is a fully wired API server plus the database behind it, so tests
// can both drive requests and inspect the audit trail they produce.
type authzEnv struct {
	ts     *httptest.Server
	db     *sql.DB
	mailer *mail.FilelogSender
	links  *authn.EmailLinkService
}

// setupAuthzTest builds the API with authn.Middleware in front of it and seeds
// one user holding exactly the roles named in roleCodes. Passing no roles
// produces an authenticated user with no capabilities at all.
func setupAuthzTest(t *testing.T, roleCodes ...string) *authzEnv {
	t.Helper()
	d := openTestDB(t)

	cookieName := "bcars_session"
	store := authn.NewSessionStore(d, authn.SessionConfig{
		CookieName: cookieName,
		TTL:        1 * time.Hour,
	})
	authSvc := authn.NewAuthService(d, store, nil)
	mailer := mail.NewFilelogSender(t.TempDir())
	emailLinks := authn.NewEmailLinkService(d, mailer, authn.EmailLinkConfig{
		BaseURL: "http://localhost:8080",
		TTL:     1 * time.Hour,
	})

	handler, api := httpapi.NewRouter(httpapi.Config{
		Version: "test",
		DB:      d,
		// Keyed so the recorded client-address hashes are the real thing;
		// without a key the server records "unknown" and the tests that check
		// the value would pass on an empty string.
		ClientIP: httpapi.ClientIPConfig{HashKey: []byte("authz-test-client-ip-secret-32b!")},
	})
	capLoader := &authn.SQLCapabilityLoader{DB: d}
	wrapped := authn.Middleware(store, capLoader, authn.SessionCookieConfig{Name: cookieName})(handler)

	httpapi.RegisterAll(api, httpapi.Deps{
		DB:               d,
		AuthService:      authSvc,
		SessionStore:     store,
		EmailLinkService: emailLinks,
		CookieName:       cookieName,
	})
	require.NoError(t, httpapi.VerifyAll(api))

	hash, err := authn.HashPassword("correcthorsebatterystaple", nil, authn.DefaultParams())
	require.NoError(t, err)
	_, err = d.Exec(`INSERT INTO users (email, password_hash, is_active) VALUES (?, ?, 1)`,
		"user@bcars.org", hash)
	require.NoError(t, err)

	for _, role := range roleCodes {
		_, err = d.Exec(
			`INSERT INTO user_role_grants (user_id, role_code, granted_by, granted_at) VALUES (1, ?, 1, datetime('now'))`,
			role)
		require.NoError(t, err)
	}

	ts := httptest.NewServer(wrapped)
	t.Cleanup(ts.Close)
	return &authzEnv{ts: ts, db: d, mailer: mailer, links: emailLinks}
}

func (e *authzEnv) signIn(t *testing.T) *http.Cookie {
	t.Helper()
	body := `{"email":"user@bcars.org","password":"correcthorsebatterystaple"}`
	resp, err := e.ts.Client().Post(e.ts.URL+"/api/v1/sessions", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	for _, c := range resp.Cookies() {
		if c.Name == "bcars_session" {
			return c
		}
	}
	t.Fatal("no session cookie returned by sign-in")
	return nil
}

// do issues a request, attaching cookie when non-nil.
func (e *authzEnv) do(t *testing.T, method, path string, cookie *http.Cookie, body string) *http.Response {
	t.Helper()
	var r *http.Request
	var err error
	if body != "" {
		r, err = http.NewRequest(method, e.ts.URL+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r, err = http.NewRequest(method, e.ts.URL+path, nil)
	}
	require.NoError(t, err)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	resp, err := e.ts.Client().Do(r)
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// guardedOps are the direct-query endpoints that were reachable by any
// authenticated caller before capability enforcement existed. Each names the
// capability it must now require.
var guardedOps = []struct {
	name       string
	method     string
	path       string
	body       string
	capability string
}{
	{"audit read", http.MethodGet, "/api/v1/audit-events", "", "audit.read"},
	{"member export", http.MethodPost, "/api/v1/exports/members", `{}`, "member.export"},
	{"role grant", http.MethodPost, "/api/v1/users/1/role-grants", `{"role_code":"treasurer"}`, "role.grant"},
	{"import commit", http.MethodPost, "/api/v1/imports/1/commit", `{}`, "import.commit"},
	{"import upload list", http.MethodGet, "/api/v1/imports", "", "import.upload"},
}

// TestGuardedOps_DenyAuthenticatedWithoutCapability is the core regression for
// bcars-portal-fmc.1: these endpoints previously required only a session.
func TestGuardedOps_DenyAuthenticatedWithoutCapability(t *testing.T) {
	// "member" role holds only session.self.read.
	env := setupAuthzTest(t, "member")
	cookie := env.signIn(t)

	for _, op := range guardedOps {
		t.Run(op.name, func(t *testing.T) {
			resp := env.do(t, op.method, op.path, cookie, op.body)
			assert.Equal(t, http.StatusForbidden, resp.StatusCode,
				"%s %s must require %s, not merely a session", op.method, op.path, op.capability)
		})
	}
}

func TestGuardedOps_DenyUnauthenticated(t *testing.T) {
	env := setupAuthzTest(t)

	for _, op := range guardedOps {
		t.Run(op.name, func(t *testing.T) {
			resp := env.do(t, op.method, op.path, nil, op.body)
			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
				"%s %s must reject anonymous callers", op.method, op.path)
		})
	}
}

// TestGuardedOps_AllowWithCapability proves the middleware is not simply
// denying everything: an administrator holds every capability and must get
// past the authorization layer on each of these endpoints.
func TestGuardedOps_AllowWithCapability(t *testing.T) {
	env := setupAuthzTest(t, "administrator")
	cookie := env.signIn(t)

	for _, op := range guardedOps {
		t.Run(op.name, func(t *testing.T) {
			resp := env.do(t, op.method, op.path, cookie, op.body)
			assert.NotEqual(t, http.StatusForbidden, resp.StatusCode,
				"administrator holds %s and must not be denied", op.capability)
			assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode)
		})
	}
}

// TestPublicOperationsStayPublic guards against the enforcement layer
// accidentally locking out sign-in and other PublicCapability endpoints.
func TestPublicOperationsStayPublic(t *testing.T) {
	env := setupAuthzTest(t)

	resp := env.do(t, http.MethodPost, "/api/v1/sessions", nil,
		`{"email":"user@bcars.org","password":"correcthorsebatterystaple"}`)
	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"sign-in is PublicCapability and must not require authentication")
}

// TestPartialCapabilityIsNotBlanketAccess checks that holding one capability
// does not grant a neighbouring one — import.upload must not imply
// import.commit.
func TestPartialCapabilityIsNotBlanketAccess(t *testing.T) {
	env := setupAuthzTest(t)

	_, err := env.db.Exec(
		`INSERT INTO user_capability_grants (user_id, capability_code, granted_by, granted_at)
		 VALUES (1, 'import.upload', 1, datetime('now'))`)
	require.NoError(t, err)

	// Sign in after the grant so the session carries it.
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodGet, "/api/v1/imports", cookie, "")
	assert.NotEqual(t, http.StatusForbidden, resp.StatusCode, "import.upload was granted")

	resp = env.do(t, http.MethodPost, "/api/v1/imports/1/commit", cookie, `{}`)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"import.upload must not imply import.commit")
}

// --- Audit emission ---

type auditRow struct {
	Action       string
	Actor        sql.NullInt64
	Outcome      string
	ReasonCode   sql.NullString
	ResourceKind sql.NullString
	ResourceID   sql.NullInt64
	Detail       sql.NullString
}

func (e *authzEnv) auditEvents(t *testing.T, action string) []auditRow {
	t.Helper()
	rows, err := e.db.Query(
		`SELECT action, actor_user_id, outcome, reason_code, resource_kind, resource_id, detail_json
		 FROM audit_events WHERE action = ? ORDER BY id`, action)
	require.NoError(t, err)
	defer rows.Close()

	var out []auditRow
	for rows.Next() {
		var r auditRow
		require.NoError(t, rows.Scan(&r.Action, &r.Actor, &r.Outcome, &r.ReasonCode,
			&r.ResourceKind, &r.ResourceID, &r.Detail))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

// TestAuditEmittedForDeclaredAction verifies the generic audit path: the
// handler contains no audit code, so a recorded event can only have come from
// the declared AuditAction.
func TestAuditEmittedForDeclaredAction(t *testing.T) {
	env := setupAuthzTest(t, "administrator")
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodPost, "/api/v1/members", cookie,
		`{"display_name":"Ada Lovelace","sort_name":"Lovelace, Ada","base_type":"full"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	events := env.auditEvents(t, "member.create")
	require.Len(t, events, 1, "exactly one audit event for the create — the domain "+
		"service must not emit a second, duplicate row")
	assert.Equal(t, "success", events[0].Outcome)
	assert.Equal(t, int64(1), events[0].Actor.Int64)

	// A create has no {id} path parameter; the handler stamps the new row's
	// identity so the event still names what was created.
	assert.Equal(t, "person", events[0].ResourceKind.String)
	assert.NotZero(t, events[0].ResourceID.Int64, "created resource id must be recorded")

	var detail map[string]any
	require.NoError(t, json.Unmarshal([]byte(events[0].Detail.String), &detail))
	assert.Equal(t, "POST", detail["method"])
	assert.Equal(t, "/api/v1/members", detail["path"])
	assert.Equal(t, "member.create", detail["required_capability"])
}

// TestAuditEmittedOnDenial ensures a rejected request leaves a trail — the
// case that matters most for detecting probing.
func TestAuditEmittedOnDenial(t *testing.T) {
	env := setupAuthzTest(t, "member")
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodPost, "/api/v1/members", cookie,
		`{"display_name":"Ada Lovelace","sort_name":"Lovelace, Ada","base_type":"full"}`)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	events := env.auditEvents(t, "member.create")
	require.Len(t, events, 1)
	assert.Equal(t, "denied", events[0].Outcome)
	assert.Equal(t, "missing_capability", events[0].ReasonCode.String)
	assert.Equal(t, int64(1), events[0].Actor.Int64)

	var detail map[string]any
	require.NoError(t, json.Unmarshal([]byte(events[0].Detail.String), &detail))
	assert.Equal(t, float64(http.StatusForbidden), detail["status"],
		"the denial must record the status it actually returned")
}

// TestAuditDenialForReadOnlyOperation covers operations that declare no
// AuditAction: the success path stays quiet, but denials are still recorded.
func TestAuditDenialForReadOnlyOperation(t *testing.T) {
	env := setupAuthzTest(t, "member")
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodGet, "/api/v1/audit-events", cookie, "")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	events := env.auditEvents(t, "authz.denied.audit-events-list")
	require.Len(t, events, 1)
	assert.Equal(t, "denied", events[0].Outcome)
	assert.Equal(t, "missing_capability", events[0].ReasonCode.String)
}

func TestAuditRecordsUnauthenticatedDenial(t *testing.T) {
	env := setupAuthzTest(t)

	resp := env.do(t, http.MethodGet, "/api/v1/audit-events", nil, "")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	events := env.auditEvents(t, "authz.denied.audit-events-list")
	require.Len(t, events, 1)
	assert.Equal(t, "denied", events[0].Outcome)
	assert.Equal(t, "unauthenticated", events[0].ReasonCode.String)
	assert.False(t, events[0].Actor.Valid, "no actor for an anonymous request")
}

// TestEveryOperationDeclaresKnownCapability locks in the invariant the
// enforcement layer depends on: metadata exists for every registered
// operation, so lookupMeta never has to fail closed in production.
func TestEveryOperationDeclaresKnownCapability(t *testing.T) {
	_, api := httpapi.NewRouter(httpapi.Config{Version: "test"})
	httpapi.RegisterAll(api, httpapi.Deps{})
	require.NoError(t, httpapi.VerifyAll(api))

	meta := httpapi.AllMeta()
	require.NotEmpty(t, meta)
	for opID, m := range meta {
		assert.NotEmpty(t, m.RequiredCapability, "operation %q declares no capability", opID)
	}
}

// TestInvitationConsumeGrantsIntendedRole closes the loop for
// bcars-portal-fmc.3 at the API layer: an invitation that carries a role must
// produce a principal holding that role's capabilities, and the resulting
// session must pass the capability middleware on a guarded endpoint.
func TestInvitationConsumeGrantsIntendedRole(t *testing.T) {
	env := setupAuthzTest(t)

	token, err := env.links.CreateInvitation(context.Background(), "newadmin@bcars.org", "administrator", false)
	require.NoError(t, err)

	resp := env.do(t, http.MethodPost, "/api/v1/auth/invitations/consume", nil,
		`{"token":"`+token+`","new_password":"correcthorsebatterystaple"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		UserID int64 `json:"user_id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.NotZero(t, body.UserID)

	caps, err := (&authn.SQLCapabilityLoader{DB: env.db}).EffectiveCapabilities(body.UserID)
	require.NoError(t, err)
	assert.Contains(t, caps, "audit.read", "the invited role must actually be granted")

	// And the account works: sign in and reach a capability-guarded endpoint.
	signin := env.do(t, http.MethodPost, "/api/v1/sessions", nil,
		`{"email":"newadmin@bcars.org","password":"correcthorsebatterystaple"}`)
	require.Equal(t, http.StatusOK, signin.StatusCode)
	var cookie *http.Cookie
	for _, c := range signin.Cookies() {
		if c.Name == "bcars_session" {
			cookie = c
		}
	}
	require.NotNil(t, cookie)

	guarded := env.do(t, http.MethodGet, "/api/v1/audit-events", cookie, "")
	assert.Equal(t, http.StatusOK, guarded.StatusCode,
		"a bootstrapped administrator must pass the capability middleware")
}
