package httpapi_test

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/httpapi"
)

// ConfirmationLevel was declared on every operation and read by nothing, so an
// operation marked explicit-confirm was reachable exactly like one marked none
// (bcars-portal-6q6.1). These tests assert the declaration is now the
// enforcement, the same property that keeps RequiredCapability honest.

// newRequest builds a request WITHOUT the confirmation header, which the shared
// helpers set by default. Tests about the absence of confirmation need to
// construct the absence themselves.
func newRequest(t *testing.T, env *authzEnv, method, path string, cookie *http.Cookie, body string) *http.Request {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, env.ts.URL+path, rdr)
	require.NoError(t, err)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return req
}

// confirmTarget is a real explicit-confirm operation, driven through the real
// router. Batch abandonment is the one the bead names as having had no
// enforcement of any kind.
func abandonPath(batchID int64) string {
	return fmt.Sprintf("/api/v1/payment-batches/%d/abandon", batchID)
}

// TestExplicitConfirmIsRefusedWithoutConfirmation is the core assertion.
func TestExplicitConfirmIsRefusedWithoutConfirmation(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	b, _ := openBatchViaAPI(t, env, cookie, "Abandon me")

	resp := doWithHeaders(t, env, http.MethodPost, abandonPath(b.ID), cookie,
		`{"reason":"opened by mistake"}`,
		map[string]string{
			"If-Match":            fmt.Sprintf(`"%d"`, b.Version),
			httpapi.ConfirmHeader: "false",
		})
	assert.Equal(t, http.StatusPreconditionRequired, resp.StatusCode,
		"an explicit-confirm operation must be refused without confirmation")

	// And nothing happened.
	var state string
	require.NoError(t, env.db.QueryRow(
		`SELECT state FROM payment_batches WHERE id = ?`, b.ID).Scan(&state))
	assert.Equal(t, "open", state, "a refused confirmation must not change state")
}

// TestExplicitConfirmIsPermittedWithConfirmation is the other half: the control
// must not simply block everything.
func TestExplicitConfirmIsPermittedWithConfirmation(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	b, _ := openBatchViaAPI(t, env, cookie, "Abandon me")

	resp := doWithHeaders(t, env, http.MethodPost, abandonPath(b.ID), cookie,
		`{"reason":"opened by mistake"}`,
		map[string]string{
			"If-Match":            fmt.Sprintf(`"%d"`, b.Version),
			httpapi.ConfirmHeader: "true",
		})
	require.Less(t, resp.StatusCode, 300, "a confirmed request must proceed: %s", readAll(t, resp))

	var state string
	require.NoError(t, env.db.QueryRow(
		`SELECT state FROM payment_batches WHERE id = ?`, b.ID).Scan(&state))
	assert.Equal(t, "abandoned", state)
}

// TestMissingConfirmHeaderIsRefused proves an absent header is not treated as
// consent. Sending nothing is the common case for a client that never learned
// about the control.
func TestMissingConfirmHeaderIsRefused(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	b, _ := openBatchViaAPI(t, env, cookie, "Abandon me")

	// doWithHeaders defaults the header, so build the request without it.
	req := newRequest(t, env, http.MethodPost, abandonPath(b.ID), cookie,
		`{"reason":"opened by mistake"}`)
	req.Header.Set("If-Match", fmt.Sprintf(`"%d"`, b.Version))
	resp, err := env.ts.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })

	assert.Equal(t, http.StatusPreconditionRequired, resp.StatusCode,
		"an absent confirmation header is not consent")
}

// TestAmbiguousConfirmValuesAreNotConsent proves only unambiguous affirmatives
// count. A client sending "false" is stating the opposite, and treating mere
// presence as consent would make the control satisfiable by accident.
func TestAmbiguousConfirmValuesAreNotConsent(t *testing.T) {
	for _, value := range []string{"false", "no", "0", "", "maybe", "TRUE-ish"} {
		t.Run("value "+value, func(t *testing.T) {
			env := setupAuthzTest(t, "treasurer")
			cookie := env.signIn(t)
			b, _ := openBatchViaAPI(t, env, cookie, "Abandon me")

			resp := doWithHeaders(t, env, http.MethodPost, abandonPath(b.ID), cookie,
				`{"reason":"opened by mistake"}`,
				map[string]string{
					"If-Match":            fmt.Sprintf(`"%d"`, b.Version),
					httpapi.ConfirmHeader: value,
				})
			assert.Equal(t, http.StatusPreconditionRequired, resp.StatusCode)
		})
	}
}

