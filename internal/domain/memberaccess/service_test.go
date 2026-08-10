package memberaccess

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/db"
	"github.com/bcars/bcars-portal/internal/domain/authz"
)

// A file-backed database, not ":memory:".
//
// An in-memory SQLite database belongs to ONE connection, so a pooled second
// connection sees an empty schema. A concurrency test against ":memory:" fails
// with "no such table" and proves nothing about the constraint under test.
func openConcurrentTestDB(t *testing.T) *Service {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "concurrency.db"))
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	require.NoError(t, db.Migrate(d))

	_, err = d.Exec(`INSERT INTO persons (display_name, sort_name) VALUES ('Contested Record', 'contested')`)
	require.NoError(t, err)
	_, err = d.Exec(`INSERT INTO users (email) VALUES ('race@example.test')`)
	require.NoError(t, err)
	return NewService(d)
}

// TestConcurrentGrantsProduceOneActiveGrant proves the DATABASE decides, not a
// check-then-write race in the service. Two officers clicking at the same
// moment must not both create a grant.
func TestConcurrentGrantsProduceOneActiveGrant(t *testing.T) {
	svc := openConcurrentTestDB(t)
	ctx := context.Background()
	p := &authz.Principal{UserID: 1}

	const attempts = 6
	errs := make([]error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = svc.GrantAccess(ctx, p, 1, GrantParams{PersonID: 1}, time.Now())
		}(i)
	}
	wg.Wait()

	granted := 0
	for _, err := range errs {
		if err == nil {
			granted++
		}
	}
	assert.Equal(t, 1, granted, "exactly one concurrent grant may succeed; errors: %v", errs)

	var active int
	require.NoError(t, svc.DB.QueryRow(
		`SELECT count(*) FROM member_access_grants WHERE revoked_at IS NULL`).Scan(&active))
	assert.Equal(t, 1, active)
}

// TestNormalizeEmail pins the one normalization used everywhere, because two
// spellings of one address becoming two accounts is the split this package
// exists to prevent.
func TestNormalizeEmail(t *testing.T) {
	assert.Equal(t, "member@example.test", NormalizeEmail("  Member@Example.TEST  "))
	assert.Empty(t, NormalizeEmail("   "))
}
