// Package members provides administrative member operations — the domain
// service layer sitting between HTTP handlers and the sqlc-generated DB queries.
// Every mutating operation requires an authorized principal, records an audit
// event, and uses optimistic concurrency (version columns) to detect conflicts.
package members

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bcars/bcars-portal/internal/audit"
	"github.com/bcars/bcars-portal/internal/db"
	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/authz"
)

// Service provides member operations with authorization and audit logging.
type Service struct {
	DB *sql.DB
	Q  *sqlcgen.Queries
}

// Preference event sources. A preference event records WHO decided, and these
// distinguish an officer acting directly from an officer applying a member's
// reviewed request. The distinction is durable: it stays readable in the
// preference history long after the request itself is resolved.
const (
	PrefSourceOfficer       = "officer"
	PrefSourceMemberRequest = "member_request"
	PrefSourceImportDefault = "import_default"
)

// WithTx returns a Service bound to tx, so several adapters can be composed
// into one atomic change. Applying a reviewed request needs this: a request
// approving two items must not leave the first written and the second not.
func (s *Service) WithTx(tx *sql.Tx) *Service {
	return &Service{DB: s.DB, Q: s.Q.WithTx(tx)}
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
	// LicenseClass is what the club believes the member holds, lowercased.
	// It is a claim, not a verification: fcc_verifications records what an
	// officer checked, and this records what the club was told
	// (bcars-portal-um9).
	LicenseClass      string
	VolunteerExaminer bool
}

// CreatePerson creates a person and a pending membership.
func (s *Service) CreatePerson(ctx context.Context, p *authz.Principal, params CreatePersonParams) (sqlcgen.Person, error) {
	if err := authz.Authorize(ctx, p, "member.create", nil); err != nil {
		return sqlcgen.Person{}, err
	}

	person, err := s.Q.CreatePerson(ctx, sqlcgen.CreatePersonParams{
		DisplayName:       params.DisplayName,
		SortName:          params.SortName,
		CallSign:          sqlNullString(params.CallSign),
		LicenseClass:      sqlNullString(strings.ToLower(strings.TrimSpace(params.LicenseClass))),
		VolunteerExaminer: boolToInt(params.VolunteerExaminer),
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

	audit.StampResource(ctx, "person", person.ID)
	return person, nil
}

// UpdatePersonParams contains fields for updating person data.
type UpdatePersonParams struct {
	ID                int64
	DisplayName       string
	SortName          string
	CallSign          string
	LicenseClass      string
	VolunteerExaminer bool
	Version           int64
}

// UpdatePerson updates person fields with optimistic concurrency.
func (s *Service) UpdatePerson(ctx context.Context, p *authz.Principal, params UpdatePersonParams) (sqlcgen.Person, error) {
	if err := authz.Authorize(ctx, p, "member.update", nil); err != nil {
		return sqlcgen.Person{}, err
	}

	person, err := s.Q.UpdatePerson(ctx, sqlcgen.UpdatePersonParams{
		DisplayName:       params.DisplayName,
		SortName:          params.SortName,
		CallSign:          sqlNullString(params.CallSign),
		LicenseClass:      sqlNullString(strings.ToLower(strings.TrimSpace(params.LicenseClass))),
		VolunteerExaminer: boolToInt(params.VolunteerExaminer),
		ID:                params.ID,
		Version:           params.Version,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return sqlcgen.Person{}, db.ErrStale
		}
		return sqlcgen.Person{}, fmt.Errorf("members: update person: %w", err)
	}

	audit.StampResource(ctx, "person", params.ID)
	return person, nil
}

// DeactivatePerson soft-deletes a person.
func (s *Service) DeactivatePerson(ctx context.Context, p *authz.Principal, id, version int64) error {
	if err := authz.Authorize(ctx, p, "member.deactivate", nil); err != nil {
		return err
	}

	// Read first so a missing person is not misreported as a version conflict.
	if _, err := s.Q.GetPerson(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return fmt.Errorf("members: deactivate: %w", err)
	}

	if _, err := s.Q.DeactivatePerson(ctx, sqlcgen.DeactivatePersonParams{ID: id, Version: version}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return db.ErrStale
		}
		return fmt.Errorf("members: deactivate: %w", err)
	}

	audit.StampResource(ctx, "person", id)
	return nil
}

// ReactivatePerson restores a deactivated person.
func (s *Service) ReactivatePerson(ctx context.Context, p *authz.Principal, id, version int64) error {
	if err := authz.Authorize(ctx, p, "member.deactivate", nil); err != nil {
		return err
	}

	// Read first so a missing person is not misreported as a version conflict.
	if _, err := s.Q.GetPerson(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return fmt.Errorf("members: reactivate: %w", err)
	}

	if _, err := s.Q.ReactivatePerson(ctx, sqlcgen.ReactivatePersonParams{ID: id, Version: version}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return db.ErrStale
		}
		return fmt.Errorf("members: reactivate: %w", err)
	}

	audit.StampResource(ctx, "person", id)
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

	audit.StampResource(ctx, "membership", membershipID)
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

	audit.StampResource(ctx, "membership", membershipID)
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

	audit.StampResource(ctx, "membership", membershipID)
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

	audit.StampResource(ctx, "membership", membershipID)
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

	audit.StampResource(ctx, "fcc_verification", verificationID)
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

	audit.StampResource(ctx, "membership", params.MembershipID)
	return g, nil
}

