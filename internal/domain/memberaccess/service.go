// Package memberaccess provisions optional passwordless member accounts and
// the explicit grants that let one see a person record (bcars-portal-4ux.4).
//
// The rule this package exists to enforce is ADR-0010's: a contact method is
// not login authority, and a family relationship is not access. An officer
// decides, explicitly, that this account may see that record. Nothing here
// derives a grant from a matching email, and nothing here reads
// person_relationships at all.
//
// One account may hold several grants, which is how a shared household mailbox
// reaches more than one record. That is why provisioning REUSES an existing
// account for a known address rather than creating a rival one: two accounts
// for one mailbox would mean two sign-in links and a member seeing half their
// household depending on which they clicked.
package memberaccess

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/bcars/bcars-portal/internal/db"
	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/authz"
)

// MemberRole is the only role provisioning ever assigns.
const MemberRole = "member"

// Access kinds. `self` is the member's own record; `delegate` is someone acting
// for them, which an officer grants deliberately and can revoke.
const (
	AccessSelf     = "self"
	AccessDelegate = "delegate"
)

const isoTimestamp = "2006-01-02T15:04:05.000Z"

var (
	// ErrEmailRequired is returned for a blank or unusable address.
	ErrEmailRequired = errors.New("memberaccess: a member account needs an email address")

	// ErrUnknownPerson is returned when a named person does not exist.
	ErrUnknownPerson = errors.New("memberaccess: person does not exist")

	// ErrUnknownUser is returned when a named account does not exist.
	ErrUnknownUser = errors.New("memberaccess: member account does not exist")

	// ErrAlreadyGranted is returned when an active grant already exists for
	// this pair. The database refuses it too; this is the readable version.
	ErrAlreadyGranted = errors.New("memberaccess: this account already has access to that record")

	// ErrGrantNotFound is returned when revoking a grant that does not exist or
	// is already revoked.
	ErrGrantNotFound = errors.New("memberaccess: no active grant to revoke")

	// ErrUnknownAccessKind is returned for an access kind outside the set.
	ErrUnknownAccessKind = errors.New("memberaccess: access kind must be self or delegate")
)

// Service provisions member accounts and record access.
type Service struct {
	DB *sql.DB
	Q  *sqlcgen.Queries
}

// NewService creates a member-access service over database.
func NewService(database *sql.DB) *Service {
	return &Service{DB: database, Q: sqlcgen.New(database)}
}

// Account is a provisioned member account.
type Account struct {
	UserID int64
	Email  string
	// Created reports whether this call created the account. False means an
	// existing account for that address was reused.
	Created bool
	Grants  []Grant
}

// Grant is one record-access grant.
type Grant struct {
	ID           int64
	UserID       int64
	PersonID     int64
	DisplayName  string
	AccessKind   string
	Reason       string
	GrantedBy    int64
	GrantedAt    string
	RevokedAt    string
	RevokedBy    int64
	RevokeReason string
	Version      int64
}

// Active reports whether the grant currently confers access.
func (g Grant) Active() bool { return g.RevokedAt == "" }

// ProvisionParams describes an account to create or reuse.
type ProvisionParams struct {
	Email string
}

