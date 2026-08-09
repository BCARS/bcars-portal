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
	"github.com/bcars/bcars-portal/internal/ratelimit"
)

// The limiter's whole purpose is that being refused reveals nothing. These
// tests therefore compare a KNOWN address against an UNKNOWN one at the same
// request count, through the shipped endpoints, and assert the responses are
// indistinguishable (bcars-portal-fmc.20).

const limitSecret = "recovery-limit-test-secret-32b!!"

type limitEnv struct {
	db      *sql.DB
	handler http.Handler
	clock   *time.Time
}

func setupLimitEnv(t *testing.T) *limitEnv {
	t.Helper()
	d := openTestDB(t)

	// The admin UI builds its own limiter internally and therefore runs on the
	// real clock. Starting the injected clock at the real one keeps the two
	// agreeing, so a cross-surface test measures shared counts rather than a
	// clock offset; advancing it still exercises window expiry.
	now := time.Now().UTC()
	env := &limitEnv{db: d, clock: &now}

	limiter := ratelimit.New(d, ratelimit.Config{
		HashKey: []byte(limitSecret),
		Now:     func() time.Time { return *env.clock },
	})

	cookieName := "bcars_session"
	store := authn.NewSessionStore(d, authn.SessionConfig{CookieName: cookieName, TTL: time.Hour})
	mailer := mail.NewFilelogSender(t.TempDir())
	emailLinks := authn.NewEmailLinkService(d, mailer, authn.EmailLinkConfig{
		BaseURL: "http://portal.example",
		TTL:     time.Hour,
		Limiter: limiter,
	})

	handler, api := httpapi.NewRouter(httpapi.Config{
		Version:  "test",
		DB:       d,
		Mailer:   mailer,
		BaseURL:  "http://portal.example",
		ClientIP: httpapi.ClientIPConfig{HashKey: []byte(limitSecret)},
	})
	httpapi.RegisterAll(api, httpapi.Deps{
		DB:               d,
		SessionStore:     store,
		EmailLinkService: emailLinks,
		CookieName:       cookieName,
	})
	require.NoError(t, httpapi.VerifyAll(api))

	hash, err := authn.HashPassword("correcthorsebatterystaple", nil, authn.DefaultParams())
	require.NoError(t, err)
	_, err = d.Exec(`INSERT INTO users (email, password_hash, is_active) VALUES (?, ?, 1)`,
		"known@bcars.org", hash)
	require.NoError(t, err)

	env.handler = handler
	return env
}

// apiRecover posts the JSON endpoint and returns the full response.
func (e *limitEnv) apiRecover(t *testing.T, email, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/recovery/request",
		strings.NewReader(`{"email":"`+email+`"}`))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = remoteAddr
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, r)
	return w
}

// uiRecover posts the admin UI form and returns the full response.
func (e *limitEnv) uiRecover(t *testing.T, email, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"email": {email}}
	r := httptest.NewRequest(http.MethodPost, "/forgot-password", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = remoteAddr
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, r)
	return w
}

// TestRecoveryIsBoundedPerSource proves the endpoint stops returning success
// once a caller exceeds the bound.
func TestRecoveryIsBoundedPerSource(t *testing.T) {
	e := setupLimitEnv(t)
	const addr = "203.0.113.7:41234"

	for i := 0; i < ratelimit.RecoveryRule.MaxPerSource; i++ {
		w := e.apiRecover(t, "known@bcars.org", addr)
		require.Less(t, w.Code, 300, "attempt %d must succeed", i+1)
	}

	w := e.apiRecover(t, "known@bcars.org", addr)
	assert.Equal(t, http.StatusTooManyRequests, w.Code,
		"an unbounded recovery endpoint is unlimited recovery mail to any known address")
}

// TestRecoveryLimitIsNotAnEnumerationOracle is the central property. At the
// same request count, a known and an unknown address must be refused
// identically — same status, same body. If the limiter consulted existence,
// this is where it would show.
func TestRecoveryLimitIsNotAnEnumerationOracle(t *testing.T) {
	e := setupLimitEnv(t)

	// Two callers, so each hits its own per-source bound independently.
	const knownAddr = "203.0.113.7:41234"
	const unknownAddr = "198.51.100.4:41234"

	exhaust := func(email, addr string) {
		for i := 0; i < ratelimit.RecoveryRule.MaxPerSource; i++ {
			require.Less(t, e.apiRecover(t, email, addr).Code, 300)
		}
	}
	exhaust("known@bcars.org", knownAddr)
	exhaust("definitely-not-a-member@example.test", unknownAddr)

	knownRes := e.apiRecover(t, "known@bcars.org", knownAddr)
	unknownRes := e.apiRecover(t, "definitely-not-a-member@example.test", unknownAddr)

	assert.Equal(t, http.StatusTooManyRequests, knownRes.Code)
	assert.Equal(t, knownRes.Code, unknownRes.Code,
		"a known and an unknown address must be refused with the same status")
	assert.Equal(t, knownRes.Body.String(), unknownRes.Body.String(),
		"the refusal body must not differ by whether the address is a member")
}

