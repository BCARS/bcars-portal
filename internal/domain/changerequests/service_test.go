package changerequests_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/db/dbtest"
	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/domain/changerequests"
)

// Domain-level rules for member submission and withdrawal (bcars-portal-4ux.6).
//
// The API tests drive these through HTTP, which is where the properties matter.
// These cover the same rules one layer down, so a rule that survives only
// because a handler happens to check it first is still visible here.

func newService(t *testing.T) (*changerequests.Service, *sql.DB) {
	t.Helper()
	d := dbtest.Open(t)
	var err error

	_, err = d.Exec(`INSERT INTO users (email, is_active) VALUES ('member@bcars.example', 1)`)
	require.NoError(t, err)
	_, err = d.Exec(`INSERT INTO users (email, is_active) VALUES ('officer@bcars.example', 1)`)
	require.NoError(t, err)

	return changerequests.NewService(d), d
}

func memberPrincipal(userID int64) *authz.Principal {
	return &authz.Principal{UserID: userID}
}

func memberParams(summary string) changerequests.CreateParams {
	return changerequests.CreateParams{
		Source:          changerequests.SourceMember,
		RequesterUserID: 1,
		SuppliedName:    "Marguerite Ashby",
		Summary:         summary,
		Items:           []changerequests.ItemInput{{Operation: changerequests.OpOther}},
	}
}

// TestPublicSourceIsGone holds the line the corrected plan drew, at both
// levels it has to hold at.
//
// The service refusing 'public' is the cheap half. The half that matters is
// the database refusing it too: an application-only guard leaves the schema
// disagreeing with the application about what is possible, and the schema is
// what a hand-run UPDATE, a future writer, or the next reader will believe.
func TestPublicSourceIsGone(t *testing.T) {
	svc, d := newService(t)
	ctx := context.Background()

	params := memberParams("Anonymous submission")
	params.Source = "public"
	params.RequesterUserID = 0

	_, err := svc.Create(ctx, nil, params, "public-1", time.Now())
	require.ErrorIs(t, err, changerequests.ErrSourceRequired,
		"'public' is not an intake channel")

	// Straight past the service, at the table itself.
	_, err = d.Exec(
		`INSERT INTO member_change_requests (source, status, summary, submitted_at)
		 VALUES ('public', 'submitted', 'Anonymous submission', '2026-08-12T00:00:00.000Z')`)
	require.Error(t, err,
		"migration 0013 removed 'public' from the source constraint; the database must refuse it")

	var rows int
	require.NoError(t, d.QueryRow(
		`SELECT count(*) FROM member_change_requests WHERE source = 'public'`).Scan(&rows))
	assert.Zero(t, rows)

	// The rebuild kept every other rule the original table carried.
	_, err = d.Exec(
		`INSERT INTO member_change_requests (source, status, summary, submitted_at)
		 VALUES ('member', 'submitted', 'No requester named', '2026-08-12T00:00:00.000Z')`)
	require.Error(t, err, "a member request must still name its requester")

	_, err = d.Exec(
		`INSERT INTO member_change_requests (source, status, summary, submitted_at)
		 VALUES ('officer_mail', 'submitted', '   ', '2026-08-12T00:00:00.000Z')`)
	require.Error(t, err, "a request must still carry a summary")
}

// TestMemberSourceRequiresARequester matches the schema CHECK: a member
// request names the member who made it.
func TestMemberSourceRequiresARequester(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()

	params := memberParams("Please fix the newsletter spelling")
	params.RequesterUserID = 0

	_, err := svc.Create(ctx, nil, params, "anon-1", time.Now())
	require.Error(t, err, "an unattributed member request must not be storable")
}

// TestGetForRequesterRefusesAnotherSubmitter proves the ownership test lives in
// the read rather than in whichever handler happens to call it.
func TestGetForRequesterRefusesAnotherSubmitter(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()

	created, err := svc.Create(ctx, memberPrincipal(1), memberParams("My address is wrong"), "own-1", time.Now())
	require.NoError(t, err)

	got, err := svc.GetForRequester(ctx, memberPrincipal(1), created.ID)
	require.NoError(t, err)
	assert.Equal(t, created.ID, got.ID)

	_, err = svc.GetForRequester(ctx, memberPrincipal(2), created.ID)
	assert.ErrorIs(t, err, changerequests.ErrNotYours)

	_, err = svc.GetForRequester(ctx, nil, created.ID)
	assert.ErrorIs(t, err, changerequests.ErrNotYours,
		"an anonymous caller owns nothing")
}

