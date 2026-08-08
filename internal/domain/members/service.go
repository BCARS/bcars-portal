// Package members provides administrative member operations — the domain
// service layer sitting between HTTP handlers and the sqlc-generated DB queries.
// Every mutating operation requires an authorized principal, records an audit
// event, and uses optimistic concurrency (version columns) to detect conflicts.
package members

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/bcars/bcars-portal/internal/db"
	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/authz"
)

// Service provides member operations with authorization and audit logging.
type Service struct {
	DB *sql.DB
	Q  *sqlcgen.Queries
}

// NewService creates a member operations service.
func NewService(database *sql.DB) *Service {
	return &Service{
		DB: database,
		Q:  sqlcgen.New(database),
	}
}

// --- Person CRUD ---

// GetPerson returns a single person by ID.
func (s *Service) GetPerson(ctx context.Context, p *authz.Principal, id int64) (sqlcgen.Person, error) {
	if err := authz.Authorize(ctx, p, "member.read", nil); err != nil {
		return sqlcgen.Person{}, err
	}
	return s.Q.GetPerson(ctx, id)
}

// ListPersonsParams configures person listing and search.
type ListPersonsParams struct {
	Query  string // optional name/callsign search term
	Limit  int64
	Offset int64
}

// PersonSummary is a unified type for person list results.
type PersonSummary struct {
	ID            int64
	DisplayName   string
	SortName      string
	CallSign      sql.NullString
	DeceasedAt    sql.NullString
	DeactivatedAt sql.NullString
	Version       int64
	CreatedAt     string
	UpdatedAt     string
}

// ListPersons returns active persons, optionally filtered by name.
func (s *Service) ListPersons(ctx context.Context, p *authz.Principal, params ListPersonsParams) ([]PersonSummary, error) {
	if err := authz.Authorize(ctx, p, "member.read", nil); err != nil {
		return nil, err
	}
	if params.Limit <= 0 {
		params.Limit = 50
	}
	if params.Query != "" {
		rows, err := s.Q.ListPersonsByName(ctx, sqlcgen.ListPersonsByNameParams{
			Column1: sqlNullString(params.Query),
			Column2: sqlNullString(params.Query),
			Limit:   params.Limit,
			Offset:  params.Offset,
		})
		if err != nil {
			return nil, err
		}
		result := make([]PersonSummary, len(rows))
		for i, r := range rows {
			result[i] = PersonSummary{
				ID: r.ID, DisplayName: r.DisplayName, SortName: r.SortName,
				CallSign: r.CallSign, DeceasedAt: r.DeceasedAt,
				DeactivatedAt: r.DeactivatedAt, Version: r.Version,
				CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
			}
		}
		return result, nil
	}
	rows, err := s.Q.ListPersons(ctx, sqlcgen.ListPersonsParams{
		Limit:  params.Limit,
		Offset: params.Offset,
	})
	if err != nil {
		return nil, err
	}
	result := make([]PersonSummary, len(rows))
	for i, r := range rows {
		result[i] = PersonSummary{
			ID: r.ID, DisplayName: r.DisplayName, SortName: r.SortName,
			CallSign: r.CallSign, DeceasedAt: r.DeceasedAt,
			DeactivatedAt: r.DeactivatedAt, Version: r.Version,
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		}
	}
	return result, nil
}

// CreatePersonParams contains fields for creating a new person.
type CreatePersonParams struct {
	DisplayName string
	SortName    string
	CallSign    string
	BaseType    string // membership base type: "full" or "associate"
}

