package importd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
)

// Service orchestrates the import pipeline: upload → parse → normalize → match → stage.
type Service struct {
	DB *sql.DB
	Q  *sqlcgen.Queries
}

// NewService creates an import service.
func NewService(database *sql.DB) *Service {
	return &Service{
		DB: database,
		Q:  sqlcgen.New(database),
	}
}

// UploadResult contains the result of uploading and staging an import file.
type UploadResult struct {
	RunID         int64
	TotalRows     int
	AutoRows      int
	ManualRows    int
	NewRows       int
	MatchedRows   int
	AmbiguousRows int
}

// Upload parses a file, normalizes all records, matches against existing data,
// and stages them in the database. sourceKind is "csv" or "json".
func (s *Service) Upload(ctx context.Context, r io.Reader, sourceKind, filename string, uploadedBy int64, idempotencyKey string) (*UploadResult, error) {
	// Read all content for hashing.
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("importd: read upload: %w", err)
	}

	hash := sha256.Sum256(data)
	hashHex := hex.EncodeToString(hash[:])

	// Parse.
	var records []RawRecord
	switch sourceKind {
	case "csv":
		records, err = ParseCSV(bytes.NewReader(data))
	case "json":
		records, err = ParseJSON(bytes.NewReader(data))
	default:
		return nil, fmt.Errorf("importd: unknown source kind %q", sourceKind)
	}
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	// Create import run.
	run, err := s.Q.CreateImportRun(ctx, sqlcgen.CreateImportRunParams{
		SourceKind:     sourceKind,
		SourceFilename: filename,
		SourceSha256:   hashHex,
		UploadedBy:     uploadedBy,
		UploadedAt:     now,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return nil, fmt.Errorf("importd: create run: %w", err)
	}

	matcher := NewMatcher(s.DB)
	result := &UploadResult{RunID: run.ID, TotalRows: len(records)}

	for i, raw := range records {
		norm := Normalize(raw)
		match, err := matcher.Match(norm)
		if err != nil {
			return nil, fmt.Errorf("importd: match row %d: %w", i, err)
		}

		rawJSON, _ := json.Marshal(raw)
		normJSON, _ := json.Marshal(norm)

		action := proposeAction(norm, match)

		params := sqlcgen.CreateStagedRowParams{
			ImportRunID:    run.ID,
			SourceRowIndex: int64(i),
			RawJson:        string(rawJSON),
			NormalizedJson: string(normJSON),
			MatchMethod:    sqlNullString(match.Method),
			ProposedAction: action,
			RequiresManual: boolToInt(norm.RequiresManual || match.Ambiguous),
		}

		if raw.ExternalID != "" {
			params.SourceExternalID = sqlNullString(raw.ExternalID)
		}
		if match.PersonID > 0 {
			params.MatchPersonID = sql.NullInt64{Int64: match.PersonID, Valid: true}
		}
		if norm.RequiresManual {
			params.ManualReason = sqlNullString(norm.ManualReason)
		} else if match.Ambiguous {
			params.ManualReason = sqlNullString("ambiguous_" + match.Method)
		}

		if _, err := s.Q.CreateStagedRow(ctx, params); err != nil {
			return nil, fmt.Errorf("importd: stage row %d: %w", i, err)
		}

		if norm.RequiresManual || match.Ambiguous {
			result.ManualRows++
		} else {
			result.AutoRows++
		}
		if match.PersonID > 0 {
			result.MatchedRows++
		} else if match.Ambiguous {
			result.AmbiguousRows++
		} else {
			result.NewRows++
		}
	}

	// Transition to staged.
	_, err = s.Q.UpdateImportRunStatus(ctx, sqlcgen.UpdateImportRunStatusParams{
		Status:  "staged",
		ID:      run.ID,
		Version: run.Version,
	})
	if err != nil {
		return nil, fmt.Errorf("importd: update run status: %w", err)
	}

	return result, nil
}

// CommitResult contains the result of committing an import run.
type CommitResult struct {
	Created int
	Updated int
	Skipped int
	Errors  int
}

