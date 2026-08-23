package main

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/authn"
	"github.com/bcars/bcars-portal/internal/db/dbtest"
)

func newMigratedDB(t *testing.T) *sql.DB {
	t.Helper()
	d := dbtest.Open(t)
	return d
}

// invitationToken pulls the raw token back out of the printed URL. bootstrapAdmin
// prints it rather than returning it, and the database stores only the hash.
func invitationToken(t *testing.T, out string) string {
	t.Helper()
	idx := strings.Index(out, "token=")
	require.GreaterOrEqual(t, idx, 0, "bootstrap output must contain the invitation URL")
	rest := out[idx+len("token="):]
	if nl := strings.IndexAny(rest, "\r\n"); nl >= 0 {
		rest = rest[:nl]
	}
	token := strings.TrimSpace(rest)
	require.NotEmpty(t, token)
	return token
}

// TestBootstrapAdminProducesWorkingAdministrator is the acceptance test for
// bcars-portal-fmc.3: a fresh installation must be able to reach a functional
// administrator through the documented path. Before this change bootstrap
// created an invitation whose consumption produced an account with no
// capabilities at all.
func TestBootstrapAdminProducesWorkingAdministrator(t *testing.T) {
	d := newMigratedDB(t)

	out := captureStdout(t, func() {
		require.NoError(t, bootstrapAdmin(d, "admin@bcars.org", false, "http://localhost:8080"))
	})
	token := invitationToken(t, out)

	links := authn.NewEmailLinkService(d, nil, authn.EmailLinkConfig{
		BaseURL: "http://localhost:8080",
	})
	link, err := links.ConsumeLink(token)
	require.NoError(t, err)
	assert.Equal(t, authn.PurposeInvitation, link.Purpose)
	require.Equal(t, "administrator", link.IntendedRoleCode,
		"the bootstrap invitation must carry the role it confers")

	store := authn.NewSessionStore(d, authn.SessionConfig{CookieName: "s"})
	auth := authn.NewAuthService(d, store, nil)
	userID, err := auth.CreateUserFromInvitation(context.Background(), link, "correcthorsebatterystaple")
	require.NoError(t, err)

	// The whole point: the resulting principal must actually hold
	// administrator capabilities.
	caps, err := (&authn.SQLCapabilityLoader{DB: d}).EffectiveCapabilities(userID)
	require.NoError(t, err)
	for _, code := range []string{"role.grant", "member.read", "audit.read", "import.commit", "system.admin"} {
		assert.Contains(t, caps, code, "bootstrapped administrator must hold %s", code)
	}

	var role string
	var grantedBy int64
	require.NoError(t, d.QueryRow(
		`SELECT role_code, granted_by FROM user_role_grants WHERE user_id = ? AND revoked_at IS NULL`,
		userID).Scan(&role, &grantedBy))
	assert.Equal(t, "administrator", role)
	assert.Equal(t, userID, grantedBy, "bootstrap self-attributes: no prior user exists")
}

// TestOrdinaryInvitationGrantsNoRole is the other half of the contract — the
// role-granting path must not leak into normal invitations.
func TestOrdinaryInvitationGrantsNoRole(t *testing.T) {
	d := newMigratedDB(t)

	links := authn.NewEmailLinkService(d, nil, authn.EmailLinkConfig{
		BaseURL: "http://localhost:8080",
		TTL:     24 * time.Hour,
	})
	token, err := links.CreateInvitation(context.Background(), "member@bcars.org", "", false)
	require.NoError(t, err)

	link, err := links.ConsumeLink(token)
	require.NoError(t, err)
	assert.Empty(t, link.IntendedRoleCode)

	store := authn.NewSessionStore(d, authn.SessionConfig{CookieName: "s"})
	auth := authn.NewAuthService(d, store, nil)
	userID, err := auth.CreateUserFromInvitation(context.Background(), link, "correcthorsebatterystaple")
	require.NoError(t, err)

	caps, err := (&authn.SQLCapabilityLoader{DB: d}).EffectiveCapabilities(userID)
	require.NoError(t, err)
	assert.Empty(t, caps, "an ordinary invitation must confer no capabilities")

	var grants int
	require.NoError(t, d.QueryRow(
		`SELECT COUNT(*) FROM user_role_grants WHERE user_id = ?`, userID).Scan(&grants))
	assert.Zero(t, grants)
}

// TestBootstrapAdminIsAudited covers the acceptance requirement that the
// bootstrap is recorded.
func TestBootstrapAdminIsAudited(t *testing.T) {
	d := newMigratedDB(t)

	captureStdout(t, func() {
		require.NoError(t, bootstrapAdmin(d, "admin@bcars.org", false, "http://localhost:8080"))
	})

	var outcome, detail string
	require.NoError(t, d.QueryRow(
		`SELECT outcome, detail_json FROM audit_events WHERE action = 'auth.bootstrap_admin.invite'`,
	).Scan(&outcome, &detail))
	assert.Equal(t, "success", outcome)
	assert.Contains(t, detail, `"role":"administrator"`)
	assert.NotContains(t, detail, "admin@bcars.org", "the invitee address must be redacted in audit detail")
}

// TestBootstrapAdminRefusesWhenAdminExists guards the safety check, and
// TestBootstrapAdminForceIsAudited proves the override message is honest.
func TestBootstrapAdminRefusesWhenAdminExists(t *testing.T) {
	d := newMigratedDB(t)
	seedActiveAdmin(t, d)

	err := bootstrapAdmin(d, "second@bcars.org", false, "http://localhost:8080")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestBootstrapAdminForceIsAudited(t *testing.T) {
	d := newMigratedDB(t)
	seedActiveAdmin(t, d)

	captureStdout(t, func() {
		require.NoError(t, bootstrapAdmin(d, "second@bcars.org", true, "http://localhost:8080"))
	})

	var outcome, detail string
	require.NoError(t, d.QueryRow(
		`SELECT outcome, detail_json FROM audit_events WHERE action = 'auth.bootstrap_admin.force'`,
	).Scan(&outcome, &detail))
	assert.Equal(t, "success", outcome)
	assert.Contains(t, detail, `"existing_admins":1`)
}

func seedActiveAdmin(t *testing.T, d *sql.DB) {
	t.Helper()
	_, err := d.Exec(`INSERT INTO users (email, password_hash, is_active) VALUES ('existing@bcars.org', 'x', 1)`)
	require.NoError(t, err)
	_, err = d.Exec(`INSERT INTO user_role_grants (user_id, role_code, granted_by, granted_at)
		VALUES (1, 'administrator', 1, datetime('now'))`)
	require.NoError(t, err)
}
