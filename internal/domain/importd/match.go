package importd

import (
	"database/sql"
	"strings"
)

// MatchResult describes how a normalized record matched against existing data.
type MatchResult struct {
	PersonID  int64
	Method    string // "external_id", "call_sign", "email", "none"
	Ambiguous bool   // true if >1 candidate found
}

// Matcher finds existing persons that match an import record.
type Matcher struct {
	DB *sql.DB
}

// NewMatcher creates a Matcher backed by db.
func NewMatcher(db *sql.DB) *Matcher {
	return &Matcher{DB: db}
}

// Match runs the four-method cascade: external_id → call_sign → email → manual.
// Returns the best match result. When ambiguous, PersonID is 0 and Ambiguous is true.
func (m *Matcher) Match(rec NormalizedRecord) (MatchResult, error) {
	// 1. External ID match.
	if rec.ExternalID != "" {
		id, err := m.matchByExternalID(rec.ExternalID)
		if err != nil {
			return MatchResult{}, err
		}
		if id > 0 {
			return MatchResult{PersonID: id, Method: "external_id"}, nil
		}
	}

	// 2. Call sign match.
	if rec.CallSign != "" {
		id, err := m.matchByCallSign(rec.CallSign)
		if err != nil {
			return MatchResult{}, err
		}
		if id > 0 {
			return MatchResult{PersonID: id, Method: "call_sign"}, nil
		}
	}

	// 3. Email match.
	if rec.Email != "" {
		ids, err := m.matchByEmail(rec.Email)
		if err != nil {
			return MatchResult{}, err
		}
		if len(ids) == 1 {
			return MatchResult{PersonID: ids[0], Method: "email"}, nil
		}
		if len(ids) > 1 {
			return MatchResult{Ambiguous: true, Method: "email"}, nil
		}
	}

	// 4. No match.
	return MatchResult{Method: "none"}, nil
}

func (m *Matcher) matchByExternalID(externalID string) (int64, error) {
	var entityID int64
	err := m.DB.QueryRow(
		`SELECT entity_id FROM external_ids WHERE system = 'groupsio.contact_row' AND external_id = ? AND entity_kind = 'person'`,
		externalID,
	).Scan(&entityID)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return entityID, err
}

func (m *Matcher) matchByCallSign(callSign string) (int64, error) {
	var id int64
	err := m.DB.QueryRow(
		`SELECT id FROM persons WHERE call_sign = ? AND deactivated_at IS NULL`,
		strings.ToUpper(callSign),
	).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

func (m *Matcher) matchByEmail(email string) ([]int64, error) {
	rows, err := m.DB.Query(
		`SELECT DISTINCT cm.person_id FROM contact_methods cm
		 JOIN persons p ON p.id = cm.person_id
		 WHERE cm.kind = 'email' AND cm.value_norm = ? AND cm.archived_at IS NULL AND p.deactivated_at IS NULL`,
		strings.ToLower(email),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