// CreatePerson creates a person and a pending membership.
func (s *Service) CreatePerson(ctx context.Context, p *authz.Principal, params CreatePersonParams) (sqlcgen.Person, error) {
	if err := authz.Authorize(ctx, p, "member.create", nil); err != nil {
		return sqlcgen.Person{}, err
	}

	person, err := s.Q.CreatePerson(ctx, sqlcgen.CreatePersonParams{
		DisplayName: params.DisplayName,
		SortName:    params.SortName,
		CallSign:    sqlNullString(params.CallSign),
	})
	if err != nil {
		return sqlcgen.Person{}, fmt.Errorf("members: create person: %w", err)
	}

	if params.BaseType != "" {
		_, err = s.Q.CreateMembership(ctx, sqlcgen.CreateMembershipParams{
			PersonID: person.ID,
			BaseType: params.BaseType,
		})
		if err != nil {
			return sqlcgen.Person{}, fmt.Errorf("members: create membership: %w", err)
		}
	}

	s.audit(ctx, p, "member.create", "person", person.ID, "success", nil)
	return person, nil
}

// UpdatePersonParams contains fields for updating person data.
type UpdatePersonParams struct {
	ID          int64
	DisplayName string
	SortName    string
	CallSign    string
	Version     int64
}

// UpdatePerson updates person fields with optimistic concurrency.
func (s *Service) UpdatePerson(ctx context.Context, p *authz.Principal, params UpdatePersonParams) (sqlcgen.Person, error) {
	if err := authz.Authorize(ctx, p, "member.update", nil); err != nil {
		return sqlcgen.Person{}, err
	}

	person, err := s.Q.UpdatePerson(ctx, sqlcgen.UpdatePersonParams{
		DisplayName: params.DisplayName,
		SortName:    params.SortName,
		CallSign:    sqlNullString(params.CallSign),
		ID:          params.ID,
		Version:     params.Version,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return sqlcgen.Person{}, db.ErrStale
		}
		return sqlcgen.Person{}, fmt.Errorf("members: update person: %w", err)
	}

	s.audit(ctx, p, "member.update", "person", params.ID, "success", nil)
	return person, nil
}

// DeactivatePerson soft-deletes a person.
func (s *Service) DeactivatePerson(ctx context.Context, p *authz.Principal, id, version int64) error {
	if err := authz.Authorize(ctx, p, "member.deactivate", nil); err != nil {
		return err
	}

	err := s.Q.DeactivatePerson(ctx, sqlcgen.DeactivatePersonParams{ID: id, Version: version})
	if err != nil {
		return fmt.Errorf("members: deactivate: %w", err)
	}

	s.audit(ctx, p, "member.deactivate", "person", id, "success", nil)
	return nil
}

// ReactivatePerson restores a deactivated person.
func (s *Service) ReactivatePerson(ctx context.Context, p *authz.Principal, id, version int64) error {
	if err := authz.Authorize(ctx, p, "member.deactivate", nil); err != nil {
		return err
	}

	err := s.Q.ReactivatePerson(ctx, sqlcgen.ReactivatePersonParams{ID: id, Version: version})
	if err != nil {
		return fmt.Errorf("members: reactivate: %w", err)
	}

	s.audit(ctx, p, "member.deactivate", "person", id, "success", nil)
	return nil
}

// --- Membership operations ---

