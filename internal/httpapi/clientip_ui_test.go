package httpapi_test

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/authn"
	"github.com/bcars/bcars-portal/internal/httpapi"
	"github.com/bcars/bcars-portal/internal/mail"
)

// The admin UI and the API record client addresses into the same columns. This
// file asserts they record the SAME value for the same caller, driven through
// both shipped surfaces rather than through the hashing helper both call.
//
// bcars-portal-fmc.21: the UI passed an empty hash because the resolver was a
// Huma middleware covering only /api/v1, so every UI-initiated recovery stored
// NULL while the API stored the real value. A helper-level test would have
// passed throughout.

const uiClientIPSecret = "ui-client-ip-test-secret-32-byte"

// assemblyEnv is one router carrying both surfaces, exactly as cmd/portal
// assembles them: one Config feeds the API middleware and the admin UI.
type assemblyEnv struct {
	db      *sql.DB
	handler http.Handler
}

func setupAssembly(t *testing.T, trustedHeader string) *assemblyEnv {
	t.Helper()
	d := openTestDB(t)

	cookieName := "bcars_session"
	store := authn.NewSessionStore(d, authn.SessionConfig{CookieName: cookieName, TTL: time.Hour})
	authSvc := authn.NewAuthService(d, store, nil)
	mailer := mail.NewFilelogSender(t.TempDir())
	emailLinks := authn.NewEmailLinkService(d, mailer, authn.EmailLinkConfig{
		BaseURL: "http://portal.example",
		TTL:     time.Hour,
	})

	handler, api := httpapi.NewRouter(httpapi.Config{
		Version: "test",
		DB:      d,
		Mailer:  mailer,
		BaseURL: "http://portal.example",
		ClientIP: httpapi.ClientIPConfig{
			HashKey:            []byte(uiClientIPSecret),
			TrustedProxyHeader: trustedHeader,
		},
	})
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
		"member@bcars.org", hash)
	require.NoError(t, err)

	return &assemblyEnv{db: d, handler: handler}
}

// recoverViaUI posts the admin UI's forgot-password form.
func (e *assemblyEnv) recoverViaUI(t *testing.T, remoteAddr string, headers map[string]string) {
	t.Helper()
	form := url.Values{"email": {"member@bcars.org"}}
	r := httptest.NewRequest(http.MethodPost, "/forgot-password", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = remoteAddr
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, r)
	require.Less(t, w.Code, 400, "UI recovery failed: %s", w.Body.String())
}

// recoverViaAPI posts the JSON recovery endpoint.
func (e *assemblyEnv) recoverViaAPI(t *testing.T, remoteAddr string, headers map[string]string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/recovery/request",
		strings.NewReader(`{"email":"member@bcars.org"}`))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = remoteAddr
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, r)
	require.Less(t, w.Code, 400, "API recovery failed: %s", w.Body.String())
}

