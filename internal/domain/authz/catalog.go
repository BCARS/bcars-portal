// Package authz provides the capability catalog and authorization policy engine.
//
// The catalog is the single source of truth for all known capabilities. The
// seed migration (WS3.2) must mirror this list exactly. The policy engine
// (WS4.6) consults the catalog to validate that every operation registered in
// httpapi declares a real capability.
package authz

// Category groups capabilities by domain.
const (
	CategoryMembership = "membership"
	CategoryTreasury   = "treasury"
	CategoryAudit      = "audit"
	CategorySystem     = "system"
)

// AI tool eligibility values for capability catalog entries.
const (
	AIEligibilityNever    = "never"
	AIEligibilityReadOnly = "read-only"
	AIEligibilityCurated  = "curated"
)

// Capability is a discrete permission that can be granted to a role.
type Capability struct {
	Code              string
	Description       string
	Category          string
	AIToolEligibility string
}

// All known capabilities. Append here when adding new features; remove only
// after dropping the corresponding seed migration row and role_capabilities rows.
var All = [...]Capability{
	// System
	{Code: "session.self.read", Category: CategorySystem,
		Description:       "Read own session and effective capabilities.",
		AIToolEligibility: AIEligibilityReadOnly},
	{Code: "role.grant", Category: CategorySystem,
		Description:       "Grant or revoke roles and direct capabilities.",
		AIToolEligibility: AIEligibilityNever},
	{Code: "user.invite", Category: CategorySystem,
		Description:       "Invite a new officer or user.",
		AIToolEligibility: AIEligibilityNever},
	{Code: "integration.config.write", Category: CategorySystem,
		Description:       "Manage non-secret integration settings.",
		AIToolEligibility: AIEligibilityNever},
	{Code: "system.admin", Category: CategorySystem,
		Description:       "Bootstrap and low-level administrative operations.",
		AIToolEligibility: AIEligibilityNever},

	// Membership
	{Code: "member.read", Category: CategoryMembership,
		Description:       "Read canonical member data (server-filtered by role).",
		AIToolEligibility: AIEligibilityReadOnly},
	{Code: "member.export", Category: CategoryMembership,
		Description:       "Export member data to CSV or JSON.",
		AIToolEligibility: AIEligibilityCurated},
	{Code: "member.create", Category: CategoryMembership,
		Description:       "Create a person and pending membership.",
		AIToolEligibility: AIEligibilityCurated},
	{Code: "member.update", Category: CategoryMembership,
		Description:       "Update person fields.",
		AIToolEligibility: AIEligibilityCurated},
	{Code: "member.deactivate", Category: CategoryMembership,
		Description:       "Deactivate or reactivate a member.",
		AIToolEligibility: AIEligibilityNever},
	{Code: "contact_method.write", Category: CategoryMembership,
		Description:       "Add, edit, make-primary, or archive contact methods.",
		AIToolEligibility: AIEligibilityCurated},
	{Code: "sharing_pref.write.officer", Category: CategoryMembership,
		Description:       "Change directory visibility or ACS-ARES sharing prefs on behalf of a member.",
		AIToolEligibility: AIEligibilityNever},
	{Code: "membership.approve", Category: CategoryMembership,
		Description:       "Approve or reject membership applications and set the base type.",
		AIToolEligibility: AIEligibilityNever},
	{Code: "membership.lifecycle", Category: CategoryMembership,
		Description:       "Change membership lifecycle to inactive, resigned, or deceased.",
		AIToolEligibility: AIEligibilityNever},
	{Code: "fcc.verify", Category: CategoryMembership,
		Description:       "Record or revoke an FCC license verification.",
		AIToolEligibility: AIEligibilityCurated},
	{Code: "honorary.grant", Category: CategoryMembership,
		Description:       "Create, edit, expire, or revoke honorary grants.",
		AIToolEligibility: AIEligibilityNever},
	{Code: "notes.write.officer", Category: CategoryMembership,
		Description:       "Write officer-visibility notes.",
		AIToolEligibility: AIEligibilityCurated},
	{Code: "import.upload", Category: CategoryMembership,
		Description:       "Upload and stage an import file for review.",
		AIToolEligibility: AIEligibilityNever},
	{Code: "import.commit", Category: CategoryMembership,
		Description:       "Commit a reconciled import run to canonical data.",
		AIToolEligibility: AIEligibilityNever},

	// Treasury
	{Code: "notes.write.treasurer", Category: CategoryTreasury,
		Description:       "Write treasurer-visibility notes.",
		AIToolEligibility: AIEligibilityNever},
	{Code: "notes.read.treasurer", Category: CategoryTreasury,
		Description:       "Read treasurer-visibility notes.",
		AIToolEligibility: AIEligibilityNever},
	{Code: "dues.read", Category: CategoryTreasury,
		Description:       "Read safe dues standing, rates, and suggestions.",
		AIToolEligibility: AIEligibilityReadOnly},
	{Code: "dues.rate.manage", Category: CategoryTreasury,
		Description:       "Create or revise the dues rate for a year.",
		AIToolEligibility: AIEligibilityNever},
	{Code: "coverage.read", Category: CategoryTreasury,
		Description:       "Read the append-only paid-through coverage history.",
		AIToolEligibility: AIEligibilityReadOnly},
	{Code: "coverage.adjust", Category: CategoryTreasury,
		Description:       "Record an independent paid-through adjustment.",
		AIToolEligibility: AIEligibilityNever},
	{Code: "payment.read", Category: CategoryTreasury,
		Description:       "Read payment detail, references, receipts, and corrections.",
		AIToolEligibility: AIEligibilityNever},
	{Code: "payment.batch.manage", Category: CategoryTreasury,
		Description:       "Open, edit, and abandon draft payment batches.",
		AIToolEligibility: AIEligibilityNever},
	{Code: "payment.post", Category: CategoryTreasury,
		Description:       "Post a batch or a single payment to the ledger.",
		AIToolEligibility: AIEligibilityNever},
	{Code: "payment.correct", Category: CategoryTreasury,
		Description:       "Correct a posted payment by reversal and replacement.",
		AIToolEligibility: AIEligibilityNever},
	{Code: "payment.export", Category: CategoryTreasury,
		Description:       "Export treasury records to CSV.",
		AIToolEligibility: AIEligibilityNever},
	{Code: "dues.worksheet.manage", Category: CategoryTreasury,
		Description:       "Generate and read renewal worksheet runs.",
		AIToolEligibility: AIEligibilityNever},

	// Audit
	{Code: "audit.read", Category: CategoryAudit,
		Description:       "Search and read audit events.",
		AIToolEligibility: AIEligibilityReadOnly},
}

// ByCode returns the Capability with the given code. The second return value
// is false when the code is not found.
func ByCode(code string) (Capability, bool) {
	for _, c := range All {
		if c.Code == code {
			return c, true
		}
	}
	return Capability{}, false
}

// Codes returns the set of all known capability codes, for startup validation.
func Codes() map[string]struct{} {
	m := make(map[string]struct{}, len(All))
	for _, c := range All {
		m[c.Code] = struct{}{}
	}
	return m
}
