package authn

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	maxFailedAttempts = 10
	lockoutWindow     = 15 * time.Minute
	lockoutDuration   = 15 * time.Minute
)

var (
	ErrInvalidCredentials = errors.New("authn: invalid email or password")
	ErrAccountLocked      = errors.New("authn: account temporarily locked")
	ErrAccountInactive    = errors.New("authn: account is not active")
)

// AuthService handles sign-in, sign-out, and account lockout.
type AuthService struct {
	db     *sql.DB
	store  *SessionStore
	pepper []byte
}

// NewAuthService creates an AuthService.
func NewAuthService(db *sql.DB, store *SessionStore, pepper []byte) *AuthService {
	return &AuthService{db: db, store: store, pepper: pepper}
}

// SignIn authenticates a user by email and password. Returns a session ID on
// success. Uses constant-time comparison regardless of whether the email exists.
func (s *AuthService) SignIn(email, password, ipHash, userAgent string) (string, error) {
	var (
		userID       int64
		passwordHash sql.NullString
		isActive     int
		failedCount  int
		lockedUntil  sql.NullString
	)

	err := s.db.QueryRow(
		`SELECT id, password_hash, is_active, failed_login_count, locked_until FROM users WHERE email = ?`,
		email,
	).Scan(&userID, &passwordHash, &isActive, &failedCount, &lockedUntil)

	if errors.Is(err, sql.ErrNoRows) {
		// Constant-time: hash a dummy password so timing doesn't reveal email existence.
		_, _ = HashPassword(password, s.pepper, DefaultParams())
		return "", ErrInvalidCredentials
	}
	if err != nil {
		return "", fmt.Errorf("authn: sign-in lookup: %w", err)
	}

	if isActive == 0 {
		return "", ErrAccountInactive
	}

	// Check lockout.
	if lockedUntil.Valid {
		locked, _ := time.Parse(time.RFC3339Nano, lockedUntil.String)
		if time.Now().UTC().Before(locked) {
			return "", ErrAccountLocked
		}
		// Lockout expired — reset counter.
		_, _ = s.db.Exec(
			`UPDATE users SET failed_login_count = 0, locked_until = NULL WHERE id = ?`,
			userID,
		)
	}

	if !passwordHash.Valid {
		return "", ErrInvalidCredentials
	}

	ok, err := VerifyPassword(password, passwordHash.String, s.pepper)
	if err != nil {
		return "", fmt.Errorf("authn: verify password: %w", err)
	}
	if !ok {
		s.recordFailedLogin(userID, failedCount)
		return "", ErrInvalidCredentials
	}

	// Success — reset counter and record login.
	_, _ = s.db.Exec(
		`UPDATE users SET last_login_at = ?, failed_login_count = 0, locked_until = NULL, version = version + 1, updated_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339Nano),
		time.Now().UTC().Format(time.RFC3339Nano),
		userID,
	)

	return s.store.Create(userID, ipHash, userAgent)
}

// SignOut revokes the given session.
func (s *AuthService) SignOut(sessionID string) error {
	return s.store.Revoke(sessionID)
}

func (s *AuthService) recordFailedLogin(userID int64, currentCount int) {
	newCount := currentCount + 1
	now := time.Now().UTC().Format(time.RFC3339Nano)

	if newCount >= maxFailedAttempts {
		locked := time.Now().UTC().Add(lockoutDuration).Format(time.RFC3339Nano)
		_, _ = s.db.Exec(
			`UPDATE users SET failed_login_count = ?, locked_until = ?, version = version + 1, updated_at = ? WHERE id = ?`,
			newCount, locked, now, userID,
		)
	} else {
		_, _ = s.db.Exec(
			`UPDATE users SET failed_login_count = ?, version = version + 1, updated_at = ? WHERE id = ?`,
			newCount, now, userID,
		)
	}
}
