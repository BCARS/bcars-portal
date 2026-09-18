package members

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/db"
	"github.com/bcars/bcars-portal/internal/db/dbtest"
	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/authz"
)

func setupTest(t *testing.T) (*Service, *authz.Principal) {
	t.Helper()
	d := dbtest.Open(t)
	var err error

	// Create a test user.
	_, err = d.Exec(`INSERT INTO users (email, is_active) VALUES ('admin@bcars.org', 1)`)
	require.NoError(t, err)

	svc := NewService(d)

	// Principal with all capabilities.
	caps := authz.Codes()
	principal := &authz.Principal{UserID: 1, Capabilities: caps}

	return svc, principal
}

// --- Person CRUD ---

func TestCreateAndGetPerson(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()

	person, err := svc.CreatePerson(ctx, p, CreatePersonParams{
		DisplayName: "Alice Test",
		SortName:    "Test, Alice",
		CallSign:    "KA1AAA",
		BaseType:    "full",
	})
	require.NoError(t, err)
	assert.Equal(t, "Alice Test", person.DisplayName)
	assert.Equal(t, int64(1), person.ID)

	got, err := svc.GetPerson(ctx, p, person.ID)
	require.NoError(t, err)
	assert.Equal(t, "Alice Test", got.DisplayName)
	assert.Equal(t, "KA1AAA", got.CallSign.String)
}

func TestListPersons(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()

	_, err := svc.CreatePerson(ctx, p, CreatePersonParams{
		DisplayName: "Alice Test", SortName: "Test, Alice", CallSign: "KA1AAA",
	})
	require.NoError(t, err)
	_, err = svc.CreatePerson(ctx, p, CreatePersonParams{
		DisplayName: "Bob Sample", SortName: "Sample, Bob", CallSign: "KA1BBB",
	})
	require.NoError(t, err)

	// List all.
	all, err := svc.ListPersons(ctx, p, ListPersonsParams{})
	require.NoError(t, err)
	assert.Len(t, all, 2)

	// Search by name.
	found, err := svc.ListPersons(ctx, p, ListPersonsParams{Query: "Alice"})
	require.NoError(t, err)
	assert.Len(t, found, 1)
	assert.Equal(t, "Alice Test", found[0].DisplayName)
}

func TestUpdatePerson(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()

	person, err := svc.CreatePerson(ctx, p, CreatePersonParams{
		DisplayName: "Old Name", SortName: "Name, Old",
	})
	require.NoError(t, err)

	updated, err := svc.UpdatePerson(ctx, p, UpdatePersonParams{
		ID:          person.ID,
		DisplayName: "New Name",
		SortName:    "Name, New",
		Version:     person.Version,
	})
	require.NoError(t, err)
	assert.Equal(t, "New Name", updated.DisplayName)
	assert.Equal(t, person.Version+1, updated.Version)
}

func TestUpdatePersonStale(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()

	person, err := svc.CreatePerson(ctx, p, CreatePersonParams{
		DisplayName: "Test", SortName: "Test",
	})
	require.NoError(t, err)

	// First update succeeds.
	_, err = svc.UpdatePerson(ctx, p, UpdatePersonParams{
		ID: person.ID, DisplayName: "V2", SortName: "V2", Version: person.Version,
	})
	require.NoError(t, err)

	// Second update with stale version fails.
	_, err = svc.UpdatePerson(ctx, p, UpdatePersonParams{
		ID: person.ID, DisplayName: "V3", SortName: "V3", Version: person.Version,
	})
	assert.ErrorIs(t, err, db.ErrStale)
}

func TestDeactivateReactivate(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()

	person, err := svc.CreatePerson(ctx, p, CreatePersonParams{
		DisplayName: "Test", SortName: "Test",
	})
	require.NoError(t, err)

	err = svc.DeactivatePerson(ctx, p, person.ID, person.Version)
	require.NoError(t, err)

	got, err := svc.GetPerson(ctx, p, person.ID)
	require.NoError(t, err)
	assert.True(t, got.DeactivatedAt.Valid)

	err = svc.ReactivatePerson(ctx, p, person.ID, got.Version)
	require.NoError(t, err)

	got, err = svc.GetPerson(ctx, p, person.ID)
	require.NoError(t, err)
	assert.False(t, got.DeactivatedAt.Valid)
}

