package importd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
)

// Errors returned by the import service.
var (
	ErrInvalidTransition  = errors.New("importd: invalid state transition")
	ErrUnresolvedManual   = errors.New("importd: unresolved manual rows remain")
	ErrRowNotManual       = errors.New("importd: row does not require manual review")
	ErrRowNotInRun        = errors.New("importd: row does not belong to this run")
	ErrRunNotFound        = errors.New("importd: run not found")
	ErrRowNotFound        = errors.New("importd: row not found")
	ErrIdempotentConflict = errors.New("importd: idempotency key already used for a different operation")
)

// Service orchestrates the import pipeline per ADR-0008:
// upload → validated → previewed → committed / discarded / failed.
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
// On success the run transitions to "validated".
func (s *Service) Upload(ctx context.Context, r io.Reader, sourceKind, filename string, uploadedBy int64, idempotencyKey string) (*UploadResult, error) {
	// Check idempotency: if a run already exists with this key, return an error.
	_, err := s.Q.GetImportRunByIdempotencyKey(ctx, idempotencyKey)
	if err == nil {
		return nil, fmt.Errorf("importd: duplicate idempotency key %q", idempotencyKey)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("importd: check idempotency: %w", err)
	}

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

	// Create import run (starts as "uploaded").
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

	// Transition to "validated" (ADR-0008 state machine).
	_, err = s.Q.UpdateImportRunStatus(ctx, sqlcgen.UpdateImportRunStatusParams{
		Status:  "validated",
		ID:      run.ID,
		Version: run.Version,
	})
	if err != nil {
		return nil, fmt.Errorf("importd: update run status: %w", err)
	}

	return result, nil
}

// ListRuns returns import runs ordered by creation date (newest first).
func (s *Service) ListRuns(ctx context.Context, limit, offset int64) ([]sqlcgen.ImportRun, error) {
	return s.Q.ListImportRuns(ctx, sqlcgen.ListImportRunsParams{
		Limit:  limit,
		Offset: offset,
	})
}

// GetRun returns a single import run by ID.
func (s *Service) GetRun(ctx context.Context, runID int64) (sqlcgen.ImportRun, error) {
	run, err := s.Q.GetImportRun(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return run, ErrRunNotFound
		}
		return run, fmt.Errorf("importd: get run: %w", err)
	}
	return run, nil
}

// ListRows returns staged rows for an import run.
func (s *Service) ListRows(ctx context.Context, runID int64, limit, offset int64) ([]sqlcgen.StagedImportRow, error) {
	return s.Q.ListStagedRows(ctx, sqlcgen.ListStagedRowsParams{
		ImportRunID: runID,
		Limit:       limit,
		Offset:      offset,
	})
}

// GetRow returns a single staged row by ID.
func (s *Service) GetRow(ctx context.Context, rowID int64) (sqlcgen.StagedImportRow, error) {
	row, err := s.Q.GetStagedRow(ctx, rowID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return row, ErrRowNotFound
		}
		return row, fmt.Errorf("importd: get row: %w", err)
	}
	return row, nil
}

// DecisionInput is the input for recording a reconciliation decision.
type DecisionInput struct {
	RowID       int64
	DecidedBy   int64
	Action      string // "approve_create", "approve_update", "skip", "override_action"
	PayloadJSON string // optional JSON with override details
}

