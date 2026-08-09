//go:build demoseed

package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/authn"
)

const testPepper = "seed-demo-test-pepper-0123456789"

// TestSeedDemoRefusesDatabaseWithNonDemoUsers is the acceptance test for
// bcars-portal-fmc.11's second half: the seeding upsert overwrites an existing
// account's password, so run against a real database it would replace an
// officer's password with a word published in the source. The guard must stop
// before anything is written.
func TestSeedDemoRefusesDatabaseWithNonDemoUsers(t *testing.T) {
	d := newMigratedDB(t)
	t.Setenv(authn.PepperEnvVar, testPepper)

	_, err := d.Exec(
		`INSERT INTO users (email, password_hash, password_algo_params, is_active)
		 VALUES ('treasurer@bcars.org', 'real-officer-hash', 'argon2id', 1)`)
	require.NoError(t, err)

	err = seedDemo(d)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to seed")

	// The real account is untouched...
	var hash string
	require.NoError(t, d.QueryRow(
		`SELECT password_hash FROM users WHERE email = 'treasurer@bcars.org'`).Scan(&hash))
	assert.Equal(t, "real-officer-hash", hash)

	// ...and no demo account was created.
	var demoCount int
	require.NoError(t, d.QueryRow(
		`SELECT COUNT(*) FROM users WHERE email LIKE '%@demo.local'`).Scan(&demoCount))
	assert.Zero(t, demoCount)
}

// TestSeedDemoOnEmptyDatabase keeps the happy path honest: the guard must not
// block a throwaway database, and the seeded passwords must verify against the
// pepper the server uses (the fmc.14 fix).
func TestSeedDemoOnEmptyDatabase(t *testing.T) {
	d := newMigratedDB(t)
	t.Setenv(authn.PepperEnvVar, testPepper)

	out := captureStdout(t, func() {
		require.NoError(t, seedDemo(d))
	})
	assert.Contains(t, out, "admin@demo.local")

	for _, u := range demoUsers {
		var hash string
		require.NoError(t, d.QueryRow(
			`SELECT password_hash FROM users WHERE email = ?`, u.Email).Scan(&hash),
			"user %s must exist", u.Email)

		ok, err := authn.VerifyPassword(u.Password, hash, []byte(testPepper))
		require.NoError(t, err)
		assert.True(t, ok, "seeded password for %s must verify with the server's pepper", u.Email)

		var grants int
		require.NoError(t, d.QueryRow(
			`SELECT COUNT(*) FROM user_role_grants g JOIN users u ON u.id = g.user_id
			 WHERE u.email = ? AND g.role_code = ? AND g.revoked_at IS NULL`,
			u.Email, u.Role).Scan(&grants))
		assert.Equal(t, 1, grants, "role %s must be granted to %s", u.Role, u.Email)
	}

	// Re-seeding a database that now holds only demo users is still allowed.
	_ = captureStdout(t, func() {
		assert.NoError(t, seedDemo(d))
	})
}

// TestSeedDemoRegisteredInDemoBuild pairs with
// TestSeedDemoAbsentFromDefaultBuild in the untagged test file.
func TestSeedDemoRegisteredInDemoBuild(t *testing.T) {
	_, ok := demoCommands["seed-demo"]
	assert.True(t, ok, "demoseed builds must dispatch seed-demo")
	assert.True(t, strings.Contains(strings.Join(demoCommandUsage, "\n"), "seed-demo"),
		"help text must advertise seed-demo when it is compiled in")
}
