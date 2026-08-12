// Package relationships maintains informational links between people —
// spouse, parent, child, household — for officer context (bcars-portal-4ux.8).
//
// Read the list of things this package does not import: memberaccess, authz
// grants, sessions, roles. That is deliberate and it is the entire point. A
// relationship is a note an officer wrote down about how two people are
// connected. It is not a permission.
//
// The temptation this package exists to refuse is the obvious-seeming shortcut:
// "she's his wife, so of course she can see his record." BCARS decided
// otherwise in ADR-0010, because a marriage ending does not file a change
// request, and a household breaking up is exactly when derived access is most
// wrong and least likely to be noticed. Access is a separate, revocable
// decision an officer makes and can unmake; see internal/domain/memberaccess.
//
// The independence runs both ways. A member does not need a relationship row to
// suggest a correction about someone else — any authenticated member may, and
// an officer reviews it. So a relationship neither grants anything nor gates
// anything. It only tells a reviewing officer why this suggestion might be
// arriving from this person.
package relationships

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/bcars/bcars-portal/internal/db"
	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/authz"
)

// The checked-in vocabulary. It is small and closed on purpose: a free-text
// relationship field turns into a hundred spellings of "wife" that no officer
// can filter and no report can total. Anything outside the set is `other` with
// a context note.
//
// These constants must stay in step with the CHECK constraint in migration
// 0009; KindsInOrder is asserted against the database schema in the tests.
const (
	KindSpousePartner  = "spouse_partner"
	KindParentGuardian = "parent_guardian"
	KindChildDependent = "child_dependent"
	KindHousehold      = "household"
	KindOther          = "other"
)

// KindsInOrder is the vocabulary in the order a UI should offer it.
var KindsInOrder = []string{
	KindSpousePartner,
	KindParentGuardian,
	KindChildDependent,
	KindHousehold,
	KindOther,
}

// Direction values reported by the list queries, relative to the person asked
// about. `outgoing` means the subject is the from_person; `incoming` means the
// subject is the to_person.
const (
	DirectionOutgoing = "outgoing"
	DirectionIncoming = "incoming"
)

const isoTimestamp = "2006-01-02T15:04:05.000Z"

// maxContextLength bounds the restricted note. It is a short explanation for a
// reviewing officer, not a case file.
const maxContextLength = 1000

var (
	// ErrUnknownKind is returned for a relationship kind outside the
	// checked-in vocabulary.
	ErrUnknownKind = errors.New("relationships: kind must be one of spouse_partner, parent_guardian, child_dependent, household, other")

	// ErrUnknownPerson is returned when either end of the relationship does
	// not exist.
	ErrUnknownPerson = errors.New("relationships: person does not exist")

	// ErrSelfRelationship is returned when both ends are the same person.
	ErrSelfRelationship = errors.New("relationships: a person cannot be related to themselves")

	// ErrDuplicate is returned when an identical active relationship already
	// exists. The partial unique index refuses it too; this is the readable
	// version.
	ErrDuplicate = errors.New("relationships: that relationship is already recorded")

	// ErrNotFound is returned for an unknown relationship.
	ErrNotFound = errors.New("relationships: relationship not found")

	// ErrArchived is returned when changing or archiving a relationship that
	// is already archived. Archiving keeps history, so the row is still
	// readable; it is simply no longer current.
	ErrArchived = errors.New("relationships: that relationship is already archived")

	// ErrContextTooLong is returned for an oversized context note.
	ErrContextTooLong = errors.New("relationships: context note is too long")
)

// Service maintains informational person relationships.
type Service struct {
	DB *sql.DB
	Q  *sqlcgen.Queries
}

// NewService creates a relationship service over database.
func NewService(database *sql.DB) *Service {
	return &Service{DB: database, Q: sqlcgen.New(database)}
}

// Relationship is one informational link between two people.
//
// It carries no access field, no capability, and no "can view" flag, because
// there is nothing of that kind to carry.
type Relationship struct {
	ID           int64
	FromPersonID int64
	ToPersonID   int64
	Kind         string

	// Context is the officer's restricted note explaining the link. It is
	// never returned by a member-facing surface — not the directory, not the
	// member profile — because "he is her carer after the stroke" is a
	// sentence a member wrote to an officer in confidence, not directory data.
	Context string

	CreatedBy int64
	CreatedAt string
	UpdatedAt string

	ArchivedAt    string
	ArchivedBy    int64
	ArchiveReason string

	Version int64

	// Direction and the Other* fields are filled in only by the per-person
	// listings, where "the other person" is well defined. Direction is
	// relative to the person asked about.
	Direction        string
	OtherPersonID    int64
	OtherDisplayName string
	OtherCallSign    string
}

