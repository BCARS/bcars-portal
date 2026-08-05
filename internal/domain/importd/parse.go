package importd

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Expected CSV header columns in order.
var expectedCSVHeaders = []string{
	"Contact Name", "Call Sign", "Current Until", "Note",
	"Membership Type", "Class", "Phone", "Email",
	"Street Address", "City", "Postal Code", "State/Province",
	"Volunteer Examiner",
}

// ParseCSV parses a Groups.io CSV contact export.
func ParseCSV(r io.Reader) ([]RawRecord, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1 // allow variable fields

	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("importd: read CSV header: %w", err)
	}

	// Validate header.
	if len(header) < len(expectedCSVHeaders) {
		return nil, fmt.Errorf("importd: CSV has %d columns, expected at least %d", len(header), len(expectedCSVHeaders))
	}
	for i, want := range expectedCSVHeaders {
		got := strings.TrimSpace(header[i])
		if !strings.EqualFold(got, want) {
			return nil, fmt.Errorf("importd: CSV column %d: expected %q, got %q", i, want, got)
		}
	}

	var records []RawRecord
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("importd: read CSV row: %w", err)
		}

		rec := RawRecord{}
		if len(row) > 0 {
			rec.ContactName = strings.TrimSpace(row[0])
		}
		if len(row) > 1 {
			rec.CallSign = strings.TrimSpace(row[1])
		}
		if len(row) > 2 {
			rec.CurrentUntil = strings.TrimSpace(row[2])
		}
		if len(row) > 3 {
			rec.Note = strings.TrimSpace(row[3])
		}
		if len(row) > 4 {
			rec.MembershipType = strings.TrimSpace(row[4])
		}
		if len(row) > 5 {
			rec.Class = strings.TrimSpace(row[5])
		}
		if len(row) > 6 {
			rec.Phone = strings.TrimSpace(row[6])
		}
		if len(row) > 7 {
			rec.Email = strings.TrimSpace(row[7])
		}
		if len(row) > 8 {
			rec.StreetAddress = strings.TrimSpace(row[8])
		}
		if len(row) > 9 {
			rec.City = strings.TrimSpace(row[9])
		}
		if len(row) > 10 {
			rec.PostalCode = strings.TrimSpace(row[10])
		}
		if len(row) > 11 {
			rec.StateProvince = strings.TrimSpace(row[11])
		}
		if len(row) > 12 {
			rec.VolunteerExaminer = strings.TrimSpace(row[12])
		}
		records = append(records, rec)
	}

	return records, nil
}

// Groups.io JSON table export structure.
type groupsioExport struct {
	Table struct {
		Columns []groupsioColumn `json:"columns"`
	} `json:"table"`
	Rows []groupsioRow `json:"rows"`
}

type groupsioColumn struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type groupsioRow struct {
	ID   int64         `json:"id"`
	Vals []groupsioVal `json:"vals"`
}

type groupsioVal struct {
	ColID   int    `json:"col_id"`
	Text    string `json:"text"`
	Checked *bool  `json:"checked,omitempty"`
}

// ParseJSON parses a Groups.io JSON table export.
func ParseJSON(r io.Reader) ([]RawRecord, error) {
	var export groupsioExport
	if err := json.NewDecoder(r).Decode(&export); err != nil {
		return nil, fmt.Errorf("importd: decode JSON: %w", err)
	}

	// Build column ID → name map.
	colNames := make(map[int]string, len(export.Table.Columns))
	for _, col := range export.Table.Columns {
		colNames[col.ID] = col.Name
	}

	// Validate required columns exist.
	required := map[string]bool{
		"Contact Name": false, "Membership Type": false, "Email": false,
	}
	for _, name := range colNames {
		if _, ok := required[name]; ok {
			required[name] = true
		}
	}
	for name, found := range required {
		if !found {
			return nil, fmt.Errorf("importd: JSON missing required column %q", name)
		}
	}

	records := make([]RawRecord, 0, len(export.Rows))
	for _, row := range export.Rows {
		rec := RawRecord{
			ExternalID: strconv.FormatInt(row.ID, 10),
		}

		for _, val := range row.Vals {
			colName, ok := colNames[val.ColID]
			if !ok {
				continue
			}
			text := strings.TrimSpace(val.Text)

			switch colName {
			case "Contact Name":
				rec.ContactName = text
			case "Call Sign":
				rec.CallSign = text
			case "Current Until":
				rec.CurrentUntil = text
			case "Note":
				rec.Note = text
			case "Membership Type":
				rec.MembershipType = text
			case "Class":
				rec.Class = text
			case "Phone":
				rec.Phone = text
			case "Email":
				rec.Email = text
			case "Street Address":
				rec.StreetAddress = text
			case "City":
				rec.City = text
			case "Postal Code":
				rec.PostalCode = text
			case "State/Province":
				rec.StateProvince = text
			case "Volunteer Examiner":
				if val.Checked != nil {
					if *val.Checked {
						rec.VolunteerExaminer = "true"
					} else {
						rec.VolunteerExaminer = "false"
					}
				} else {
					rec.VolunteerExaminer = text
				}
			}
		}
		records = append(records, rec)
	}

	return records, nil
}