// --- Authorization ---

func TestAuthorizationDenied(t *testing.T) {
	svc, _ := setupTest(t)
	ctx := context.Background()

	// Principal with no capabilities.
	noCaps := &authz.Principal{UserID: 1, Capabilities: map[string]struct{}{}}

	_, err := svc.GetPerson(ctx, noCaps, 1)
	assert.ErrorIs(t, err, authz.ErrDenied)

	_, err = svc.CreatePerson(ctx, noCaps, CreatePersonParams{DisplayName: "X", SortName: "X"})
	assert.ErrorIs(t, err, authz.ErrDenied)

	_, err = svc.ListPersons(ctx, noCaps, ListPersonsParams{})
	assert.ErrorIs(t, err, authz.ErrDenied)
}

func TestUnauthenticated(t *testing.T) {
	svc, _ := setupTest(t)
	ctx := context.Background()

	_, err := svc.GetPerson(ctx, nil, 1)
	assert.ErrorIs(t, err, authz.ErrUnauthenticated)
}

// --- Membership operations ---

func TestApproveMembership(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()

	person, err := svc.CreatePerson(ctx, p, CreatePersonParams{
		DisplayName: "Test", SortName: "Test", BaseType: "full",
	})
	require.NoError(t, err)

	memberships, err := svc.ListMembershipsByPerson(ctx, p, person.ID)
	require.NoError(t, err)
	require.Len(t, memberships, 1)
	assert.Equal(t, "pending", memberships[0].Lifecycle)

	m, err := svc.ApproveMembership(ctx, p, memberships[0].ID, memberships[0].Version, "full", "Meets requirements")
	require.NoError(t, err)
	assert.Equal(t, "approved", m.Lifecycle)
	assert.Equal(t, "full", m.BaseType)
}

func TestRejectMembership(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()

	person, err := svc.CreatePerson(ctx, p, CreatePersonParams{
		DisplayName: "Test", SortName: "Test", BaseType: "full",
	})
	require.NoError(t, err)

	memberships, err := svc.ListMembershipsByPerson(ctx, p, person.ID)
	require.NoError(t, err)

	m, err := svc.RejectMembership(ctx, p, memberships[0].ID, memberships[0].Version, "Does not qualify")
	require.NoError(t, err)
	assert.Equal(t, "rejected", m.Lifecycle)
}

func TestTransitionLifecycle(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()

	person, err := svc.CreatePerson(ctx, p, CreatePersonParams{
		DisplayName: "Test", SortName: "Test", BaseType: "full",
	})
	require.NoError(t, err)

	memberships, err := svc.ListMembershipsByPerson(ctx, p, person.ID)
	require.NoError(t, err)

	// Approve first.
	m, err := svc.ApproveMembership(ctx, p, memberships[0].ID, memberships[0].Version, "full", "ok")
	require.NoError(t, err)

	// Transition to resigned.
	m, err = svc.TransitionLifecycle(ctx, p, m.ID, m.Version, "resigned")
	require.NoError(t, err)
	assert.Equal(t, "resigned", m.Lifecycle)
	assert.True(t, m.EndedOn.Valid)
}

// --- FCC Verification ---

func TestFCCVerification(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()

	person, err := svc.CreatePerson(ctx, p, CreatePersonParams{
		DisplayName: "Test", SortName: "Test", BaseType: "full",
	})
	require.NoError(t, err)

	memberships, err := svc.ListMembershipsByPerson(ctx, p, person.ID)
	require.NoError(t, err)

	v, err := svc.VerifyFCC(ctx, p, memberships[0].ID, "KA1AAA", "General", "manual_check")
	require.NoError(t, err)
	assert.Equal(t, "KA1AAA", v.CallSign)
	assert.Equal(t, "manual_check", v.VerificationSource)

	err = svc.RevokeFCCVerification(ctx, p, v.ID, "License expired")
	require.NoError(t, err)
}

