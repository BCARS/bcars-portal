package authn

import (
	"context"
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

// SetPassword updates a user's password hash.
func (s *AuthService) SetPassword(ctx context.Context, userID int64, newPassword string) error {
	hash, err := HashPassword(newPassword, s.pepper, DefaultParams())
	if err != nil {
		return fmt.Errorf("authn: hash password: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = ?, version = version + 1, updated_at = ? WHERE id = ?`,
		hash, now, userID,
	)
	if err != nil {
		return fmt.Errorf("authn: set password: %w", err)
	}
	return nil
}

// LoginByUserID creates a session for an already-authenticated user (e.g. after
// recovery or invitation). Returns the session ID.
func (s *AuthService) LoginByUserID(ctx context.Context, userID int64, store *SessionStore) (string, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = s.db.ExecContext(ctx,
		`UPDATE users SET last_login_at = ?, failed_login_count = 0, locked_until = NULL, version = version + 1, updated_at = ? WHERE id = ?`,
		now, now, userID,
	)

	sessionID, err := store.Create(userID, "", "recovery/invitation")
	if err != nil {
		return "", fmt.Errorf("authn: create session: %w", err)
	}
	return sessionID, nil
}

// CreateUser creates a new user with the given email and password. Returns the user ID.
func (s *AuthService) CreateUser(ctx context.Context, email, password string) (int64, error) {
	hash, err := HashPassword(password, s.pepper, DefaultParams())
	if err != nil {
		return 0, fmt.Errorf("authn: hash password: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (email, password_hash, is_active, created_at, updated_at, version) VALUES (?, ?, 1, ?, ?, 1)`,
		email, hash, now, now,
	)
	if err != nil {
		return 0, fmt.Errorf("authn: create user: %w", err)
	}
	return res.LastInsertId()
}

// CreateUserFromInvitation creates the account for a consumed invitation and
// grants the role the invitation carried, in a single transaction.
//
// It exists so the API and the web UI cannot disagree about what accepting an
// invitation does. Both previously called CreateUser and stopped there, which
// is why portalctl bootstrap-admin produced an account with no capabilities
// and a fresh installation had no route to a working administrator.
//
// The grant is attributed to the new user when no granting user is available,
// which is the bootstrap case: user_role_grants.granted_by is NOT NULL and
// there is no prior account to point at. The reason column records the real
// provenance, and the caller audits the grant.
func (s *AuthService) CreateUserFromInvitation(ctx context.Context, link *ConsumedLink, password string) (int64, error) {
	if link == nil {
		return 0, fmt.Errorf("authn: nil invitation link")
	}

	hash, err := HashPassword(password, s.pepper, DefaultParams())
	if err != nil {
		return 0, fmt.Errorf("authn: hash password: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("authn: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx,
		`INSERT INTO users (email, password_hash, is_active, created_at, updated_at, version) VALUES (?, ?, 1, ?, ?, 1)`,
		link.Email, hash, now, now,
	)
	if err != nil {
		return 0, fmt.Errorf("authn: create user: %w", err)
	}
	userID, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("authn: user id: %w", err)
	}

	if link.IntendedRoleCode != "" {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO user_role_grants (user_id, role_code, granted_by, granted_at, reason)
			 VALUES (?, ?, ?, ?, ?)`,
			userID, link.IntendedRoleCode, userID, now,
			fmt.Sprintf("invitation %d accepted", link.ID),
		)
		if err != nil {
			// An unknown role code fails the FK here rather than silently
			// producing a capability-less account.
			return 0, fmt.Errorf("authn: grant invited role %q: %w", link.IntendedRoleCode, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("authn: commit: %w", err)
	}
	return userID, nil
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
