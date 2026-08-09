package authn

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/bcars/bcars-portal/internal/mail"
	"github.com/bcars/bcars-portal/internal/ratelimit"
)

// Purpose identifies what an email link authorizes.
//
// It is a struct rather than a string alias so a raw literal cannot be
// compared against it: `link.Purpose != "recovery"` fails to compile. That
// exact typo — comparing against "recovery" when the stored value is
// "password_recovery" — silently disabled password recovery on both the API
// and the web UI, and a named string type would not have caught it because an
// untyped constant converts implicitly.
type Purpose struct{ code string }

func (p Purpose) String() string { return p.code }

// Value implements driver.Valuer so a Purpose can be written to SQL directly.
func (p Purpose) Value() (driver.Value, error) { return p.code, nil }

// Scan implements sql.Scanner so a Purpose can be read from SQL directly.
// An unrecognised code scans into a zero Purpose, which matches no known
// purpose and therefore fails every comparison closed.
func (p *Purpose) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		p.code = ""
	case string:
		p.code = v
	case []byte:
		p.code = string(v)
	default:
		return fmt.Errorf("authn: cannot scan %T into Purpose", src)
	}
	return nil
}

var (
	PurposeRecovery    = Purpose{"password_recovery"}
	PurposeInvitation  = Purpose{"invitation"}
	PurposeVerifyEmail = Purpose{"verify_email"}
)

var (
	ErrLinkNotFound = errors.New("authn: email link not found, expired, or consumed")
	ErrLinkConsumed = errors.New("authn: email link already consumed")
)

// EmailLinkConfig holds configuration for email link generation.
type EmailLinkConfig struct {
	BaseURL string        // e.g. "https://portal.bcars.org"
	TTL     time.Duration // e.g. 24h

	// RecoveryPath and InvitationPath are the paths the emailed links point
	// at. They are configured rather than hardcoded here because authn must
	// not know the web UI's routing table, and because hardcoding them is how
	// the recovery link came to point at /auth/recovery/consume — a path no
	// router ever served. The assembly wires these from the constants the web
	// package exports, and a test asserts they resolve to real routes.
	RecoveryPath   string
	InvitationPath string

	// Limiter bounds recovery requests per source and per target. Nil disables
	// limiting, which is correct for a service assembled without a database
	// but must never be the production configuration.
	Limiter *ratelimit.Limiter
}

// Defaults used when a path is not configured.
const (
	defaultRecoveryPath   = "/reset-password"
	defaultInvitationPath = "/auth/invitations/consume"
)

func (c EmailLinkConfig) recoveryPath() string {
	if c.RecoveryPath == "" {
		return defaultRecoveryPath
	}
	return c.RecoveryPath
}

func (c EmailLinkConfig) invitationPath() string {
	if c.InvitationPath == "" {
		return defaultInvitationPath
	}
	return c.InvitationPath
}

// EmailLinkService manages one-time email tokens for recovery and invitations.
type EmailLinkService struct {
	db     *sql.DB
	mailer mail.Sender
	cfg    EmailLinkConfig

	// limiter bounds recovery requests. It lives here rather than in each
	// transport so a surface cannot forget it: the admin UI form and the API
	// endpoint both reach recovery through this method, and an unbounded
	// second door would make the first door's bound pointless. Nil disables
	// limiting.
	limiter *ratelimit.Limiter
}

// ErrRateLimited reports that a request exceeded its abuse bound.
//
// Callers MUST translate it to the same response for every target. It is
// returned without consulting whether the address exists, so the caller has
// nothing target-specific to leak — but a caller that rendered a different
// message for a known address would reintroduce the enumeration oracle this
// limiter is shaped to avoid.
var ErrRateLimited = errors.New("authn: too many requests")

// NewEmailLinkService creates a new service.
func NewEmailLinkService(db *sql.DB, mailer mail.Sender, cfg EmailLinkConfig) *EmailLinkService {
	return &EmailLinkService{db: db, mailer: mailer, cfg: cfg, limiter: cfg.Limiter}
}

// RequestRecovery creates a recovery link and sends it via the mailer.
// Always succeeds (returns nil) even if the email doesn't exist, to prevent
// email enumeration.
func (s *EmailLinkService) RequestRecovery(ctx context.Context, email, ipHash string) error {
	// The bound is evaluated BEFORE the existence lookup below, and counts
	// requests rather than sends. That ordering is the whole design: a limiter
	// that only counted real addresses would make "were you limited" a reliable
	// answer to "is this address a member", which is exactly what the uniform
	// success response exists to hide.
	if d := s.limiter.Allow(ctx, ratelimit.RecoveryRule, ipHash, email); !d.Allowed {
		return ErrRateLimited
	}

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

	if s.mailer == nil {
		return fmt.Errorf("authn: no mail sender configured; recovery mail cannot be delivered")
	}

	linkURL := s.linkURL(s.cfg.recoveryPath(), token)
	return s.mailer.Send(ctx, mail.Message{
		To:         email,
		TemplateID: "password_recovery",
		Payload:    map[string]string{"url": linkURL, "token": token},
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
		if s.mailer == nil {
			return token, fmt.Errorf("authn: no mail sender configured; invitation mail cannot be delivered")
		}
		linkURL := s.linkURL(s.cfg.invitationPath(), token)
		if err := s.mailer.Send(ctx, mail.Message{
			To:         email,
			TemplateID: "invitation",
			Payload:    map[string]string{"url": linkURL, "token": token},
		}); err != nil {
			return token, fmt.Errorf("authn: send invitation email: %w", err)
		}
	}

	return token, nil
}

// linkURL builds the absolute URL for an emailed link.
func (s *EmailLinkService) linkURL(path, token string) string {
	return fmt.Sprintf("%s%s?token=%s", strings.TrimRight(s.cfg.BaseURL, "/"), path, url.QueryEscape(token))
}

// InvitationURL returns the URL an invitation token resolves to. portalctl
// prints it, so it must be built the same way the emailed link is.
func (s *EmailLinkService) InvitationURL(token string) string {
	return s.linkURL(s.cfg.invitationPath(), token)
}

// ConsumeLink validates and consumes a token. Returns the email_links row data.
// The caller is responsible for setting the password and creating the user
// (for invitations) or resetting the password (for recovery).
type ConsumedLink struct {
	ID      int64
	Purpose Purpose
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