// TestRecoveryBelowTheLimitIsAlsoUniform pins the other half: below the bound,
// both addresses still get the same success response, and only the known one
// produces mail.
func TestRecoveryBelowTheLimitIsAlsoUniform(t *testing.T) {
	e := setupLimitEnv(t)

	known := e.apiRecover(t, "known@bcars.org", "203.0.113.7:41234")
	unknown := e.apiRecover(t, "nobody@example.test", "198.51.100.4:41234")

	assert.Less(t, known.Code, 300)
	assert.Equal(t, known.Code, unknown.Code,
		"below the bound the endpoint keeps its uniform success response")
	assert.Equal(t, known.Body.String(), unknown.Body.String())
}

// TestRecoveryWindowExpires proves the bound is rolling, through the endpoint.
func TestRecoveryWindowExpires(t *testing.T) {
	e := setupLimitEnv(t)
	const addr = "203.0.113.7:41234"

	for i := 0; i < ratelimit.RecoveryRule.MaxPerSource; i++ {
		require.Less(t, e.apiRecover(t, "known@bcars.org", addr).Code, 300)
	}
	require.Equal(t, http.StatusTooManyRequests, e.apiRecover(t, "known@bcars.org", addr).Code)

	*e.clock = e.clock.Add(ratelimit.RecoveryRule.Window + time.Minute)

	assert.Less(t, e.apiRecover(t, "known@bcars.org", addr).Code, 300,
		"the caller must recover once the rolling window has passed")
}

// TestRecoveryLimitIsSharedAcrossSurfaces proves the admin UI form is not an
// unbounded second door. Exhausting the bound through the API must refuse the
// UI for the same caller, and vice versa.
func TestRecoveryLimitIsSharedAcrossSurfaces(t *testing.T) {
	e := setupLimitEnv(t)
	const addr = "203.0.113.7:41234"

	for i := 0; i < ratelimit.RecoveryRule.MaxPerSource; i++ {
		require.Less(t, e.apiRecover(t, "known@bcars.org", addr).Code, 300)
	}

	w := e.uiRecover(t, "known@bcars.org", addr)
	assert.Equal(t, http.StatusTooManyRequests, w.Code,
		"the UI form must share the API's counts, or the API bound is decorative")
}

// TestUIRecoveryLimitIsUniform proves the HTML surface refuses a known and an
// unknown address with the same status and the same page.
func TestUIRecoveryLimitIsUniform(t *testing.T) {
	e := setupLimitEnv(t)

	const knownAddr = "203.0.113.7:41234"
	const unknownAddr = "198.51.100.4:41234"

	for i := 0; i < ratelimit.RecoveryRule.MaxPerSource; i++ {
		require.Less(t, e.uiRecover(t, "known@bcars.org", knownAddr).Code, 300)
		require.Less(t, e.uiRecover(t, "nobody@example.test", unknownAddr).Code, 300)
	}

	knownRes := e.uiRecover(t, "known@bcars.org", knownAddr)
	unknownRes := e.uiRecover(t, "nobody@example.test", unknownAddr)

	assert.Equal(t, http.StatusTooManyRequests, knownRes.Code)
	assert.Equal(t, knownRes.Code, unknownRes.Code)
	assert.Equal(t, knownRes.Body.String(), unknownRes.Body.String(),
		"the rendered page must not differ by whether the address is a member")
}

// TestRecoveryDenialIsAudited proves a refusal is recorded as a denial rather
// than lost among generic failures, on both surfaces.
func TestRecoveryDenialIsAudited(t *testing.T) {
	e := setupLimitEnv(t)
	const addr = "203.0.113.7:41234"

	for i := 0; i < ratelimit.RecoveryRule.MaxPerSource; i++ {
		require.Less(t, e.apiRecover(t, "known@bcars.org", addr).Code, 300)
	}
	require.Equal(t, http.StatusTooManyRequests, e.apiRecover(t, "known@bcars.org", addr).Code)
	require.Equal(t, http.StatusTooManyRequests, e.uiRecover(t, "known@bcars.org", addr).Code)

	var denials int
	require.NoError(t, e.db.QueryRow(`
		SELECT count(*) FROM audit_events
		 WHERE action = 'auth.recovery.request' AND outcome = 'denied'`).Scan(&denials))
	assert.GreaterOrEqual(t, denials, 2,
		"both the API and the UI refusal must be audited as denials")
}

// TestRecoveryMailStopsAtTheLimit proves the bound actually stops the mail,
// which is the abuse this exists to prevent.
func TestRecoveryMailStopsAtTheLimit(t *testing.T) {
	e := setupLimitEnv(t)
	const addr = "203.0.113.7:41234"

	for i := 0; i < ratelimit.RecoveryRule.MaxPerSource+5; i++ {
		e.apiRecover(t, "known@bcars.org", addr)
	}

	var links int
	require.NoError(t, e.db.QueryRow(
		`SELECT count(*) FROM email_links WHERE purpose = 'password_recovery'`).Scan(&links))
	assert.Equal(t, ratelimit.RecoveryRule.MaxPerSource, links,
		"no recovery link may be created past the bound")
}
