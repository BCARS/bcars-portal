package main

import (
	"database/sql"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/db"
)

// captureStdout runs fn with os.Stdout redirected and returns what it printed.
// bootstrapAdmin prints the invitation URL rather than returning it, and the
// database stores only the token hash, so the printed output is the only way
// a test can obtain the raw token — which is also the only way a real operator
// obtains it.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)

	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fn()
	require.NoError(t, w.Close())
	return <-done
}

// newMigratedDBAt opens and migrates a database at an explicit path.
func newMigratedDBAt(t *testing.T, path string) *sql.DB {
	t.Helper()
	d, err := db.Open(path)
	require.NoError(t, err)
	require.NoError(t, db.Migrate(d))
	return d
}

// openExisting opens a database without migrating it.
func openExisting(t *testing.T, path string) *sql.DB {
	t.Helper()
	d, err := db.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	return d
}
