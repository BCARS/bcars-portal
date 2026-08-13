//go:build demoseed

package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/authn"
)

// TestDemoMembersLandInTheIntendedStanding is the test this fixture most needs.
//
// The point of seeding members is that each one demonstrates a state the UI has
// to render, and a fixture whose rows quietly drift into the wrong bucket is
// worse than no fixture: it makes a screen look reviewed when the state it was
// meant to show was never on the page. That is not hypothetical. The first
// draft expressed the club-year offset in days, so "-1" moved back one day,
// landed in the same calendar year, and the member meant to read as EXPIRED
// rendered as comfortably current. Nothing failed; the number on the dashboard
// was simply wrong.
//
// So this asserts the resolved coverage date against what each row claims to
// be, rather than asserting that some rows exist.
func TestDemoMembersLandInTheIntendedStanding(t *testing.T) {
	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	warning := now.AddDate(0, 0, 60).Format("2006-01-02")

	byName := map[string]demoMember{}
	for _, m := range demoMembers {
		byName[m.DisplayName] = m
	}

	cases := []struct {
		name     string
		standing string // current | expiring | expired | none | waived
	}{
		{"Dana Whitfield", "current"},
		{"Marcus Reed", "current"},
		{"Priya Raman", "expiring"},
		{"Glenn Hostetler", "expired"},
		{"Sam Okafor", "expired"},
		{"Bernice Coughenour", "none"},
		{"Ruth Delaney", "current"},
		{"Harold Bierly", "waived"},
		{"Carol Zeller", "current"},
		{"Frank Zeller", "current"},
		{"Joe Kettering", "current"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := byName[tc.name]
			require.True(t, ok, "fixture %s has gone missing", tc.name)

			switch tc.standing {
			case "waived":
				assert.True(t, m.Honorary, "%s must have dues waived", tc.name)
				return
			case "none":
				assert.False(t, hasCoverage(m), "%s must have no coverage recorded", tc.name)
				return
			}

			require.True(t, hasCoverage(m), "%s needs coverage to have a standing", tc.name)
			paid := coverageDate(m, now)

			switch tc.standing {
			case "current":
				assert.Greater(t, paid, warning,
					"%s should be comfortably current, but paid_through %s is inside the 60-day warning window",
					tc.name, paid)
			case "expiring":
				assert.Greater(t, paid, today, "%s must not already be expired", tc.name)
				assert.LessOrEqual(t, paid, warning,
					"%s should be expiring, but paid_through %s is outside the 60-day window",
					tc.name, paid)
			case "expired":
				assert.Less(t, paid, today,
					"%s should be expired, but paid_through %s has not passed", tc.name, paid)
			}
		})
	}
}

// TestDemoMembersCoverTheStatesTheUIMustRender keeps the spread from eroding.
// Deleting the only honorary member or the only pending one costs nothing at
// the time and silently removes a state from every subsequent design review.
func TestDemoMembersCoverTheStatesTheUIMustRender(t *testing.T) {
	var pending, honorary, associate, noCallSign, withheld, shared, linked int
	emails := map[string]int{}

	for _, m := range demoMembers {
		if m.Lifecycle == "pending" {
			pending++
		}
		if m.Honorary {
			honorary++
		}
		if m.BaseType == "associate" {
			associate++
		}
		if m.CallSign == "" {
			noCallSign++
		}
		if m.WithholdPhone {
			withheld++
		}
		if m.SharedEmail != "" {
			shared++
		}
		if m.LinkUserEmail != "" {
			linked++
		}
		emails[demoEmailFor(m)]++
	}

	assert.GreaterOrEqual(t, pending, 1, "the approval queue needs someone waiting in it")
	assert.GreaterOrEqual(t, honorary, 1, "dues waived is a state the treasury UI must render")
	assert.GreaterOrEqual(t, associate, 1, "an Associate is refused the directory and must be testable")
	assert.GreaterOrEqual(t, noCallSign, 1, "a member without a call sign must not break a call-sign column")
	assert.Equal(t, 1, withheld, "the directory needs exactly one 'Not shared' cell to render")
	assert.GreaterOrEqual(t, linked, 1, "the member surface needs a record attached to a login")

	// The household: two people, one address, listed separately by the owner's
	// decision of 2026-08-13.
	var sharedCount int
	for _, n := range emails {
		if n > 1 {
			sharedCount++
		}
	}
	assert.Equal(t, 1, sharedCount, "exactly one household should share an email address")
	assert.Equal(t, 2, shared, "a household of two is what makes separate rows visible")
}

