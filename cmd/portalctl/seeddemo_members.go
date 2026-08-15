//go:build demoseed

package main

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Synthetic members for development (bcars-portal-xdx).
//
// # WHY THIS EXISTS
//
// seed-demo used to create the three demo accounts and nothing else, so a
// freshly seeded database rendered every dashboard figure as 0 and every table
// as an empty state. That is enough to exercise sign-in and authorization, and
// not nearly enough to review a design: table density, badge colours, the
// overdue and honorary states, the printed worksheet's EXPIRED rows and the
// directory's "not shared" cells are all invisible against no data.
//
// # WHAT THE DATA IS
//
// Every person here is invented. Names, call signs, addresses and telephone
// numbers are made up; the locality is Bedford, PA 15522 with the 814 area code
// because that is where BCARS actually is, and using the wrong county in
// fixtures is how "Butler County" propagated through this repository once
// before. Real member records are supplied out of band, live under ignored
// paths, and must never appear here.
//
// Call signs are synthetic and deliberately take an X as the first suffix
// letter, a combination that reads as a call sign without matching the shape of
// the ones the club's members actually hold. Nothing should be inferred from
// them and none is claimed to be unassigned.
//
// Email addresses are built from names at run time rather than written as
// literals. That keeps this file free of anything the secrets gate has to be
// told to ignore, which matters more than it looks: an allowlist entry is a
// permanent hole, and this file would sit next to the one that already needs
// one.
const demoEmailDomain = "demo.local"

// demoMember is one synthetic person and the state their record should be in.
type demoMember struct {
	DisplayName string
	SortName    string
	CallSign    string
	BaseType    string // "full" or "associate"
	Lifecycle   string // "approved" or "pending"

	// UseClubYear anchors coverage to 31 December, which is what the club
	// actually does: the year ends on a fixed date, never on an anniversary of
	// joining. ClubYearOffset then selects which one, in YEARS relative to the
	// current club year -- 0 is this December, -1 the last one, +1 the next.
	//
	// The offset is in years rather than days because an earlier draft of this
	// file expressed it in days, and "-1 day" lands in the same calendar year,
	// so a member meant to read as expired rendered as comfortably current.
	UseClubYear    bool
	ClubYearOffset int

	// PaidThroughDays places coverage a number of days from today, for the
	// records that are NOT on a club-year boundary. Zero with UseClubYear
	// false means no coverage was ever recorded, which is its own standing.
	PaidThroughDays int
	CoverageNote    string

	// Honorary marks dues waived rather than paid. Such a member owes nothing
	// and must never appear on the renewal worksheet.
	Honorary bool

	// SharedEmail, when set, is used verbatim instead of a per-person address.
	// Two members sharing one household address is an ordinary case in a small
	// club and the directory has to stay readable when it happens.
	SharedEmail string

	// WithholdPhone records a visibility decision against the telephone number
	// so the directory has a "Not shared" cell to render. A directory where
	// every field is populated never exercises the case the design is most
	// particular about.
	WithholdPhone bool

	// LinkUserEmail attaches this person to a seeded login, so the member
	// self-service surface has a record to show rather than an empty landing.
	LinkUserEmail string

	Phone  string
	Street string
}