// Commit applies all auto-resolvable staged rows to the database.
// Rows that require manual review are skipped.
func (s *Service) Commit(ctx context.Context, runID int64, committedBy int64) (*CommitResult, error) {
	run, err := s.Q.GetImportRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("importd: get run: %w", err)
	}
	if run.Status != "staged" {
		return nil, fmt.Errorf("importd: run %d has status %q, expected \"staged\"", runID, run.Status)
	}

	result := &CommitResult{}

	// Process rows in batches.
	const batchSize = 500
	for offset := int64(0); ; offset += batchSize {
		rows, err := s.Q.ListStagedRows(ctx, sqlcgen.ListStagedRowsParams{
			ImportRunID: runID,
			Limit:       batchSize,
			Offset:      offset,
		})
		if err != nil {
			return nil, fmt.Errorf("importd: list staged rows: %w", err)
		}
		if len(rows) == 0 {
			break
		}

		for _, row := range rows {
			if row.RequiresManual == 1 {
				result.Skipped++
				continue
			}

			if err := s.applyRow(ctx, row, committedBy); err != nil {
				result.Errors++
				continue
			}

			switch row.ProposedAction {
			case "create":
				result.Created++
			case "update":
				result.Updated++
			default:
				result.Skipped++
			}
		}
	}

	// Mark committed.
	summaryJSON, _ := json.Marshal(result)
	_, err = s.Q.CommitImportRun(ctx, sqlcgen.CommitImportRunParams{
		CommittedBy:       sql.NullInt64{Int64: committedBy, Valid: true},
		ResultSummaryJson: sql.NullString{String: string(summaryJSON), Valid: true},
		ID:                runID,
		Version:           run.Version,
	})
	if err != nil {
		return nil, fmt.Errorf("importd: commit run: %w", err)
	}

	return result, nil
}

// Discard marks an import run as discarded. Staged rows remain for audit but are not applied.
func (s *Service) Discard(ctx context.Context, runID int64) error {
	run, err := s.Q.GetImportRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("importd: get run: %w", err)
	}
	if run.Status != "staged" {
		return fmt.Errorf("importd: run %d has status %q, expected \"staged\"", runID, run.Status)
	}

	_, err = s.Q.UpdateImportRunStatus(ctx, sqlcgen.UpdateImportRunStatusParams{
		Status:  "discarded",
		ID:      runID,
		Version: run.Version,
	})
	if err != nil {
		return fmt.Errorf("importd: discard run: %w", err)
	}
	return nil
}