// --- Honorary Grants ---

func TestHonoraryGrant(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()

	person, err := svc.CreatePerson(ctx, p, CreatePersonParams{
		DisplayName: "Test", SortName: "Test", BaseType: "associate",
	})
	require.NoError(t, err)

	memberships, err := svc.ListMembershipsByPerson(ctx, p, person.ID)
	require.NoError(t, err)

	g, err := svc.CreateHonoraryGrant(ctx, p, CreateHonoraryGrantParams{
		MembershipID: memberships[0].ID,
		StartsOn:     "2026-01-01",
		IsLifetime:   true,
		Reason:       "Outstanding service",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), g.IsLifetime)
	assert.Equal(t, "Outstanding service", g.Reason)

	err = svc.RevokeHonoraryGrant(ctx, p, g.ID, g.Version, "Revoked by board")
	require.NoError(t, err)
}

// newHonoraryGrant creates a person, membership and lifetime honorary grant,
// returning the grant.
func newHonoraryGrant(t *testing.T, svc *Service, p *authz.Principal) sqlcgen.HonoraryGrant {
	t.Helper()
	ctx := context.Background()

	person, err := svc.CreatePerson(ctx, p, CreatePersonParams{
		DisplayName: "Test", SortName: "Test", BaseType: "associate",
	})
	require.NoError(t, err)

	memberships, err := svc.ListMembershipsByPerson(ctx, p, person.ID)
	require.NoError(t, err)

	g, err := svc.CreateHonoraryGrant(ctx, p, CreateHonoraryGrantParams{
		MembershipID: memberships[0].ID,
		StartsOn:     "2026-01-01",
		IsLifetime:   true,
		Reason:       "Outstanding service",
	})
	require.NoError(t, err)
	return g
}

func TestUpdateHonoraryGrant(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()
	g := newHonoraryGrant(t, svc, p)

	// Reason only: the lifetime flag survives.
	updated, err := svc.UpdateHonoraryGrant(ctx, p, UpdateHonoraryGrantParams{
		GrantID: g.ID, Version: g.Version, Reason: "Corrected citation",
	})
	require.NoError(t, err)
	assert.Equal(t, "Corrected citation", updated.Reason)
	assert.Equal(t, int64(1), updated.IsLifetime)
	assert.Equal(t, g.Version+1, updated.Version)

	// Adding an end date converts the lifetime grant to a term grant.
	updated, err = svc.UpdateHonoraryGrant(ctx, p, UpdateHonoraryGrantParams{
		GrantID: g.ID, Version: updated.Version, EndsOn: "2027-06-30",
	})
	require.NoError(t, err)
	assert.Equal(t, "2027-06-30", updated.EndsOn.String)
	assert.Equal(t, int64(0), updated.IsLifetime)
	assert.Equal(t, "Corrected citation", updated.Reason, "omitted fields keep their value")
}

func TestUpdateHonoraryGrantVersionConflict(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()
	g := newHonoraryGrant(t, svc, p)

	_, err := svc.UpdateHonoraryGrant(ctx, p, UpdateHonoraryGrantParams{
		GrantID: g.ID, Version: g.Version + 1, Reason: "Stale write",
	})
	require.ErrorIs(t, err, db.ErrStale)
}

func TestUpdateHonoraryGrantNotFound(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()

	_, err := svc.UpdateHonoraryGrant(ctx, p, UpdateHonoraryGrantParams{
		GrantID: 999, Version: 1, Reason: "No such grant",
	})
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestExpireHonoraryGrant(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()
	g := newHonoraryGrant(t, svc, p)

	require.NoError(t, svc.ExpireHonoraryGrant(ctx, p, g.ID, g.Version))

	got, err := svc.Q.GetHonoraryGrant(ctx, g.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), got.IsLifetime)
	assert.Equal(t, time.Now().UTC().Format("2006-01-02"), got.EndsOn.String)
	assert.Equal(t, g.Version+1, got.Version)
	assert.False(t, got.RevokedAt.Valid, "expiry is not a revocation")
}

