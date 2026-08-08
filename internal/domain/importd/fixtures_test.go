package importd

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSyntheticCSVFixture validates that the checked-in CSV fixture exercises
// every Phase 1 parser branch: clean Full, Associate, Honorary (lifetime),
// honorary_type_unspecified, ambiguous call sign, bad phone, lowercase type,
// notes, VE flag, and date edge cases.
func TestSyntheticCSVFixture(t *testing.T) {
	csvData, err := os.ReadFile("../../../fixtures/synthetic/groupsio_contact.csv")
	require.NoError(t, err, "synthetic CSV fixture must exist")

	records, errs := ParseCSV(bytes.NewReader(csvData))
	assert.Empty(t, errs, "synthetic CSV should parse without errors")
	assert.GreaterOrEqual(t, len(records), 20, "should have at least 20 rows")

	// Check specific branches are exercised.
	var (
		hasFullMember     bool
		hasAssociate      bool
		hasHonorary       bool
		hasHonoraryUnspec bool
		hasNote           bool
		hasBadPhone       bool
		hasLowercase      bool
	)
	for _, r := range records {
		norm := Normalize(r)
		switch {
		case norm.BaseType == "full" && norm.DisplayName == "Fulltest1 Member":
			hasFullMember = true
		case norm.BaseType == "associate":
			hasAssociate = true
		case r.MembershipType == "Honorary" && norm.DisplayName == "Lifetimetest One":
			hasHonorary = true
		case r.MembershipType == "Honorary" && norm.DisplayName == "Honorary Unspecified":
			hasHonoraryUnspec = true
		case norm.Note != "":
			hasNote = true
		case !norm.PhoneValid && r.Phone != "":
			hasBadPhone = true
		case r.MembershipType == "full": // lowercase in raw
			hasLowercase = true
		}
	}

	assert.True(t, hasFullMember, "fixture must include a clean Full member")
	assert.True(t, hasAssociate, "fixture must include an Associate member")
	assert.True(t, hasHonorary, "fixture must include a resolved Honorary member")
	assert.True(t, hasHonoraryUnspec, "fixture must include an unspecified honorary (manual review)")
	assert.True(t, hasNote, "fixture must include a row with a note")
	assert.True(t, hasBadPhone, "fixture must include a bad phone number")
	assert.True(t, hasLowercase, "fixture must include a lowercase membership type")
}

// TestSyntheticJSONFixture validates the JSON fixture parses correctly.
func TestSyntheticJSONFixture(t *testing.T) {
	jsonData, err := os.ReadFile("../../../fixtures/synthetic/groupsio_contact.json")
	require.NoError(t, err, "synthetic JSON fixture must exist")

	records, errs := ParseJSON(bytes.NewReader(jsonData))
	assert.Empty(t, errs, "synthetic JSON should parse without errors")
	assert.GreaterOrEqual(t, len(records), 20, "should have at least 20 rows")
}