// demoMembers spans the states the officer and member UIs must render. Adding a
// member is cheap; the value is in the spread, so keep at least one of each
// state below if you edit this list.
var demoMembers = []demoMember{
	// Dues current, comfortably inside the club year.
	// The administrator's own member record. Officers are members: the club
	// elects them from its membership, and the portal takes that as the rule
	// rather than an accident (bcars-portal-j10). Before this, every officer
	// login was unlinked, so an administrator was refused the member directory
	// — a screen they are meant to hand out at meetings — because eligibility
	// counts grants to an approved full membership and they had none.
	{DisplayName: "Dana Whitfield", SortName: "Whitfield, Dana", CallSign: "W3XAB",
		BaseType: "full", Lifecycle: "approved",
		UseClubYear: true, ClubYearOffset: 1,
		LinkUserEmail: "admin@" + demoEmailDomain,
		Phone:         "814-555-0118", Street: "412 Juniata Street"},

	// The treasurer's own member record, for the same reason.
	{DisplayName: "Marcus Reed", SortName: "Reed, Marcus", CallSign: "K3XCD",
		BaseType: "full", Lifecycle: "approved",
		UseClubYear: true, ClubYearOffset: 0,
		LinkUserEmail: "treasurer@" + demoEmailDomain,
		Phone:         "814-555-0143", Street: "89 Richard Street"},

	// Expiring inside the warning window. This one is deliberately NOT anchored
	// to 31 December: with a fixed club year the expiring bucket is empty for
	// ten months of the year, so a Dec-31 date would leave that dashboard
	// figure and its amber styling unreviewable except in November. A legacy
	// import is exactly how a non-standard coverage date arrives in practice,
	// so that is what this is recorded as.
	{DisplayName: "Priya Raman", SortName: "Raman, Priya", CallSign: "N3XEF",
		BaseType: "full", Lifecycle: "approved",
		PaidThroughDays: 45,
		CoverageNote:    "Legacy date carried in from the Groups.io export; not a club-year renewal.",
		Phone:           "814-555-0167", Street: "27 Pitt Street"},

	// Expired last club year, and expired long ago. Two of them so the
	// worksheet's "longest overdue" ordering has something to order.
	{DisplayName: "Glenn Hostetler", SortName: "Hostetler, Glenn", CallSign: "W3XGH",
		BaseType: "full", Lifecycle: "approved",
		UseClubYear: true, ClubYearOffset: -1,
		Phone: "814-555-0192", Street: "1140 Bedford Valley Road"},

	{DisplayName: "Sam Okafor", SortName: "Okafor, Sam", CallSign: "K3XJK",
		BaseType: "full", Lifecycle: "approved",
		UseClubYear: true, ClubYearOffset: -3,
		Phone: "814-555-0175", Street: "63 East Penn Street"},

	// Never paid anything: the "no dues recorded" standing, which is not the
	// same as expired and is styled differently.
	{DisplayName: "Bernice Coughenour", SortName: "Coughenour, Bernice", CallSign: "",
		BaseType: "associate", Lifecycle: "approved",
		PaidThroughDays: 0,
		Phone:           "814-555-0104", Street: "8 Thomas Street"},

	// Associate: holds no licence, so no call sign, and the directory must not
	// list them even though they are a member in good standing.
	{DisplayName: "Ruth Delaney", SortName: "Delaney, Ruth", CallSign: "",
		BaseType: "associate", Lifecycle: "approved",
		UseClubYear: true, ClubYearOffset: 1,
		Phone: "814-555-0129", Street: "305 South Juliana Street"},

	// Waiting for approval. Nothing is charged and no dues exist yet.
	{DisplayName: "Ellis Nagy", SortName: "Nagy, Ellis", CallSign: "N3XLM",
		BaseType: "full", Lifecycle: "pending",
		Phone: "814-555-0151", Street: "77 West John Street"},

	{DisplayName: "Tomas Vidal", SortName: "Vidal, Tomas", CallSign: "W3XNP",
		BaseType: "full", Lifecycle: "pending",
		Phone: "814-555-0186", Street: "22 Shed Road"},

	// Dues waived (honorary). Owes nothing and must be absent from the
	// worksheet however long ago the waiver started.
	{DisplayName: "Harold Bierly", SortName: "Bierly, Harold", CallSign: "K3XQR",
		BaseType: "full", Lifecycle: "approved",
		Honorary: true,
		Phone:    "814-555-0137", Street: "914 Sweet Root Road"},

	// A household sharing one email address. The owner decided on 2026-08-13
	// that the directory lists these as separate rows rather than collapsing
	// them, so the fixture has to make that visible.
	{DisplayName: "Carol Zeller", SortName: "Zeller, Carol", CallSign: "W3XSU",
		BaseType: "full", Lifecycle: "approved",
		UseClubYear: true, ClubYearOffset: 1,
		SharedEmail: "zeller.household@" + demoEmailDomain,
		Phone:       "814-555-0160", Street: "530 Cumberland Street"},

	// The second half of the household withholds their telephone number, which
	// is what makes the directory render a "Not shared" cell next to a row that
	// is otherwise complete.
	{DisplayName: "Frank Zeller", SortName: "Zeller, Frank", CallSign: "K3XTV",
		BaseType: "full", Lifecycle: "approved",
		UseClubYear: true, ClubYearOffset: 1,
		SharedEmail:   "zeller.household@" + demoEmailDomain,
		WithholdPhone: true,
		Phone:         "814-555-0160", Street: "530 Cumberland Street"},

	// Attached to the seeded member login, so /member/ shows a real record
	// instead of a landing with nothing on it.
	{DisplayName: "Joe Kettering", SortName: "Kettering, Joe", CallSign: "N3XWX",
		BaseType: "full", Lifecycle: "approved",
		UseClubYear: true, ClubYearOffset: 0,
		LinkUserEmail: "joe@" + demoEmailDomain,
		Phone:         "814-555-0113", Street: "18 North Richard Street"},
}