// hashes returns every recorded requested_ip_hash, oldest first. A NULL becomes
// "" so a missing value is distinguishable from a recorded one.
func (e *assemblyEnv) hashes(t *testing.T) []string {
	t.Helper()
	rows, err := e.db.Query(`SELECT COALESCE(requested_ip_hash, '') FROM email_links ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()

	var out []string
	for rows.Next() {
		var h string
		require.NoError(t, rows.Scan(&h))
		out = append(out, h)
	}
	require.NoError(t, rows.Err())
	return out
}

// TestUIRecoveryRecordsSameClientAddressAsAPI is the fmc.21 regression. It
// fails if the UI stores NULL, and it fails if the UI stores something the API
// would not have stored for the same caller.
func TestUIRecoveryRecordsSameClientAddressAsAPI(t *testing.T) {
	e := setupAssembly(t, "")

	const addr = "203.0.113.7:41234"
	e.recoverViaUI(t, addr, nil)
	e.recoverViaAPI(t, addr, nil)

	got := e.hashes(t)
	require.Len(t, got, 2, "both surfaces must have created a recovery link")

	assert.NotEmpty(t, got[0], "a UI-initiated recovery must record the client address")
	assert.Equal(t, got[1], got[0],
		"the UI must record what the API records for the same caller, or a limiter cannot group them")
}

// TestUIRecoveryDistinguishesSources proves the recorded value is a real
// grouping key: two different callers must not collapse into one, and the same
// caller must stay stable across requests.
func TestUIRecoveryDistinguishesSources(t *testing.T) {
	e := setupAssembly(t, "")

	e.recoverViaUI(t, "203.0.113.7:41234", nil)
	e.recoverViaUI(t, "203.0.113.7:59999", nil) // same source, different port
	e.recoverViaUI(t, "198.51.100.4:41234", nil)

	got := e.hashes(t)
	require.Len(t, got, 3)

	assert.Equal(t, got[0], got[1], "one source stays one group regardless of ephemeral port")
	assert.NotEqual(t, got[0], got[2], "two sources must not collapse into one group")
}

// TestUIRecoveryIgnoresForgedForwardingHeader proves the UI honours the same
// trust rule as the API: without a configured trusted header, a caller cannot
// choose its own recorded source and escape a future per-source limit.
func TestUIRecoveryIgnoresForgedForwardingHeader(t *testing.T) {
	e := setupAssembly(t, "")

	const addr = "203.0.113.7:41234"
	e.recoverViaUI(t, addr, map[string]string{"X-Forwarded-For": "198.51.100.1"})
	e.recoverViaUI(t, addr, map[string]string{"X-Forwarded-For": "198.51.100.2"})

	got := e.hashes(t)
	require.Len(t, got, 2)
	assert.Equal(t, got[0], got[1],
		"an untrusted forwarding header must not let one caller look like many")
}

// TestUIRecoveryHonoursTrustedForwardingHeader proves the UI reads the header
// once the deployment declares it trusted, and agrees with the API when it does.
func TestUIRecoveryHonoursTrustedForwardingHeader(t *testing.T) {
	e := setupAssembly(t, "X-Forwarded-For")

	const proxy = "10.0.0.9:41234"
	e.recoverViaUI(t, proxy, map[string]string{"X-Forwarded-For": "198.51.100.1, 10.0.0.9"})
	e.recoverViaAPI(t, proxy, map[string]string{"X-Forwarded-For": "198.51.100.1, 10.0.0.9"})
	e.recoverViaUI(t, proxy, map[string]string{"X-Forwarded-For": "198.51.100.2"})

	got := e.hashes(t)
	require.Len(t, got, 3)

	assert.NotEmpty(t, got[0])
	assert.Equal(t, got[1], got[0], "both surfaces must read the trusted header identically")
	assert.NotEqual(t, got[2], got[0], "a different forwarded client is a different source")
}

// TestUISignInRecordsClientAddress covers the second UI call site: a session
// created through the login form records the same source the API would record,
// so session provenance is not blank for UI users.
func TestUISignInRecordsClientAddress(t *testing.T) {
	e := setupAssembly(t, "")

	form := url.Values{"email": {"member@bcars.org"}, "password": {"correcthorsebatterystaple"}}
	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "203.0.113.7:41234"
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, r)
	require.Equal(t, http.StatusSeeOther, w.Code, "sign-in failed: %s", w.Body.String())

	var sessionHash string
	require.NoError(t, e.db.QueryRow(
		`SELECT COALESCE(ip_hash, '') FROM sessions ORDER BY rowid DESC LIMIT 1`).Scan(&sessionHash))
	assert.NotEmpty(t, sessionHash, "a UI sign-in must record the client address")

	// Same construction as the recovery path, which the previous test pinned
	// to the API's value.
	e.recoverViaUI(t, "203.0.113.7:41234", nil)
	got := e.hashes(t)
	require.Len(t, got, 1)
	assert.Equal(t, got[0], sessionHash,
		"sign-in and recovery must record one caller as one source")
}
