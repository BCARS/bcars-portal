package treasury_test

import (
	"math"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bcars/bcars-portal/internal/domain/treasury"
)

// TestCentsNeverUsesFloat pins the money formatting, including the cases a
// float round trip gets wrong.
func TestCentsNeverUsesFloat(t *testing.T) {
	for _, tc := range []struct {
		cents int64
		want  string
	}{
		{0, "0.00"},
		{1, "0.01"},
		{9, "0.09"},
		{10, "0.10"},
		{99, "0.99"},
		{100, "1.00"},
		{4000, "40.00"},
		{40000, "400.00"},
		{51000, "510.00"},
		{-40000, "-400.00"},
		{-1, "-0.01"},
		{123456789, "1234567.89"},
		{math.MaxInt64 / 100 * 100, "92233720368547758.00"},
	} {
		t.Run(strconv.FormatInt(tc.cents, 10), func(t *testing.T) {
			assert.Equal(t, tc.want, treasury.Cents(tc.cents))
		})
	}
}

// TestCentsMatchesIntegerArithmetic proves the formatting agrees with plain
// integer maths across a wide sweep, which a float implementation would not.
func TestCentsMatchesIntegerArithmetic(t *testing.T) {
	for v := int64(-1000); v <= 1000; v++ {
		got := treasury.Cents(v)
		sign := ""
		abs := v
		if v < 0 {
			sign = "-"
			abs = -v
		}
		want := sign + strconv.FormatInt(abs/100, 10) + "." +
			strconv.FormatInt(abs%100/10, 10) + strconv.FormatInt(abs%10, 10)
		assert.Equal(t, want, got, "cents %d", v)
	}
}

// TestSafeCellNeutralizesFormulas proves a note or reference cannot become an
// executable cell when the treasurer opens the export in a spreadsheet.
func TestSafeCellNeutralizesFormulas(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    string
		want  string
		notes string
	}{
		{"equals", "=1+1", "'=1+1", "the classic formula injection"},
		{"plus", "+1", "'+1", ""},
		{"minus", "-1", "'-1", ""},
		{"at", "@SUM(A1)", "'@SUM(A1)", ""},
		{"tab", "\tvalue", "'\tvalue", ""},
		{"carriage return", "\rvalue", "'\rvalue", ""},
		{"hyperlink attack", `=HYPERLINK("http://evil.test","click")`, `'=HYPERLINK("http://evil.test","click")`, ""},
		{"cmd attack", "=cmd|'/c calc'!A1", "'=cmd|'/c calc'!A1", ""},
		{"ordinary text", "Paid at the meeting", "Paid at the meeting", "untouched"},
		{"empty", "", "", ""},
		{"digits", "1042", "1042", "a check number is not a formula"},
		{"internal equals", "note = paid", "note = paid", "only a leading character matters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, treasury.SafeCell(tc.in))
		})
	}
}

// TestSafeCellLeavesNegativeAmountsAlone documents a deliberate consequence:
// a negative amount rendered by Cents starts with "-", so it is quoted in the
// CSV. That is correct — the alternative is leaving a real injection vector
// open for the sake of prettier reversals.
func TestSafeCellQuotesNegativeAmounts(t *testing.T) {
	assert.Equal(t, "'-400.00", treasury.SafeCell(treasury.Cents(-40000)))
	assert.Equal(t, "400.00", treasury.SafeCell(treasury.Cents(40000)))
}
