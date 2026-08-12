package relationships

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/db"
	"github.com/bcars/bcars-portal/internal/domain/authz"
)

// A file-backed database, not ":memory:".
//
// An in-memory SQLite database belongs to ONE connection, so a pooled second
// connection sees an empty schema. A concurrency test against ":memory:" fails
// with "no such table" and proves nothing about the constraint under test.
func openTestService(t *testing.T) *Service {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relationships.db"))
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	require.NoError(t, db.Migrate(d))

	for _, name := range []string{"Person One", "Person Two"} {
		_, err = d.Exec(`INSERT INTO persons (display_name, sort_name) VALUES (?, ?)`, name, name)
		require.NoError(t, err)
	}
	_, err = d.Exec(`INSERT INTO users (email) VALUES ('officer@example.test')`)
	require.NoError(t, err)
	return NewService(d)
}

// TestVocabularyMatchesTheSchema keeps the Go constants and the CHECK
// constraint from drifting apart. A kind this package accepts and the database
// refuses would surface as a 500 on an ordinary officer action.
func TestVocabularyMatchesTheSchema(t *testing.T) {
	svc := openTestService(t)

	var schema string
	require.NoError(t, svc.DB.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'person_relationships'`).
		Scan(&schema))

	for _, kind := range KindsInOrder {
		assert.Contains(t, schema, "'"+kind+"'",
			"kind %q is accepted by the service but not by the CHECK constraint", kind)
	}

	// And the other direction: every kind the schema allows is offered.
	kindsClause := schema[strings.Index(schema, "kind"):]
	kindsClause = kindsClause[:strings.Index(kindsClause, "))")]
	for _, quoted := range strings.Split(kindsClause, "'") {
		q := strings.TrimSpace(quoted)
		if q == "" || strings.ContainsAny(q, "(),") || q == "kind" {
			continue
		}
		assert.True(t, ValidKind(q),
			"the schema allows kind %q but the service does not offer it", q)
	}
}

// TestConcurrentCreatesProduceOneActiveRelationship proves the DATABASE
// decides, not a check-then-write race in the service.
func TestConcurrentCreatesProduceOneActiveRelationship(t *testing.T) {
	svc := openTestService(t)
	ctx := context.Background()
	p := &authz.Principal{UserID: 1}

	const attempts = 6
	errs := make([]error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = svc.Create(ctx, p, CreateParams{
				FromPersonID: 1, ToPersonID: 2, Kind: KindHousehold,
			})
		}(i)
	}
	wg.Wait()

	created := 0
	for _, err := range errs {
		if err == nil {
			created++
		}
	}
	assert.Equal(t, 1, created, "exactly one concurrent create may succeed; errors: %v", errs)

	var active int
	require.NoError(t, svc.DB.QueryRow(
		`SELECT count(*) FROM person_relationships WHERE archived_at IS NULL`).Scan(&active))
	assert.Equal(t, 1, active)
}

// TestNormalizationTrimsAndLowercases pins the input handling, so "Household "
// pasted from a spreadsheet is the same kind as "household".
func TestNormalizationTrimsAndLowercases(t *testing.T) {
	svc := openTestService(t)
	ctx := context.Background()
	p := &authz.Principal{UserID: 1}

	rel, err := svc.Create(ctx, p, CreateParams{
		FromPersonID: 1, ToPersonID: 2,
		Kind:    "  Household ",
		Context: "   Shares an address.   ",
	})
	require.NoError(t, err)
	assert.Equal(t, KindHousehold, rel.Kind)
	assert.Equal(t, "Shares an address.", rel.Context)

	_, err = svc.Create(ctx, p, CreateParams{
		FromPersonID: 1, ToPersonID: 2, Kind: KindOther,
		Context: strings.Repeat("x", maxContextLength+1),
	})
	assert.ErrorIs(t, err, ErrContextTooLong)
}

// TestArchivedRelationshipsStayReadable is the reason there is no delete: an
// officer reviewing last spring's request needs the household as recorded then.
func TestArchivedRelationshipsStayReadable(t *testing.T) {
	svc := openTestService(t)
	ctx := context.Background()
	p := &authz.Principal{UserID: 1}

	rel, err := svc.Create(ctx, p, CreateParams{
		FromPersonID: 1, ToPersonID: 2, Kind: KindSpousePartner,
	})
	require.NoError(t, err)

	_, err = svc.Archive(ctx, p, rel.ID, ArchiveParams{
		Reason: "Separated.", ExpectedVersion: rel.Version,
	}, time.Now())
	require.NoError(t, err)

	current, err := svc.ListForPerson(ctx, p, 1)
	require.NoError(t, err)
	assert.Empty(t, current)

	history, err := svc.ListHistoryForPerson(ctx, p, 1)
	require.NoError(t, err)
	require.Len(t, history, 1)
	assert.False(t, history[0].Active())
	assert.Equal(t, "Separated.", history[0].ArchiveReason)
	assert.Equal(t, int64(1), history[0].ArchivedBy)
	assert.Equal(t, DirectionOutgoing, history[0].Direction)
	assert.Equal(t, int64(2), history[0].OtherPersonID)

	// From the other side the same row reads as incoming.
	fromTwo, err := svc.ListHistoryForPerson(ctx, p, 2)
	require.NoError(t, err)
	require.Len(t, fromTwo, 1)
	assert.Equal(t, DirectionIncoming, fromTwo[0].Direction)
	assert.Equal(t, int64(1), fromTwo[0].OtherPersonID)
}

// TestRelationshipsTouchNoAuthorizationTables is the invariant stated as a
// test. Whatever else this service does, it must leave access alone.
func TestRelationshipsTouchNoAuthorizationTables(t *testing.T) {
	svc := openTestService(t)
	ctx := context.Background()
	p := &authz.Principal{UserID: 1}

	rel, err := svc.Create(ctx, p, CreateParams{
		FromPersonID: 1, ToPersonID: 2, Kind: KindSpousePartner, Context: "Married.",
	})
	require.NoError(t, err)
	_, err = svc.Update(ctx, p, rel.ID, UpdateParams{
		Kind: KindHousehold, ExpectedVersion: rel.Version,
	})
	require.NoError(t, err)
	_, err = svc.Archive(ctx, p, rel.ID, ArchiveParams{ExpectedVersion: rel.Version + 1}, time.Now())
	require.NoError(t, err)

	for _, table := range []string{"member_access_grants", "user_role_grants", "sessions"} {
		var n int
		require.NoError(t, svc.DB.QueryRow(`SELECT count(*) FROM `+table).Scan(&n))
		assert.Zero(t, n, "relationship maintenance must write nothing to %s", table)
	}
}

// TestUpdateRefusesAStaleVersion covers the concurrency guard at the service
// level, where the HTTP layer's If-Match ultimately lands.
func TestUpdateRefusesAStaleVersion(t *testing.T) {
	svc := openTestService(t)
	ctx := context.Background()
	p := &authz.Principal{UserID: 1}

	rel, err := svc.Create(ctx, p, CreateParams{
		FromPersonID: 1, ToPersonID: 2, Kind: KindHousehold,
	})
	require.NoError(t, err)

	_, err = svc.Update(ctx, p, rel.ID, UpdateParams{
		Kind: KindSpousePartner, ExpectedVersion: rel.Version,
	})
	require.NoError(t, err)

	_, err = svc.Update(ctx, p, rel.ID, UpdateParams{
		Kind: KindOther, ExpectedVersion: rel.Version,
	})
	assert.ErrorIs(t, err, db.ErrStale)

	_, err = svc.Archive(ctx, p, rel.ID, ArchiveParams{ExpectedVersion: rel.Version}, time.Now())
	assert.ErrorIs(t, err, db.ErrStale, "archiving is version-guarded too")
}
