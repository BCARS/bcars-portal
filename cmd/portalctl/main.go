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
		// Development-only subcommands register themselves in demoCommands
		// from a file guarded by a build tag. In a default (production) build
		// the map is empty and the command simply does not exist.
		if run, ok := demoCommands[os.Args[1]]; ok {
			if err := run(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "portalctl %s: %v\n", os.Args[1], err)
				os.Exit(1)
			}
			break
		}
		fmt.Fprintf(os.Stderr, "portalctl: unknown command %q\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
}

// demoCommands holds subcommands that exist only in development builds. It is
// populated by init() in files carrying the `demoseed` build tag; a default
// build does not compile those files at all, so neither the dispatch entry nor
// the code behind it is present in the shipped binary.
var demoCommands = map[string]func([]string) error{}

// demoCommandUsage and demoEnvUsage are the corresponding help-text lines,
// registered by the same tagged files so `help` never advertises a command the
// binary cannot run.
var (
	demoCommandUsage []string
	demoEnvUsage     []string
)

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

func usage(w *os.File) {
	fmt.Fprintln(w, `portalctl — BCARS members portal admin CLI.

Commands:
  bootstrap-admin --email <addr> --db <path>   Create the first administrator.
  backup --db <path> --to <dir>                Encrypted database backup.
  restore --from <path> --into <dir>           Restore an encrypted backup.
  version                                      Print detailed build info.
  --version                                    Print short version identifier.
  help                                         Show this help.`)
	for _, line := range demoCommandUsage {
		fmt.Fprintln(w, line)
	}

	fmt.Fprintln(w, `
Environment:
  PORTAL_BACKUP_PASSPHRASE   required by backup and restore; encrypts the
                             backup with age. Minimum 12 characters.
  PORTAL_PASSWORD_PEPPER     the server's password pepper. NOT stored in
                             backups — restoring a working instance needs the
                             backup passphrase AND the original pepper.`)
	for _, line := range demoEnvUsage {
		fmt.Fprintln(w, line)
	}

	if len(demoCommandUsage) == 0 {
		fmt.Fprintln(w, `
Development builds only:
  seed-demo (throwaway demo accounts with published passwords) is compiled out
  of this binary. Developers who need it build with:
      go build -tags demoseed ./cmd/portalctl`)
	}
}
