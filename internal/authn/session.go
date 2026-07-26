package authn

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// SessionConfig holds session-related configuration.
type SessionConfig struct {
	CookieName string
	TTL        time.Duration
}

// SessionStore manages server-side sessions in SQLite.
type SessionStore struct {
	db  *sql.DB
	cfg SessionConfig
}

// NewSessionStore creates a SessionStore backed by db.
func NewSessionStore(db *sql.DB, cfg SessionConfig) *SessionStore {
	return &SessionStore{db: db, cfg: cfg}
}

var (
	ErrSessionNotFound = errors.New("authn: session not found or expired")
	ErrSessionRevoked  = errors.New("authn: session revoked")
)

// Session represents a row in the sessions table.
type Session struct {
	ID         string
	UserID     int64
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	IPHash     string
	UserAgent  string
	RevokedAt  *time.Time
}

// Create issues a new session for the given user. Returns the session ID
// (which is also the cookie value).
func (s *SessionStore) Create(userID int64, ipHash, userAgent string) (string, error) {
	id, err := generateSessionID()
	if err != nil {
		return "", err
	}

	now := time.Now().UTC()
	expires := now.Add(s.cfg.TTL)

	_, err = s.db.Exec(
		`INSERT INTO sessions (id, user_id, created_at, last_seen_at, expires_at, ip_hash, user_agent) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, userID,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		expires.Format(time.RFC3339Nano),
		ipHash, userAgent,
	)
	if err != nil {
		return "", fmt.Errorf("authn: create session: %w", err)
	}
	return id, nil
}

// Get retrieves a session by ID. Returns ErrSessionNotFound if the session
// doesn't exist, is expired, or is revoked.
func (s *SessionStore) Get(id string) (*Session, error) {
	row := s.db.QueryRow(
		`SELECT id, user_id, created_at, last_seen_at, expires_at, ip_hash, user_agent, revoked_at FROM sessions WHERE id = ?`,
		id,
	)

	var sess Session
	var createdAt, lastSeen, expiresAt string
	var ipHash, ua, revokedAt sql.NullString

	err := row.Scan(&sess.ID, &sess.UserID, &createdAt, &lastSeen, &expiresAt, &ipHash, &ua, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("authn: get session: %w", err)
	}

	sess.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	sess.LastSeenAt, _ = time.Parse(time.RFC3339Nano, lastSeen)
	sess.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expiresAt)
	if ipHash.Valid {
		sess.IPHash = ipHash.String
	}
	if ua.Valid {
		sess.UserAgent = ua.String
	}
	if revokedAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, revokedAt.String)
		sess.RevokedAt = &t
		return nil, ErrSessionRevoked
	}

	if time.Now().UTC().After(sess.ExpiresAt) {
		return nil, ErrSessionNotFound
	}

	return &sess, nil
}

// Touch updates last_seen_at for the session.
func (s *SessionStore) Touch(id string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(`UPDATE sessions SET last_seen_at = ? WHERE id = ?`, now, id)
	if err != nil {
		return fmt.Errorf("authn: touch session: %w", err)
	}
	return nil
}

// Revoke marks a session as revoked.
func (s *SessionStore) Revoke(id string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(`UPDATE sessions SET revoked_at = ? WHERE id = ?`, now, id)
	if err != nil {
		return fmt.Errorf("authn: revoke session: %w", err)
	}
	return nil
}

// RevokeAllForUser revokes all active sessions for a user.
func (s *SessionStore) RevokeAllForUser(userID int64) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(
		`UPDATE sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`,
		now, userID,
	)
	if err != nil {
		return fmt.Errorf("authn: revoke all sessions: %w", err)
	}
	return nil
}

// Rotate revokes the old session and creates a new one for the same user,
// preserving IP/UA. Returns the new session ID.
func (s *SessionStore) Rotate(oldID string) (string, error) {
	old, err := s.Get(oldID)
	if err != nil {
		return "", err
	}

	if err := s.Revoke(oldID); err != nil {
		return "", err
	}

	return s.Create(old.UserID, old.IPHash, old.UserAgent)
}

func generateSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("authn: generate session id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
