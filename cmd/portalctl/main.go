// Package main is the BCARS portal admin CLI.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/bcars/bcars-portal/internal/audit"
	"github.com/bcars/bcars-portal/internal/authn"
	"github.com/bcars/bcars-portal/internal/db"
	"github.com/bcars/bcars-portal/internal/mail"
	"github.com/bcars/bcars-portal/internal/obs"
	"github.com/bcars/bcars-portal/internal/version"
	"github.com/bcars/bcars-portal/internal/web"
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "-h", "--help", "help":
		usage(os.Stdout)
	case "--version":
		fmt.Println(version.Get().Short())
	case "version":
		fmt.Print(version.Get().Long())
	case "bootstrap-admin":
		if err := runBootstrapAdmin(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "portalctl bootstrap-admin: %v\n", err)
			os.Exit(1)
		}
	case "seed-demo":
		if err := runSeedDemo(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "portalctl seed-demo: %v\n", err)
			os.Exit(1)
		}
	case "backup":
		if err := runBackup(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "portalctl backup: %v\n", err)
			os.Exit(1)
		}
	case "restore":
		if err := runRestore(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "portalctl restore: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "portalctl: unknown command %q\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
}

func runBootstrapAdmin(args []string) error {
	fs := flag.NewFlagSet("bootstrap-admin", flag.ExitOnError)
	emailFlag := fs.String("email", "", "Email address for the bootstrap administrator (required)")
	dbPath := fs.String("db", "", "Path to SQLite database (required)")
	force := fs.Bool("force", false, "Allow bootstrap even if an active administrator exists")
	baseURL := fs.String("base-url", "http://localhost:8080", "Base URL for the invitation link")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *emailFlag == "" || *dbPath == "" {
		fs.Usage()
		return fmt.Errorf("--email and --db are required")
	}

	d, err := db.Open(*dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer d.Close()

	if err := db.Migrate(d); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	return bootstrapAdmin(d, *emailFlag, *force, *baseURL)
}

// bootstrapRoleCode is the role a bootstrap invitation confers. Consuming the
// invitation grants it, which is what makes the resulting account a usable
// administrator rather than an account with no capabilities.
const bootstrapRoleCode = "administrator"

func bootstrapAdmin(d *sql.DB, email string, force bool, baseURL string) error {
	// Check for existing active administrator.
	var count int
	err := d.QueryRow(`
		SELECT COUNT(*) FROM user_role_grants urg
		JOIN users u ON u.id = urg.user_id
		WHERE urg.role_code = 'administrator' AND urg.revoked_at IS NULL AND u.is_active = 1
	`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check existing admin: %w", err)
	}
	if count > 0 && !force {
		return fmt.Errorf("an active administrator already exists; use --force to override (this will be audited)")
	}

	ctx := context.Background()
	rec := audit.NewSQLRecorder(d, nil)

	// The refusal message above promises the override is audited, so record
	// it. Creating a second administrator out of band is exactly the event
	// someone reviewing the log later needs to find.
	if count > 0 && force {
		rec.Record(ctx, audit.Event{
			Action:       "auth.bootstrap_admin.force",
			Outcome:      audit.OutcomeSuccess,
			ResourceKind: "email_link",
			DetailJSON: fmt.Sprintf(`{"invitee":%q,"role":%q,"existing_admins":%d,"surface":"portalctl"}`,
				obs.SafeEmail(email), bootstrapRoleCode, count),
		})
	}

	// Create invitation link (no email sent — print to stdout).
	mailer := mail.NewFilelogSender(os.TempDir())
	links := authn.NewEmailLinkService(d, mailer, authn.EmailLinkConfig{
		BaseURL: baseURL,
		TTL:     24 * time.Hour,
		// Same route constants the server registers, so the URL printed here
		// is the URL that actually resolves.
		RecoveryPath:   web.RouteResetPassword,
		InvitationPath: web.RouteInvitationConsume,
	})

	token, err := links.CreateInvitation(ctx, email, bootstrapRoleCode, false)
	if err != nil {
		return fmt.Errorf("create invitation: %w", err)
	}

	// portalctl runs outside the HTTP stack, so the generic middleware audit
	// does not cover it. Record the issuance here; consuming the invitation is
	// audited by the API under auth.invitation.consume.
	rec.Record(ctx, audit.Event{
		Action:       "auth.bootstrap_admin.invite",
		Outcome:      audit.OutcomeSuccess,
		ResourceKind: "email_link",
		DetailJSON: fmt.Sprintf(`{"invitee":%q,"role":%q,"surface":"portalctl"}`,
			obs.SafeEmail(email), bootstrapRoleCode),
	})

	fmt.Println("Bootstrap administrator invitation created.")
	fmt.Printf("Email:   %s\n", email)
	fmt.Printf("Role:    %s (granted when the invitation is accepted)\n", bootstrapRoleCode)
	fmt.Printf("Expires: %s\n", time.Now().Add(24*time.Hour).Format(time.RFC3339))
	fmt.Printf("URL:     %s\n", links.InvitationURL(token))
	fmt.Println("\nShare this URL securely. It can only be used once.")

	return nil
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
	Role     string
}

func seedDemo(d *sql.DB) error {
	// The server hashes with the configured pepper; seeding without it would
	// produce accounts whose passwords can never verify.
	pepper := []byte(os.Getenv(authn.PepperEnvVar))
	if err := authn.BindPepper(d, pepper); err != nil {
		return fmt.Errorf("seed-demo: %w", err)
	}

	users := []demoUser{
		{Email: "admin@demo.local", Password: "admin", Role: "administrator"},
		{Email: "treasurer@demo.local", Password: "treasurer", Role: "treasurer"},
		{Email: "joe@demo.local", Password: "joe", Role: "member"},
	}

	for _, u := range users {
		hash, err := authn.HashPassword(u.Password, pepper, authn.DefaultParams())
		if err != nil {
			return fmt.Errorf("hash password for %s: %w", u.Email, err)
		}

		// Upsert user.
		res, err := d.Exec(
			`INSERT INTO users (email, password_hash, password_algo_params, is_active)
			 VALUES (?, ?, 'argon2id', 1)
			 ON CONFLICT(email) DO UPDATE SET password_hash = excluded.password_hash, updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z','now')`,
			u.Email, hash,
		)
		if err != nil {
			return fmt.Errorf("upsert user %s: %w", u.Email, err)
		}

		userID, _ := res.LastInsertId()
		if userID == 0 {
			// ON CONFLICT updated — look up the ID.
			if err := d.QueryRow(`SELECT id FROM users WHERE email = ?`, u.Email).Scan(&userID); err != nil {
				return fmt.Errorf("lookup user %s: %w", u.Email, err)
			}
		}

		// Grant role (skip if already granted).
		now := time.Now().UTC().Format(time.RFC3339Nano)
		_, err = d.Exec(
			`INSERT INTO user_role_grants (user_id, role_code, granted_by, granted_at, reason)
			 SELECT ?, ?, ?, ?, 'seed-demo'
			 WHERE NOT EXISTS (
			   SELECT 1 FROM user_role_grants WHERE user_id = ? AND role_code = ? AND revoked_at IS NULL
			 )`,
			userID, u.Role, userID, now, userID, u.Role,
		)
		if err != nil {
			return fmt.Errorf("grant role %s to %s: %w", u.Role, u.Email, err)
		}

		fmt.Printf("  %-28s  role=%-15s  password=%s\n", u.Email, u.Role, u.Password)
	}

	fmt.Println("\nDemo users seeded. Sign in at /login")
	return nil
}

func usage(w *os.File) {
	fmt.Fprintln(w, `portalctl — BCARS members portal admin CLI.

Commands:
  bootstrap-admin --email <addr> --db <path>   Create the first administrator.
  seed-demo --db <path>                        Create demo users (admin, treasurer, member).
  backup --to <dir>                            Encrypted database backup (WS8.2).
  restore --from <path> --into <dir>           Restore an encrypted backup (WS8.2).
  version                                      Print detailed build info.
  --version                                    Print short version identifier.
  help                                         Show this help.`)
}
