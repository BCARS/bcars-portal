// Package dbtest provides a fast, migrated SQLite database for tests.
//
// Running the Goose migration chain per test is the dominant cost of the race
// test suite: under -race each Up() costs roughly half a second, and the suite
// opens hundreds of test databases (bcars-portal-42d). This package runs the
// chain exactly once per test binary and hands each caller a byte-for-byte copy
// of the resulting file, so every test still gets a private database with the
// real schema — including rows seeded by data migrations, which a schema-only
// snapshot would silently drop.
package dbtest

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/bcars/bcars-portal/internal/db"
)

var (
	templateOnce  sync.Once
	templateBytes []byte
	templateErr   error
)

// buildTemplate migrates a scratch database and returns its bytes.
func buildTemplate() ([]byte, error) {
	dir, err := os.MkdirTemp("", "bcars-dbtest-template")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "template.db")
	d, err := db.Open(path)
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(d); err != nil {
		d.Close()
		return nil, err
	}

	// db.Open puts the database in WAL mode, which leaves the committed schema
	// in a sidecar -wal file. Switching back to DELETE checkpoints that content
	// into the main file and removes the sidecar, so the bytes below are a
	// complete database on their own.
	if _, err := d.Exec("PRAGMA journal_mode = DELETE"); err != nil {
		d.Close()
		return nil, err
	}
	if err := d.Close(); err != nil {
		return nil, err
	}

	return os.ReadFile(path)
}

// Open returns a migrated SQLite database that is private to t, closed when t
// finishes. It is a drop-in replacement for db.Open(":memory:") followed by
// db.Migrate.
func Open(t *testing.T) *sql.DB {
	t.Helper()

	templateOnce.Do(func() { templateBytes, templateErr = buildTemplate() })
	if templateErr != nil {
		t.Fatalf("dbtest: build migrated template: %v", templateErr)
	}

	path := filepath.Join(t.TempDir(), "test.db")
	if err := os.WriteFile(path, templateBytes, 0o600); err != nil {
		t.Fatalf("dbtest: write test database: %v", err)
	}

	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("dbtest: open test database: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}