// UpdateHonoraryGrantParams contains the mutable fields of an honorary grant.
// Empty strings mean "leave unchanged", matching PATCH semantics.
type UpdateHonoraryGrantParams struct {
	GrantID int64
	Version int64
	Reason  string
	EndsOn  string
}

// UpdateHonoraryGrant edits an existing honorary grant under optimistic
// concurrency. Supplying an end date converts a lifetime grant into a term
// grant, because the schema forbids a lifetime grant from carrying one.
func (s *Service) UpdateHonoraryGrant(ctx context.Context, p *authz.Principal, params UpdateHonoraryGrantParams) (sqlcgen.HonoraryGrant, error) {
	if err := authz.Authorize(ctx, p, "honorary.grant", nil); err != nil {
		return sqlcgen.HonoraryGrant{}, err
	}

	// Read first so a missing grant is reported as not-found rather than as a
	// version conflict, and so unset fields keep their current values.
	existing, err := s.Q.GetHonoraryGrant(ctx, params.GrantID)
	if err != nil {
		if err == sql.ErrNoRows {
			return sqlcgen.HonoraryGrant{}, err
		}
		return sqlcgen.HonoraryGrant{}, fmt.Errorf("members: update honorary: %w", err)
	}

	reason := params.Reason
	if reason == "" {
		reason = existing.Reason
	}
	endsOn := existing.EndsOn
	isLifetime := existing.IsLifetime
	if params.EndsOn != "" {
		endsOn = sqlNullString(params.EndsOn)
		isLifetime = 0
	}

	g, err := s.Q.UpdateHonoraryGrant(ctx, sqlcgen.UpdateHonoraryGrantParams{
		Reason:     reason,
		EndsOn:     endsOn,
		IsLifetime: isLifetime,
		ID:         params.GrantID,
		Version:    params.Version,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return sqlcgen.HonoraryGrant{}, db.ErrStale
		}
		return sqlcgen.HonoraryGrant{}, fmt.Errorf("members: update honorary: %w", err)
	}

	audit.StampResource(ctx, "honorary_grant", params.GrantID)
	return g, nil
}

// ExpireHonoraryGrant ends an honorary grant as of today. Expiry is the
// non-punitive counterpart of revocation: the grant simply stops, so it is
// recorded as an end date rather than a revocation.
func (s *Service) ExpireHonoraryGrant(ctx context.Context, p *authz.Principal, grantID, version int64) error {
	if err := authz.Authorize(ctx, p, "honorary.grant", nil); err != nil {
		return err
	}

	// Read first so a missing grant is not misreported as a version conflict.
	if _, err := s.Q.GetHonoraryGrant(ctx, grantID); err != nil {
		if err == sql.ErrNoRows {
			return err
		}
		return fmt.Errorf("members: expire honorary: %w", err)
	}

	if _, err := s.Q.ExpireHonoraryGrant(ctx, sqlcgen.ExpireHonoraryGrantParams{
		ID:      grantID,
		Version: version,
	}); err != nil {
		if err == sql.ErrNoRows {
			return db.ErrStale
		}
		return fmt.Errorf("members: expire honorary: %w", err)
	}

	audit.StampResource(ctx, "honorary_grant", grantID)
	return nil
}