// Provision creates or reuses an account and gives it member self-service.
//
// A NEW account stores no password: it exists to receive a sign-in link, and a
// member who never uses one simply has an account that does nothing.
//
// An EXISTING account is reused as-is. One person is one identity: an officer
// who is also a member holds both roles on one account, because a role adds
// permissions rather than defining a separate kind of login. Provisioning never
// sets, clears, or reads a password, and never removes a role.
//
// SECURITY NOTE FOR PASSWORDLESS SIGN-IN (bcars-portal-4ux.5): once an officer
// holds the member role, an email sign-in link reaches an identity that also
// carries officer capabilities. Whether such a session may exercise them is a
// decision that belongs to sign-in, not here; see bcars-portal-4ux.15.
func (s *Service) Provision(ctx context.Context, p *authz.Principal, params ProvisionParams, now time.Time) (Account, error) {
	email := NormalizeEmail(params.Email)
	if email == "" {
		return Account{}, ErrEmailRequired
	}

	var actorID int64
	if p != nil {
		actorID = p.UserID
	}

	var out Account
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		existing, err := q.GetUserByEmail(ctx, email)
		switch {
		case err == nil:
			// Reuse, whatever roles the account already holds.
			//
			// An individual is a member; some members are also officers, and an
			// officer role only ADDS permissions to the same identity. There is
			// no such thing as a separate "officer account" to protect from a
			// separate "member account", so provisioning an address that already
			// signs in is simply that person gaining member self-service.
			//
			// Provisioning never touches an existing password, and never removes
			// a role. It only adds the member role if it is missing.
			out = Account{UserID: existing.ID, Email: existing.Email}
		case errors.Is(err, sql.ErrNoRows):
			created, err := q.CreateUser(ctx, sqlcgen.CreateUserParams{
				Email: email,
				// No password hash and no algorithm parameters. This account
				// cannot be signed into with a password because it has none.
				PasswordHash:       sql.NullString{},
				PasswordAlgoParams: sql.NullString{},
				// users.person_id stays empty. It is the legacy officer
				// identity link and explicitly NOT an access authority
				// (ADR-0010); access comes from member_access_grants alone.
				PersonID: sql.NullInt64{},
			})
			if err != nil {
				return err
			}
			out = Account{UserID: created.ID, Email: created.Email, Created: true}
		default:
			return err
		}

		// Ensure the member role, without duplicating it on reuse.
		roles, err := q.ActiveRolesForUser(ctx, out.UserID)
		if err != nil {
			return err
		}
		hasMember := false
		for _, r := range roles {
			if r.RoleCode == MemberRole {
				hasMember = true
			}
		}
		if !hasMember {
			if _, err := q.CreateRoleGrant(ctx, sqlcgen.CreateRoleGrantParams{
				UserID:    out.UserID,
				RoleCode:  MemberRole,
				GrantedBy: actorID,
				GrantedAt: now.UTC().Format(isoTimestamp),
				Reason:    nullString("Passwordless member account provisioned by an officer."),
			}); err != nil {
				return err
			}
		}

		out.Grants, err = s.loadGrants(ctx, q, out.UserID)
		return err
	})
	if err != nil {
		return Account{}, err
	}
	return out, nil
}

// GrantParams describes one record-access grant.
type GrantParams struct {
	PersonID   int64
	AccessKind string
	Reason     string
}

// GrantAccess gives an account access to one person record.
func (s *Service) GrantAccess(ctx context.Context, p *authz.Principal, userID int64, params GrantParams, now time.Time) (Grant, error) {
	kind := params.AccessKind
	if kind == "" {
		kind = AccessSelf
	}
	if kind != AccessSelf && kind != AccessDelegate {
		return Grant{}, ErrUnknownAccessKind
	}

	var actorID int64
	if p != nil {
		actorID = p.UserID
	}

	var out Grant
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := q.GetUser(ctx, userID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrUnknownUser
			}
			return err
		}
		person, err := q.GetPerson(ctx, params.PersonID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrUnknownPerson
			}
			return err
		}

		row, err := q.GrantMemberAccess(ctx, sqlcgen.GrantMemberAccessParams{
			UserID:     userID,
			PersonID:   params.PersonID,
			AccessKind: kind,
			Reason:     nullString(params.Reason),
			GrantedBy:  sql.NullInt64{Int64: actorID, Valid: actorID != 0},
			GrantedAt:  now.UTC().Format(isoTimestamp),
		})
		if err != nil {
			// The partial unique index from 4ux.1 refuses a second active
			// grant for the same pair.
			if isUniqueViolation(err) {
				return ErrAlreadyGranted
			}
			return err
		}

		out = grantFrom(row)
		out.DisplayName = person.DisplayName
		return nil
	})
	if err != nil {
		return Grant{}, err
	}
	return out, nil
}

// RevokeParams describes a revocation.
type RevokeParams struct {
	PersonID        int64
	Reason          string
	ExpectedVersion int64
}

