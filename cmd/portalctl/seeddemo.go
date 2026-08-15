//go:build demoseed

// seed-demo creates accounts whose passwords are published in this source
// file. It exists only in builds made with `-tags demoseed`; a default build
// does not compile this file, so the shipped binary contains neither the
// command nor the credentials.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bcars/bcars-portal/internal/authn"
	"github.com/bcars/bcars-portal/internal/db"
)

func init() {
	demoCommands["seed-demo"] = runSeedDemo
	demoCommandUsage = append(demoCommandUsage,
		"  seed-demo --db <path>                        DEVELOPMENT BUILD ONLY. Create demo",
		"                                               users with published passwords. Refuses",
		"                                               to run on a database holding any other",
		"                                               user account.",
	)
	demoEnvUsage = append(demoEnvUsage,
		"  PORTAL_PASSWORD_PEPPER     required by seed-demo; demo passwords must be",
		"                             hashed with the same pepper the server verifies with.",
	)
}

func runSeedDemo(args []string) error {
	fs := flag.NewFlagSet("seed-demo", flag.ExitOnError)
	dbPath := fs.String("db", "", "Path to SQLite database (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *dbPath == "" {
		fs.Usage()
		return fmt.Errorf("--db is required")
	}

	d, err := db.Open(*dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer d.Close()

	if err := db.Migrate(d); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	return seedDemo(d)
}

type demoUser struct {
	Email    string
	Password string
	// Roles are additive, which is how an officer who is also a member is
	// expressed: they hold their officer role AND the member role.
	//
	// This is not belt and braces. The officer roles deliberately exclude the
	// member capabilities — a treasurer holds no profile.self.read and no
	// directory.read (see 0009_member_requests_access.sql) — so a treasurer
	// linked to a person record is still refused their own records and the
	// directory until they also hold the member role. The administrator only
	// appeared to work because that role is granted the entire catalog.
	Roles []string
}

// Role returns the role this account exists to demonstrate, for output.
func (u demoUser) Role() string { return u.Roles[0] }

// demoUsers are the only accounts seed-demo may touch. Every address is under
// the reserved .local suffix so it can never collide with a real member.
//
// Officers are members (PLANNING.md, "Officers and Treasurer"), so the officer
// accounts hold the member role too and are linked to their own person records
// in seeddemo_members.go.
var demoUsers = []demoUser{
	{Email: "admin@demo.local", Password: "admin", Roles: []string{"administrator", "member"}},
	{Email: "treasurer@demo.local", Password: "treasurer", Roles: []string{"treasurer", "member"}},
	{Email: "joe@demo.local", Password: "joe", Roles: []string{"member"}},
}

// assertDemoDatabase refuses to seed a database that holds any account other
// than the demo users themselves. The seeding upsert overwrites an existing
// account's password, so pointing this at a live database would silently
// replace a real officer's password with a word printed in this file. Being
// pointed at the wrong --db is the realistic mistake, and the operator has no
// way to tell afterwards, so the check is unconditional: there is no --force.
func assertDemoDatabase(d *sql.DB) error {
	allowed := make([]any, 0, len(demoUsers))
	for _, u := range demoUsers {
		allowed = append(allowed, u.Email)
	}
	query := fmt.Sprintf(
		`SELECT COUNT(*) FROM users WHERE email NOT IN (%s)`,
		strings.TrimSuffix(strings.Repeat("?,", len(allowed)), ","),
	)

	var others int
	if err := d.QueryRow(query, allowed...).Scan(&others); err != nil {
		return fmt.Errorf("inspect existing users: %w", err)
	}
	if others > 0 {
		return fmt.Errorf(
			"refusing to seed: database holds %d non-demo user account(s); "+
				"seed-demo is only for throwaway development databases and would "+
				"overwrite real passwords", others)
	}
	return nil
}

func seedDemo(d *sql.DB) error {
	if err := assertDemoDatabase(d); err != nil {
		return err
	}

	// The server hashes with the configured pepper; seeding without it would
	// produce accounts whose passwords can never verify.
	pepper := []byte(os.Getenv(authn.PepperEnvVar))
	if err := authn.BindPepper(d, pepper); err != nil {
		return fmt.Errorf("seed-demo: %w", err)
	}

	for _, u := range demoUsers {
		hash, err := authn.HashPassword(u.Password, pepper, authn.DefaultParams())
		if err != nil {
			return fmt.Errorf("hash password for %s: %w", u.Email, err)
		}

		// Upsert user.
		_, err = d.Exec(
			`INSERT INTO users (email, password_hash, password_algo_params, is_active)
			 VALUES (?, ?, 'argon2id', 1)
			 ON CONFLICT(email) DO UPDATE SET password_hash = excluded.password_hash, updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z','now')`,
			u.Email, hash,
		)
		if err != nil {
			return fmt.Errorf("upsert user %s: %w", u.Email, err)
		}

		// Always read the id back rather than trusting LastInsertId: on the
		// ON CONFLICT-update path SQLite leaves the previous statement's
		// rowid in place, which silently grants the role to the wrong user.
		var userID int64
		if err := d.QueryRow(`SELECT id FROM users WHERE email = ?`, u.Email).Scan(&userID); err != nil {
			return fmt.Errorf("lookup user %s: %w", u.Email, err)
		}

		// Grant roles (skip any already granted).
		now := time.Now().UTC().Format(time.RFC3339Nano)
		for _, role := range u.Roles {
			_, err = d.Exec(
				`INSERT INTO user_role_grants (user_id, role_code, granted_by, granted_at, reason)
				 SELECT ?, ?, ?, ?, 'seed-demo'
				 WHERE NOT EXISTS (
				   SELECT 1 FROM user_role_grants WHERE user_id = ? AND role_code = ? AND revoked_at IS NULL
				 )`,
				userID, role, userID, now, userID, role,
			)
			if err != nil {
				return fmt.Errorf("grant role %s to %s: %w", role, u.Email, err)
			}
		}

		fmt.Printf("  %-28s  roles=%-22s  password=%s\n",
			u.Email, strings.Join(u.Roles, ","), u.Password)
	}

	// Members are seeded against the administrator account, which is the
	// closest thing this tool has to an actor: coverage events and honorary
	// grants both record who decided them, and a NULL there would be a row no
	// officer could have created.
	var adminID int64
	if err := d.QueryRow(`SELECT id FROM users WHERE email = ?`, demoUsers[0].Email).Scan(&adminID); err != nil {
		return fmt.Errorf("lookup seeding actor: %w", err)
	}
	if err := seedDemoMembers(d, adminID); err != nil {
		return err
	}

	fmt.Println("\nDemo users and members seeded. Sign in at /login")
	return nil
}