// RevokeHonoraryGrant revokes an active honorary grant under optimistic
// concurrency: a stale version leaves the grant untouched and reports a
// conflict rather than silently doing nothing.
func (s *Service) RevokeHonoraryGrant(ctx context.Context, p *authz.Principal, grantID, version int64, reason string) error {
	if err := authz.Authorize(ctx, p, "honorary.grant", nil); err != nil {
		return err
	}

	// Read first so a missing grant is not misreported as a version conflict.
	if _, err := s.Q.GetHonoraryGrant(ctx, grantID); err != nil {
		if err == sql.ErrNoRows {
			return err
		}
		return fmt.Errorf("members: revoke honorary: %w", err)
	}

	if _, err := s.Q.RevokeHonoraryGrant(ctx, sqlcgen.RevokeHonoraryGrantParams{
		RevokedBy:    sql.NullInt64{Int64: p.UserID, Valid: true},
		RevokeReason: sqlNullString(reason),
		ID:           grantID,
		Version:      version,
	}); err != nil {
		if err == sql.ErrNoRows {
			return db.ErrStale
		}
		return fmt.Errorf("members: revoke honorary: %w", err)
	}

	audit.StampResource(ctx, "honorary_grant", grantID)
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

	audit.StampResource(ctx, "person", params.PersonID)
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

	// Read first so a missing contact method is not misreported as a conflict.
	if _, err := s.Q.GetContactMethod(ctx, contactMethodID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return fmt.Errorf("members: archive contact method: %w", err)
	}

	_, err := s.Q.ArchiveContactMethod(ctx, sqlcgen.ArchiveContactMethodParams{
		ID:      contactMethodID,
		Version: version,
	})
	if errors.Is(err, sql.ErrNoRows) {
		err = db.ErrStale
	}
	if err != nil {
		return fmt.Errorf("members: archive contact: %w", err)
	}

	audit.StampResource(ctx, "contact_method", contactMethodID)
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

	audit.StampResource(ctx, "contact_method", contactMethodID)
	return nil
}

// UpdateContactMethodParams contains the editable fields of a contact method.
type UpdateContactMethodParams struct {
	ID      int64
	Version int64
	Label   string

	ValueRaw  string
	ValueNorm string

	PostalLine1      string
	PostalLine2      string
	PostalCity       string
	PostalState      string
	PostalPostalCode string
	PostalCountry    string
}

// UpdateContactMethod edits an existing contact method in place.
//
// The query for this existed from Phase 1 with no service method and no caller.
// Correcting a number a member already has is the single most common member
// correction there is, so the reviewed-request path needs a real adapter rather
// than archive-then-add, which would lose the contact's identity and its
// visibility history.
func (s *Service) UpdateContactMethod(ctx context.Context, p *authz.Principal, params UpdateContactMethodParams) (sqlcgen.ContactMethod, error) {
	if err := authz.Authorize(ctx, p, "contact_method.write", nil); err != nil {
		return sqlcgen.ContactMethod{}, err
	}

	// Read first so a missing contact method is a 404 rather than a
	// misreported conflict.
	if _, err := s.Q.GetContactMethod(ctx, params.ID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sqlcgen.ContactMethod{}, err
		}
		return sqlcgen.ContactMethod{}, fmt.Errorf("members: update contact method: %w", err)
	}

	cm, err := s.Q.UpdateContactMethod(ctx, sqlcgen.UpdateContactMethodParams{
		Label:            sqlNullString(params.Label),
		ValueRaw:         params.ValueRaw,
		ValueNorm:        params.ValueNorm,
		PostalLine1:      sqlNullString(params.PostalLine1),
		PostalLine2:      sqlNullString(params.PostalLine2),
		PostalCity:       sqlNullString(params.PostalCity),
		PostalState:      sqlNullString(params.PostalState),
		PostalPostalCode: sqlNullString(params.PostalPostalCode),
		PostalCountry:    sqlNullString(params.PostalCountry),
		ID:               params.ID,
		Version:          params.Version,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return sqlcgen.ContactMethod{}, db.ErrStale
	}
	if err != nil {
		return sqlcgen.ContactMethod{}, fmt.Errorf("members: update contact: %w", err)
	}

	audit.StampResource(ctx, "contact_method", params.ID)
	return cm, nil
}

