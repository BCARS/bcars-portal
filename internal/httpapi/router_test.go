package httpapi_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/db"
	"github.com/bcars/bcars-portal/internal/httpapi"
)

// openTestDB creates an in-memory SQLite database with all migrations applied.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	require.NoError(t, db.Migrate(d))
	return d
}

// TestHealthz verifies that GET /healthz returns 200 OK with no body.
func TestHealthz(t *testing.T) {
	handler, api := httpapi.NewRouter(httpapi.Config{Version: "test"})
	httpapi.RegisterAll(api, httpapi.Deps{})
	require.NoError(t, httpapi.VerifyAll(api))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestReadyzHealthy verifies readyz returns 200 with a properly migrated DB.
func TestReadyzHealthy(t *testing.T) {
	d := openTestDB(t)
	handler, api := httpapi.NewRouter(httpapi.Config{Version: "test", DB: d})
	httpapi.RegisterAll(api)
	require.NoError(t, httpapi.VerifyAll(api))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"status":"ok"`)
	assert.Contains(t, rec.Body.String(), `"foreign_keys":true`)
}

// TestReadyzNoDB verifies readyz returns 503 when no database is configured.
func TestReadyzNoDB(t *testing.T) {
	handler, api := httpapi.NewRouter(httpapi.Config{Version: "test"})
	httpapi.RegisterAll(api)
	require.NoError(t, httpapi.VerifyAll(api))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), `"unavailable"`)
}

// TestReadyzClosedDB verifies readyz returns 503 when the database connection is closed.
func TestReadyzClosedDB(t *testing.T) {
	d := openTestDB(t)
	handler, api := httpapi.NewRouter(httpapi.Config{Version: "test", DB: d})
	httpapi.RegisterAll(api)
	require.NoError(t, httpapi.VerifyAll(api))

	// Close the DB to simulate unreachable.
	d.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), `"unavailable"`)
}

// TestReadyzSchemaMismatch verifies readyz returns 503 when the migration version is wrong.
func TestReadyzSchemaMismatch(t *testing.T) {
	d := openTestDB(t)
	// Sabotage: add a fake higher migration version.
	_, err := d.Exec("INSERT INTO goose_db_version (version_id, is_applied) VALUES (999, 1)")
	require.NoError(t, err)

	handler, api := httpapi.NewRouter(httpapi.Config{Version: "test", DB: d})
	httpapi.RegisterAll(api)
	require.NoError(t, httpapi.VerifyAll(api))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), `"schema version mismatch"`)
}

// TestOpenAPIEndpoint verifies that GET /openapi.json returns a JSON document.
func TestOpenAPIEndpoint(t *testing.T) {
	handler, api := httpapi.NewRouter(httpapi.Config{Version: "test"})
	httpapi.RegisterAll(api, httpapi.Deps{})
	require.NoError(t, httpapi.VerifyAll(api))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "json")
	body := rec.Body.String()
	assert.Contains(t, body, `"openapi"`)
	assert.Contains(t, body, `"BCARS Portal API"`)
}

// TestVerifyAllCatchesRaw verifies that VerifyAll returns an error when a raw
// huma.Register call bypasses the metadata wrapper.
func TestVerifyAllCatchesRaw(t *testing.T) {
	mux := http.NewServeMux()
	cfg := huma.DefaultConfig("Test", "0.0.0")
	api := humago.NewWithPrefix(mux, "/api/v1", cfg)

	// Register via raw huma.Register — no metadata recorded.
	type In struct{}
	type Out struct{}
	huma.Register(api, huma.Operation{
		OperationID: "unguarded-op",
		Method:      http.MethodGet,
		Path:        "/unguarded",
	}, func(_ context.Context, _ *In) (*Out, error) { return nil, nil })

	err := httpapi.VerifyAll(api)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unguarded-op")
}

// --- Pagination helper tests ---

func TestEncodeCursorRoundtrip(t *testing.T) {
	cases := []string{"", "42", "somekey:value", "abc123"}
	for _, raw := range cases {
		encoded := httpapi.EncodeCursor(raw)
		decoded, err := httpapi.DecodeCursor(encoded)
		require.NoError(t, err, "raw=%q", raw)
		assert.Equal(t, raw, decoded, "raw=%q", raw)
	}
}

func TestDecodeCursorInvalid(t *testing.T) {
	_, err := httpapi.DecodeCursor("!!!not-base64!!!")
	assert.Error(t, err)
}

// --- If-Match / ETag tests ---

func TestFormatAndParseETag(t *testing.T) {
	tag := httpapi.FormatETag(42)
	assert.Equal(t, `"42"`, tag)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-Match", tag)
	v, err := httpapi.ParseIfMatch(req)
	require.NoError(t, err)
	assert.Equal(t, int64(42), v)
}

func TestParseIfMatchAbsent(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	v, err := httpapi.ParseIfMatch(req)
	require.NoError(t, err)
	assert.Equal(t, int64(0), v)
}

func TestParseIfMatchMalformed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-Match", `"not-a-number"`)
	_, err := httpapi.ParseIfMatch(req)
	assert.Error(t, err)
}

func TestWriteETag(t *testing.T) {
	rec := httptest.NewRecorder()
	httpapi.WriteETag(rec, 7)
	assert.Equal(t, `"7"`, rec.Header().Get("ETag"))
}

// --- Idempotency middleware tests ---

func TestIdempotencyMiddleware(t *testing.T) {
	var gotKey string
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotKey = httpapi.IdempotencyKeyFrom(r.Context())
	})
	handler := httpapi.IdempotencyMiddleware(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.Header.Set("Idempotency-Key", "test-key-abc")
	handler.ServeHTTP(rec, req)
	assert.Equal(t, "test-key-abc", gotKey)
}

func TestIdempotencyMiddlewareAbsent(t *testing.T) {
	var gotKey string
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotKey = httpapi.IdempotencyKeyFrom(r.Context())
	})
	handler := httpapi.IdempotencyMiddleware(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	handler.ServeHTTP(rec, req)
	assert.Equal(t, "", gotKey)
}

// --- Error code tests ---

func TestErrorCodes(t *testing.T) {
	// Verify the constants exist and are distinct.
	codes := []string{
		httpapi.ErrCodeStale,
		httpapi.ErrCodeValidation,
		httpapi.ErrCodeForbidden,
		httpapi.ErrCodeNotFoundOrForbidden,
		httpapi.ErrCodeConflict,
		httpapi.ErrCodeIdempotencyMismatch,
	}
	seen := map[string]bool{}
	for _, c := range codes {
		require.NotEmpty(t, c)
		require.False(t, seen[c], "duplicate error code: %s", c)
		seen[c] = true
	}
}