func TestExpireHonoraryGrantVersionConflict(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()
	g := newHonoraryGrant(t, svc, p)

	err := svc.ExpireHonoraryGrant(ctx, p, g.ID, g.Version+1)
	require.ErrorIs(t, err, db.ErrStale)
}

func TestExpireHonoraryGrantNotFound(t *testing.T) {
	svc, p := setupTest(t)

	err := svc.ExpireHonoraryGrant(context.Background(), p, 999, 1)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestRevokeHonoraryGrant(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()
	g := newHonoraryGrant(t, svc, p)

	require.NoError(t, svc.RevokeHonoraryGrant(ctx, p, g.ID, g.Version, "Revoked by board"))

	got, err := svc.Q.GetHonoraryGrant(ctx, g.ID)
	require.NoError(t, err)
	assert.True(t, got.RevokedAt.Valid)
	assert.Equal(t, "Revoked by board", got.RevokeReason.String)
	assert.Equal(t, p.UserID, got.RevokedBy.Int64)
	assert.Equal(t, g.Version+1, got.Version)
}

func TestRevokeHonoraryGrantVersionConflict(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()
	g := newHonoraryGrant(t, svc, p)

	err := svc.RevokeHonoraryGrant(ctx, p, g.ID, g.Version+1, "Stale revoke")
	require.ErrorIs(t, err, db.ErrStale)

	// A rejected revoke must leave the grant exactly as it was.
	got, err := svc.Q.GetHonoraryGrant(ctx, g.ID)
	require.NoError(t, err)
	assert.False(t, got.RevokedAt.Valid, "stale revoke must not mark the grant revoked")
	assert.False(t, got.RevokeReason.Valid)
	assert.False(t, got.RevokedBy.Valid)
	assert.Equal(t, g.Version, got.Version)
	assert.Equal(t, g.UpdatedAt, got.UpdatedAt)
}

func TestRevokeHonoraryGrantNotFound(t *testing.T) {
	svc, p := setupTest(t)

	err := svc.RevokeHonoraryGrant(context.Background(), p, 999, 1, "No such grant")
	require.ErrorIs(t, err, sql.ErrNoRows)
}

// --- Contact Methods ---

func TestContactMethods(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()

	person, err := svc.CreatePerson(ctx, p, CreatePersonParams{
		DisplayName: "Test", SortName: "Test",
	})
	require.NoError(t, err)

	// Add email.
	email, err := svc.CreateContactMethod(ctx, p, CreateContactMethodParams{
		PersonID:  person.ID,
		Kind:      "email",
		ValueRaw:  "test@example.invalid",
		ValueNorm: "test@example.invalid",
		IsPrimary: true,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), email.IsPrimary)

	// Add phone.
	phone, err := svc.CreateContactMethod(ctx, p, CreateContactMethodParams{
		PersonID:  person.ID,
		Kind:      "phone",
		ValueRaw:  "555-111-2222",
		ValueNorm: "5551112222",
	})
	require.NoError(t, err)

	// List.
	methods, err := svc.ListContactMethods(ctx, p, person.ID)
	require.NoError(t, err)
	assert.Len(t, methods, 2)

	// Make phone primary.
	err = svc.MakePrimary(ctx, p, phone.ID)
	require.NoError(t, err)

	methods, err = svc.ListContactMethods(ctx, p, person.ID)
	require.NoError(t, err)
	for _, m := range methods {
		if m.ID == phone.ID {
			assert.Equal(t, int64(1), m.IsPrimary)
		} else {
			assert.Equal(t, int64(0), m.IsPrimary)
		}
	}

	// Archive — re-fetch version since MakePrimary bumped it.
	methods, err = svc.ListContactMethods(ctx, p, person.ID)
	require.NoError(t, err)
	var emailVersion int64
	for _, m := range methods {
		if m.ID == email.ID {
			emailVersion = m.Version
		}
	}
	err = svc.ArchiveContactMethod(ctx, p, email.ID, emailVersion)
	require.NoError(t, err)

	methods, err = svc.ListContactMethods(ctx, p, person.ID)
	require.NoError(t, err)
	assert.Len(t, methods, 1, "archived method not listed")
}

// --- Notes ---

func TestNotes(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()

	person, err := svc.CreatePerson(ctx, p, CreatePersonParams{
		DisplayName: "Test", SortName: "Test",
	})
	require.NoError(t, err)

	note, err := svc.CreateNote(ctx, p, CreateNoteParams{
		SubjectKind: "person",
		SubjectID:   person.ID,
		Category:    "general",
		Visibility:  "officer",
		Body:        "Initial note",
	})
	require.NoError(t, err)
	assert.Equal(t, "Initial note", note.Body)

	// Update preserves revision.
	updated, err := svc.UpdateNote(ctx, p, note.ID, note.Version, "Updated note", "typo fix")
	require.NoError(t, err)
	assert.Equal(t, "Updated note", updated.Body)
	assert.Equal(t, note.Version+1, updated.Version)

	// List.
	notes, err := svc.ListNotes(ctx, p, "person", person.ID, 0, 0)
	require.NoError(t, err)
	assert.Len(t, notes, 1)
	assert.Equal(t, "Updated note", notes[0].Body)
}

func TestNoteTreasurerCapability(t *testing.T) {
	svc, _ := setupTest(t)
	ctx := context.Background()

	// Principal with only officer notes capability.
	officerOnly := &authz.Principal{
		UserID: 1,
		Capabilities: map[string]struct{}{
			"member.read":         {},
			"member.create":       {},
			"notes.write.officer": {},
		},
	}

	person, err := svc.CreatePerson(ctx, officerOnly, CreatePersonParams{
		DisplayName: "Test", SortName: "Test",
	})
	require.NoError(t, err)

	// Officer note works.
	_, err = svc.CreateNote(ctx, officerOnly, CreateNoteParams{
		SubjectKind: "person", SubjectID: person.ID,
		Category: "general", Visibility: "officer", Body: "ok",
	})
	require.NoError(t, err)

	// Treasurer note denied.
	_, err = svc.CreateNote(ctx, officerOnly, CreateNoteParams{
		SubjectKind: "person", SubjectID: person.ID,
		Category: "general", Visibility: "treasurer", Body: "nope",
	})
	assert.ErrorIs(t, err, authz.ErrDenied)
}

// --- Sharing Preferences ---

func TestSharingPreferences(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()

	person, err := svc.CreatePerson(ctx, p, CreatePersonParams{
		DisplayName: "Test", SortName: "Test",
	})
	require.NoError(t, err)

	email, err := svc.CreateContactMethod(ctx, p, CreateContactMethodParams{
		PersonID: person.ID, Kind: "email",
		ValueRaw: "test@example.invalid", ValueNorm: "test@example.invalid",
	})
	require.NoError(t, err)

	// Set directory visibility.
	vis, err := svc.SetDirectoryVisibility(ctx, p, email.ID, AudienceFullMembers, PrefSourceOfficer)
	require.NoError(t, err)
	assert.Equal(t, AudienceFullMembers, vis.Audience)

	// An audience the directory does not know is refused, not recorded. This
	// test used to record "members_only", which the directory would have read
	// as "not full_members" and silently hidden.
	_, err = svc.SetDirectoryVisibility(ctx, p, email.ID, "members_only", PrefSourceOfficer)
	require.ErrorIs(t, err, ErrInvalidAudience)
	latest, err := svc.Q.GetLatestVisibility(ctx, email.ID)
	require.NoError(t, err)
	assert.Equal(t, AudienceFullMembers, latest.Audience, "a refused audience must not be written")

	// Set ACS/ARES sharing.
	sharing, err := svc.SetAcsAresSharing(ctx, p, person.ID, true, "Joined ARES team", PrefSourceOfficer)
	require.NoError(t, err)
	assert.Equal(t, int64(1), sharing.Participates)
}

// --- Version-conflict detection (bcars-portal-fmc.19) ---
//
// Each of these used a :exec query, so a stale version updated nothing and
// reported success. The assertions check the row is untouched, not merely that
// an error came back — a test that only checks the error would still pass if
// the write happened AND an error was returned.

func TestDeactivatePersonDetectsStaleVersion(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()

	person, err := svc.CreatePerson(ctx, p, CreatePersonParams{
		DisplayName: "Stale Deactivate", SortName: "Deactivate, Stale", BaseType: "full",
	})
	require.NoError(t, err)

	err = svc.DeactivatePerson(ctx, p, person.ID, person.Version+99)
	require.ErrorIs(t, err, db.ErrStale)

	after, err := svc.GetPerson(ctx, p, person.ID)
	require.NoError(t, err)
	assert.False(t, after.DeactivatedAt.Valid, "a stale deactivate must not deactivate")
	assert.Equal(t, person.Version, after.Version, "a stale deactivate must not bump version")
}

func TestDeactivatePersonSucceedsWithCurrentVersion(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()

	person, err := svc.CreatePerson(ctx, p, CreatePersonParams{
		DisplayName: "Good Deactivate", SortName: "Deactivate, Good", BaseType: "full",
	})
	require.NoError(t, err)

	require.NoError(t, svc.DeactivatePerson(ctx, p, person.ID, person.Version))

	after, err := svc.GetPerson(ctx, p, person.ID)
	require.NoError(t, err)
	assert.True(t, after.DeactivatedAt.Valid)
}

func TestDeactivateMissingPersonIsNotFoundNotConflict(t *testing.T) {
	svc, p := setupTest(t)

	err := svc.DeactivatePerson(context.Background(), p, 999999, 1)
	assert.ErrorIs(t, err, sql.ErrNoRows,
		"a missing person must not be misreported as a version conflict")
}

func TestReactivatePersonDetectsStaleVersion(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()

	person, err := svc.CreatePerson(ctx, p, CreatePersonParams{
		DisplayName: "Stale Reactivate", SortName: "Reactivate, Stale", BaseType: "full",
	})
	require.NoError(t, err)
	require.NoError(t, svc.DeactivatePerson(ctx, p, person.ID, person.Version))

	// The deactivate moved the version, so the original one is now stale.
	err = svc.ReactivatePerson(ctx, p, person.ID, person.Version)
	require.ErrorIs(t, err, db.ErrStale)

	after, err := svc.GetPerson(ctx, p, person.ID)
	require.NoError(t, err)
	assert.True(t, after.DeactivatedAt.Valid, "a stale reactivate must not reactivate")
}

func TestArchiveContactMethodDetectsStaleVersion(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()

	person, err := svc.CreatePerson(ctx, p, CreatePersonParams{
		DisplayName: "Stale Archive", SortName: "Archive, Stale", BaseType: "full",
	})
	require.NoError(t, err)

	cm, err := svc.CreateContactMethod(ctx, p, CreateContactMethodParams{
		PersonID: person.ID, Kind: "email",
		ValueRaw: "stale@bcars.example", ValueNorm: "stale@bcars.example",
	})
	require.NoError(t, err)

	err = svc.ArchiveContactMethod(ctx, p, cm.ID, cm.Version+99)
	require.ErrorIs(t, err, db.ErrStale)

	after, err := svc.Q.GetContactMethod(ctx, cm.ID)
	require.NoError(t, err)
	assert.False(t, after.ArchivedAt.Valid, "a stale archive must not archive")
	assert.Equal(t, cm.Version, after.Version)
}

// --- ACS/ARES sharing preference read-back (bcars-portal-fmc.24) ---

func TestAcsAresSharingRoundTrip(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()

	person, err := svc.CreatePerson(ctx, p, CreatePersonParams{
		DisplayName: "Sharing Member", SortName: "Member, Sharing", BaseType: "full",
	})
	require.NoError(t, err)

	_, err = svc.SetAcsAresSharing(ctx, p, person.ID, true, "asked at a meeting", PrefSourceOfficer)
	require.NoError(t, err)

	got, err := svc.GetAcsAresSharing(ctx, p, person.ID)
	require.NoError(t, err)
	assert.Equal(t, person.ID, got.PersonID)
	assert.Equal(t, int64(1), got.Participates)
	assert.Equal(t, "asked at a meeting", got.Reason.String)
}

// TestAcsAresSharingReturnsMostRecent is the whole point of the immutable
// preference-history pattern: the current value is the newest event, not the
// first one.
func TestAcsAresSharingReturnsMostRecent(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()

	person, err := svc.CreatePerson(ctx, p, CreatePersonParams{
		DisplayName: "Changed Mind", SortName: "Mind, Changed", BaseType: "full",
	})
	require.NoError(t, err)

	_, err = svc.SetAcsAresSharing(ctx, p, person.ID, true, "opted in", PrefSourceOfficer)
	require.NoError(t, err)
	time.Sleep(2 * time.Millisecond) // effective_at has millisecond resolution
	_, err = svc.SetAcsAresSharing(ctx, p, person.ID, false, "opted out later", PrefSourceOfficer)
	require.NoError(t, err)

	got, err := svc.GetAcsAresSharing(ctx, p, person.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), got.Participates, "the latest preference must win")
	assert.Equal(t, "opted out later", got.Reason.String)

	// The earlier event is retained — the history is immutable.
	history, err := svc.Q.ListAcsAresSharingHistory(ctx, person.ID)
	require.NoError(t, err)
	assert.Len(t, history, 2, "setting a preference must not overwrite the previous one")
}