// demoEmailFor builds a synthetic address from a person's name.
func demoEmailFor(m demoMember) string {
	if m.SharedEmail != "" {
		return m.SharedEmail
	}
	parts := strings.Fields(strings.ToLower(m.DisplayName))
	return strings.Join(parts, ".") + "@" + demoEmailDomain
}

// coverageDate resolves a member's paid-through date.
func coverageDate(m demoMember, now time.Time) string {
	if m.UseClubYear {
		return time.Date(now.Year()+m.ClubYearOffset,
			time.December, 31, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	}
	return now.AddDate(0, 0, m.PaidThroughDays).Format("2006-01-02")
}

// hasCoverage reports whether a member should have any coverage recorded at
// all. Someone with none is "no dues recorded", which is a different standing
// from expired and is styled differently.
func hasCoverage(m demoMember) bool {
	return m.UseClubYear || m.PaidThroughDays != 0
}

// seedDemoMembers creates the synthetic membership records. It is idempotent:
// a person is matched by display name, so re-running against an already-seeded
// database updates rather than duplicating.
func seedDemoMembers(d *sql.DB, actorUserID int64) error {
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)

	for _, m := range demoMembers {
		personID, err := upsertDemoPerson(d, m)
		if err != nil {
			return err
		}
		membershipID, err := upsertDemoMembership(d, m, personID, stamp)
		if err != nil {
			return err
		}
		if err := upsertDemoContacts(d, m, personID, actorUserID, stamp); err != nil {
			return err
		}
		if m.Honorary {
			if err := upsertDemoHonorary(d, membershipID, actorUserID, now, stamp); err != nil {
				return err
			}
		} else if hasCoverage(m) {
			if err := upsertDemoCoverage(d, m, membershipID, actorUserID, now, stamp); err != nil {
				return err
			}
		}
		if m.LinkUserEmail != "" {
			if err := linkDemoUser(d, m, personID, actorUserID, stamp); err != nil {
				return err
			}
		}
	}

	fmt.Printf("\n  %d synthetic members seeded (Bedford County, PA — all invented).\n", len(demoMembers))
	for _, m := range demoMembers {
		if m.LinkUserEmail != "" {
			fmt.Printf("  %-28s  is the member record for %s\n", m.LinkUserEmail, m.DisplayName)
		}
	}
	return nil
}