// TestAffirmativeSpellingsAreAccepted keeps the accepted set small but usable.
func TestAffirmativeSpellingsAreAccepted(t *testing.T) {
	for _, value := range []string{"true", "TRUE", " yes ", "1"} {
		t.Run("value "+value, func(t *testing.T) {
			env := setupAuthzTest(t, "treasurer")
			cookie := env.signIn(t)
			b, _ := openBatchViaAPI(t, env, cookie, "Abandon me")

			resp := doWithHeaders(t, env, http.MethodPost, abandonPath(b.ID), cookie,
				`{"reason":"opened by mistake"}`,
				map[string]string{
					"If-Match":            fmt.Sprintf(`"%d"`, b.Version),
					httpapi.ConfirmHeader: value,
				})
			assert.Less(t, resp.StatusCode, 300, readAll(t, resp))
		})
	}
}

// TestConfirmNoneOperationsAreUnaffected proves the control applies only where
// declared. A blanket requirement would be a different bug.
func TestConfirmNoneOperationsAreUnaffected(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)

	req := newRequest(t, env, http.MethodGet, "/api/v1/members", cookie, "")
	resp, err := env.ts.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })

	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"an operation declaring ConfirmNone must not require the header")
}

// TestConfirmationIsCheckedAfterCapability proves an under-privileged caller
// gets the authorization answer, not a hint about the confirmation contract.
// Leaking the order would tell an unauthorized caller which operations are
// consequential.
func TestConfirmationIsCheckedAfterCapability(t *testing.T) {
	env := setupAuthzTest(t, "acs_coordinator") // holds no treasury capability
	cookie := env.signIn(t)

	req := newRequest(t, env, http.MethodPost, abandonPath(1), cookie, `{"reason":"x"}`)
	resp, err := env.ts.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })

	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"an unauthorized caller must be refused for lacking the capability, not for lacking confirmation")
}

// TestRefusedConfirmationIsAudited proves the denial is recorded, so repeated
// unconfirmed attempts are visible rather than silent.
func TestRefusedConfirmationIsAudited(t *testing.T) {
	env := setupAuthzTest(t, "treasurer")
	cookie := env.signIn(t)
	b, _ := openBatchViaAPI(t, env, cookie, "Abandon me")

	doWithHeaders(t, env, http.MethodPost, abandonPath(b.ID), cookie,
		`{"reason":"opened by mistake"}`,
		map[string]string{
			"If-Match":            fmt.Sprintf(`"%d"`, b.Version),
			httpapi.ConfirmHeader: "false",
		})

	var n int
	require.NoError(t, env.db.QueryRow(`
		SELECT count(*) FROM audit_events
		 WHERE outcome = 'denied' AND reason_code = 'missing_confirmation'`).Scan(&n))
	assert.Equal(t, 1, n, "a refused confirmation must be audited as a denial")
}

// TestEveryOperationDeclaresAKnownConfirmationLevel is the catalog-wide guard.
// Register panics on an unknown level, so this asserts the shipped set is
// exactly the enforceable one — in particular that "recent-auth", which was
// declared and inert, is gone rather than merely unused.
func TestEveryOperationDeclaresAKnownConfirmationLevel(t *testing.T) {
	for opID, meta := range httpapi.AllMeta() {
		assert.Contains(t,
			[]string{httpapi.ConfirmNone, httpapi.ConfirmExplicit},
			meta.ConfirmationLevel,
			"operation %s declares an unenforceable confirmation level", opID)
		assert.NotEqual(t, "recent-auth", meta.ConfirmationLevel,
			"operation %s declares a level with no implementation", opID)
	}
}
