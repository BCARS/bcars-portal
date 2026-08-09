package authn

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/bcars/bcars-portal/internal/mail"
)

const (
	PurposeRecovery    = "password_recovery"
	PurposeInvitation  = "invitation"
	PurposeVerifyEmail = "verify_email"
)

var (
	ErrLinkNotFound = errors.New("authn: email link not found, expired, or consumed")
	ErrLinkConsumed = errors.New("authn: email link already consumed")
)

// EmailLinkConfig holds configuration for email link generation.
type EmailLinkConfig struct {
	BaseURL string        // e.g. "https://portal.bcars.org"
	TTL     time.Duration // e.g. 24h
}

// EmailLinkService manages one-time email tokens for recovery and invitations.
type EmailLinkService struct {
	db     *sql.DB
	mailer mail.Sender
	cfg    EmailLinkConfig
}

// NewEmailLinkService creates a new service.
func NewEmailLinkService(db *sql.DB, mailer mail.Sender, cfg EmailLinkConfig) *EmailLinkService {
	return &EmailLinkService{db: db, mailer: mailer, cfg: cfg}
}

// RequestRecovery creates a recovery link and sends it via the mailer.
// Always succeeds (returns nil) even if the email doesn't exist, to prevent
// email enumeration.
func (s *EmailLinkService) RequestRecovery(ctx context.Context, email, ipHash string) error {
	// Check if user exists.
	var userID int64
	err := s.db.QueryRow("SELECT id FROM users WHERE email = ? AND is_active = 1", email).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		// Don't reveal whether the email exists.
		return nil
	}
	if err != nil {
		return fmt.Errorf("authn: recovery lookup: %w", err)
	}

	token, tokenHash, err := generateToken()
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	expires := now.Add(s.cfg.TTL)

	_, err = s.db.Exec(
		`INSERT INTO email_links (purpose, user_id, email, token_hash, created_at, expires_at, requested_ip_hash) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		PurposeRecovery, userID, email, tokenHash,
		now.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano), ipHash,
	)
	if err != nil {
		return fmt.Errorf("authn: create recovery link: %w", err)
	}

	url := fmt.Sprintf("%s/auth/recovery/consume?token=%s", s.cfg.BaseURL, token)
	return s.mailer.Send(ctx, mail.Message{
		To:         email,
		TemplateID: "password_recovery",
		Payload:    map[string]string{"url": url, "token": token},
	})
}

// CreateInvitation creates an invitation link for a new user and returns the
// raw token (for portalctl to print). The mailer is used only if sendEmail is
// true.
//
// roleCode is the role consuming the invitation will grant. Pass "" for an
// ordinary invitation that confers no elevated role. The role is recorded on
// the link rather than applied at consumption time by the caller, so that the
// authority an invitation carries is fixed when it is issued and is visible
// for audit before anyone accepts it.
func (s *EmailLinkService) CreateInvitation(ctx context.Context, email, roleCode string, sendEmail bool) (string, error) {
	token, tokenHash, err := generateToken()
	if err != nil {
		return "", err
	}

	now := time.Now().UTC()
	expires := now.Add(s.cfg.TTL)

	var role sql.NullString
	if roleCode != "" {
		role = sql.NullString{String: roleCode, Valid: true}
	}

	_, err = s.db.Exec(
		`INSERT INTO email_links (purpose, email, token_hash, created_at, expires_at, intended_role_code)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		PurposeInvitation, email, tokenHash,
		now.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano), role,
	)
	if err != nil {
		return "", fmt.Errorf("authn: create invitation: %w", err)
	}

	if sendEmail {
		url := fmt.Sprintf("%s/auth/invitations/consume?token=%s", s.cfg.BaseURL, token)
		if err := s.mailer.Send(ctx, mail.Message{
			To:         email,
			TemplateID: "invitation",
			Payload:    map[string]string{"url": url, "token": token},
		}); err != nil {
			return token, fmt.Errorf("authn: send invitation email: %w", err)
		}
	}

	return token, nil
}

// ConsumeLink validates and consumes a token. Returns the email_links row data.
// The caller is responsible for setting the password and creating the user
// (for invitations) or resetting the password (for recovery).
type ConsumedLink struct {
	ID      int64
	Purpose string
	UserID  *int64
	Email   string
	// IntendedRoleCode is the role this invitation confers, or "" for none.
	IntendedRoleCode string
}

func (s *EmailLinkService) ConsumeLink(token string) (*ConsumedLink, error) {
	tokenHash := hashToken(token)

	var link ConsumedLink
	var userID sql.NullInt64
	var consumedAt sql.NullString
	var roleCode sql.NullString

	err := s.db.QueryRow(
		`SELECT id, purpose, user_id, email, consumed_at, intended_role_code
		 FROM email_links WHERE token_hash = ?`,
		tokenHash,
	).Scan(&link.ID, &link.Purpose, &userID, &link.Email, &consumedAt, &roleCode)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrLinkNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("authn: consume link: %w", err)
	}

	if consumedAt.Valid {
		return nil, ErrLinkConsumed
	}

	// Check expiry.
	var expiresAt string
	_ = s.db.QueryRow("SELECT expires_at FROM email_links WHERE id = ?", link.ID).Scan(&expiresAt)
	exp, _ := time.Parse(time.RFC3339Nano, expiresAt)
	if time.Now().UTC().After(exp) {
		return nil, ErrLinkNotFound
	}

	// Mark consumed.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.Exec("UPDATE email_links SET consumed_at = ? WHERE id = ?", now, link.ID)
	if err != nil {
		return nil, fmt.Errorf("authn: mark consumed: %w", err)
	}

	if userID.Valid {
		link.UserID = &userID.Int64
	}
	link.IntendedRoleCode = roleCode.String
	return &link, nil
}

func generateToken() (raw string, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("authn: generate token: %w", err)
	}
	raw = hex.EncodeToString(b)
	return raw, hashToken(raw), nil
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
