// Package main is the BCARS portal admin CLI.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/bcars/bcars-portal/internal/authn"
	"github.com/bcars/bcars-portal/internal/db"
	"github.com/bcars/bcars-portal/internal/mail"
	"github.com/bcars/bcars-portal/internal/version"
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
	case "backup", "restore":
		fmt.Fprintln(os.Stderr, "portalctl "+os.Args[1]+": not yet implemented (WS8.2).")
		os.Exit(0)
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

	// Create invitation link (no email sent — print to stdout).
	mailer := mail.NewFilelogSender(os.TempDir())
	links := authn.NewEmailLinkService(d, mailer, authn.EmailLinkConfig{
		BaseURL: baseURL,
		TTL:     24 * time.Hour,
	})

	token, err := links.CreateInvitation(context.Background(), email, false)
	if err != nil {
		return fmt.Errorf("create invitation: %w", err)
	}

	url := fmt.Sprintf("%s/auth/invitations/consume?token=%s", baseURL, token)
	fmt.Println("Bootstrap administrator invitation created.")
	fmt.Printf("Email:   %s\n", email)
	fmt.Printf("Expires: %s\n", time.Now().Add(24*time.Hour).Format(time.RFC3339))
	fmt.Printf("URL:     %s\n", url)
	fmt.Println("\nShare this URL securely. It can only be used once.")

	return nil
}

func usage(w *os.File) {
	fmt.Fprintln(w, `portalctl — BCARS members portal admin CLI.

Commands:
  bootstrap-admin --email <addr> --db <path>   Create the first administrator.
  backup --to <dir>                            Encrypted database backup (WS8.2).
  restore --from <path> --into <dir>           Restore an encrypted backup (WS8.2).
  version                                      Print detailed build info.
  --version                                    Print short version identifier.
  help                                         Show this help.`)
}
