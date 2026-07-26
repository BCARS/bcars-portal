package db

import (
	"database/sql"
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/domain/authz"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMigrationsUpDownUp(t *testing.T) {
	db := openTestDB(t)

	// Up
	require.NoError(t, Migrate(db))

	// Verify a table from each migration exists.
	for _, tbl := range []string{"persons", "import_runs", "capabilities", "role_capabilities"} {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", tbl).Scan(&name)
		require.NoError(t, err, "table %s should exist after up", tbl)
	}

	// Down
	require.NoError(t, MigrateDown(db))

	// Verify tables are gone.
	var count int
	err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'goose_%' AND name != 'sqlite_sequence'").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "all application tables should be dropped after down")

	// Up again — proves idempotent re-migration.
	require.NoError(t, Migrate(db))
}

func TestForeignKeysEnabled(t *testing.T) {
	db := openTestDB(t)
	var fk int
	require.NoError(t, db.QueryRow("PRAGMA foreign_keys").Scan(&fk))
	assert.Equal(t, 1, fk)
}

func TestWALMode(t *testing.T) {
	// WAL only works on real files, not :memory:.
	f, err := os.CreateTemp(t.TempDir(), "test-*.db")
	require.NoError(t, err)
	f.Close()

	db, err := Open(f.Name())
	require.NoError(t, err)
	defer db.Close()

	var mode string
	require.NoError(t, db.QueryRow("PRAGMA journal_mode").Scan(&mode))
	assert.Equal(t, "wal", mode)
}

// TestCapabilitySeedMatchesCatalog verifies that after migration, the DB
// capabilities table contains exactly the same codes as authz.All.
func TestCapabilitySeedMatchesCatalog(t *testing.T) {
	db := openTestDB(t)
	require.NoError(t, Migrate(db))

	rows, err := db.Query("SELECT code FROM capabilities ORDER BY code")
	require.NoError(t, err)
	defer rows.Close()

	var dbCodes []string
	for rows.Next() {
		var code string
		require.NoError(t, rows.Scan(&code))
		dbCodes = append(dbCodes, code)
	}
	require.NoError(t, rows.Err())

	goCodes := make([]string, 0, len(authz.All))
	for _, c := range authz.All {
		goCodes = append(goCodes, c.Code)
	}
	sort.Strings(goCodes)

	assert.Equal(t, goCodes, dbCodes, "DB capability codes must match authz.All")
}

// TestRoleCapabilityEdges verifies the role→capability mapping matches the
// design spec (§5). Compares against a golden structure.
func TestRoleCapabilityEdges(t *testing.T) {
	db := openTestDB(t)
	require.NoError(t, Migrate(db))

	rows, err := db.Query("SELECT role_code, capability_code FROM role_capabilities ORDER BY role_code, capability_code")
	require.NoError(t, err)
	defer rows.Close()

	edges := map[string][]string{}
	for rows.Next() {
		var role, cap string
		require.NoError(t, rows.Scan(&role, &cap))
		edges[role] = append(edges[role], cap)
	}
	require.NoError(t, rows.Err())

	// Administrator should have all capabilities.
	allCodes := make([]string, 0, len(authz.All))
	for _, c := range authz.All {
		allCodes = append(allCodes, c.Code)
	}
	sort.Strings(allCodes)
	assert.Equal(t, allCodes, edges["administrator"], "administrator should have all capabilities")

	// Webmaster
	assert.ElementsMatch(t, []string{
		"session.self.read", "system.admin", "integration.config.write",
		"audit.read", "user.invite", "role.grant",
	}, edges["webmaster"])

	// Treasurer has everything president has + treasurer notes.
	presidentSet := edges["president"]
	for _, pc := range presidentSet {
		assert.Contains(t, edges["treasurer"], pc, "treasurer should include president cap: %s", pc)
	}
	assert.Contains(t, edges["treasurer"], "notes.write.treasurer")
	assert.Contains(t, edges["treasurer"], "notes.read.treasurer")

	// member: only session.self.read
	assert.Equal(t, []string{"session.self.read"}, edges["member"])

	// acs_coordinator
	assert.ElementsMatch(t, []string{"session.self.read", "member.read"}, edges["acs_coordinator"])
}

// TestCheckVersionStale verifies the optimistic concurrency helper.
func TestCheckVersionStale(t *testing.T) {
	db := openTestDB(t)
	require.NoError(t, Migrate(db))

	// Insert a person.
	_, err := db.Exec(`INSERT INTO persons (display_name, sort_name) VALUES ('Test', 'test')`)
	require.NoError(t, err)

	// Update with correct version succeeds.
	result, err := db.Exec(`UPDATE persons SET display_name='Updated', version=version+1 WHERE id=1 AND version=1`)
	require.NoError(t, CheckVersion(result, err))

	// Update with stale version returns ErrStale.
	result, err = db.Exec(`UPDATE persons SET display_name='Stale', version=version+1 WHERE id=1 AND version=1`)
	assert.ErrorIs(t, CheckVersion(result, err), ErrStale)
}

// TestRoleSeedGoldenJSON compares the role→capability mapping against a
// checked-in golden file. If the golden file doesn't exist, it creates it.
func TestRoleSeedGoldenJSON(t *testing.T) {
	db := openTestDB(t)
	require.NoError(t, Migrate(db))

	rows, err := db.Query("SELECT role_code, capability_code FROM role_capabilities ORDER BY role_code, capability_code")
	require.NoError(t, err)
	defer rows.Close()

	edges := map[string][]string{}
	for rows.Next() {
		var role, cap string
		require.NoError(t, rows.Scan(&role, &cap))
		edges[role] = append(edges[role], cap)
	}
	require.NoError(t, rows.Err())

	actual, err := json.MarshalIndent(edges, "", "  ")
	require.NoError(t, err)

	goldenPath := "testdata/role_capabilities_golden.json"
	if _, err := os.Stat(goldenPath); os.IsNotExist(err) {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(goldenPath, actual, 0o644))
		t.Log("created golden file; re-run to verify")
		return
	}

	expected, err := os.ReadFile(goldenPath)
	require.NoError(t, err)
	assert.JSONEq(t, string(expected), string(actual), "role_capabilities drift from golden file; update with: go test -run TestRoleSeedGoldenJSON -count=1")
}