// TestDemoMembersCarryNoRealMemberData is a standing check on the fixture, not
// on the code. Synthetic data drifting toward the real export is the failure
// this repository has already had once with the wrong county.
func TestDemoMembersCarryNoRealMemberData(t *testing.T) {
	for _, m := range demoMembers {
		assert.Contains(t, demoEmailFor(m), "@"+demoEmailDomain,
			"%s must have a .local address that cannot collide with a real member", m.DisplayName)
		if m.Phone != "" {
			// 555-01xx is the reserved fictional range.
			assert.Contains(t, m.Phone, "555-01",
				"%s must use a reserved fictional telephone number", m.DisplayName)
			assert.Contains(t, m.Phone, "814-",
				"%s should carry the Bedford County area code", m.DisplayName)
		}
	}
}

// TestSeedDemoMembersIsIdempotent covers the realistic operator action of
// running the seeder twice against the same development database.
func TestSeedDemoMembersIsIdempotent(t *testing.T) {
	d := newMigratedDB(t)
	t.Setenv(authn.PepperEnvVar, testPepper)

	_ = captureStdout(t, func() { require.NoError(t, seedDemo(d)) })

	count := func(table string) int {
		var n int
		require.NoError(t, d.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&n))
		return n
	}
	persons, memberships, coverage := count("persons"), count("memberships"), count("coverage_events")
	require.Equal(t, len(demoMembers), persons)

	_ = captureStdout(t, func() { require.NoError(t, seedDemo(d)) })

	assert.Equal(t, persons, count("persons"), "re-seeding must not duplicate people")
	assert.Equal(t, memberships, count("memberships"), "re-seeding must not duplicate memberships")
	assert.Equal(t, coverage, count("coverage_events"), "re-seeding must not stack coverage events")
}

// TestSeededMemberCanReachTheirOwnRecord covers the half of the linkage that
// is easy to get wrong. users.person_id looks like it should be sufficient and
// is not: visibility comes only from member_access_grants (ADR-0010), and the
// directory additionally requires an active grant against an approved full
// membership. Without the grant, the member landing and the directory both
// render empty, which is precisely the state this fixture exists to end.
func TestSeededMemberCanReachTheirOwnRecord(t *testing.T) {
	d := newMigratedDB(t)
	t.Setenv(authn.PepperEnvVar, testPepper)
	_ = captureStdout(t, func() { require.NoError(t, seedDemo(d)) })

	var linked int
	require.NoError(t, d.QueryRow(
		`SELECT COUNT(*) FROM users WHERE person_id IS NOT NULL`).Scan(&linked))
	assert.NotZero(t, linked, "a demo login must be attached to a person")

	var eligible int
	require.NoError(t, d.QueryRow(
		`SELECT COUNT(*) FROM member_access_grants g
		   JOIN memberships m ON m.person_id = g.person_id
		  WHERE g.revoked_at IS NULL
		    AND m.lifecycle = 'approved' AND m.base_type = 'full'`).Scan(&eligible))
	assert.NotZero(t, eligible,
		"a seeded member needs an active grant to an approved full membership, "+
			"or the member landing and the directory both render empty")
}

// TestSeedDemoMembersMakesTheDashboardNonZero asserts the reason this bead
// exists, through the same queries the dashboard runs. Counting rows in the
// fixture slice would pass whether or not the seeder wrote anything.
func TestSeedDemoMembersMakesTheDashboardNonZero(t *testing.T) {
	d := newMigratedDB(t)
	t.Setenv(authn.PepperEnvVar, testPepper)
	_ = captureStdout(t, func() { require.NoError(t, seedDemo(d)) })

	for _, q := range []struct {
		label string
		sql   string
	}{
		{"Total members", `SELECT count(*) FROM persons WHERE deactivated_at IS NULL`},
		{"Active memberships", `SELECT count(*) FROM memberships WHERE lifecycle = 'approved'`},
		{"Waiting for approval", `SELECT count(*) FROM memberships WHERE lifecycle = 'pending'`},
	} {
		var n int
		require.NoError(t, d.QueryRow(q.sql).Scan(&n))
		assert.NotZero(t, n, "%s must not read 0 on a seeded database", q.label)
	}

	// Someone must actually owe, or the treasury surfaces stay empty.
	var expired int
	require.NoError(t, d.QueryRow(
		`SELECT count(*) FROM coverage_events ce
		   JOIN memberships m ON m.id = ce.membership_id
		  WHERE m.lifecycle = 'approved' AND ce.paid_through < date('now')`).Scan(&expired))
	assert.GreaterOrEqual(t, expired, 2,
		"the worksheet needs more than one overdue member for its ordering to mean anything")
}
