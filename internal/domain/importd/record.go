// Package importd provides Groups.io import parsing, normalization, matching,
// and staging logic for the BCARS membership portal.
//
// The package name uses "importd" (import-domain) because "import" is a Go
// reserved word.
package importd

// RawRecord is a single row parsed from a Groups.io export (JSON or CSV),
// with field names normalized to a canonical set.
type RawRecord struct {
	ExternalID        string // Groups.io row id (JSON only; empty for CSV)
	ContactName       string
	CallSign          string
	CurrentUntil      string // raw date string, e.g. "12/31/2026"
	Note              string
	MembershipType    string // "Full", "Associate", "Honorary"
	Class             string // FCC license class
	Phone             string
	Email             string
	StreetAddress     string
	City              string
	PostalCode        string
	StateProvince     string
	VolunteerExaminer string // "true"/"false"/"checked"/""
}

// NormalizedRecord is a RawRecord after normalization.
type NormalizedRecord struct {
	ExternalID        string
	DisplayName       string // trimmed ContactName
	SortName          string // "Last, First" or same as DisplayName
	CallSign          string // uppercased, trimmed
	CurrentUntil      string // ISO date or empty
	CurrentUntilFlag  string // "", "sentinel_null", "lifetime_known", "lifetime_unknown"
	Note              string
	MembershipType    string // "Full", "Associate", "Honorary"
	BaseType          string // "full", "associate", or "" (unknown for some Honorary)
	LicenseClass      string // lowercased
	Phone             string // digits only, or original if unparseable
	PhoneValid        bool
	Email             string // lowercased, trimmed
	StreetAddress     string
	City              string
	PostalCode        string
	StateProvince     string
	VolunteerExaminer bool
	RequiresManual    bool
	ManualReason      string
}

// KnownLifetimeExternalIDs are the two Groups.io row IDs that are confirmed
// lifetime honorary members per the design spec. These get auto-proposed as
// lifetime honorary Associate grants.
var KnownLifetimeExternalIDs = map[string]bool{
	"900001": true, // Lifetimetest One (synthetic fixture)
	"900002": true, // Lifetimetest Two (synthetic fixture)
}
