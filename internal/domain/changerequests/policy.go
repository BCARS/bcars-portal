package changerequests

// The sensitivity policy and the adapter map are checked-in, tested artifacts
// rather than logic scattered through a handler or a template
// (bcars-portal-4ux.3).
//
// Both answer questions a reviewer's UI must not answer for itself:
//
//   - which operations change something consequential enough that one officer
//     should not be able to both request and approve it; and
//   - which operations can be applied at all.
//
// A table is checkable. Fifteen `if` statements spread across a handler are
// not, which is how the confirmation control came to be declared and never
// enforced.

// Sensitivity classes an item may carry.
//
// MinimumSensitivity is the floor for an operation: a submitter may mark
// something MORE sensitive than the policy requires, never less. That direction
// matters — a member who says "this one is sensitive" should be believed, but a
// submitter must not be able to downgrade a call-sign change into an ordinary
// edit by saying so.
var MinimumSensitivity = map[string]string{
	// A call sign is the member's licensed identity and the club's directory
	// key. Changing it on someone's say-so is how a directory entry silently
	// becomes someone else's.
	"person.call_sign.set": SensitivitySensitive,

	// A display name is how the member is addressed. Wrong is embarrassing,
	// not dangerous.
	"person.display_name.set": SensitivityOrdinary,

	// Contact changes are the everyday correction. Ordinary by default; an
	// officer may still mark one sensitive when something feels wrong.
	"contact_method.add":         SensitivityOrdinary,
	"contact_method.update":      SensitivityOrdinary,
	"contact_method.archive":     SensitivityOrdinary,
	"contact_method.set_primary": SensitivityOrdinary,

	// Sharing preferences decide who can see a member's details. Getting one
	// wrong publishes something the member asked to keep private, which is not
	// undoable in the way a wrong phone number is.
	"contact_method.visibility.set": SensitivitySensitive,
	"sharing_pref.acs_ares.set":     SensitivitySensitive,

	// Relationships are informational and confer nothing (ADR-0010), so they
	// are ordinary. They are also not appliable yet; see Adapters.
	"relationship.add":     SensitivityOrdinary,
	"relationship.correct": SensitivityOrdinary,

	// `other` can never be approved at all, so its class is academic.
	OpOther: SensitivityOrdinary,
}

// EffectiveSensitivity returns the class an item is reviewed under: the higher
// of what the policy requires and what the submitter declared.
func EffectiveSensitivity(operation, declared string) string {
	if declared == SensitivitySensitive {
		return SensitivitySensitive
	}
	if MinimumSensitivity[operation] == SensitivitySensitive {
		return SensitivitySensitive
	}
	return SensitivityOrdinary
}

// AdapterKind names the domain service call an approved item is applied
// through. There is deliberately no "generic update" kind: an operation either
// maps to an explicit adapter or cannot be applied.
type AdapterKind string

const (
	AdapterPersonUpdate      AdapterKind = "person.update"
	AdapterContactCreate     AdapterKind = "contact_method.create"
	AdapterContactUpdate     AdapterKind = "contact_method.update"
	AdapterContactArchive    AdapterKind = "contact_method.archive"
	AdapterContactSetPrimary AdapterKind = "contact_method.set_primary"
	AdapterContactVisibility AdapterKind = "contact_method.visibility"
	AdapterSharingPreference AdapterKind = "sharing_pref.acs_ares"
	AdapterNone              AdapterKind = ""
)

// Adapters maps each supported operation to the domain call that applies it.
//
// An operation absent from this map, or mapped to AdapterNone, cannot be
// approved. That is enforced rather than documented: approval is refused with
// ErrNoAdapter before anything is recorded, so an item can never sit approved
// and unapplied.
//
// Relationship operations are intentionally unmapped. The relationship service
// belongs to bcars-portal-4ux.8; capturing the proposal now and refusing to
// apply it is honest, whereas inventing a half-adapter here would put
// relationship semantics in two packages.
var Adapters = map[string]AdapterKind{
	"person.display_name.set":       AdapterPersonUpdate,
	"person.call_sign.set":          AdapterPersonUpdate,
	"contact_method.add":            AdapterContactCreate,
	"contact_method.update":         AdapterContactUpdate,
	"contact_method.archive":        AdapterContactArchive,
	"contact_method.set_primary":    AdapterContactSetPrimary,
	"contact_method.visibility.set": AdapterContactVisibility,
	"sharing_pref.acs_ares.set":     AdapterSharingPreference,

	"relationship.add":     AdapterNone,
	"relationship.correct": AdapterNone,
	OpOther:                AdapterNone,
}

// CanApply reports whether an approved item of this operation can be applied.
func CanApply(operation string) bool {
	return Adapters[operation] != AdapterNone
}

// RequiresTargetID reports whether an operation names an existing resource it
// changes. An add creates something new and has no target; everything else
// edits a row that must already exist.
func RequiresTargetID(operation string) bool {
	switch Adapters[operation] {
	case AdapterContactUpdate, AdapterContactArchive,
		AdapterContactSetPrimary, AdapterContactVisibility:
		return true
	default:
		return false
	}
}

// RequiresValue reports whether an operation needs a proposed value.
//
// Archiving and making-primary name a target and change nothing else, so
// demanding a value would force submitters to invent a meaningless one.
func RequiresValue(operation string) bool {
	switch Adapters[operation] {
	case AdapterContactArchive, AdapterContactSetPrimary:
		return false
	case AdapterNone:
		// `other` is free prose and relationship proposals describe themselves.
		return operation != OpOther
	default:
		return true
	}
}
