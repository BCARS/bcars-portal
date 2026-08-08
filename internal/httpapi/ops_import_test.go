package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/authn"
	"github.com/bcars/bcars-portal/internal/httpapi"
)

// setupImportTest creates a test server with import endpoints wired and a
// seeded admin user with import.upload + import.commit capabilities.
func setupImportTest(t *testing.T) *httptest.Server {
	t.Helper()
	d := openTestDB(t)

	cookieName := "bcars_session"
	store := authn.NewSessionStore(d, authn.SessionConfig{
		CookieName: cookieName,
		TTL:        1 * time.Hour,
	})
	authSvc := authn.NewAuthService(d, store, nil)

	handler, api := httpapi.NewRouter(httpapi.Config{Version: "test", DB: d})
	capLoader := &authn.SQLCapabilityLoader{DB: d}
	wrappedHandler := authn.Middleware(store, capLoader, cookieName)(handler)

	httpapi.RegisterAll(api, httpapi.Deps{
		DB:           d,
		AuthService:  authSvc,
		SessionStore: store,
		CookieName:   cookieName,
	})
	require.NoError(t, httpapi.VerifyAll(api))

	// Seed admin user.
	hash, err := authn.HashPassword("correcthorsebatterystaple", nil, authn.DefaultParams())
	require.NoError(t, err)
	_, err = d.Exec(`INSERT INTO users (email, password_hash, is_active) VALUES (?, ?, 1)`,
		"admin@bcars.org", hash)
	require.NoError(t, err)

	// Grant president role, which holds import.upload + import.commit.
	// (webmaster does not — it is the technical role and has no member ops.
	// These tests passed under webmaster only because capabilities were not
	// enforced; see bcars-portal-fmc.1.)
	_, err = d.Exec(`INSERT INTO user_role_grants (user_id, role_code, granted_by, granted_at) VALUES (1, 'president', 1, datetime('now'))`)
	require.NoError(t, err)

	ts := httptest.NewServer(wrappedHandler)
	t.Cleanup(ts.Close)
	return ts
}

// signIn signs in and returns the session cookie.
func signIn(t *testing.T, ts *httptest.Server) *http.Cookie {
	t.Helper()
	body := `{"email":"admin@bcars.org","password":"correcthorsebatterystaple"}`
	resp, err := ts.Client().Post(ts.URL+"/api/v1/sessions", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	for _, c := range resp.Cookies() {
		if c.Name == "bcars_session" {
			return c
		}
	}
	t.Fatal("no session cookie returned")
	return nil
}

// uploadCSV uploads the synthetic CSV fixture and returns the response body.
func uploadCSV(t *testing.T, ts *httptest.Server, cookie *http.Cookie, idemKey string) map[string]interface{} {
	t.Helper()

	csvData, err := os.ReadFile("../../fixtures/synthetic/groupsio_contact.csv")
	require.NoError(t, err)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", "groupsio_contact.csv")
	require.NoError(t, err)
	_, err = fw.Write(csvData)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/imports", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Idempotency-Key", idemKey)
	req.AddCookie(cookie)

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "upload response: %s", string(respBody))

	var result map[string]interface{}
	require.NoError(t, json.Unmarshal(respBody, &result))
	return result
}

