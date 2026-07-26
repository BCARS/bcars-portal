package authn

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/db"
)

func testDB(t *testing.T) *SessionStore {
	t.Helper()
	d, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	require.NoError(t, db.Migrate(d))

	// Insert a test user.
	_, err = d.Exec(`INSERT INTO users (email, is_active) VALUES ('test@example.com', 1)`)
	require.NoError(t, err)

	return NewSessionStore(d, SessionConfig{
		CookieName: "test_session",
		TTL:        1 * time.Hour,
	})
}

func TestSessionCreateAndGet(t *testing.T) {
	store := testDB(t)

	id, err := store.Create(1, "iphash", "TestAgent/1.0")
	require.NoError(t, err)
	assert.Len(t, id, 64, "session ID should be 32 bytes hex-encoded")

	sess, err := store.Get(id)
	require.NoError(t, err)
	assert.Equal(t, int64(1), sess.UserID)
	assert.Equal(t, "iphash", sess.IPHash)
	assert.Equal(t, "TestAgent/1.0", sess.UserAgent)
	assert.True(t, sess.ExpiresAt.After(time.Now()))
}

func TestSessionNotFound(t *testing.T) {
	store := testDB(t)

	_, err := store.Get("nonexistent-id")
	assert.ErrorIs(t, err, ErrSessionNotFound)
}

func TestSessionRevoke(t *testing.T) {
	store := testDB(t)

	id, err := store.Create(1, "", "")
	require.NoError(t, err)

	require.NoError(t, store.Revoke(id))

	_, err = store.Get(id)
	assert.ErrorIs(t, err, ErrSessionRevoked)
}

func TestSessionExpiry(t *testing.T) {
	d, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	require.NoError(t, db.Migrate(d))
	_, err = d.Exec(`INSERT INTO users (email, is_active) VALUES ('test@example.com', 1)`)
	require.NoError(t, err)

	// Create a store with a very short TTL.
	store := NewSessionStore(d, SessionConfig{TTL: 1 * time.Millisecond})

	id, err := store.Create(1, "", "")
	require.NoError(t, err)

	// Wait for expiry.
	time.Sleep(5 * time.Millisecond)

	_, err = store.Get(id)
	assert.ErrorIs(t, err, ErrSessionNotFound)
}

func TestSessionRotate(t *testing.T) {
	store := testDB(t)

	oldID, err := store.Create(1, "ip", "ua")
	require.NoError(t, err)

	newID, err := store.Rotate(oldID)
	require.NoError(t, err)
	assert.NotEqual(t, oldID, newID)

	// Old session is revoked.
	_, err = store.Get(oldID)
	assert.ErrorIs(t, err, ErrSessionRevoked)

	// New session works.
	sess, err := store.Get(newID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), sess.UserID)
	assert.Equal(t, "ip", sess.IPHash)
}

func TestRevokeAllForUser(t *testing.T) {
	store := testDB(t)

	id1, err := store.Create(1, "", "")
	require.NoError(t, err)
	id2, err := store.Create(1, "", "")
	require.NoError(t, err)

	require.NoError(t, store.RevokeAllForUser(1))

	_, err = store.Get(id1)
	assert.ErrorIs(t, err, ErrSessionRevoked)
	_, err = store.Get(id2)
	assert.ErrorIs(t, err, ErrSessionRevoked)
}

func TestSessionTouch(t *testing.T) {
	store := testDB(t)

	id, err := store.Create(1, "", "")
	require.NoError(t, err)

	sess1, err := store.Get(id)
	require.NoError(t, err)

	time.Sleep(2 * time.Millisecond)
	require.NoError(t, store.Touch(id))

	sess2, err := store.Get(id)
	require.NoError(t, err)
	assert.True(t, sess2.LastSeenAt.After(sess1.LastSeenAt) || sess2.LastSeenAt.Equal(sess1.LastSeenAt))
}