// ApproveMembership approves a pending membership with a decided base type.
func (s *Service) ApproveMembership(ctx context.Context, p *authz.Principal, membershipID, version int64, baseType, reason string) (sqlcgen.Membership, error) {
	if err := authz.Authorize(ctx, p, "membership.approve", nil); err != nil {
		return sqlcgen.Membership{}, err
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	m, err := s.Q.ApproveMembership(ctx, sqlcgen.ApproveMembershipParams{
		BaseType: baseType,
		JoinedOn: sqlNullString(now[:10]),
		ID:       membershipID,
		Version:  version,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return sqlcgen.Membership{}, db.ErrStale
		}
		return sqlcgen.Membership{}, fmt.Errorf("members: approve: %w", err)
	}

	_, err = s.Q.CreateMembershipApproval(ctx, sqlcgen.CreateMembershipApprovalParams{
		MembershipID: membershipID,
		Decision:     "approved",
		ApprovedType: sqlNullString(baseType),
		DecidedBy:    p.UserID,
		DecidedAt:    now,
		Reason:       sqlNullString(reason),
	})
	if err != nil {
		return sqlcgen.Membership{}, fmt.Errorf("members: record approval: %w", err)
	}

	s.audit(ctx, p, "membership.approve", "membership", membershipID, "success", nil)
	return m, nil
}

// RejectMembership rejects a pending membership.
func (s *Service) RejectMembership(ctx context.Context, p *authz.Principal, membershipID, version int64, reason string) (sqlcgen.Membership, error) {
	if err := authz.Authorize(ctx, p, "membership.approve", nil); err != nil {
		return sqlcgen.Membership{}, err
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	m, err := s.Q.RejectMembership(ctx, sqlcgen.RejectMembershipParams{
		ID:      membershipID,
		Version: version,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return sqlcgen.Membership{}, db.ErrStale
		}
		return sqlcgen.Membership{}, fmt.Errorf("members: reject: %w", err)
	}

	_, err = s.Q.CreateMembershipApproval(ctx, sqlcgen.CreateMembershipApprovalParams{
		MembershipID: membershipID,
		Decision:     "rejected",
		DecidedBy:    p.UserID,
		DecidedAt:    now,
		Reason:       sqlNullString(reason),
	})
	if err != nil {
		return sqlcgen.Membership{}, fmt.Errorf("members: record rejection: %w", err)
	}

	s.audit(ctx, p, "membership.approve", "membership", membershipID, "success", nil)
	return m, nil
}

// TransitionLifecycle changes membership lifecycle (inactive, resigned, deceased).
func (s *Service) TransitionLifecycle(ctx context.Context, p *authz.Principal, membershipID, version int64, lifecycle string) (sqlcgen.Membership, error) {
	if err := authz.Authorize(ctx, p, "membership.lifecycle", nil); err != nil {
		return sqlcgen.Membership{}, err
	}

	now := time.Now().UTC().Format("2006-01-02")
	var endedOn sql.NullString
	if lifecycle == "inactive" || lifecycle == "resigned" || lifecycle == "deceased" {
		endedOn = sqlNullString(now)
	}

	m, err := s.Q.TransitionLifecycle(ctx, sqlcgen.TransitionLifecycleParams{
		Lifecycle: lifecycle,
		EndedOn:   endedOn,
		ID:        membershipID,
		Version:   version,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return sqlcgen.Membership{}, db.ErrStale
		}
		return sqlcgen.Membership{}, fmt.Errorf("members: transition: %w", err)
	}

	s.audit(ctx, p, "membership.lifecycle", "membership", membershipID, "success", nil)
	return m, nil
}

// ListMembershipsByPerson returns all memberships for a person.
func (s *Service) ListMembershipsByPerson(ctx context.Context, p *authz.Principal, personID int64) ([]sqlcgen.Membership, error) {
	if err := authz.Authorize(ctx, p, "member.read", nil); err != nil {
		return nil, err
	}
	return s.Q.ListMembershipsByPerson(ctx, personID)
}

// --- FCC Verification ---

// VerifyFCC records a manual FCC license verification.
func (s *Service) VerifyFCC(ctx context.Context, p *authz.Principal, membershipID int64, callSign, licenseClass, source string) (sqlcgen.FccVerification, error) {
	if err := authz.Authorize(ctx, p, "fcc.verify", nil); err != nil {
		return sqlcgen.FccVerification{}, err
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	v, err := s.Q.CreateFCCVerification(ctx, sqlcgen.CreateFCCVerificationParams{
		MembershipID:       membershipID,
		CallSign:           callSign,
		LicenseClass:       sqlNullString(licenseClass),
		VerificationSource: source,
		VerifiedBy:         p.UserID,
		VerifiedAt:         now,
	})
	if err != nil {
		return sqlcgen.FccVerification{}, fmt.Errorf("members: fcc verify: %w", err)
	}

	s.audit(ctx, p, "fcc.verify", "membership", membershipID, "success", nil)
	return v, nil
}

// RevokeFCCVerification revokes an existing FCC verification.
func (s *Service) RevokeFCCVerification(ctx context.Context, p *authz.Principal, verificationID int64, notes string) error {
	if err := authz.Authorize(ctx, p, "fcc.verify", nil); err != nil {
		return err
	}

	err := s.Q.RevokeFCCVerification(ctx, sqlcgen.RevokeFCCVerificationParams{
		Notes: sqlNullString(notes),
		ID:    verificationID,
	})
	if err != nil {
		return fmt.Errorf("members: revoke fcc: %w", err)
	}

	s.audit(ctx, p, "fcc.verify", "fcc_verification", verificationID, "success", nil)
	return nil
}

// --- Honorary Grants ---

// CreateHonoraryGrantParams contains fields for creating an honorary grant.
type CreateHonoraryGrantParams struct {
	MembershipID int64
	StartsOn     string
	EndsOn       string // empty for lifetime
	IsLifetime   bool
	Reason       string
}

// CreateHonoraryGrant creates an honorary grant on a membership.
func (s *Service) CreateHonoraryGrant(ctx context.Context, p *authz.Principal, params CreateHonoraryGrantParams) (sqlcgen.HonoraryGrant, error) {
	if err := authz.Authorize(ctx, p, "honorary.grant", nil); err != nil {
		return sqlcgen.HonoraryGrant{}, err
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	var isLifetime int64
	if params.IsLifetime {
		isLifetime = 1
	}

	g, err := s.Q.CreateHonoraryGrant(ctx, sqlcgen.CreateHonoraryGrantParams{
		MembershipID: params.MembershipID,
		StartsOn:     params.StartsOn,
		EndsOn:       sqlNullString(params.EndsOn),
		IsLifetime:   isLifetime,
		Reason:       params.Reason,
		ApprovedBy:   p.UserID,
		ApprovedAt:   now,
	})
	if err != nil {
		return sqlcgen.HonoraryGrant{}, fmt.Errorf("members: create honorary: %w", err)
	}

	s.audit(ctx, p, "honorary.grant", "membership", params.MembershipID, "success", nil)
	return g, nil
}

// RevokeHonoraryGrant revokes an active honorary grant.
func (s *Service) RevokeHonoraryGrant(ctx context.Context, p *authz.Principal, grantID, version int64, reason string) error {
	if err := authz.Authorize(ctx, p, "honorary.grant", nil); err != nil {
		return err
	}

	err := s.Q.RevokeHonoraryGrant(ctx, sqlcgen.RevokeHonoraryGrantParams{
		RevokedBy:    sql.NullInt64{Int64: p.UserID, Valid: true},
		RevokeReason: sqlNullString(reason),
		ID:           grantID,
		Version:      version,
	})
	if err != nil {
		return fmt.Errorf("members: revoke honorary: %w", err)
	}

	s.audit(ctx, p, "honorary.grant", "honorary_grant", grantID, "success", nil)
	return nil
}

// --- Contact Methods ---

// CreateContactMethodParams contains fields for adding a contact method.
type CreateContactMethodParams struct {
	PersonID         int64
	Kind             string // "email", "phone", "postal"
	Label            string
	ValueRaw         string
	ValueNorm        string
	IsPrimary        bool
	PostalLine1      string
	PostalLine2      string
	PostalCity       string
	PostalState      string
	PostalPostalCode string
	PostalCountry    string
}

// CreateContactMethod adds a contact method to a person.
func (s *Service) CreateContactMethod(ctx context.Context, p *authz.Principal, params CreateContactMethodParams) (sqlcgen.ContactMethod, error) {
	if err := authz.Authorize(ctx, p, "contact_method.write", nil); err != nil {
		return sqlcgen.ContactMethod{}, err
	}

	var isPrimary int64
	if params.IsPrimary {
		isPrimary = 1
		// Clear existing primary for this person.
		if err := s.Q.ClearPrimaryForPerson(ctx, params.PersonID); err != nil {
			return sqlcgen.ContactMethod{}, fmt.Errorf("members: clear primary: %w", err)
		}
	}

	cm, err := s.Q.CreateContactMethod(ctx, sqlcgen.CreateContactMethodParams{
		PersonID:         params.PersonID,
		Kind:             params.Kind,
		Label:            sqlNullString(params.Label),
		ValueRaw:         params.ValueRaw,
		ValueNorm:        params.ValueNorm,
		IsPrimary:        isPrimary,
		PostalLine1:      sqlNullString(params.PostalLine1),
		PostalLine2:      sqlNullString(params.PostalLine2),
		PostalCity:       sqlNullString(params.PostalCity),
		PostalState:      sqlNullString(params.PostalState),
		PostalPostalCode: sqlNullString(params.PostalPostalCode),
		PostalCountry:    sqlNullString(params.PostalCountry),
	})
	if err != nil {
		return sqlcgen.ContactMethod{}, fmt.Errorf("members: create contact: %w", err)
	}

	s.audit(ctx, p, "contact_method.write", "person", params.PersonID, "success", nil)
	return cm, nil
}

// ListContactMethods returns active contact methods for a person.
func (s *Service) ListContactMethods(ctx context.Context, p *authz.Principal, personID int64) ([]sqlcgen.ContactMethod, error) {
	if err := authz.Authorize(ctx, p, "member.read", nil); err != nil {
		return nil, err
	}
	return s.Q.ListContactMethods(ctx, personID)
}

// ArchiveContactMethod soft-deletes a contact method.
func (s *Service) ArchiveContactMethod(ctx context.Context, p *authz.Principal, contactMethodID, version int64) error {
	if err := authz.Authorize(ctx, p, "contact_method.write", nil); err != nil {
		return err
	}

	err := s.Q.ArchiveContactMethod(ctx, sqlcgen.ArchiveContactMethodParams{
		ID:      contactMethodID,
		Version: version,
	})
	if err != nil {
		return fmt.Errorf("members: archive contact: %w", err)
	}

	s.audit(ctx, p, "contact_method.write", "contact_method", contactMethodID, "success", nil)
	return nil
}

// MakePrimary sets a contact method as primary, clearing others.
func (s *Service) MakePrimary(ctx context.Context, p *authz.Principal, contactMethodID int64) error {
	if err := authz.Authorize(ctx, p, "contact_method.write", nil); err != nil {
		return err
	}

	cm, err := s.Q.GetContactMethod(ctx, contactMethodID)
	if err != nil {
		return fmt.Errorf("members: get contact: %w", err)
	}

	if err := s.Q.ClearPrimaryForPerson(ctx, cm.PersonID); err != nil {
		return fmt.Errorf("members: clear primary: %w", err)
	}

	if err := s.Q.SetPrimary(ctx, contactMethodID); err != nil {
		return fmt.Errorf("members: set primary: %w", err)
	}

	s.audit(ctx, p, "contact_method.write", "contact_method", contactMethodID, "success", nil)
	return nil
}

// --- Notes ---

// CreateNoteParams contains fields for creating a note.
type CreateNoteParams struct {
	SubjectKind string // "person", "membership", "honorary_grant", etc.
	SubjectID   int64
	Category    string // "general", "approval", "honorary", "coverage"
	Visibility  string // "officer", "treasurer", "system"
	Body        string
}

// CreateNote creates a categorized, permissioned note.
func (s *Service) CreateNote(ctx context.Context, p *authz.Principal, params CreateNoteParams) (sqlcgen.Note, error) {
	cap := "notes.write.officer"
	if params.Visibility == "treasurer" {
		cap = "notes.write.treasurer"
	}
	if err := authz.Authorize(ctx, p, cap, nil); err != nil {
		return sqlcgen.Note{}, err
	}

	note, err := s.Q.CreateNote(ctx, sqlcgen.CreateNoteParams{
		SubjectKind: params.SubjectKind,
		SubjectID:   params.SubjectID,
		Category:    params.Category,
		Visibility:  params.Visibility,
		Body:        params.Body,
		AuthorID:    p.UserID,
		Source:      "officer",
	})
	if err != nil {
		return sqlcgen.Note{}, fmt.Errorf("members: create note: %w", err)
	}

	s.audit(ctx, p, cap, "note", note.ID, "success", nil)
	return note, nil
}

// UpdateNote updates a note body, preserving edit history.
func (s *Service) UpdateNote(ctx context.Context, p *authz.Principal, noteID, version int64, body, editReason string) (sqlcgen.Note, error) {
	existing, err := s.Q.GetNote(ctx, noteID)
	if err != nil {
		return sqlcgen.Note{}, fmt.Errorf("members: get note: %w", err)
	}

	cap := "notes.write.officer"
	if existing.Visibility == "treasurer" {
		cap = "notes.write.treasurer"
	}
	if err := authz.Authorize(ctx, p, cap, nil); err != nil {
		return sqlcgen.Note{}, err
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	// Save revision of old body.
	_, err = s.Q.CreateNoteRevision(ctx, sqlcgen.CreateNoteRevisionParams{
		NoteID:   noteID,
		Body:     existing.Body,
		EditedBy: p.UserID,
		EditedAt: now,
		Reason:   sqlNullString(editReason),
	})
	if err != nil {
		return sqlcgen.Note{}, fmt.Errorf("members: create revision: %w", err)
	}

	note, err := s.Q.UpdateNote(ctx, sqlcgen.UpdateNoteParams{
		Body:    body,
		ID:      noteID,
		Version: version,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return sqlcgen.Note{}, db.ErrStale
		}
		return sqlcgen.Note{}, fmt.Errorf("members: update note: %w", err)
	}

	s.audit(ctx, p, cap, "note", noteID, "success", nil)
	return note, nil
}

// ListNotes returns notes for a subject.
func (s *Service) ListNotes(ctx context.Context, p *authz.Principal, subjectKind string, subjectID int64, limit, offset int64) ([]sqlcgen.Note, error) {
	if err := authz.Authorize(ctx, p, "member.read", nil); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	return s.Q.ListNotes(ctx, sqlcgen.ListNotesParams{
		SubjectKind: subjectKind,
		SubjectID:   subjectID,
		Limit:       limit,
		Offset:      offset,
	})
}

// --- Sharing Preferences ---

// SetDirectoryVisibility records a visibility preference for a contact method.
func (s *Service) SetDirectoryVisibility(ctx context.Context, p *authz.Principal, contactMethodID int64, audience string) (sqlcgen.ContactMethodVisibilityEvent, error) {
	if err := authz.Authorize(ctx, p, "sharing_pref.write.officer", nil); err != nil {
		return sqlcgen.ContactMethodVisibilityEvent{}, err
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	ev, err := s.Q.CreateVisibilityEvent(ctx, sqlcgen.CreateVisibilityEventParams{
		ContactMethodID: contactMethodID,
		Audience:        audience,
		Source:          "officer",
		EffectiveAt:     now,
		ActorUserID:     sql.NullInt64{Int64: p.UserID, Valid: true},
	})
	if err != nil {
		return sqlcgen.ContactMethodVisibilityEvent{}, fmt.Errorf("members: set visibility: %w", err)
	}

	s.audit(ctx, p, "sharing_pref.write.officer", "contact_method", contactMethodID, "success", nil)
	return ev, nil
}

// SetAcsAresSharing records an ACS/ARES sharing preference for a person.
func (s *Service) SetAcsAresSharing(ctx context.Context, p *authz.Principal, personID int64, participates bool, reason string) (sqlcgen.AcsAresSharingEvent, error) {
	if err := authz.Authorize(ctx, p, "sharing_pref.write.officer", nil); err != nil {
		return sqlcgen.AcsAresSharingEvent{}, err
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	var part int64
	if participates {
		part = 1
	}

	ev, err := s.Q.CreateAcsAresSharingEvent(ctx, sqlcgen.CreateAcsAresSharingEventParams{
		PersonID:     personID,
		Participates: part,
		Source:       "officer",
		EffectiveAt:  now,
		ActorUserID:  sql.NullInt64{Int64: p.UserID, Valid: true},
		Reason:       sqlNullString(reason),
	})
	if err != nil {
		return sqlcgen.AcsAresSharingEvent{}, fmt.Errorf("members: set acs/ares: %w", err)
	}

	s.audit(ctx, p, "sharing_pref.write.officer", "person", personID, "success", nil)
	return ev, nil
}

// --- Audit ---

func (s *Service) audit(ctx context.Context, p *authz.Principal, action, resourceKind string, resourceID int64, outcome string, detail map[string]string) {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	var actorID sql.NullInt64
	if p != nil {
		actorID = sql.NullInt64{Int64: p.UserID, Valid: true}
	}

	// Best-effort audit logging — don't fail the operation if audit write fails.
	_, _ = s.Q.CreateAuditEvent(ctx, sqlcgen.CreateAuditEventParams{
		OccurredAt:   now,
		ActorUserID:  actorID,
		Action:       action,
		ResourceKind: sqlNullString(resourceKind),
		ResourceID:   sql.NullInt64{Int64: resourceID, Valid: resourceID > 0},
		Outcome:      outcome,
	})
}

// --- Helpers ---

// TimelineEvent represents a unified event in a member's history.
type TimelineEvent struct {
	Kind       string // "audit", "note", "membership", "import"
	Detail     string
	OccurredAt string // RFC3339
}

// Timeline returns a reverse-chronological merged timeline for a person.
func (s *Service) Timeline(ctx context.Context, p *authz.Principal, personID int64, limit int) ([]TimelineEvent, error) {
	if err := authz.Authorize(ctx, p, "member.read", nil); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}

	var events []TimelineEvent

	// Audit events for this person.
	auditEvents, err := s.Q.ListAuditEventsByResource(ctx, sqlcgen.ListAuditEventsByResourceParams{
		ResourceKind: sqlNullString("person"),
		ResourceID:   sql.NullInt64{Int64: personID, Valid: true},
		Limit:        int64(limit),
		Offset:       0,
	})
	if err == nil {
		for _, ae := range auditEvents {
			events = append(events, TimelineEvent{
				Kind:       "audit",
				Detail:     ae.Action + ": " + ae.Outcome,
				OccurredAt: ae.OccurredAt,
			})
		}
	}

	// Notes for this person.
	notes, err := s.Q.ListNotes(ctx, sqlcgen.ListNotesParams{
		SubjectKind: "person",
		SubjectID:   personID,
		Limit:       int64(limit),
		Offset:      0,
	})
	if err == nil {
		for _, n := range notes {
			detail := n.Body
			if len(detail) > 120 {
				detail = detail[:120] + "…"
			}
			events = append(events, TimelineEvent{
				Kind:       "note",
				Detail:     detail,
				OccurredAt: n.CreatedAt,
			})
		}
	}

	// Memberships.
	memberships, err := s.Q.ListMembershipsByPerson(ctx, personID)
	if err == nil {
		for _, m := range memberships {
			events = append(events, TimelineEvent{
				Kind:       "membership",
				Detail:     m.BaseType + " — " + m.Lifecycle,
				OccurredAt: m.CreatedAt,
			})
		}
	}

	// Sort by occurred_at descending.
	sort.Slice(events, func(i, j int) bool {
		return events[i].OccurredAt > events[j].OccurredAt
	})

	if len(events) > limit {
		events = events[:limit]
	}

	return events, nil
}

func sqlNullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