func TestImportUploadAndList(t *testing.T) {
	ts := setupImportTest(t)
	cookie := signIn(t, ts)

	result := uploadCSV(t, ts, cookie, "test-upload-1")

	// Verify upload result.
	run := result["run"].(map[string]interface{})
	assert.Equal(t, "validated", run["status"])
	assert.Equal(t, "csv", run["source_kind"])
	assert.Equal(t, float64(21), result["total_rows"])
	assert.Greater(t, result["manual_rows"].(float64), float64(0))

	runID := int64(run["id"].(float64))

	// List imports.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/imports", nil)
	req.AddCookie(cookie)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var listResult struct {
		Data []map[string]interface{} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&listResult))
	assert.Len(t, listResult.Data, 1)

	// Get single import.
	req, _ = http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/v1/imports/%d", ts.URL, runID), nil)
	req.AddCookie(cookie)
	resp, err = ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestImportRowsAndDecision(t *testing.T) {
	ts := setupImportTest(t)
	cookie := signIn(t, ts)

	result := uploadCSV(t, ts, cookie, "test-rows-1")
	run := result["run"].(map[string]interface{})
	runID := int64(run["id"].(float64))

	// List rows.
	req, _ := http.NewRequest(http.MethodGet,
		fmt.Sprintf("%s/api/v1/imports/%d/rows?limit=50", ts.URL, runID), nil)
	req.AddCookie(cookie)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var rowsList struct {
		Data []struct {
			ID             int64  `json:"id"`
			RequiresManual bool   `json:"requires_manual"`
			ManualReason   string `json:"manual_reason"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rowsList))
	assert.Equal(t, 21, len(rowsList.Data))

	// Find a manual row.
	var manualRowID int64
	for _, r := range rowsList.Data {
		if r.RequiresManual {
			manualRowID = r.ID
			break
		}
	}
	require.NotZero(t, manualRowID, "should have at least one manual row")

	// Record a decision.
	decBody := `{"action":"skip"}`
	req, _ = http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/api/v1/imports/%d/rows/%d/decisions", ts.URL, runID, manualRowID),
		strings.NewReader(decBody))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	resp, err = ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestImportPreviewAndCommit(t *testing.T) {
	ts := setupImportTest(t)
	cookie := signIn(t, ts)

	// Upload a simple CSV with no manual rows.
	csvData := "Contact Name,Call Sign,Current Until,Note,Membership Type,Class,Phone,Email,Street Address,City,Postal Code,State/Province,Volunteer Examiner\nAlice Test,KA1AAA,12/31/2026,,Full,General,555-111-1111,alice@example.invalid,1 Main,Butler,16001,PA,false\n"

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("file", "simple.csv")
	_, _ = fw.Write([]byte(csvData))
	w.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/imports", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Idempotency-Key", "commit-test-1")
	req.AddCookie(cookie)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var uploadResult struct {
		Run struct {
			ID int64 `json:"id"`
		} `json:"run"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&uploadResult))
	runID := uploadResult.Run.ID

	// Preview.
	req, _ = http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/api/v1/imports/%d/preview", ts.URL, runID), nil)
	req.AddCookie(cookie)
	resp, err = ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var preview struct {
		Ready   bool `json:"ready"`
		Creates int  `json:"creates"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&preview))
	assert.True(t, preview.Ready)
	assert.Equal(t, 1, preview.Creates)

	// Commit.
	req, _ = http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/api/v1/imports/%d/commit", ts.URL, runID), nil)
	req.AddCookie(cookie)
	resp, err = ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var commitResult struct {
		Created int `json:"created"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&commitResult))
	assert.Equal(t, 1, commitResult.Created)

	// Idempotent retry — should return same result.
	req, _ = http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/api/v1/imports/%d/commit", ts.URL, runID), nil)
	req.AddCookie(cookie)
	resp, err = ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestImportDiscard(t *testing.T) {
	ts := setupImportTest(t)
	cookie := signIn(t, ts)

	csvData := "Contact Name,Call Sign,Current Until,Note,Membership Type,Class,Phone,Email,Street Address,City,Postal Code,State/Province,Volunteer Examiner\nDiscard Test,KA1DIS,12/31/2026,,Full,General,555-555-5555,dis@example.invalid,5 Main,Butler,16001,PA,false\n"

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("file", "discard.csv")
	_, _ = fw.Write([]byte(csvData))
	w.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/imports", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Idempotency-Key", "discard-test-1")
	req.AddCookie(cookie)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var uploadResult struct {
		Run struct {
			ID int64 `json:"id"`
		} `json:"run"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&uploadResult))

	// Discard.
	req, _ = http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/api/v1/imports/%d/discard", ts.URL, uploadResult.Run.ID), nil)
	req.AddCookie(cookie)
	resp, err = ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	// Verify status is discarded.
	req, _ = http.NewRequest(http.MethodGet,
		fmt.Sprintf("%s/api/v1/imports/%d", ts.URL, uploadResult.Run.ID), nil)
	req.AddCookie(cookie)
	resp, err = ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	var getResult struct {
		Status string `json:"status"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&getResult))
	assert.Equal(t, "discarded", getResult.Status)
}

func TestImportCommitRefusesUnresolved(t *testing.T) {
	ts := setupImportTest(t)
	cookie := signIn(t, ts)

	// Upload fixture with manual rows.
	result := uploadCSV(t, ts, cookie, "unresolved-test-1")
	run := result["run"].(map[string]interface{})
	runID := int64(run["id"].(float64))

	// Preview (transitions to previewed).
	req, _ := http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/api/v1/imports/%d/preview", ts.URL, runID), nil)
	req.AddCookie(cookie)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var preview struct {
		Ready bool `json:"ready"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&preview))
	assert.False(t, preview.Ready, "should not be ready with unresolved manual rows")

	// Commit should fail.
	req, _ = http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/api/v1/imports/%d/commit", ts.URL, runID), nil)
	req.AddCookie(cookie)
	resp, err = ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
}

func TestImportAnonymousRejected(t *testing.T) {
	ts := setupImportTest(t)

	// Try to list imports without auth.
	resp, err := ts.Client().Get(ts.URL + "/api/v1/imports")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}
