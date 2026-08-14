package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

// Configuration reaches the server two ways: flags, and the environment
// variables bound to them here. Containers and process supervisors set
// environment variables far more naturally than they build argument lists, and
// docs/deployment.md has documented such variables since before any code read
// them (bcars-portal-fmc.8).
//
// Secrets are deliberately absent from this table. PORTAL_PASSWORD_PEPPER,
// PORTAL_SMTP_PASSWORD and PORTAL_BACKUP_PASSPHRASE are environment-only and
// have no flag at all, because a flag is visible in the process table and in
// shell history. Binding them here would create the flag that must not exist.
//
// The development-only switches (-allow-empty-pepper, -allow-insecure-cookies)
// are also absent. Both weaken a production deployment, and an environment
// variable is exactly the kind of setting that gets copied from a development
// manifest into a production one without being read.

// envBinding ties a flag to the environment variable that supplies its value
// when the flag is not given on the command line.
type envBinding struct {
	Flag string
	Env  string
}

// envBindings is the whole table, in the order it is documented. It is
// exported to tests, which assert that docs/deployment.md describes exactly
// these bindings.
var envBindings = []envBinding{
	{Flag: "db", Env: "PORTAL_DB"},
	{Flag: "addr", Env: "PORTAL_ADDR"},
	{Flag: "log-level", Env: "PORTAL_LOG_LEVEL"},
	{Flag: "base-url", Env: "PORTAL_BASE_URL"},
	{Flag: "migrate", Env: "PORTAL_MIGRATE"},
	{Flag: "mail-transport", Env: "PORTAL_MAIL_TRANSPORT"},
	{Flag: "mail-dir", Env: "PORTAL_MAIL_DIR"},
	{Flag: "smtp-host", Env: "PORTAL_SMTP_HOST"},
	{Flag: "smtp-port", Env: "PORTAL_SMTP_PORT"},
	{Flag: "smtp-user", Env: "PORTAL_SMTP_USER"},
	{Flag: "smtp-from", Env: "PORTAL_SMTP_FROM"},
	{Flag: "trusted-proxy-header", Env: "PORTAL_TRUSTED_PROXY_HEADER"},
}

// applyEnv fills in flags the caller did not pass from the environment.
//
// Precedence is flag, then environment, then the flag's default. It is
// resolved by asking the flag set which flags were actually seen on the
// command line rather than by comparing against defaults: a flag passed
// explicitly with its default value is still a decision the operator made, and
// an environment variable must not quietly replace it.
//
// An empty value counts as unset. Exporting PORTAL_DB= in a shell that then
// starts the server should not blank the database path; a variable that is
// present but empty is nearly always an unfilled template, not an intent.
func applyEnv(fs *flag.FlagSet, getenv func(string) string) error {
	fromCommandLine := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { fromCommandLine[f.Name] = true })

	for _, b := range envBindings {
		if fromCommandLine[b.Flag] {
			continue
		}
		value := strings.TrimSpace(getenv(b.Env))
		if value == "" {
			continue
		}
		if fs.Lookup(b.Flag) == nil {
			// Unreachable unless a flag is renamed without updating the table,
			// which the binding test catches first.
			return fmt.Errorf("no -%s flag for %s", b.Flag, b.Env)
		}
		if err := fs.Set(b.Flag, value); err != nil {
			return fmt.Errorf("%s=%q is not a valid value for -%s: %w", b.Env, value, b.Flag, err)
		}
	}
	return nil
}

// envUsage renders the bound variables for -help, so the binary itself states
// what it reads rather than leaving the documentation as the only source.
func envUsage() string {
	var b strings.Builder
	for _, bind := range envBindings {
		fmt.Fprintf(&b, "  %-26s default for -%s\n", bind.Env, bind.Flag)
	}
	return b.String()
}

// osGetenv is the production lookup. Tests pass their own.
func osGetenv(name string) string { return os.Getenv(name) }
