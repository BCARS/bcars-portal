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

// Contact is one shared contact value.
type Contact struct {
	Value string
	// Label distinguishes a member's numbers from each other, e.g. "home" and
	// "mobile". It matters here: a member whose mobile has no signal at home
	// needs the reader to know which number is which.
	Label string
	// Primary marks the member's main contact of that kind. Primary values are
	// listed first.
	Primary bool
}

// Entry is one directory row.
//
// Emails and Phones carry EVERY value this member shares, because a member may
// share more than one and both can matter. An empty list means the member
// shares none of that kind — withheld and absent are deliberately
// indistinguishable.
type Entry struct {
	PersonID    int64
	DisplayName string
	CallSign    string
	BaseType    string
	Emails      []Contact
	Phones      []Contact
}

// EmailShared reports whether any email may be displayed. A UI renders
// "Not shared" when this is false, without knowing which reason applies.
func (e Entry) EmailShared() bool { return len(e.Emails) > 0 }

// PhoneShared reports whether any phone may be displayed.
func (e Entry) PhoneShared() bool { return len(e.Phones) > 0 }

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

// Membership-type filters. More query options are expected; each one belongs
// in the SQL population predicate so the total always matches what is listed.
const (
	FilterAll       = ""
	FilterFull      = "full"
	FilterAssociate = "associate"
)

// ValidFilter reports whether a membership-type filter is known.
func ValidFilter(f string) bool {
	switch f {
	case FilterAll, FilterFull, FilterAssociate:
		return true
	}
	return false
}

// Query selects a page.
type Query struct {
	// Search matches display name or call sign, case-insensitively.
	Search string
	// BaseType narrows to one membership type. Empty lists both.
	BaseType string
	Limit    int64
	Offset   int64
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
	baseType := searchFilter(q.BaseType)

	total, err := s.Q.CountDirectoryEntries(ctx, sqlcgen.CountDirectoryEntriesParams{
		BaseType: baseType,
		Search:   search,
	})
	if err != nil {
		return Page{}, err
	}

	rows, err := s.Q.ListDirectoryEntries(ctx, sqlcgen.ListDirectoryEntriesParams{
		BaseType:   baseType,
		Search:     search,
		PageLimit:  limit,
		PageOffset: offset,
	})
	if err != nil {
		return Page{}, err
	}

	// One query for the same page, returning every shared contact. Scoped by
	// the same predicate and bounds, so it covers exactly these members.
	contacts, err := s.Q.ListDirectoryContacts(ctx, sqlcgen.ListDirectoryContactsParams{
		BaseType:   baseType,
		Search:     search,
		PageLimit:  limit,
		PageOffset: offset,
	})
	if err != nil {
		return Page{}, err
	}

	emails := map[int64][]Contact{}
	phones := map[int64][]Contact{}
	for _, c := range contacts {
		entry := Contact{Value: c.Value, Label: c.Label.String, Primary: c.IsPrimary != 0}
		switch c.Kind {
		case "email":
			emails[c.PersonID] = append(emails[c.PersonID], entry)
		case "phone":
			phones[c.PersonID] = append(phones[c.PersonID], entry)
		}
	}

	entries := make([]Entry, 0, len(rows))
	for _, r := range rows {
		entries = append(entries, Entry{
			PersonID:    r.PersonID,
			DisplayName: r.DisplayName,
			CallSign:    r.CallSign.String,
			BaseType:    r.BaseType,
			Emails:      emails[r.PersonID],
			Phones:      phones[r.PersonID],
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
