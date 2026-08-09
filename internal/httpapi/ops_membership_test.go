package httpapi_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newHonoraryGrantViaAPI creates a member and an honorary grant on its
// membership, returning the grant id and version.
func newHonoraryGrantViaAPI(t *testing.T, env *authzEnv, cookie *http.Cookie) (grantID, version int64) {
	t.Helper()

	resp := env.do(t, http.MethodPost, "/api/v1/members", cookie,
		`{"display_name":"Grace Hopper","sort_name":"Hopper, Grace","base_type":"full"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var membershipID int64
	require.NoError(t, env.db.QueryRow(`SELECT id FROM memberships ORDER BY id DESC LIMIT 1`).
		Scan(&membershipID))

	resp = env.do(t, http.MethodPost,
		fmt.Sprintf("/api/v1/memberships/%d/honorary-grants", membershipID), cookie,
		`{"grant_type":"honorary","is_lifetime":true,"reason":"Outstanding service"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var body struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.NotZero(t, body.ID)

	require.NoError(t, env.db.QueryRow(`SELECT version FROM honorary_grants WHERE id = ?`, body.ID).
		Scan(&version))
	return body.ID, version
}

func TestHonoraryGrantUpdate(t *testing.T) {
	env := setupAuthzTest(t, "president")
	cookie := env.signIn(t)
	grantID, version := newHonoraryGrantViaAPI(t, env, cookie)

	resp := env.do(t, http.MethodPatch, fmt.Sprintf("/api/v1/honorary-grants/%d", grantID), cookie,
		fmt.Sprintf(`{"reason":"Corrected citation","ends_on":"2027-06-30","version":%d}`, version))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		ID         int64  `json:"id"`
		Reason     string `json:"reason"`
		EndsOn     string `json:"ends_on"`
		IsLifetime bool   `json:"is_lifetime"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, grantID, body.ID)
	assert.Equal(t, "Corrected citation", body.Reason)
	assert.False(t, body.IsLifetime, "an end date converts a lifetime grant to a term grant")

	events := env.auditEvents(t, "honorary.grant.update")
	require.Len(t, events, 1)
	assert.Equal(t, "success", events[0].Outcome)
	assert.Equal(t, "honorary_grant", events[0].ResourceKind.String)
	assert.Equal(t, grantID, events[0].ResourceID.Int64)
}

func TestHonoraryGrantUpdateVersionConflict(t *testing.T) {
	env := setupAuthzTest(t, "president")
	cookie := env.signIn(t)
	grantID, version := newHonoraryGrantViaAPI(t, env, cookie)

	resp := env.do(t, http.MethodPatch, fmt.Sprintf("/api/v1/honorary-grants/%d", grantID), cookie,
		fmt.Sprintf(`{"reason":"Stale write","version":%d}`, version+1))
	assert.Equal(t, http.StatusPreconditionFailed, resp.StatusCode)
}

func TestHonoraryGrantUpdateNotFound(t *testing.T) {
	env := setupAuthzTest(t, "president")
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodPatch, "/api/v1/honorary-grants/999", cookie,
		`{"reason":"No such grant","version":1}`)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestHonoraryGrantUpdateRequiresCapability(t *testing.T) {
	admin := setupAuthzTest(t, "administrator")
	adminCookie := admin.signIn(t)
	grantID, version := newHonoraryGrantViaAPI(t, admin, adminCookie)

	// "webmaster" holds no member operations at all.
	env := setupAuthzTest(t, "webmaster")
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodPatch, fmt.Sprintf("/api/v1/honorary-grants/%d", grantID), cookie,
		fmt.Sprintf(`{"reason":"Not allowed","version":%d}`, version))
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	resp = env.do(t, http.MethodPost, fmt.Sprintf("/api/v1/honorary-grants/%d/expire", grantID), cookie,
		fmt.Sprintf(`{"version":%d}`, version))
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestHonoraryGrantExpire(t *testing.T) {
	env := setupAuthzTest(t, "president")
	cookie := env.signIn(t)
	grantID, version := newHonoraryGrantViaAPI(t, env, cookie)

	resp := env.do(t, http.MethodPost, fmt.Sprintf("/api/v1/honorary-grants/%d/expire", grantID), cookie,
		fmt.Sprintf(`{"version":%d}`, version))
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Empty(t, body)

	var endsOn string
	var isLifetime, newVersion int64
	require.NoError(t, env.db.QueryRow(
		`SELECT ends_on, is_lifetime, version FROM honorary_grants WHERE id = ?`, grantID).
		Scan(&endsOn, &isLifetime, &newVersion))
	assert.NotEmpty(t, endsOn)
	assert.Equal(t, int64(0), isLifetime)
	assert.Equal(t, version+1, newVersion)

	events := env.auditEvents(t, "honorary.grant.expire")
	require.Len(t, events, 1)
	assert.Equal(t, "success", events[0].Outcome)
	assert.Equal(t, grantID, events[0].ResourceID.Int64)
}

func TestHonoraryGrantExpireVersionConflict(t *testing.T) {
	env := setupAuthzTest(t, "president")
	cookie := env.signIn(t)
	grantID, version := newHonoraryGrantViaAPI(t, env, cookie)

	resp := env.do(t, http.MethodPost, fmt.Sprintf("/api/v1/honorary-grants/%d/expire", grantID), cookie,
		fmt.Sprintf(`{"version":%d}`, version+1))
	assert.Equal(t, http.StatusPreconditionFailed, resp.StatusCode)
}

func TestHonoraryGrantExpireNotFound(t *testing.T) {
	env := setupAuthzTest(t, "president")
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodPost, "/api/v1/honorary-grants/999/expire", cookie, `{"version":1}`)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestHonoraryGrantRevoke(t *testing.T) {
	env := setupAuthzTest(t, "president")
	cookie := env.signIn(t)
	grantID, version := newHonoraryGrantViaAPI(t, env, cookie)

	resp := env.do(t, http.MethodPost, fmt.Sprintf("/api/v1/honorary-grants/%d/revoke", grantID), cookie,
		fmt.Sprintf(`{"reason":"Revoked by board","version":%d}`, version))
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Empty(t, body)

	var revokedAt, revokeReason string
	var newVersion int64
	require.NoError(t, env.db.QueryRow(
		`SELECT revoked_at, revoke_reason, version FROM honorary_grants WHERE id = ?`, grantID).
		Scan(&revokedAt, &revokeReason, &newVersion))
	assert.NotEmpty(t, revokedAt)
	assert.Equal(t, "Revoked by board", revokeReason)
	assert.Equal(t, version+1, newVersion)

	events := env.auditEvents(t, "honorary.grant.revoke")
	require.Len(t, events, 1)
	assert.Equal(t, "success", events[0].Outcome)
	assert.Equal(t, "honorary_grant", events[0].ResourceKind.String)
	assert.Equal(t, grantID, events[0].ResourceID.Int64)
}

func TestHonoraryGrantRevokeVersionConflict(t *testing.T) {
	env := setupAuthzTest(t, "president")
	cookie := env.signIn(t)
	grantID, version := newHonoraryGrantViaAPI(t, env, cookie)

	resp := env.do(t, http.MethodPost, fmt.Sprintf("/api/v1/honorary-grants/%d/revoke", grantID), cookie,
		fmt.Sprintf(`{"reason":"Stale revoke","version":%d}`, version+1))
	assert.Equal(t, http.StatusPreconditionFailed, resp.StatusCode)

	// The conflict must be a true no-op, not a 412 over a completed write.
	var revokedAt, revokeReason sql.NullString
	var newVersion int64
	require.NoError(t, env.db.QueryRow(
		`SELECT revoked_at, revoke_reason, version FROM honorary_grants WHERE id = ?`, grantID).
		Scan(&revokedAt, &revokeReason, &newVersion))
	assert.False(t, revokedAt.Valid)
	assert.False(t, revokeReason.Valid)
	assert.Equal(t, version, newVersion)
}

func TestHonoraryGrantRevokeNotFound(t *testing.T) {
	env := setupAuthzTest(t, "president")
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodPost, "/api/v1/honorary-grants/999/revoke", cookie,
		`{"reason":"No such grant","version":1}`)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}
