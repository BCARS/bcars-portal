// Package db provides database initialization, migration support, and shared
// repository helpers for the bcars-portal application.
package db

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"github.com/pressly/goose/v3"

	// Pure-Go SQLite driver.
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// ErrStale is returned when an optimistic-concurrency update finds zero rows
// affected, meaning another writer incremented the version first.
var ErrStale = errors.New("db: stale version — row was modified concurrently")

// Open creates (or opens) a SQLite database at path with the required PRAGMAs:
//   - foreign_keys = ON
//   - journal_mode = WAL
//   - busy_timeout = 5000
//
// Pass ":memory:" for an ephemeral in-memory database (tests).
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("db.Open: %w", err)
	}

	// SQLite PRAGMAs must be set on every connection.
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("db.Open: %s: %w", pragma, err)
		}
	}
	return db, nil
}

// Migrate runs all pending Goose migrations (embedded SQL files) against db.
func Migrate(db *sql.DB) error {
	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("db.Migrate: set dialect: %w", err)
	}
	if err := goose.Up(db, "migrations"); err != nil {
		return fmt.Errorf("db.Migrate: up: %w", err)
	}
	return nil
}

// MigrateDown rolls back all migrations. Used only in tests.
func MigrateDown(db *sql.DB) error {
	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("db.MigrateDown: set dialect: %w", err)
	}
	if err := goose.DownTo(db, "migrations", 0); err != nil {
		return fmt.Errorf("db.MigrateDown: %w", err)
	}
	return nil
}

// CheckVersion executes an UPDATE ... WHERE version=? pattern and returns
// ErrStale when zero rows were affected. Call this after Exec to implement
// optimistic concurrency control.
func CheckVersion(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("db: rows affected: %w", err)
	}
	if n == 0 {
		return ErrStale
	}
	return nil
}