// Active reports whether the relationship is current rather than archived.
func (r Relationship) Active() bool { return r.ArchivedAt == "" }

// CreateParams describes a relationship to record.
type CreateParams struct {
	FromPersonID int64
	ToPersonID   int64
	Kind         string
	Context      string
}

// Create records one informational relationship.
//
// It verifies both people exist, which keeps a typo from creating a link to a
// person ID that is not there. It does not verify, look up, or create anything
// about access: a relationship between two people says nothing about which
// accounts may read either record.
func (s *Service) Create(ctx context.Context, p *authz.Principal, params CreateParams) (Relationship, error) {
	kind, err := normalizeKind(params.Kind)
	if err != nil {
		return Relationship{}, err
	}
	note, err := normalizeContext(params.Context)
	if err != nil {
		return Relationship{}, err
	}
	if params.FromPersonID == params.ToPersonID {
		return Relationship{}, ErrSelfRelationship
	}

	var out Relationship
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		for _, id := range []int64{params.FromPersonID, params.ToPersonID} {
			if _, err := q.GetPerson(ctx, id); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return ErrUnknownPerson
				}
				return err
			}
		}

		row, err := q.CreatePersonRelationship(ctx, sqlcgen.CreatePersonRelationshipParams{
			FromPersonID: params.FromPersonID,
			ToPersonID:   params.ToPersonID,
			Kind:         kind,
			Context:      nullString(note),
			CreatedBy:    nullInt64(actorID(p)),
		})
		if err != nil {
			// ux_person_relationship_active from 0009 refuses a second active
			// row for the same pair and kind.
			if isUniqueViolation(err) {
				return ErrDuplicate
			}
			return err
		}
		out = relationshipFrom(row)
		return nil
	})
	if err != nil {
		return Relationship{}, err
	}
	return out, nil
}

// Get returns one relationship, archived ones included.
func (s *Service) Get(ctx context.Context, p *authz.Principal, id int64) (Relationship, error) {
	row, err := s.Q.GetPersonRelationship(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Relationship{}, ErrNotFound
		}
		return Relationship{}, err
	}
	return relationshipFrom(row), nil
}

// ListForPerson returns the person's current relationships in both directions.
//
// Archived rows are excluded here and included by ListHistoryForPerson, so the
// everyday officer view is "who is related to this person now" without a caller
// having to filter former spouses out by hand.
func (s *Service) ListForPerson(ctx context.Context, p *authz.Principal, personID int64) ([]Relationship, error) {
	rows, err := s.Q.ListRelationshipsForPerson(ctx, personID)
	if err != nil {
		return nil, err
	}
	out := make([]Relationship, 0, len(rows))
	for _, r := range rows {
		rel := Relationship{
			ID:               r.RelationshipID,
			FromPersonID:     r.FromPersonID,
			ToPersonID:       r.ToPersonID,
			Kind:             r.Kind,
			Context:          r.Context.String,
			CreatedAt:        r.CreatedAt,
			Version:          r.Version,
			Direction:        r.Direction,
			OtherDisplayName: r.OtherDisplayName,
			OtherCallSign:    r.OtherCallSign.String,
		}
		rel.OtherPersonID = otherPerson(personID, r.FromPersonID, r.ToPersonID)
		out = append(out, rel)
	}
	return out, nil
}

// ListHistoryForPerson returns every relationship ever recorded for a person,
// archived rows included, so "who was recorded as related to whom, and when did
// that change" stays answerable after a household changes.
func (s *Service) ListHistoryForPerson(ctx context.Context, p *authz.Principal, personID int64) ([]Relationship, error) {
	rows, err := s.Q.ListRelationshipHistoryForPerson(ctx, personID)
	if err != nil {
		return nil, err
	}
	out := make([]Relationship, 0, len(rows))
	for _, r := range rows {
		rel := Relationship{
			ID:               r.RelationshipID,
			FromPersonID:     r.FromPersonID,
			ToPersonID:       r.ToPersonID,
			Kind:             r.Kind,
			Context:          r.Context.String,
			CreatedBy:        r.CreatedBy.Int64,
			CreatedAt:        r.CreatedAt,
			UpdatedAt:        r.UpdatedAt,
			ArchivedAt:       r.ArchivedAt.String,
			ArchivedBy:       r.ArchivedBy.Int64,
			ArchiveReason:    r.ArchiveReason.String,
			Version:          r.Version,
			Direction:        r.Direction,
			OtherDisplayName: r.OtherDisplayName,
			OtherCallSign:    r.OtherCallSign.String,
		}
		rel.OtherPersonID = otherPerson(personID, r.FromPersonID, r.ToPersonID)
		out = append(out, rel)
	}
	return out, nil
}