// prefSourceOrDefault keeps an unset source meaning "officer", so an existing
// caller that does not state one behaves exactly as before.
func prefSourceOrDefault(source string) string {
	if source == "" {
		return PrefSourceOfficer
	}
	return source
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

	audit.StampResource(ctx, "note", note.ID)
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

	audit.StampResource(ctx, "note", noteID)
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
func (s *Service) SetDirectoryVisibility(ctx context.Context, p *authz.Principal, contactMethodID int64, audience, source string) (sqlcgen.ContactMethodVisibilityEvent, error) {
	if err := authz.Authorize(ctx, p, "sharing_pref.write.officer", nil); err != nil {
		return sqlcgen.ContactMethodVisibilityEvent{}, err
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	ev, err := s.Q.CreateVisibilityEvent(ctx, sqlcgen.CreateVisibilityEventParams{
		ContactMethodID: contactMethodID,
		Audience:        audience,
		Source:          prefSourceOrDefault(source),
		EffectiveAt:     now,
		ActorUserID:     sql.NullInt64{Int64: p.UserID, Valid: true},
	})
	if err != nil {
		return sqlcgen.ContactMethodVisibilityEvent{}, fmt.Errorf("members: set visibility: %w", err)
	}

	audit.StampResource(ctx, "contact_method", contactMethodID)
	return ev, nil
}

// GetAcsAresSharing returns a person's current ACS/ARES sharing preference.
//
// The preference is stored as immutable events (ADR: preference history), so
// "current" is the most recent one. A person who has never had a preference
// recorded returns sql.ErrNoRows — deliberately not a fabricated default,
// because "no preference on file" and "declined to participate" are different
// answers and an officer acting on the difference deserves the real one.
func (s *Service) GetAcsAresSharing(ctx context.Context, p *authz.Principal, personID int64) (sqlcgen.AcsAresSharingEvent, error) {
	if err := authz.Authorize(ctx, p, "member.read", nil); err != nil {
		return sqlcgen.AcsAresSharingEvent{}, err
	}

	// Distinguish "no such person" from "person with no preference recorded".
	if _, err := s.Q.GetPerson(ctx, personID); err != nil {
		return sqlcgen.AcsAresSharingEvent{}, err
	}

	ev, err := s.Q.GetLatestAcsAresSharing(ctx, personID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sqlcgen.AcsAresSharingEvent{}, err
		}
		return sqlcgen.AcsAresSharingEvent{}, fmt.Errorf("members: get acs/ares: %w", err)
	}
	return ev, nil
}

// SetAcsAresSharing records an ACS/ARES sharing preference for a person.
func (s *Service) SetAcsAresSharing(ctx context.Context, p *authz.Principal, personID int64, participates bool, reason, source string) (sqlcgen.AcsAresSharingEvent, error) {
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
		Source:       prefSourceOrDefault(source),
		EffectiveAt:  now,
		ActorUserID:  sql.NullInt64{Int64: p.UserID, Valid: true},
		Reason:       sqlNullString(reason),
	})
	if err != nil {
		return sqlcgen.AcsAresSharingEvent{}, fmt.Errorf("members: set acs/ares: %w", err)
	}

	audit.StampResource(ctx, "person", personID)
	return ev, nil
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

// boolToInt renders a flag the way SQLite stores one.
func boolToInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}
