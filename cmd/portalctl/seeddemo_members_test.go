//go:build demoseed

package main

import (
	"database/sql"
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

// TestSeededOfficersAreMembers pins the club's rule, decided 2026-08-15:
// officers are elected from the membership, so an officer login belongs to a
// person like any other member's does (bcars-portal-j10).
//
// It matters here rather than only in the web tests because the fixture is
// what was wrong. Every officer login was unlinked, so reviewing the portal as
// an administrator meant being refused the member directory — a screen an
// officer is meant to hand out at meetings — with no indication why.
//
// The assertion runs per account rather than over a total: a count is satisfied
// by the one member login that was always linked, which is exactly the state
// this test exists to reject.
func TestSeededOfficersAreMembers(t *testing.T) {
	d := newMigratedDB(t)
	t.Setenv(authn.PepperEnvVar, testPepper)
	_ = captureStdout(t, func() { require.NoError(t, seedDemo(d)) })

	for _, u := range demoUsers {
		t.Run(u.Email, func(t *testing.T) {
			var personID sql.NullInt64
			require.NoError(t, d.QueryRow(
				`SELECT person_id FROM users WHERE email = ?`, u.Email).Scan(&personID))
			require.Truef(t, personID.Valid,
				"%s holds the %s role but is not linked to a member record", u.Email, u.Role())

			// The link alone is not enough: directory eligibility counts an
			// active grant against an approved full membership, so an officer
			// linked without one is still refused.
			var eligible int
			require.NoError(t, d.QueryRow(
				`SELECT COUNT(*) FROM member_access_grants g
				   JOIN memberships m ON m.person_id = g.person_id
				  WHERE g.user_id = (SELECT id FROM users WHERE email = ?)
				    AND g.revoked_at IS NULL
				    AND m.lifecycle = 'approved' AND m.base_type = 'full'
				    AND m.ended_on IS NULL`, u.Email).Scan(&eligible))
			assert.NotZerof(t, eligible,
				"%s is linked to a person but holds no grant to an approved full membership, "+
					"so the member directory still refuses them", u.Email)

			// Linkage is not sufficient either. The officer roles deliberately
			// exclude the member capabilities, so a treasurer linked to a person
			// record was still refused their own records and the directory
			// until they also held the member role. The administrator hid this
			// because that role is granted the whole catalog.
			for _, capability := range []string{"profile.self.read", "directory.read"} {
				var held int
				require.NoError(t, d.QueryRow(
					`SELECT COUNT(*) FROM user_role_grants g
					   JOIN role_capabilities rc ON rc.role_code = g.role_code
					  WHERE g.user_id = (SELECT id FROM users WHERE email = ?)
					    AND g.revoked_at IS NULL
					    AND rc.capability_code = ?`, u.Email, capability).Scan(&held))
				assert.NotZerof(t, held,
					"%s holds no role granting %s, so the member surfaces refuse them "+
						"however their record is linked", u.Email, capability)
			}
		})
	}
}
