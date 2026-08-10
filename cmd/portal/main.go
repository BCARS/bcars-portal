// Package main is the BCARS portal HTTP server entry point.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/authn"
	"github.com/bcars/bcars-portal/internal/db"
	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/httpapi"
	"github.com/bcars/bcars-portal/internal/obs"
	"github.com/bcars/bcars-portal/internal/version"
)

func main() {
	fs := flag.NewFlagSet("portal", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `bcars-portal — BCARS members portal server.

Usage:
  portal [flags]

Flags:
`)
		fs.PrintDefaults()
		fmt.Fprintf(fs.Output(), `
Environment:
  PORTAL_SMTP_PASSWORD      password for -smtp-user (never passed as a flag)
  PORTAL_PASSWORD_PEPPER    secret mixed into every password hash; required
                            unless -allow-empty-pepper is set. Minimum 16
                            bytes. Changing it after accounts exist makes
                            every existing password unverifiable — the server
                            refuses to start rather than rejecting everyone's
                            sign-in as a bad password. It also keys the client
                            address hashes stored on sessions and email links;
                            without it those are recorded as unknown.
`)
	}

	showVersion := fs.Bool("version", false, "print version and exit")
	showVersionLong := fs.Bool("version-long", false, "print detailed build info and exit")
	addr := fs.String("addr", ":8080", "listen address")
	logLevel := fs.String("log-level", "info", "log level (debug|info|warn|error)")
	dbPath := fs.String("db", "bcars.db", "path to SQLite database file")
	migrate := fs.Bool("migrate", false, "apply pending migrations at startup")
	migrateOnly := fs.Bool("migrate-only", false, "apply migrations and exit")
	dumpOpenAPI := fs.String("dump-openapi", "", "write OpenAPI JSON to `path` and exit (used by make openapi)")
	dumpCatalog := fs.String("dump-catalog", "", "write capability catalog JSON to `path` and exit (used by make openapi)")
	baseURL := fs.String("base-url", "http://localhost:8080", "public base URL used to build recovery and invitation links")
	mailTransport := fs.String("mail-transport", "filelog", "outbound mail transport: filelog (writes JSON files, for dev) or smtp")
	mailDir := fs.String("mail-dir", "mail-outbox", "directory for the filelog mail transport (created if missing)")
	smtpHost := fs.String("smtp-host", "", "SMTP relay host (required when -mail-transport=smtp)")
	smtpPort := fs.Int("smtp-port", 587, "SMTP relay port")
	smtpUser := fs.String("smtp-user", "", "SMTP username; empty means no authentication")
	smtpFrom := fs.String("smtp-from", "", "From address for outbound mail (required when -mail-transport=smtp)")
	trustedProxyHeader := fs.String("trusted-proxy-header", "",
		"`header` carrying the real client address (e.g. X-Forwarded-For); leftmost entry is used. "+
			"Set this ONLY when a reverse proxy you control overwrites the header — otherwise any "+
			"client can forge its own recorded source address")
	allowEmptyPepper := fs.Bool("allow-empty-pepper", false,
		"start without "+authn.PepperEnvVar+" (DEVELOPMENT ONLY; passwords are hashed without a pepper)")
	allowInsecureCookies := fs.Bool("allow-insecure-cookies", false,
		"issue session cookies without the Secure attribute (DEVELOPMENT ONLY; required to sign in over plaintext http://localhost)")
	// The SMTP password is read from the environment so it never appears in
	// process listings or shell history.
	smtpPassword := os.Getenv("PORTAL_SMTP_PASSWORD")

	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		os.Exit(2)
	}
	if *showVersionLong {
		fmt.Print(version.Get().Long())
		return
	}
	if *showVersion {
		fmt.Println(version.Get().Short())
		return
	}

	logger := obs.NewLogger(os.Stderr, *logLevel)

	// Generate artifacts and exit (used by make openapi). No DB needed.
	// Use the base version without git SHA so the output is deterministic
	// across commits and openapi-diff stays clean.
	if *dumpOpenAPI != "" || *dumpCatalog != "" {
		_, api := httpapi.NewRouter(httpapi.Config{
			Logger:  logger,
			Version: version.Get().Version,
		})
		httpapi.RegisterAll(api, httpapi.Deps{})
		if err := httpapi.VerifyAll(api); err != nil {
			logger.Error("startup check failed", slog.String("error", err.Error()))
			os.Exit(1)
		}
		if err := dumpArtifacts(api, *dumpOpenAPI, *dumpCatalog); err != nil {
			logger.Error("dump failed", slog.String("error", err.Error()))
			os.Exit(1)
		}
		return
	}

	// Open the database.
	database, err := db.Open(*dbPath)
	if err != nil {
		logger.Error("failed to open database", slog.String("path", *dbPath), slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer database.Close()

	// Run migrations if requested.
	if *migrate || *migrateOnly {
		logger.Info("running migrations", slog.String("path", *dbPath))
		if err := db.Migrate(database); err != nil {
			logger.Error("migration failed", slog.String("error", err.Error()))
			os.Exit(1)
		}
		logger.Info("migrations complete")
		if *migrateOnly {
			return
		}
	}

	// Build the outbound mail transport.
	mailer, err := newMailSender(mailConfig{
		Transport:    *mailTransport,
		FilelogDir:   *mailDir,
		SMTPHost:     *smtpHost,
		SMTPPort:     *smtpPort,
		SMTPUser:     *smtpUser,
		SMTPFrom:     *smtpFrom,
		SMTPPassword: smtpPassword,
	})
	if err != nil {
		logger.Error("mail configuration invalid", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// Assemble the production handler (router + capability enforcement +
	// session-cookie authentication).
	handler, err := buildHandler(database, assemblyConfig{
		Pepper:           []byte(os.Getenv(authn.PepperEnvVar)),
		AllowEmptyPepper: *allowEmptyPepper,

		AllowInsecureCookies: *allowInsecureCookies,
		TrustedProxyHeader:   *trustedProxyHeader,
		Logger:               logger,
		Version:              version.Get().Short(),
		CookieName:           authn.DefaultSessionCookieName,
		SessionTTL:           24 * time.Hour,
		BaseURL:              *baseURL,
		EmailLinkTTL:         24 * time.Hour,
		Mailer:               mailer,
	})
	if err != nil {
		logger.Error("failed to assemble server", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// Start server.
	srv := &http.Server{
		Addr:         *addr,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
		BaseContext:  func(_ net.Listener) context.Context { return context.Background() },
	}

	logger.Info("starting portal",
		slog.String("addr", *addr),
		slog.String("version", version.Get().Short()),
	)

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}()

	// Graceful shutdown on SIGINT / SIGTERM.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("shutdown error", slog.String("error", err.Error()))
		os.Exit(1)
	}
	logger.Info("stopped")
}

// dumpArtifacts writes OpenAPI JSON and/or capability catalog JSON for CI diffing.
func dumpArtifacts(api huma.API, openapiPath, catalogPath string) error {
	if openapiPath != "" {
		b, err := json.MarshalIndent(api.OpenAPI(), "", "  ")
		if err != nil {
			return fmt.Errorf("marshal openapi: %w", err)
		}
		if err := os.WriteFile(openapiPath, b, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", openapiPath, err)
		}
	}
	if catalogPath != "" {
		type entry struct {
			Code              string `json:"code"`
			Description       string `json:"description"`
			Category          string `json:"category"`
			AIToolEligibility string `json:"ai_tool_eligibility"`
			// OperationIDs that require this capability.
			OperationIDs []string `json:"operation_ids"`
			// ConfirmOperationIDs are the subset that additionally require
			// explicit confirmation. Published because ADR-0011 made the
			// declared level enforceable: a control the catalog describes but
			// the server does not apply is exactly the defect that ADR fixed,
			// and one it never describes is invisible to an operator reading
			// what the API guards.
			ConfirmOperationIDs []string `json:"confirm_operation_ids,omitempty"`
		}
		// Build a map from capability code to operation IDs.
		opIDsByCode := map[string][]string{}
		confirmByCode := map[string][]string{}
		for opID, meta := range httpapi.AllMeta() {
			code := meta.RequiredCapability
			opIDsByCode[code] = append(opIDsByCode[code], opID)
			if meta.ConfirmationLevel == httpapi.ConfirmExplicit {
				confirmByCode[code] = append(confirmByCode[code], opID)
			}
		}
		for k := range confirmByCode {
			sort.Strings(confirmByCode[k])
		}
		for k := range opIDsByCode {
			sort.Strings(opIDsByCode[k])
		}
		var entries []entry
		for _, cap := range authz.All {
			entries = append(entries, entry{
				Code:                cap.Code,
				Description:         cap.Description,
				Category:            cap.Category,
				AIToolEligibility:   cap.AIToolEligibility,
				OperationIDs:        opIDsByCode[cap.Code],
				ConfirmOperationIDs: confirmByCode[cap.Code],
			})
		}
		b, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal catalog: %w", err)
		}
		if err := os.WriteFile(catalogPath, b, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", catalogPath, err)
		}
	}
	return nil
}