// linkDemoUser attaches a seeded login to its person and grants the explicit
// self access the member surfaces require.
//
// Setting users.person_id alone is not enough and looks like it should be.
// Record visibility comes only from member_access_grants (ADR-0010), and the
// directory additionally refuses anyone without an active grant to an approved
// full membership. Without the grant the member landing renders empty and the
// directory returns "not eligible", so the two screens this fixture exists to
// make reviewable would both still be blank.
func linkDemoUser(d *sql.DB, m demoMember, personID, actorUserID int64, stamp string) error {
	if _, err := d.Exec(
		`UPDATE users SET person_id = ? WHERE email = ?`, personID, m.LinkUserEmail); err != nil {
		return fmt.Errorf("link %s to %s: %w", m.LinkUserEmail, m.DisplayName, err)
	}

	var userID int64
	if err := d.QueryRow(`SELECT id FROM users WHERE email = ?`, m.LinkUserEmail).Scan(&userID); err != nil {
		return fmt.Errorf("lookup %s: %w", m.LinkUserEmail, err)
	}

	var grants int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM member_access_grants
		  WHERE user_id = ? AND person_id = ? AND revoked_at IS NULL`,
		userID, personID).Scan(&grants); err != nil {
		return err
	}
	if grants > 0 {
		return nil
	}

	if _, err := d.Exec(
		`INSERT INTO member_access_grants
		   (user_id, person_id, access_kind, reason, granted_by, granted_at)
		 VALUES (?, ?, 'self', 'seed-demo: development access to their own record', ?, ?)`,
		userID, personID, actorUserID, stamp); err != nil {
		return fmt.Errorf("grant self access to %s: %w", m.LinkUserEmail, err)
	}
	return nil
}

func upsertDemoPerson(d *sql.DB, m demoMember) (int64, error) {
	var callSign any
	if m.CallSign != "" {
		callSign = m.CallSign
	}

	var personID int64
	err := d.QueryRow(`SELECT id FROM persons WHERE display_name = ?`, m.DisplayName).Scan(&personID)
	switch {
	case err == sql.ErrNoRows:
		res, err := d.Exec(
			`INSERT INTO persons (display_name, sort_name, call_sign) VALUES (?, ?, ?)`,
			m.DisplayName, m.SortName, callSign)
		if err != nil {
			return 0, fmt.Errorf("create person %s: %w", m.DisplayName, err)
		}
		return res.LastInsertId()
	case err != nil:
		return 0, fmt.Errorf("lookup person %s: %w", m.DisplayName, err)
	}

	if _, err := d.Exec(
		`UPDATE persons SET sort_name = ?, call_sign = ?, version = version + 1 WHERE id = ?`,
		m.SortName, callSign, personID); err != nil {
		return 0, fmt.Errorf("update person %s: %w", m.DisplayName, err)
	}
	return personID, nil
}

func upsertDemoMembership(d *sql.DB, m demoMember, personID int64, stamp string) (int64, error) {
	var membershipID int64
	err := d.QueryRow(`SELECT id FROM memberships WHERE person_id = ?`, personID).Scan(&membershipID)
	switch {
	case err == sql.ErrNoRows:
		res, err := d.Exec(
			`INSERT INTO memberships (person_id, base_type, lifecycle, joined_on) VALUES (?, ?, ?, ?)`,
			personID, m.BaseType, m.Lifecycle, stamp[:10])
		if err != nil {
			return 0, fmt.Errorf("create membership for %s: %w", m.DisplayName, err)
		}
		return res.LastInsertId()
	case err != nil:
		return 0, fmt.Errorf("lookup membership for %s: %w", m.DisplayName, err)
	}

	if _, err := d.Exec(
		`UPDATE memberships SET base_type = ?, lifecycle = ?, version = version + 1 WHERE id = ?`,
		m.BaseType, m.Lifecycle, membershipID); err != nil {
		return 0, fmt.Errorf("update membership for %s: %w", m.DisplayName, err)
	}
	return membershipID, nil
}

// upsertDemoContacts writes the email, telephone and postal address, and
// records a visibility decision for anyone withholding a number.
func upsertDemoContacts(d *sql.DB, m demoMember, personID, actorUserID int64, stamp string) error {
	contacts := []struct {
		kind, label, value string
	}{
		{"email", "Home", demoEmailFor(m)},
		{"phone", "Mobile", m.Phone},
	}

	for _, c := range contacts {
		var id int64
		err := d.QueryRow(
			`SELECT id FROM contact_methods WHERE person_id = ? AND kind = ?`, personID, c.kind).Scan(&id)
		switch {
		case err == sql.ErrNoRows:
			res, execErr := d.Exec(
				`INSERT INTO contact_methods (person_id, kind, label, value_raw, value_norm, is_primary)
				 VALUES (?, ?, ?, ?, ?, 1)`,
				personID, c.kind, c.label, c.value, strings.ToLower(c.value))
			if execErr != nil {
				return fmt.Errorf("create %s for %s: %w", c.kind, m.DisplayName, execErr)
			}
			if id, err = res.LastInsertId(); err != nil {
				return err
			}
		case err != nil:
			return fmt.Errorf("lookup %s for %s: %w", c.kind, m.DisplayName, err)
		}

		// Withholding is expressed as a visibility event rather than by leaving
		// the field empty. The distinction is the whole point of the directory's
		// "Not shared" cell: the club holds the number, and the member chose not
		// to publish it.
		if c.kind == "phone" && m.WithholdPhone {
			var events int
			if err := d.QueryRow(
				`SELECT COUNT(*) FROM contact_method_visibility_events WHERE contact_method_id = ?`,
				id).Scan(&events); err != nil {
				return err
			}
			if events == 0 {
				if _, err := d.Exec(
					`INSERT INTO contact_method_visibility_events
					   (contact_method_id, audience, source, effective_at, actor_user_id, note)
					 VALUES (?, 'officers', 'member', ?, ?, 'seed-demo: member keeps their number off the directory')`,
					id, stamp, actorUserID); err != nil {
					return fmt.Errorf("withhold phone for %s: %w", m.DisplayName, err)
				}
			}
		}
	}

	var postal int64
	err := d.QueryRow(
		`SELECT id FROM contact_methods WHERE person_id = ? AND kind = 'postal'`, personID).Scan(&postal)
	if err == sql.ErrNoRows {
		if _, err := d.Exec(
			`INSERT INTO contact_methods
			   (person_id, kind, label, value_raw, value_norm, is_primary,
			    postal_line1, postal_city, postal_state, postal_postal_code, postal_country)
			 VALUES (?, 'postal', 'Home', ?, ?, 1, ?, 'Bedford', 'PA', '15522', 'USA')`,
			personID,
			m.Street+", Bedford, PA 15522",
			strings.ToLower(m.Street+", bedford, pa 15522"),
			m.Street); err != nil {
			return fmt.Errorf("create postal for %s: %w", m.DisplayName, err)
		}
	} else if err != nil {
		return fmt.Errorf("lookup postal for %s: %w", m.DisplayName, err)
	}
	return nil
}

func upsertDemoCoverage(d *sql.DB, m demoMember, membershipID, actorUserID int64, now time.Time, stamp string) error {
	paidThrough := coverageDate(m, now)

	var existing int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM coverage_events WHERE membership_id = ?`, membershipID).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}

	reason := "legacy_import"
	note := m.CoverageNote
	if note == "" {
		note = "seed-demo: synthetic coverage for development"
	}

	if _, err := d.Exec(
		`INSERT INTO coverage_events
		   (membership_id, paid_through, reason_kind, reason, decided_by, decided_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		membershipID, paidThrough, reason, note, actorUserID, stamp); err != nil {
		return fmt.Errorf("create coverage for %s: %w", m.DisplayName, err)
	}
	return nil
}

func upsertDemoHonorary(d *sql.DB, membershipID, actorUserID int64, now time.Time, stamp string) error {
	var existing int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM honorary_grants WHERE membership_id = ? AND revoked_at IS NULL`,
		membershipID).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}

	if _, err := d.Exec(
		`INSERT INTO honorary_grants
		   (membership_id, starts_on, ends_on, is_lifetime, reason, approved_by, approved_at)
		 VALUES (?, ?, NULL, 1, 'seed-demo: long service to the club', ?, ?)`,
		membershipID, now.AddDate(-3, 0, 0).Format("2006-01-02"), actorUserID, stamp); err != nil {
		return fmt.Errorf("create honorary grant: %w", err)
	}
	return nil
}