// RevokeAccess ends an account's access to one record.
//
// It takes effect on the caller's NEXT authorized request, including inside a
// session that is already open, because authorization loads active grants per
// request rather than caching them at sign-in.
func (s *Service) RevokeAccess(ctx context.Context, p *authz.Principal, userID int64, params RevokeParams, now time.Time) (Grant, error) {
	var actorID int64
	if p != nil {
		actorID = p.UserID
	}

	var out Grant
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		grants, err := s.loadGrants(ctx, q, userID)
		if err != nil {
			return err
		}
		var target *Grant
		for i := range grants {
			if grants[i].PersonID == params.PersonID && grants[i].Active() {
				target = &grants[i]
				break
			}
		}
		if target == nil {
			return ErrGrantNotFound
		}
		version := params.ExpectedVersion
		if version == 0 {
			version = target.Version
		}

		row, err := q.RevokeMemberAccess(ctx, sqlcgen.RevokeMemberAccessParams{
			RevokedAt:    sql.NullString{String: now.UTC().Format(isoTimestamp), Valid: true},
			RevokedBy:    sql.NullInt64{Int64: actorID, Valid: actorID != 0},
			RevokeReason: nullString(params.Reason),
			ID:           target.ID,
			Version:      version,
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// The row exists and was active a moment ago, so either the
				// version moved or another officer revoked it first.
				return db.ErrStale
			}
			return err
		}
		out = grantFrom(row)
		out.DisplayName = target.DisplayName
		return nil
	})
	if err != nil {
		return Grant{}, err
	}
	return out, nil
}

// ListGrants returns every grant for an account, revoked ones included, so an
// officer can see the history of who could reach what.
func (s *Service) ListGrants(ctx context.Context, p *authz.Principal, userID int64) ([]Grant, error) {
	if _, err := s.Q.GetUser(ctx, userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUnknownUser
		}
		return nil, err
	}
	return s.loadGrants(ctx, s.Q, userID)
}

// ListGrantsForPerson returns every account that can reach one record.
func (s *Service) ListGrantsForPerson(ctx context.Context, p *authz.Principal, personID int64) ([]Grant, error) {
	rows, err := s.Q.ListAccessGrantsForPerson(ctx, personID)
	if err != nil {
		return nil, err
	}
	out := make([]Grant, 0, len(rows))
	for _, r := range rows {
		out = append(out, Grant{
			ID: r.ID, UserID: r.UserID, PersonID: r.PersonID,
			AccessKind: r.AccessKind, Reason: r.Reason.String,
			GrantedBy: r.GrantedBy.Int64, GrantedAt: r.GrantedAt,
			RevokedAt: r.RevokedAt.String, RevokedBy: r.RevokedBy.Int64,
			RevokeReason: r.RevokeReason.String, Version: r.Version,
		})
	}
	return out, nil
}

// --- internals ---

func (s *Service) loadGrants(ctx context.Context, q *sqlcgen.Queries, userID int64) ([]Grant, error) {
	rows, err := q.ListAccessGrantsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Grant, 0, len(rows))
	for _, r := range rows {
		out = append(out, Grant{
			ID: r.ID, UserID: r.UserID, PersonID: r.PersonID,
			AccessKind: r.AccessKind, Reason: r.Reason.String,
			GrantedBy: r.GrantedBy.Int64, GrantedAt: r.GrantedAt,
			RevokedAt: r.RevokedAt.String, RevokedBy: r.RevokedBy.Int64,
			RevokeReason: r.RevokeReason.String, Version: r.Version,
			DisplayName: r.DisplayName,
		})
	}
	return out, nil
}

func (s *Service) inTx(ctx context.Context, fn func(*sqlcgen.Queries) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(s.Q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit()
}

func grantFrom(row sqlcgen.MemberAccessGrant) Grant {
	return Grant{
		ID:           row.ID,
		UserID:       row.UserID,
		PersonID:     row.PersonID,
		AccessKind:   row.AccessKind,
		Reason:       row.Reason.String,
		GrantedBy:    row.GrantedBy.Int64,
		GrantedAt:    row.GrantedAt,
		RevokedAt:    row.RevokedAt.String,
		RevokedBy:    row.RevokedBy.Int64,
		RevokeReason: row.RevokeReason.String,
		Version:      row.Version,
	}
}

// NormalizeEmail is the one normalization used for member accounts.
//
// It is exported because "is this the same mailbox" must be answered the same
// way everywhere: provisioning, sign-in, and any future lookup. Two spellings
// of one address becoming two accounts is precisely the split this package
// exists to prevent.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// isUniqueViolation reports whether err is a uniqueness failure. It matches on
// the driver's message because the SQLite driver does not expose a typed code.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "UNIQUE")
}
