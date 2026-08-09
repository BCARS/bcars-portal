// Package treasury provides the treasurer's history, receipt, and export reads.
//
// Everything here is restricted. A response from this package carries amounts,
// references, receipt codes, correction reasons, and treasurer notes, none of
// which belong in the safe dues-standing summary that ordinary officers see.
package treasury

import (
	"bytes"
	"encoding/csv"
	"strconv"
	"strings"
)

// Cents renders integer cents as a decimal string without ever touching a
// float. Money that round-trips through float64 comes back wrong often enough
// to matter in a ledger, and a treasurer reconciling against a bank statement
// will find it.
func Cents(v int64) string {
	sign := ""
	if v < 0 {
		sign = "-"
		v = -v
	}
	return sign + strconv.FormatInt(v/100, 10) + "." + pad2(v%100)
}

func pad2(v int64) string {
	if v < 10 {
		return "0" + strconv.FormatInt(v, 10)
	}
	return strconv.FormatInt(v, 10)
}

// formulaLeaders are the characters a spreadsheet treats as the start of a
// formula. A member whose note begins with one would otherwise become an
// executable cell in Excel or Sheets when the treasurer opens the export.
const formulaLeaders = "=+-@\t\r"

// SafeCell neutralizes a value that a spreadsheet would otherwise evaluate.
//
// The leading apostrophe is the conventional escape: spreadsheets treat the
// rest as literal text and do not display the apostrophe itself. The value is
// never altered otherwise, so a reader still sees exactly what was recorded.
func SafeCell(s string) string {
	if s == "" {
		return s
	}
	if strings.ContainsRune(formulaLeaders, rune(s[0])) {
		return "'" + s
	}
	return s
}

// Export is a rendered CSV document plus the metadata a reader needs to know
// what they are looking at.
type Export struct {
	// Filename is a suggested download name.
	Filename string
	// GeneratedAt is the UTC timestamp stamped into the document.
	GeneratedAt string
	// AppliedFilters states, in order, which filters produced these rows. An
	// export that does not say what it excluded is a liability at audit time.
	AppliedFilters []Filter
	RowCount       int
	// CSV is the rendered document.
	CSV string
}

// Filter is one applied filter, as it appears in the export header.
type Filter struct {
	Name  string
	Value string
}

// writeCSV renders a metadata block, a header row, and the data rows.
//
// The document is deterministic: the same filters over the same data produce
// byte-identical output apart from the generation timestamp, which is passed in
// rather than read from the clock so tests can pin it.
func writeCSV(generatedAt string, filters []Filter, header []string, rows [][]string) string {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)

	_ = w.Write([]string{"# BCARS treasury export"})
	_ = w.Write([]string{"# generated_at", generatedAt})
	if len(filters) == 0 {
		_ = w.Write([]string{"# filters", "none"})
	}
	for _, f := range filters {
		value := f.Value
		if value == "" {
			value = "(any)"
		}
		_ = w.Write([]string{"# filter." + f.Name, SafeCell(value)})
	}
	_ = w.Write([]string{"# row_count", strconv.Itoa(len(rows))})
	_ = w.Write(nil)

	_ = w.Write(header)
	for _, r := range rows {
		safe := make([]string, len(r))
		for i, cell := range r {
			safe[i] = SafeCell(cell)
		}
		_ = w.Write(safe)
	}
	w.Flush()
	return buf.String()
}