// TestWithdrawKeepsWhatWasAsked covers the retraction rule: a withdrawal is a
// status change, never a delete.
func TestWithdrawKeepsWhatWasAsked(t *testing.T) {
	svc, d := newService(t)
	ctx := context.Background()

	created, err := svc.Create(ctx, memberPrincipal(1), memberParams("My address is wrong"), "wd-1", time.Now())
	require.NoError(t, err)

	// Another member cannot withdraw it.
	_, err = svc.Withdraw(ctx, memberPrincipal(2), created.ID, time.Now())
	require.ErrorIs(t, err, changerequests.ErrNotYours)

	withdrawn, err := svc.Withdraw(ctx, memberPrincipal(1), created.ID, time.Now())
	require.NoError(t, err)
	assert.Equal(t, changerequests.StatusWithdrawn, withdrawn.Status)
	assert.NotEmpty(t, withdrawn.WithdrawnAt)
	require.Len(t, withdrawn.Items, 1, "the proposal stays on the record")
	assert.Equal(t, "Marguerite Ashby", withdrawn.SuppliedName,
		"and so does what the submitter said")

	var summary string
	require.NoError(t, d.QueryRow(
		`SELECT summary FROM member_change_requests WHERE id = ?`, created.ID).Scan(&summary))
	assert.True(t, strings.Contains(summary, "address"), "the row is still there to audit")

	// Twice is refused rather than silently repeated.
	_, err = svc.Withdraw(ctx, memberPrincipal(1), created.ID, time.Now())
	assert.ErrorIs(t, err, changerequests.ErrAlreadyResolved)
}

// TestWithdrawIsRefusedAfterADecision keeps a member from erasing the stated
// reason for something an officer already acted on.
func TestWithdrawIsRefusedAfterADecision(t *testing.T) {
	svc, d := newService(t)
	ctx := context.Background()

	created, err := svc.Create(ctx, memberPrincipal(1), memberParams("My address is wrong"), "wd-2", time.Now())
	require.NoError(t, err)
	require.Len(t, created.Items, 1)

	_, err = d.Exec(
		`UPDATE member_change_request_items
		    SET status = 'rejected', reviewed_by = 2, reviewed_at = datetime('now'),
		        decision_reason = 'Confirmed correct at the meeting'
		  WHERE id = ?`, created.Items[0].ID)
	require.NoError(t, err)

	_, err = svc.Withdraw(ctx, memberPrincipal(1), created.ID, time.Now())
	assert.ErrorIs(t, err, changerequests.ErrDecidedItems)
}

// TestCreateChangesNoCanonicalData is the premise the whole request model rests
// on, asserted rather than assumed.
func TestCreateChangesNoCanonicalData(t *testing.T) {
	svc, d := newService(t)
	ctx := context.Background()

	res, err := d.Exec(
		`INSERT INTO persons (display_name, sort_name, call_sign) VALUES ('Dale Rutherford', 'Rutherford, Dale', 'W3DLR')`)
	require.NoError(t, err)
	personID, err := res.LastInsertId()
	require.NoError(t, err)

	params := memberParams("My call sign is wrong")
	params.TargetPersonID = personID
	params.Items = []changerequests.ItemInput{{
		Operation:     "person.call_sign.set",
		ProposedValue: "W3NEW",
		TargetKind:    "person",
		TargetID:      personID,
	}}

	_, err = svc.Create(ctx, memberPrincipal(1), params, "canon-1", time.Now())
	require.NoError(t, err)

	var callSign string
	require.NoError(t, d.QueryRow(
		`SELECT call_sign FROM persons WHERE id = ?`, personID).Scan(&callSign))
	assert.Equal(t, "W3DLR", callSign,
		"a proposal is inert until an officer approves it")
}
