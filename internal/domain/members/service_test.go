package members

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/db"
	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/authz"
)

func setupTest(t *testing.T) (*Service, *authz.Principal) {
	t.Helper()
	d, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	require.NoError(t, db.Migrate(d))

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
	vis, err := svc.SetDirectoryVisibility(ctx, p, email.ID, "members_only")
	require.NoError(t, err)
	assert.Equal(t, "members_only", vis.Audience)

	// Set ACS/ARES sharing.
	sharing, err := svc.SetAcsAresSharing(ctx, p, person.ID, true, "Joined ARES team")
	require.NoError(t, err)
	assert.Equal(t, int64(1), sharing.Participates)
}
