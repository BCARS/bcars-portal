package importd

import (
	"regexp"
	"strings"
	"unicode"
)

var nonDigitRe = regexp.MustCompile(`[^\d]`)

// Normalize converts a RawRecord into a NormalizedRecord with canonical
// field values and flags for manual review.
func Normalize(raw RawRecord) NormalizedRecord {
	n := NormalizedRecord{
		ExternalID:    raw.ExternalID,
		DisplayName:   raw.ContactName,
		Note:          raw.Note,
		StreetAddress: raw.StreetAddress,
		City:          raw.City,
		PostalCode:    raw.PostalCode,
		StateProvince: raw.StateProvince,
	}

	// Sort name: try "Last, First" from "First Last".
	n.SortName = normalizeSortName(raw.ContactName)

	// Call sign: uppercase, trimmed.
	n.CallSign = strings.ToUpper(strings.TrimSpace(raw.CallSign))

	// Email: lowercase, trimmed.
	n.Email = strings.ToLower(strings.TrimSpace(raw.Email))

	// Phone normalization.
	n.Phone, n.PhoneValid = normalizePhone(raw.Phone)

	// License class: lowercase.
	n.LicenseClass = strings.ToLower(strings.TrimSpace(raw.Class))

	// Membership type: case-fold to canonical.
	n.MembershipType, n.BaseType = normalizeMembershipType(raw.MembershipType)

	// Volunteer examiner: checkbox to bool.
	n.VolunteerExaminer = normalizeCheckbox(raw.VolunteerExaminer)

	// Current Until: date + sentinel handling.
	n.CurrentUntil, n.CurrentUntilFlag = normalizeCurrentUntil(raw.CurrentUntil, raw.ExternalID)

	// Honorary special handling.
	if n.MembershipType == "Honorary" {
		if n.CurrentUntilFlag == "lifetime_known" {
			// Known lifetime: auto-propose as lifetime honorary Associate.
			n.BaseType = "associate"
		} else if n.BaseType == "" {
			// Honorary with no base type → requires manual.
			n.RequiresManual = true
			n.ManualReason = "honorary_type_unspecified"
		}
	}

	// Lifetime-like date on a non-known row.
	if n.CurrentUntilFlag == "lifetime_unknown" {
		n.RequiresManual = true
		n.ManualReason = "lifetime_like_date_needs_confirmation"
	}

	return n
}

func normalizeSortName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}

	parts := strings.Fields(name)
	if len(parts) < 2 {
		return name
	}

	// "First Last" → "Last, First"
	last := parts[len(parts)-1]
	first := strings.Join(parts[:len(parts)-1], " ")
	return last + ", " + first
}

func normalizePhone(phone string) (string, bool) {
	phone = strings.TrimSpace(phone)
	if phone == "" {
		return "", true // empty is valid (optional field)
	}

	digits := nonDigitRe.ReplaceAllString(phone, "")
	if len(digits) < 7 {
		// Not enough digits to be a valid phone number.
		return phone, false
	}

	// Check that the original has mostly digit-like content.
	digitCount := 0
	for _, r := range phone {
		if unicode.IsDigit(r) {
			digitCount++
		}
	}
	if digitCount < 7 {
		return phone, false
	}

	return digits, true
}

func normalizeMembershipType(mt string) (membershipType, baseType string) {
	mt = strings.TrimSpace(mt)
	switch strings.ToLower(mt) {
	case "full":
		return "Full", "full"
	case "associate":
		return "Associate", "associate"
	case "honorary":
		return "Honorary", "" // base type determined by officer
	default:
		return mt, ""
	}
}

func normalizeCheckbox(val string) bool {
	v := strings.ToLower(strings.TrimSpace(val))
	return v == "true" || v == "checked" || v == "1" || v == "yes"
}

func normalizeCurrentUntil(date, externalID string) (normalized, flag string) {
	date = strings.TrimSpace(date)
	if date == "" {
		return "", ""
	}

	// Sentinel: 01/01/0001 → null.
	if date == "01/01/0001" {
		return "", "sentinel_null"
	}

	// Sentinel: 12/31/2055 → lifetime check.
	if date == "12/31/2055" {
		if KnownLifetimeExternalIDs[externalID] {
			return "2055-12-31", "lifetime_known"
		}
		return "2055-12-31", "lifetime_unknown"
	}

	// Parse MM/DD/YYYY to YYYY-MM-DD.
	parts := strings.Split(date, "/")
	if len(parts) == 3 && len(parts[2]) == 4 {
		return parts[2] + "-" + parts[0] + "-" + parts[1], ""
	}

	// Already ISO format or unparseable — pass through.
	return date, ""
}