// TestAcsAresSharingUnsetIsNotFound keeps "no preference on file" distinct from
// "declined to participate" — an officer acting on the difference needs the
// real answer, not a fabricated default.
func TestAcsAresSharingUnsetIsNotFound(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()

	person, err := svc.CreatePerson(ctx, p, CreatePersonParams{
		DisplayName: "No Preference", SortName: "Preference, No", BaseType: "full",
	})
	require.NoError(t, err)

	_, err = svc.GetAcsAresSharing(ctx, p, person.ID)
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

func TestAcsAresSharingMissingPersonIsNotFound(t *testing.T) {
	svc, p := setupTest(t)

	_, err := svc.GetAcsAresSharing(context.Background(), p, 999999)
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

// --- Memberships awaiting a decision (bcars-portal-ges) ---

// TestPendingCountAndListCannotDisagree is the property the bead asks for. The
// dashboard tile said 2 and there was no way to reach the two; the fix is a
// route, but the fix that matters is that the number and the rows come from one
// predicate.
//
// The fixture is built to make a careless pair disagree: a pending membership
// that has ended, a rejected one, an approved one, and a pending one belonging
// to a deactivated person. Any of those falling on different sides of the two
// queries shows up here as a mismatch.
func TestPendingCountAndListCannotDisagree(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()

	seed := func(name, lifecycle, endedOn string, deactivated bool) {
		t.Helper()
		person, err := svc.CreatePerson(ctx, p, CreatePersonParams{DisplayName: name, SortName: name})
		require.NoError(t, err)
		_, err = svc.DB.Exec(
			`INSERT INTO memberships (person_id, base_type, lifecycle, ended_on) VALUES (?, 'full', ?, ?)`,
			person.ID, lifecycle, sqlNullString(endedOn))
		require.NoError(t, err)
		if deactivated {
			_, err = svc.DB.Exec(
				`UPDATE persons SET deactivated_at = '2026-01-01T00:00:00.000Z' WHERE id = ?`, person.ID)
			require.NoError(t, err)
		}
	}

	seed("Waiting One", "pending", "", false)
	seed("Waiting Two", "pending", "", false)
	seed("Withdrawn", "pending", "2026-02-01", false)
	seed("Refused", "rejected", "", false)
	seed("Member", "approved", "", false)
	seed("Waiting Deactivated", "pending", "", true)

	list, err := svc.ListPendingMemberships(ctx, p, 50, 0)
	require.NoError(t, err)
	count, err := svc.CountPendingMemberships(ctx, p)
	require.NoError(t, err)

	assert.Equal(t, int(count), len(list),
		"the tile's number and the queue's rows must describe the same set")
	assert.Equal(t, int64(3), count,
		"two waiting, plus the deactivated person's, which still needs a decision")

	names := make([]string, 0, len(list))
	for _, m := range list {
		names = append(names, m.DisplayName)
	}
	assert.NotContains(t, names, "Withdrawn", "an ended membership is not waiting on anyone")
	assert.NotContains(t, names, "Refused", "a decided membership is not waiting on anyone")
	assert.NotContains(t, names, "Member", "an approved membership is not waiting on anyone")

	// A deactivated person's pending membership is listed rather than hidden,
	// and says so, because hiding it is how the count and the queue drift
	// apart again.
	var deactivated PendingMembership
	for _, m := range list {
		if m.DisplayName == "Waiting Deactivated" {
			deactivated = m
		}
	}
	require.NotZero(t, deactivated.MembershipID)
	assert.True(t, deactivated.Deactivated)
}

func TestPendingMembershipsNeedMemberRead(t *testing.T) {
	svc, _ := setupTest(t)
	ctx := context.Background()
	stranger := &authz.Principal{UserID: 2}

	_, err := svc.ListPendingMemberships(ctx, stranger, 50, 0)
	assert.Error(t, err, "the queue is member data")
	_, err = svc.CountPendingMemberships(ctx, stranger)
	assert.Error(t, err, "so is the count of it")
}

// TestPersonListCarriesTheMembershipType holds the defect the bead notes
// alongside the missing route: the members list rendered a dash in the Type
// column for every row, because the query never read the membership.
func TestPersonListCarriesTheMembershipType(t *testing.T) {
	svc, p := setupTest(t)
	ctx := context.Background()

	full, err := svc.CreatePerson(ctx, p, CreatePersonParams{DisplayName: "Ada Full", SortName: "Full, Ada"})
	require.NoError(t, err)
	_, err = svc.DB.Exec(
		`INSERT INTO memberships (person_id, base_type, lifecycle) VALUES (?, 'full', 'approved')`, full.ID)
	require.NoError(t, err)

	none, err := svc.CreatePerson(ctx, p, CreatePersonParams{DisplayName: "Bob Nomember", SortName: "Nomember, Bob"})
	require.NoError(t, err)

	ended, err := svc.CreatePerson(ctx, p, CreatePersonParams{DisplayName: "Cid Lapsed", SortName: "Lapsed, Cid"})
	require.NoError(t, err)
	_, err = svc.DB.Exec(
		`INSERT INTO memberships (person_id, base_type, lifecycle, ended_on) VALUES (?, 'associate', 'approved', '2025-12-31')`,
		ended.ID)
	require.NoError(t, err)

	byID := map[int64]PersonSummary{}
	rows, err := svc.ListPersons(ctx, p, ListPersonsParams{Limit: 50})
	require.NoError(t, err)
	for _, r := range rows {
		byID[r.ID] = r
	}

	assert.Equal(t, "full", byID[full.ID].BaseType, "the list must say what the record says")
	assert.Empty(t, byID[none.ID].BaseType, "a person with no membership has no type, and the page shows a dash")
	assert.Empty(t, byID[ended.ID].BaseType, "an ended membership is not a current type")

	// The search branch is a separate query and had the same hole.
	found, err := svc.ListPersons(ctx, p, ListPersonsParams{Query: "Ada", Limit: 50})
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, "full", found[0].BaseType, "searching must not lose the type")
}
