package httpapi_test

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/db"
)

// openTestDB creates an in-memory SQLite database with all migrations applied.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	require.NoError(t, db.Migrate(d))
	return d
}
