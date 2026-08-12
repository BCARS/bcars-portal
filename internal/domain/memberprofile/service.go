// Package memberprofile serves the member-safe read model for records an
// officer explicitly granted to the signed-in user (bcars-portal-4ux.6).
//
// Two rules define this package, and both are structural rather than advisory:
//
//  1. ACCESS IS THE GRANT, NOTHING ELSE. Every read joins
//     member_access_grants for the calling user and requires the grant to be
//     active. A record the caller was never granted is not filtered out of a
//     result; it is never selected. Because the grant is re-read on every call,
//     an officer revoking access ends it inside a session that is already open
//     (ADR-0010).
//
//  2. UNGRANTED AND NON-EXISTENT ARE THE SAME ANSWER. Both produce ErrNotFound,
//     which callers translate to 404. A distinct "you may not see that" would
//     confirm the record exists, and confirming existence is how an id
//     parameter becomes a membership oracle.
//
// The package deliberately does not reuse members.Service or the administrative
// dues list. Those require member.read and dues.read, which ADR-0010 says an
// ordinary member must never hold: they read every record in the club. The
// safe read model is assembled here from short, explicit column lists instead,
// so a field an officer sees cannot arrive by accident. There is no note, no
// audit record, no payment, and no officer annotation anywhere in it.
package memberprofile

import (
	"context"
	"database/sql"
	"errors"

	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/domain/dues"
)

// ErrNotFound reports that this caller has no active grant to that record, or
// that no such record exists.
//
// The two cases are deliberately indistinguishable. Callers MUST answer 404 for
// it and must not add a message that separates them.
var ErrNotFound = errors.New("memberprofile: no such record for this caller")

// Capability is what a caller needs before any grant is even looked up.
const Capability = "profile.self.read"

// Contact is one contact method on a granted record.
type Contact struct {
	ID    int64
	Kind  string
	Label string
	Value string
	// Primary marks the member's main contact of that kind.
	Primary bool
	// SharedWith is the audience the latest visibility decision chose, or empty
	// when no decision is on file and the Phase 1 default applies.
	SharedWith string
	// Version is the row version, so a correction request can say which value
	// the member was looking at when they proposed the change.
	Version int64
}

// Profile is one granted record as its member sees it.
//
// What is absent matters as much as what is here: no notes, no payments, no
// audit trail, no officer fields, and no other member's data. Dues appear only
// as the Phase 2 derived standing summary, never as payment detail.
type Profile struct {
	PersonID    int64
	DisplayName string
	CallSign    string
	// AccessKind is 'self' for the member's own record or 'delegate' when an
	// officer granted them access on someone's behalf. It is provenance for the
	// member, not authority: both kinds read exactly the same fields.
	AccessKind string

	MembershipID int64
	// BaseType and Lifecycle are empty when the person has no membership row.
	BaseType  string
	Lifecycle string

	// Standing is the safe dues summary, or nil when the person has no
	// membership to have standing for.
	Standing *dues.Standing

	// Contacts is populated by Get and left empty by List, which is a summary.
	Contacts []Contact
}

// Service reads member-safe profiles.
type Service struct {
	DB   *sql.DB
	Q    *sqlcgen.Queries
	dues *dues.Service
}

// NewService creates a member-profile service over database.
func NewService(database *sql.DB) *Service {
	return &Service{
		DB:   database,
		Q:    sqlcgen.New(database),
		dues: dues.NewService(database),
	}
}

// List returns every record the caller may currently see.
//
// It takes no user id. The subject is always the principal, so there is no
// identifier a caller could change to read somebody else's list — the mistake
// this signature exists to make impossible.
func (s *Service) List(ctx context.Context, p *authz.Principal) ([]Profile, error) {
	if err := authz.Authorize(ctx, p, Capability, nil); err != nil {
		return nil, err
	}
	if p == nil || p.UserID == 0 {
		return nil, ErrNotFound
	}

	rows, err := s.Q.ListGrantedProfiles(ctx, p.UserID)
	if err != nil {
		return nil, err
	}

	out := make([]Profile, 0, len(rows))
	for _, r := range rows {
		profile := Profile{
			PersonID:     r.PersonID,
			DisplayName:  r.DisplayName,
			CallSign:     r.CallSign,
			AccessKind:   r.AccessKind,
			MembershipID: r.MembershipID,
			BaseType:     r.BaseType,
			Lifecycle:    r.Lifecycle,
		}
		standing, err := s.standingFor(ctx, p, r.PersonID, r.MembershipID)
		if err != nil {
			return nil, err
		}
		profile.Standing = standing
		out = append(out, profile)
	}
	return out, nil
}

// Get returns one granted record with its contact methods.
func (s *Service) Get(ctx context.Context, p *authz.Principal, personID int64) (Profile, error) {
	if err := authz.Authorize(ctx, p, Capability, nil); err != nil {
		return Profile{}, err
	}
	if p == nil || p.UserID == 0 || personID == 0 {
		return Profile{}, ErrNotFound
	}

	row, err := s.Q.GetGrantedProfile(ctx, sqlcgen.GetGrantedProfileParams{
		UserID:   p.UserID,
		PersonID: personID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	if err != nil {
		return Profile{}, err
	}

	out := Profile{
		PersonID:     row.PersonID,
		DisplayName:  row.DisplayName,
		CallSign:     row.CallSign,
		AccessKind:   row.AccessKind,
		MembershipID: row.MembershipID,
		BaseType:     row.BaseType,
		Lifecycle:    row.Lifecycle,
	}

	standing, err := s.standingFor(ctx, p, row.PersonID, row.MembershipID)
	if err != nil {
		return Profile{}, err
	}
	out.Standing = standing

	contacts, err := s.Q.ListGrantedContactMethods(ctx, sqlcgen.ListGrantedContactMethodsParams{
		UserID:   p.UserID,
		PersonID: personID,
	})
	if err != nil {
		return Profile{}, err
	}
	for _, c := range contacts {
		out.Contacts = append(out.Contacts, Contact{
			ID:         c.ContactMethodID,
			Kind:       c.Kind,
			Label:      c.Label,
			Value:      c.Value,
			Primary:    c.IsPrimary != 0,
			SharedWith: c.SharedWith,
			Version:    c.Version,
		})
	}
	return out, nil
}

// standingFor reads the safe dues summary through the dues package, which owns
// the derivation.
//
// The derivation is not reimplemented here on purpose. A second copy of "what
// counts as expired" would drift from the treasurer's view, and the member
// would be told something the club's own books disagree with.
func (s *Service) standingFor(ctx context.Context, p *authz.Principal, personID, membershipID int64) (*dues.Standing, error) {
	if membershipID == 0 {
		return nil, nil
	}
	standing, err := s.dues.StandingForGrantedPerson(ctx, p, personID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &standing, nil
}