// RecordDecision appends an officer reconciliation decision to a staged row.
// The row must belong to the given run and require manual review.
// After recording, if action is "approve_create" or "approve_update",
// the row's requires_manual flag is cleared and proposed_action is updated.
func (s *Service) RecordDecision(ctx context.Context, runID int64, input DecisionInput) (sqlcgen.ReconciliationDecision, error) {
	var empty sqlcgen.ReconciliationDecision

	// Validate run state: decisions only allowed in "validated" or "previewed".
	run, err := s.Q.GetImportRun(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return empty, ErrRunNotFound
		}
		return empty, fmt.Errorf("importd: get run: %w", err)
	}
	if run.Status != "validated" && run.Status != "previewed" {
		return empty, fmt.Errorf("%w: cannot add decisions in status %q", ErrInvalidTransition, run.Status)
	}

	// Validate row belongs to run.
	row, err := s.Q.GetStagedRow(ctx, input.RowID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return empty, ErrRowNotFound
		}
		return empty, fmt.Errorf("importd: get row: %w", err)
	}
	if row.ImportRunID != runID {
		return empty, ErrRowNotInRun
	}
	if row.RequiresManual == 0 {
		return empty, ErrRowNotManual
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	decision, err := s.Q.CreateReconciliationDecision(ctx, sqlcgen.CreateReconciliationDecisionParams{
		StagedImportRowID: input.RowID,
		DecidedBy:         input.DecidedBy,
		DecidedAt:         now,
		Action:            input.Action,
		PayloadJson:       sqlNullString(input.PayloadJSON),
	})
	if err != nil {
		return empty, fmt.Errorf("importd: create decision: %w", err)
	}

	// Update the staged row based on the decision action.
	switch input.Action {
	case "approve_create":
		_, err = s.Q.UpdateStagedRowAction(ctx, sqlcgen.UpdateStagedRowActionParams{
			ProposedAction: "create",
			RequiresManual: 0,
			ID:             input.RowID,
		})
	case "approve_update":
		_, err = s.Q.UpdateStagedRowAction(ctx, sqlcgen.UpdateStagedRowActionParams{
			ProposedAction: "update",
			RequiresManual: 0,
			ID:             input.RowID,
		})
	case "skip":
		_, err = s.Q.UpdateStagedRowAction(ctx, sqlcgen.UpdateStagedRowActionParams{
			ProposedAction: "skip",
			RequiresManual: 0,
			ID:             input.RowID,
		})
	}
	if err != nil {
		return empty, fmt.Errorf("importd: update row after decision: %w", err)
	}

	return decision, nil
}

// PreviewResult summarizes what will happen on commit.
type PreviewResult struct {
	RunID            int64
	TotalRows        int
	CreateCount      int
	UpdateCount      int
	SkipCount        int
	ManualCount      int
	UnresolvedManual int
	Ready            bool // true if no unresolved manual rows remain
}

