package authn

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/db"
	"github.com/bcars/bcars-portal/internal/mail"
)

type testEnv struct {
	db       *sql.DB
	auth     *AuthService
	sessions *SessionStore
	links    *EmailLinkService
	mailer   *mail.FilelogSender
}

func setupEnv(t *testing.T) *testEnv {
	t.Helper()
	d, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	require.NoError(t, db.Migrate(d))

	pepper := []byte("test-pepper-32-bytes-exactly!!!!!")
	store := NewSessionStore(d, SessionConfig{
		CookieName: "test_session",
		TTL:        1 * time.Hour,
	})

	mailDir := t.TempDir()
	mailer := mail.NewFilelogSender(mailDir)

	links := NewEmailLinkService(d, mailer, EmailLinkConfig{
		BaseURL: "https://portal.test",
		TTL:     24 * time.Hour,
	})

	// Create a test user with a known password.
	hash, err := HashPassword("correct-password", pepper, DefaultParams())
	require.NoError(t, err)
	_, err = d.Exec(
		`INSERT INTO users (email, password_hash, is_active) VALUES (?, ?, 1)`,
		"officer@bcars.org", hash,
	)
	require.NoError(t, err)

	auth := NewAuthService(d, store, pepper)

	return &testEnv{
		db:       d,
		auth:     auth,
		sessions: store,
		links:    links,
		mailer:   mailer,
	}
}

func TestSignInSuccess(t *testing.T) {
	env := setupEnv(t)

	sessionID, err := env.auth.SignIn("officer@bcars.org", "correct-password", "ip", "ua")
	require.NoError(t, err)
	assert.NotEmpty(t, sessionID)

	sess, err := env.sessions.Get(sessionID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), sess.UserID)
}

func TestSignInWrongPassword(t *testing.T) {
	env := setupEnv(t)

	_, err := env.auth.SignIn("officer@bcars.org", "wrong-password", "ip", "ua")
	assert.ErrorIs(t, err, ErrInvalidCredentials)
}

func TestSignInNonexistentEmail(t *testing.T) {
	env := setupEnv(t)

	_, err := env.auth.SignIn("nobody@bcars.org", "any-password", "ip", "ua")
	assert.ErrorIs(t, err, ErrInvalidCredentials)
}

func TestSignInLockout(t *testing.T) {
	env := setupEnv(t)

	// Fail 10 times to trigger lockout.
	for i := 0; i < maxFailedAttempts; i++ {
		_, err := env.auth.SignIn("officer@bcars.org", "wrong", "ip", "ua")
		assert.ErrorIs(t, err, ErrInvalidCredentials)
	}

	// 11th attempt should be locked.
	_, err := env.auth.SignIn("officer@bcars.org", "correct-password", "ip", "ua")
	assert.ErrorIs(t, err, ErrAccountLocked)
}

func TestSignOut(t *testing.T) {
	env := setupEnv(t)

	sessionID, err := env.auth.SignIn("officer@bcars.org", "correct-password", "ip", "ua")
	require.NoError(t, err)

	require.NoError(t, env.auth.SignOut(sessionID))

	_, err = env.sessions.Get(sessionID)
	assert.ErrorIs(t, err, ErrSessionRevoked)
}

func TestRecoveryFlow(t *testing.T) {
	env := setupEnv(t)
	ctx := context.Background()

	// Request recovery.
	require.NoError(t, env.links.RequestRecovery(ctx, "officer@bcars.org", "ip"))

	// Read the filelog to get the token.
	entries, err := env.mailer.ReadAll()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "password_recovery", entries[0].Message.TemplateID)

	token := entries[0].Message.Payload["token"]
	require.NotEmpty(t, token)

	// Consume the token.
	link, err := env.links.ConsumeLink(token)
	require.NoError(t, err)
	assert.Equal(t, PurposeRecovery, link.Purpose)
	assert.Equal(t, "officer@bcars.org", link.Email)
	require.NotNil(t, link.UserID)
	assert.Equal(t, int64(1), *link.UserID)

	// Second consume fails.
	_, err = env.links.ConsumeLink(token)
	assert.ErrorIs(t, err, ErrLinkConsumed)
}

func TestRecoveryNonexistentEmail(t *testing.T) {
	env := setupEnv(t)

	// Should succeed silently (no email enumeration).
	require.NoError(t, env.links.RequestRecovery(context.Background(), "nobody@bcars.org", "ip"))

	// No email sent.
	entries, err := env.mailer.ReadAll()
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestInvitationFlow(t *testing.T) {
	env := setupEnv(t)

	token, err := env.links.CreateInvitation(context.Background(), "new-officer@bcars.org", false)
	require.NoError(t, err)
	require.NotEmpty(t, token)

	// Consume invitation.
	link, err := env.links.ConsumeLink(token)
	require.NoError(t, err)
	assert.Equal(t, PurposeInvitation, link.Purpose)
	assert.Equal(t, "new-officer@bcars.org", link.Email)
	assert.Nil(t, link.UserID, "invitation has no pre-existing user")

	// Second consume fails.
	_, err = env.links.ConsumeLink(token)
	assert.ErrorIs(t, err, ErrLinkConsumed)
}

func TestInvitationWithEmail(t *testing.T) {
	env := setupEnv(t)

	_, err := env.links.CreateInvitation(context.Background(), "new@bcars.org", true)
	require.NoError(t, err)

	entries, err := env.mailer.ReadAll()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "invitation", entries[0].Message.TemplateID)
	assert.Equal(t, "new@bcars.org", entries[0].Message.To)
}

func TestExpiredLinkFails(t *testing.T) {
	d, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	require.NoError(t, db.Migrate(d))

	mailer := mail.NewFilelogSender(t.TempDir())
	links := NewEmailLinkService(d, mailer, EmailLinkConfig{
		BaseURL: "https://portal.test",
		TTL:     1 * time.Millisecond, // very short TTL
	})

	// Create user for recovery.
	hash, _ := HashPassword("pw", nil, DefaultParams())
	_, err = d.Exec(`INSERT INTO users (email, password_hash, is_active) VALUES ('x@x.com', ?, 1)`, hash)
	require.NoError(t, err)

	require.NoError(t, links.RequestRecovery(context.Background(), "x@x.com", "ip"))

	entries, err := mailer.ReadAll()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	token := entries[0].Message.Payload["token"]

	// Wait for expiry.
	time.Sleep(5 * time.Millisecond)

	_, err = links.ConsumeLink(token)
	assert.ErrorIs(t, err, ErrLinkNotFound)
}