// UpdateParams describes a correction to a recorded relationship.
//
// The two ends are not updatable. Recording "A is married to B" and then
// editing it into "A is married to C" would silently rewrite history under one
// row and one audit trail; archive the first and create the second instead.
type UpdateParams struct {
	Kind            string
	Context         string
	ExpectedVersion int64
}

// Update corrects the kind or context of a current relationship.
//
// ExpectedVersion is required. Two officers tidying the same household at the
// same meeting is an ordinary occurrence, and the loser of that race must be
// told rather than have their read-then-write silently overwrite the winner.
func (s *Service) Update(ctx context.Context, p *authz.Principal, id int64, params UpdateParams) (Relationship, error) {
	kind, err := normalizeKind(params.Kind)
	if err != nil {
		return Relationship{}, err
	}
	note, err := normalizeContext(params.Context)
	if err != nil {
		return Relationship{}, err
	}

	var out Relationship
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		current, err := q.GetPersonRelationship(ctx, id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if current.ArchivedAt.Valid {
			return ErrArchived
		}

		row, err := q.UpdatePersonRelationship(ctx, sqlcgen.UpdatePersonRelationshipParams{
			Kind:    kind,
			Context: nullString(note),
			ID:      id,
			Version: params.ExpectedVersion,
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// The row exists and was current a moment ago, so the version
				// moved under us.
				return db.ErrStale
			}
			// Correcting the kind can collide with an existing active row for
			// the same pair, which the partial unique index refuses.
			if isUniqueViolation(err) {
				return ErrDuplicate
			}
			return err
		}
		out = relationshipFrom(row)
		return nil
	})
	if err != nil {
		return Relationship{}, err
	}
	return out, nil
}

// ArchiveParams describes an archival.
type ArchiveParams struct {
	Reason          string
	ExpectedVersion int64
}

// Archive ends a relationship without deleting it.
//
// There is no delete. A relationship that stops being true — a divorce, a child
// moving out — is a fact that happened at a time, and an officer looking at a
// request from last spring needs to see the household as it was recorded then.
func (s *Service) Archive(ctx context.Context, p *authz.Principal, id int64, params ArchiveParams, now time.Time) (Relationship, error) {
	var out Relationship
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		current, err := q.GetPersonRelationship(ctx, id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if current.ArchivedAt.Valid {
			return ErrArchived
		}

		row, err := q.ArchivePersonRelationship(ctx, sqlcgen.ArchivePersonRelationshipParams{
			ArchivedAt:    sql.NullString{String: now.UTC().Format(isoTimestamp), Valid: true},
			ArchivedBy:    nullInt64(actorID(p)),
			ArchiveReason: nullString(strings.TrimSpace(params.Reason)),
			ID:            id,
			Version:       params.ExpectedVersion,
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return db.ErrStale
			}
			return err
		}
		out = relationshipFrom(row)
		return nil
	})
	if err != nil {
		return Relationship{}, err
	}
	return out, nil
}

// --- internals ---

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

// ValidKind reports whether kind is in the checked-in vocabulary.
func ValidKind(kind string) bool {
	return slices.Contains(KindsInOrder, kind)
}

func normalizeKind(kind string) (string, error) {
	k := strings.ToLower(strings.TrimSpace(kind))
	if !ValidKind(k) {
		return "", ErrUnknownKind
	}
	return k, nil
}

func normalizeContext(note string) (string, error) {
	n := strings.TrimSpace(note)
	if len(n) > maxContextLength {
		return "", ErrContextTooLong
	}
	return n, nil
}

func otherPerson(subject, from, to int64) int64 {
	if from == subject {
		return to
	}
	return from
}

func actorID(p *authz.Principal) int64 {
	if p == nil {
		return 0
	}
	return p.UserID
}

func relationshipFrom(row sqlcgen.PersonRelationship) Relationship {
	return Relationship{
		ID:            row.ID,
		FromPersonID:  row.FromPersonID,
		ToPersonID:    row.ToPersonID,
		Kind:          row.Kind,
		Context:       row.Context.String,
		CreatedBy:     row.CreatedBy.Int64,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
		ArchivedAt:    row.ArchivedAt.String,
		ArchivedBy:    row.ArchivedBy.Int64,
		ArchiveReason: row.ArchiveReason.String,
		Version:       row.Version,
	}
}

func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func nullInt64(v int64) sql.NullInt64 {
	if v == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: v, Valid: true}
}

// isUniqueViolation reports whether err is a uniqueness failure. It matches on
// the driver's message because the SQLite driver does not expose a typed code.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "UNIQUE")
}
