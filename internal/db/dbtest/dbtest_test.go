package dbtest_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/db"
	"github.com/bcars/bcars-portal/internal/db/dbtest"
)

// dumpAll returns every row of sqlite_master plus the goose version rows, which
// together cover the schema, the indexes, the triggers and the migration
// bookkeeping.
func dumpAll(t *testing.T, d *sql.DB) []string {
	t.Helper()
	var out []string
	rows, err := d.Query(`SELECT type, name, COALESCE(sql, '') FROM sqlite_master ORDER BY type, name`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var typ, name, ddl string
		require.NoError(t, rows.Scan(&typ, &name, &ddl))
		out = append(out, typ+"|"+name+"|"+ddl)
	}
	require.NoError(t, rows.Err())
	return out
}

// freshMigrated builds a database the slow way: open, then run the real chain.
func freshMigrated(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "fresh.db"))
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	require.NoError(t, db.Migrate(d))
	return d
}

// TestOpenMatchesFreshMigration is the guard on the whole optimisation: the
// copied template must be indistinguishable from running goose.Up directly,
// or every test using dbtest.Open is asserting against the wrong schema.
func TestOpenMatchesFreshMigration(t *testing.T) {
	require.Equal(t, dumpAll(t, freshMigrated(t)), dumpAll(t, dbtest.Open(t)))
}

// TestOpenAppliedEveryMigration pins the goose bookkeeping, so a template built
// from a partial chain cannot pass as a complete one.
func TestOpenAppliedEveryMigration(t *testing.T) {
	fresh, tpl := freshMigrated(t), dbtest.Open(t)

	var wantMax, gotMax int64
	require.NoError(t, fresh.QueryRow(`SELECT MAX(version_id) FROM goose_db_version`).Scan(&wantMax))
	require.NoError(t, tpl.QueryRow(`SELECT MAX(version_id) FROM goose_db_version`).Scan(&gotMax))
	require.Equal(t, wantMax, gotMax)

	var wantN, gotN int
	require.NoError(t, fresh.QueryRow(`SELECT COUNT(*) FROM goose_db_version WHERE is_applied`).Scan(&wantN))
	require.NoError(t, tpl.QueryRow(`SELECT COUNT(*) FROM goose_db_version WHERE is_applied`).Scan(&gotN))
	require.Equal(t, wantN, gotN)
}

// TestOpenEnforcesForeignKeys guards the PRAGMAs: the copy is opened through
// db.Open, so foreign keys must still be on. A template opened any other way
// would let tests insert rows the production schema rejects.
func TestOpenEnforcesForeignKeys(t *testing.T) {
	d := dbtest.Open(t)
	var fk int
	require.NoError(t, d.QueryRow(`PRAGMA foreign_keys`).Scan(&fk))
	require.Equal(t, 1, fk, "foreign_keys must be ON")
}

// TestOpenIsolatesDatabases proves the copies are independent: a write in one
// test database must not be visible in another.
func TestOpenIsolatesDatabases(t *testing.T) {
	a, b := dbtest.Open(t), dbtest.Open(t)

	_, err := a.Exec(`INSERT INTO users (email) VALUES ('isolation@example.test')`)
	require.NoError(t, err)

	var n int
	require.NoError(t, b.QueryRow(`SELECT COUNT(*) FROM users WHERE email = 'isolation@example.test'`).Scan(&n))
	require.Equal(t, 0, n, "databases handed to different callers must not share state")
}