// Preview recomputes the import summary and transitions to "previewed".
// Can be called from "validated" or "previewed" (re-preview after decisions).
func (s *Service) Preview(ctx context.Context, runID int64) (*PreviewResult, error) {
	run, err := s.Q.GetImportRun(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRunNotFound
		}
		return nil, fmt.Errorf("importd: get run: %w", err)
	}
	if run.Status != "validated" && run.Status != "previewed" {
		return nil, fmt.Errorf("%w: cannot preview from status %q", ErrInvalidTransition, run.Status)
	}

	// Count rows by action.
	actionCounts, err := s.Q.CountStagedRowsByAction(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("importd: count rows: %w", err)
	}

	// Count unresolved manual rows.
	unresolvedCount, err := s.Q.CountUnresolvedManualRows(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("importd: count unresolved: %w", err)
	}

	result := &PreviewResult{
		RunID:            runID,
		UnresolvedManual: int(unresolvedCount),
		Ready:            unresolvedCount == 0,
	}

	for _, ac := range actionCounts {
		count := int(ac.Cnt)
		result.TotalRows += count
		switch ac.ProposedAction {
		case "create":
			result.CreateCount = count
		case "update":
			result.UpdateCount = count
		case "skip":
			result.SkipCount = count
		case "manual":
			result.ManualCount = count
		}
	}

	// Transition to "previewed" if still "validated".
	if run.Status == "validated" {
		_, err = s.Q.UpdateImportRunStatus(ctx, sqlcgen.UpdateImportRunStatusParams{
			Status:  "previewed",
			ID:      run.ID,
			Version: run.Version,
		})
		if err != nil {
			return nil, fmt.Errorf("importd: transition to previewed: %w", err)
		}
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

// Commit applies all staged rows in a single transaction.
// Refuses to run if any manual rows remain unresolved.
// Idempotent: if the run is already committed, returns the stored result.
func (s *Service) Commit(ctx context.Context, runID int64, committedBy int64) (*CommitResult, error) {
	run, err := s.Q.GetImportRun(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRunNotFound
		}
		return nil, fmt.Errorf("importd: get run: %w", err)
	}

	// Idempotent retry: if already committed, return the stored result.
	if run.Status == "committed" {
		if run.ResultSummaryJson.Valid {
			var stored CommitResult
			if err := json.Unmarshal([]byte(run.ResultSummaryJson.String), &stored); err == nil {
				return &stored, nil
			}
		}
		return &CommitResult{}, nil
	}

	if run.Status != "previewed" {
		return nil, fmt.Errorf("%w: cannot commit from status %q, must be \"previewed\"", ErrInvalidTransition, run.Status)
	}

	// Refuse if any manual rows are unresolved.
	unresolvedCount, err := s.Q.CountUnresolvedManualRows(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("importd: count unresolved: %w", err)
	}
	if unresolvedCount > 0 {
		return nil, fmt.Errorf("%w: %d rows still need decisions", ErrUnresolvedManual, unresolvedCount)
	}

	// Execute in a single transaction.
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("importd: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	qtx := s.Q.WithTx(tx)
	result := &CommitResult{}

	// Process all rows.
	const batchSize = 500
	for offset := int64(0); ; offset += batchSize {
		rows, err := qtx.ListStagedRows(ctx, sqlcgen.ListStagedRowsParams{
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
			switch row.ProposedAction {
			case "create":
				if err := s.applyCreateTx(ctx, qtx, row, committedBy); err != nil {
					return nil, fmt.Errorf("importd: apply create row %d: %w", row.SourceRowIndex, err)
				}
				result.Created++
			case "update":
				if err := s.applyUpdateTx(ctx, qtx, row, committedBy); err != nil {
					return nil, fmt.Errorf("importd: apply update row %d: %w", row.SourceRowIndex, err)
				}
				result.Updated++
			default:
				result.Skipped++
			}
		}
	}

	// Mark committed within the transaction.
	summaryJSON, _ := json.Marshal(result)
	_, err = qtx.CommitImportRun(ctx, sqlcgen.CommitImportRunParams{
		CommittedBy:       sql.NullInt64{Int64: committedBy, Valid: true},
		ResultSummaryJson: sql.NullString{String: string(summaryJSON), Valid: true},
		ID:                runID,
		Version:           run.Version,
	})
	if err != nil {
		return nil, fmt.Errorf("importd: commit run: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("importd: commit tx: %w", err)
	}

	return result, nil
}

// Discard marks an import run as discarded. Allowed from "validated" or "previewed".
func (s *Service) Discard(ctx context.Context, runID int64) error {
	run, err := s.Q.GetImportRun(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRunNotFound
		}
		return fmt.Errorf("importd: get run: %w", err)
	}
	if run.Status != "validated" && run.Status != "previewed" {
		return fmt.Errorf("%w: cannot discard from status %q", ErrInvalidTransition, run.Status)
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

// applyCreateTx creates a person and related records within a transaction.
func (s *Service) applyCreateTx(ctx context.Context, qtx *sqlcgen.Queries, staged sqlcgen.StagedImportRow, actorID int64) error {
	var norm NormalizedRecord
	if err := json.Unmarshal([]byte(staged.NormalizedJson), &norm); err != nil {
		return fmt.Errorf("unmarshal normalized: %w", err)
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	// Create person.
	person, err := qtx.CreatePerson(ctx, sqlcgen.CreatePersonParams{
		DisplayName: norm.DisplayName,
		SortName:    norm.SortName,
		CallSign:    sqlNullString(norm.CallSign),
	})
	if err != nil {
		return fmt.Errorf("create person: %w", err)
	}

	// Link external ID if present.
	if norm.ExternalID != "" {
		_, err = qtx.CreateExternalID(ctx, sqlcgen.CreateExternalIDParams{
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
		_, err = qtx.CreateContactMethod(ctx, sqlcgen.CreateContactMethodParams{
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
		_, err = qtx.CreateContactMethod(ctx, sqlcgen.CreateContactMethodParams{
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
		_, err = qtx.CreateContactMethod(ctx, sqlcgen.CreateContactMethodParams{
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
		m, err := qtx.CreateMembership(ctx, sqlcgen.CreateMembershipParams{
			PersonID: person.ID,
			BaseType: norm.BaseType,
		})
		if err != nil {
			return fmt.Errorf("create membership: %w", err)
		}

		_, err = qtx.ApproveMembership(ctx, sqlcgen.ApproveMembershipParams{
			BaseType: norm.BaseType,
			JoinedOn: sqlNullString(now[:10]),
			ID:       m.ID,
			Version:  m.Version,
		})
		if err != nil {
			return fmt.Errorf("approve membership: %w", err)
		}

		_, err = qtx.CreateMembershipApproval(ctx, sqlcgen.CreateMembershipApprovalParams{
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

	// Import notes (deduplicated sentences).
	if err := createImportNotes(ctx, qtx, person.ID, norm.Note, actorID); err != nil {
		return fmt.Errorf("create notes: %w", err)
	}

	return nil
}

// applyUpdateTx updates an existing person within a transaction.
func (s *Service) applyUpdateTx(ctx context.Context, qtx *sqlcgen.Queries, staged sqlcgen.StagedImportRow, actorID int64) error {
	if !staged.MatchPersonID.Valid {
		return nil
	}
	personID := staged.MatchPersonID.Int64

	var norm NormalizedRecord
	if err := json.Unmarshal([]byte(staged.NormalizedJson), &norm); err != nil {
		return fmt.Errorf("unmarshal normalized: %w", err)
	}

	person, err := qtx.GetPerson(ctx, personID)
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
		_, err = qtx.UpdatePerson(ctx, sqlcgen.UpdatePersonParams{
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
		_, err := qtx.FindExternalID(ctx, sqlcgen.FindExternalIDParams{
			System:     "groupsio.contact_row",
			ExternalID: norm.ExternalID,
		})
		if err == sql.ErrNoRows {
			_, err = qtx.CreateExternalID(ctx, sqlcgen.CreateExternalIDParams{
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

	// Import notes (deduplicated sentences) for updates too.
	if err := createImportNotes(ctx, qtx, personID, norm.Note, actorID); err != nil {
		return fmt.Errorf("create notes: %w", err)
	}

	return nil
}

// splitNotes splits a note string into individual deduplicated sentences.
// Notes from Groups.io are period-separated: "Paid via PayPal on 1/1/2024. Paid via PayPal on 1/2/2025."
func splitNotes(note string) []string {
	note = strings.TrimSpace(note)
	if note == "" {
		return nil
	}

	// Split on ". " (period-space) to separate sentences.
	// Also handle trailing period.
	parts := strings.Split(note, ". ")
	seen := make(map[string]bool)
	var unique []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.TrimRight(p, ".")
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		lower := strings.ToLower(p)
		if seen[lower] {
			continue
		}
		seen[lower] = true
		unique = append(unique, p)
	}
	return unique
}

// createImportNotes splits a note field into deduplicated sentences and creates
// one note per unique sentence, checking for existing notes to avoid duplicates
// across re-imports.
func createImportNotes(ctx context.Context, qtx *sqlcgen.Queries, personID int64, noteField string, actorID int64) error {
	sentences := splitNotes(noteField)
	if len(sentences) == 0 {
		return nil
	}

	// Load existing notes for this person to deduplicate across imports.
	existing, err := qtx.ListNotes(ctx, sqlcgen.ListNotesParams{
		SubjectKind: "person",
		SubjectID:   personID,
		Limit:       1000,
		Offset:      0,
	})
	if err != nil {
		return fmt.Errorf("list existing notes: %w", err)
	}

	existingBodies := make(map[string]bool)
	for _, n := range existing {
		existingBodies[strings.ToLower(strings.TrimSpace(n.Body))] = true
	}

	for _, sentence := range sentences {
		if existingBodies[strings.ToLower(sentence)] {
			continue
		}
		_, err := qtx.CreateNote(ctx, sqlcgen.CreateNoteParams{
			SubjectKind: "person",
			SubjectID:   personID,
			Category:    "general",
			Visibility:  "officer",
			Body:        sentence,
			AuthorID:    actorID,
			Source:      "groupsio_import",
		})
		if err != nil {
			return fmt.Errorf("create note: %w", err)
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
