// Package directory serves the private member directory (bcars-portal-4ux.7).
//
// Two separate questions decide what a caller sees, and conflating them is the
// mistake this package exists to avoid:
//
//  1. MAY THIS CALLER BROWSE AT ALL? Holding directory.read is not the answer.
//     Eligibility is an active explicit access grant to a person whose
//     membership is an active approved FULL membership. An Associate holds the
//     capability, uses their own profile, and is still refused the listing.
//
//  2. WHICH VALUES MAY THEY SEE? Decided per contact by its latest visibility
//     event, in SQL, before any row reaches this package. A value the caller
//     may not see is never selected, so no DTO field, template variable, or log
//     line can leak it.
//
// Postal addresses are not part of the Phase 3 directory at all. Neither are
// dues, payments, notes, audit records, or any administrative field: the query
// selects four columns and a base type, so there is nothing else to leak.
package directory

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/authz"
)

// DefaultLimit caps an unbounded listing.
const DefaultLimit = 50

// MaxLimit bounds one page.
//
// PrintLimit is larger because printing the roster is a primary action and a
// club-sized directory fits on a few sheets. It is still bounded: an unbounded
// query is a denial-of-service waiting for the club to grow.
const (
	MaxLimit   = 200
	PrintLimit = 1000
)

// ErrNotEligible is returned when the caller may not browse the directory.
//
// Callers must translate it to the SAME response an unknown resource produces.
// A distinct "you are not eligible" answer would tell an Associate that the
// directory exists and that others can read it, which is more than they need to
// know.
var ErrNotEligible = errors.New("directory: caller is not an active Full member")

// Entry is one directory row.
//
// Email and Phone are empty when the value is withheld OR absent. The two are
// deliberately indistinguishable; see NotShared.
type Entry struct {
	PersonID    int64
	DisplayName string
	CallSign    string
	BaseType    string
	Email       string
	Phone       string
}

// EmailShared reports whether an email may be displayed. A UI renders
// "Not shared" when this is false, without knowing which reason applies.
func (e Entry) EmailShared() bool { return e.Email != "" }

// PhoneShared reports whether a phone may be displayed.
func (e Entry) PhoneShared() bool { return e.Phone != "" }

// Page is one screen or sheet of the directory.
type Page struct {
	Entries []Entry
	// Total counts every member in the directory, not every member whose
	// contact details this caller can see. A total that varied by viewer would
	// itself disclose how many members hide their details.
	Total  int64
	Limit  int64
	Offset int64
}

// Query selects a page.
type Query struct {
	// Search matches display name or call sign, case-insensitively.
	Search string
	Limit  int64
	Offset int64
	// Print raises the page bound so a whole club-sized roster prints as one
	// sheet. Screen and print consume the identical filtered result; print is
	// not a second, laxer path.
	Print bool
}

// Service reads the directory.
type Service struct {
	DB *sql.DB
	Q  *sqlcgen.Queries
}

// NewService creates a directory service over database.
func NewService(database *sql.DB) *Service {
	return &Service{DB: database, Q: sqlcgen.New(database)}
}

// Eligible reports whether the caller may browse the directory.
//
// It is evaluated on every call rather than cached on the session, so revoking
// a grant or ending a membership takes effect on the caller's next request
// instead of whenever they next sign in.
func (s *Service) Eligible(ctx context.Context, p *authz.Principal) (bool, error) {
	if p == nil || p.UserID == 0 {
		return false, nil
	}
	n, err := s.Q.CountDirectoryEligibleGrants(ctx, p.UserID)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// List returns one page of the directory, or ErrNotEligible.
func (s *Service) List(ctx context.Context, p *authz.Principal, q Query) (Page, error) {
	eligible, err := s.Eligible(ctx, p)
	if err != nil {
		return Page{}, err
	}
	if !eligible {
		return Page{}, ErrNotEligible
	}

	limit := q.Limit
	max := int64(MaxLimit)
	if q.Print {
		max = PrintLimit
		if limit <= 0 {
			limit = PrintLimit
		}
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > max {
		limit = max
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}

	search := searchFilter(q.Search)

	total, err := s.Q.CountDirectoryEntries(ctx, search)
	if err != nil {
		return Page{}, err
	}

	rows, err := s.Q.ListDirectoryEntries(ctx, sqlcgen.ListDirectoryEntriesParams{
		Search:     search,
		PageLimit:  limit,
		PageOffset: offset,
	})
	if err != nil {
		return Page{}, err
	}

	entries := make([]Entry, 0, len(rows))
	for _, r := range rows {
		entries = append(entries, Entry{
			PersonID:    r.PersonID,
			DisplayName: r.DisplayName,
			CallSign:    r.CallSign.String,
			BaseType:    r.BaseType,
			Email:       r.Email,
			Phone:       r.Phone,
		})
	}

	return Page{Entries: entries, Total: total, Limit: limit, Offset: offset}, nil
}

// searchFilter converts an optional search term into the interface{} shape
// sqlc emits for sqlc.narg, treating blank input as no filter.
func searchFilter(s string) interface{} {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return s
}
