package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createMember returns the id of a member created through the API.
func createMember(t *testing.T, env *authzEnv, cookie *http.Cookie, name string) int64 {
	t.Helper()
	resp := env.do(t, http.MethodPost, "/api/v1/members", cookie,
		`{"display_name":"`+name+`","sort_name":"`+name+`","base_type":"full"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode, readAll(t, resp))

	var body struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.NotZero(t, body.ID)
	return body.ID
}

// TestACSARESSharingReadBack is the regression for bcars-portal-fmc.24: the
// preference could be set and never read back, so an officer had no way to
// confirm a member's current ACS/ARES sharing status through the API.
func TestACSARESSharingReadBack(t *testing.T) {
	env := setupAuthzTest(t, "administrator")
	cookie := env.signIn(t)
	id := createMember(t, env, cookie, "Sharing Member")

	path := "/api/v1/members/" + itoa(id) + "/acs-ares-sharing"

	resp := env.do(t, http.MethodPost, path, cookie,
		`{"participates":true,"reason":"asked at a meeting"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode, readAll(t, resp))

	resp = env.do(t, http.MethodGet, path, cookie, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, readAll(t, resp))

	var got struct {
		PersonID     int64  `json:"person_id"`
		Participates bool   `json:"participates"`
		Source       string `json:"source"`
		Reason       string `json:"reason"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, id, got.PersonID)
	assert.True(t, got.Participates)
	assert.Equal(t, "officer", got.Source)
	assert.Equal(t, "asked at a meeting", got.Reason)
}

func TestACSARESSharingUnsetIsNotFound(t *testing.T) {
	env := setupAuthzTest(t, "administrator")
	cookie := env.signIn(t)
	id := createMember(t, env, cookie, "No Preference")

	resp := env.do(t, http.MethodGet, "/api/v1/members/"+itoa(id)+"/acs-ares-sharing", cookie, "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode,
		"no preference on file must be distinguishable from declining to participate")
}

func TestACSARESSharingRequiresMemberRead(t *testing.T) {
	env := setupAuthzTest(t, "member") // holds only session.self.read
	cookie := env.signIn(t)

	resp := env.do(t, http.MethodGet, "/api/v1/members/1/acs-ares-sharing", cookie, "")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
