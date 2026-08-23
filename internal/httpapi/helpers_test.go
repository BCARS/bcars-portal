package httpapi_test

import (
	"database/sql"
	"testing"

	"github.com/bcars/bcars-portal/internal/db/dbtest"
)

// openTestDB creates a SQLite database with all migrations applied.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return dbtest.Open(t)
}