// applyRow applies a single staged row to the database.
func (s *Service) applyRow(ctx context.Context, staged sqlcgen.StagedImportRow, actorID int64) error {
	var norm NormalizedRecord
	if err := json.Unmarshal([]byte(staged.NormalizedJson), &norm); err != nil {
		return fmt.Errorf("unmarshal normalized: %w", err)
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	switch staged.ProposedAction {
	case "create":
		return s.applyCreate(ctx, staged, norm, actorID, now)
	case "update":
		return s.applyUpdate(ctx, staged, norm, actorID, now)
	default:
		return nil // skip
	}
}

func (s *Service) applyCreate(ctx context.Context, staged sqlcgen.StagedImportRow, norm NormalizedRecord, actorID int64, now string) error {
	// Create person.
	person, err := s.Q.CreatePerson(ctx, sqlcgen.CreatePersonParams{
		DisplayName: norm.DisplayName,
		SortName:    norm.SortName,
		CallSign:    sqlNullString(norm.CallSign),
	})
	if err != nil {
		return fmt.Errorf("create person: %w", err)
	}

	// Link external ID if present.
	if norm.ExternalID != "" {
		_, err = s.Q.CreateExternalID(ctx, sqlcgen.CreateExternalIDParams{
			EntityKind: "person",
			EntityID:   person.ID,
			System:     "groupsio.contact_row",
			ExternalID: norm.ExternalID,
		})
		if err != nil {
			return fmt.Errorf("create external id: %w", err)
		}
	}

	// Create contact methods.
	if norm.Email != "" {
		_, err = s.Q.CreateContactMethod(ctx, sqlcgen.CreateContactMethodParams{
			PersonID:  person.ID,
			Kind:      "email",
			ValueRaw:  norm.Email,
			ValueNorm: norm.Email,
			IsPrimary: 1,
		})
		if err != nil {
			return fmt.Errorf("create email: %w", err)
		}
	}

	if norm.Phone != "" && norm.PhoneValid {
		var raw RawRecord
		_ = json.Unmarshal([]byte(staged.RawJson), &raw)
		_, err = s.Q.CreateContactMethod(ctx, sqlcgen.CreateContactMethodParams{
			PersonID:  person.ID,
			Kind:      "phone",
			ValueRaw:  raw.Phone,
			ValueNorm: norm.Phone,
		})
		if err != nil {
			return fmt.Errorf("create phone: %w", err)
		}
	}

	if norm.StreetAddress != "" {
		_, err = s.Q.CreateContactMethod(ctx, sqlcgen.CreateContactMethodParams{
			PersonID:         person.ID,
			Kind:             "postal",
			ValueRaw:         norm.StreetAddress,
			ValueNorm:        norm.StreetAddress,
			PostalLine1:      sqlNullString(norm.StreetAddress),
			PostalCity:       sqlNullString(norm.City),
			PostalState:      sqlNullString(norm.StateProvince),
			PostalPostalCode: sqlNullString(norm.PostalCode),
		})
		if err != nil {
			return fmt.Errorf("create postal: %w", err)
		}
	}

	// Create membership if base type is known.
	if norm.BaseType != "" {
		m, err := s.Q.CreateMembership(ctx, sqlcgen.CreateMembershipParams{
			PersonID: person.ID,
			BaseType: norm.BaseType,
		})
		if err != nil {
			return fmt.Errorf("create membership: %w", err)
		}

		// Auto-approve with legacy current_until.
		_, err = s.Q.ApproveMembership(ctx, sqlcgen.ApproveMembershipParams{
			BaseType: norm.BaseType,
			JoinedOn: sqlNullString(now[:10]),
			ID:       m.ID,
			Version:  m.Version,
		})
		if err != nil {
			return fmt.Errorf("approve membership: %w", err)
		}

		// Record approval.
		_, err = s.Q.CreateMembershipApproval(ctx, sqlcgen.CreateMembershipApprovalParams{
			MembershipID: m.ID,
			Decision:     "approved",
			ApprovedType: sqlNullString(norm.BaseType),
			DecidedBy:    actorID,
			DecidedAt:    now,
			Reason:       sqlNullString("import from Groups.io"),
		})
		if err != nil {
			return fmt.Errorf("create approval: %w", err)
		}
	}

	return nil
}

func (s *Service) applyUpdate(ctx context.Context, staged sqlcgen.StagedImportRow, norm NormalizedRecord, actorID int64, now string) error {
	if !staged.MatchPersonID.Valid {
		return nil
	}
	personID := staged.MatchPersonID.Int64

	person, err := s.Q.GetPerson(ctx, personID)
	if err != nil {
		return fmt.Errorf("get person: %w", err)
	}

	// Update person fields if changed.
	needsUpdate := false
	displayName := person.DisplayName
	sortName := person.SortName
	callSign := person.CallSign

	if norm.DisplayName != "" && norm.DisplayName != person.DisplayName {
		displayName = norm.DisplayName
		needsUpdate = true
	}
	if norm.SortName != "" && norm.SortName != person.SortName {
		sortName = norm.SortName
		needsUpdate = true
	}
	if norm.CallSign != "" && norm.CallSign != person.CallSign.String {
		callSign = sqlNullString(norm.CallSign)
		needsUpdate = true
	}

	if needsUpdate {
		_, err = s.Q.UpdatePerson(ctx, sqlcgen.UpdatePersonParams{
			DisplayName: displayName,
			SortName:    sortName,
			CallSign:    callSign,
			ID:          person.ID,
			Version:     person.Version,
		})
		if err != nil {
			return fmt.Errorf("update person: %w", err)
		}
	}

	// Link external ID if not already linked.
	if norm.ExternalID != "" {
		_, err := s.Q.FindExternalID(ctx, sqlcgen.FindExternalIDParams{
			System:     "groupsio.contact_row",
			ExternalID: norm.ExternalID,
		})
		if err == sql.ErrNoRows {
			_, err = s.Q.CreateExternalID(ctx, sqlcgen.CreateExternalIDParams{
				EntityKind: "person",
				EntityID:   personID,
				System:     "groupsio.contact_row",
				ExternalID: norm.ExternalID,
			})
			if err != nil {
				return fmt.Errorf("create external id: %w", err)
			}
		}
	}

	return nil
}

// proposeAction determines what action to take for a normalized record + match.
func proposeAction(norm NormalizedRecord, match MatchResult) string {
	if match.Ambiguous {
		return "manual"
	}
	if norm.RequiresManual {
		return "manual"
	}
	if match.PersonID > 0 {
		return "update"
	}
	return "create"
}

func sqlNullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
