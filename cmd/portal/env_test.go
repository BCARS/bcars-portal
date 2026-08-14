package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseWith builds the server's REAL flag set — same names, same defaults, same
// parsers — applies the given command line, and then resolves the environment
// against it. Tests that constructed their own flag set could agree with
// themselves while every binding in envBindings named a flag the binary does
// not have.
func parseWith(t *testing.T, args []string, env map[string]string) *options {
	t.Helper()

	fs := flag.NewFlagSet("portal", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := registerFlags(fs)
	require.NoError(t, fs.Parse(args))
	require.NoError(t, applyEnv(fs, func(name string) string { return env[name] }))
	return o
}

func TestEnvironmentSuppliesFlagsThatWereNotPassed(t *testing.T) {
	o := parseWith(t, nil, map[string]string{
		"PORTAL_DB":        "/srv/portal/portal.db",
		"PORTAL_ADDR":      ":9090",
		"PORTAL_LOG_LEVEL": "debug",
		"PORTAL_BASE_URL":  "https://portal.example.org",
		"PORTAL_MIGRATE":   "true",
		"PORTAL_SMTP_PORT": "2525",
	})

	assert.Equal(t, "/srv/portal/portal.db", *o.dbPath)
	assert.Equal(t, ":9090", *o.addr)
	assert.Equal(t, "debug", *o.logLevel)
	assert.Equal(t, "https://portal.example.org", *o.baseURL)
	assert.True(t, *o.migrate, "a bool binding must parse, not merely be non-empty")
	assert.Equal(t, 2525, *o.smtpPort, "an int binding must parse into the flag's type")
}

func TestFlagsBeatTheEnvironment(t *testing.T) {
	o := parseWith(t,
		[]string{"-db", "/from/flag.db", "-addr", ":7000"},
		map[string]string{"PORTAL_DB": "/from/env.db", "PORTAL_ADDR": ":9999"})

	assert.Equal(t, "/from/flag.db", *o.dbPath)
	assert.Equal(t, ":7000", *o.addr)
}

// TestAFlagPassedAtItsDefaultStillBeatsTheEnvironment is the case a
// compare-against-default implementation gets wrong. Passing -addr :8080 is a
// decision even though :8080 is also the default, and an environment variable
// must not override it.
func TestAFlagPassedAtItsDefaultStillBeatsTheEnvironment(t *testing.T) {
	o := parseWith(t, []string{"-addr", ":8080"}, map[string]string{"PORTAL_ADDR": ":9999"})
	assert.Equal(t, ":8080", *o.addr)
}

func TestUnsetAndEmptyVariablesLeaveDefaultsAlone(t *testing.T) {
	// Empty is treated as unset: an exported-but-empty variable is an unfilled
	// template, and honouring it would blank the database path.
	o := parseWith(t, nil, map[string]string{"PORTAL_DB": "", "PORTAL_ADDR": "   "})
	assert.Equal(t, "bcars.db", *o.dbPath)
	assert.Equal(t, ":8080", *o.addr)
}

func TestAnUnparseableVariableIsRefusedByName(t *testing.T) {
	fs := flag.NewFlagSet("portal", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	registerFlags(fs)
	require.NoError(t, fs.Parse(nil))

	err := applyEnv(fs, func(name string) string {
		if name == "PORTAL_SMTP_PORT" {
			return "not-a-port"
		}
		return ""
	})
	require.Error(t, err, "a bad value must stop startup, not be silently ignored")
	assert.Contains(t, err.Error(), "PORTAL_SMTP_PORT", "the message must name the variable to fix")
	assert.Contains(t, err.Error(), "smtp-port", "the message must name the flag it feeds")
}

// TestEveryBindingNamesARealFlag is the check that keeps the table honest: a
// renamed or removed flag turns its binding into a silently dead variable that
// an operator would set and watch have no effect.
func TestEveryBindingNamesARealFlag(t *testing.T) {
	fs := flag.NewFlagSet("portal", flag.ContinueOnError)
	registerFlags(fs)

	for _, b := range envBindings {
		assert.NotNilf(t, fs.Lookup(b.Flag), "%s is bound to -%s, which the server does not define", b.Env, b.Flag)
		assert.Truef(t, strings.HasPrefix(b.Env, "PORTAL_"), "%s should be namespaced PORTAL_", b.Env)
	}
}

// TestSecretsAndDevelopmentSwitchesHaveNoEnvironmentBinding pins two deliberate
// omissions. A secret with a flag is a secret in the process table; a
// development switch with an environment variable is one copied manifest away
// from disabling Secure cookies in production.
func TestSecretsAndDevelopmentSwitchesHaveNoEnvironmentBinding(t *testing.T) {
	bound := map[string]bool{}
	for _, b := range envBindings {
		bound[b.Flag] = true
	}

	for _, name := range []string{"allow-empty-pepper", "allow-insecure-cookies"} {
		assert.Falsef(t, bound[name], "-%s must stay a deliberate command-line decision", name)
	}

	fs := flag.NewFlagSet("portal", flag.ContinueOnError)
	registerFlags(fs)
	for _, secret := range []string{"password-pepper", "pepper", "smtp-password", "backup-passphrase"} {
		assert.Nilf(t, fs.Lookup(secret), "-%s must not exist; secrets are environment-only", secret)
	}
}

// TestDocumentedEnvironmentVariablesMatchTheCode closes the gap this bead was
// filed for: docs/deployment.md described PORTAL_DB, PORTAL_ADDR and
// PORTAL_LOG_LEVEL for months while no code read any of them. Documentation and
// table now fail together rather than drifting apart.
func TestDocumentedEnvironmentVariablesMatchTheCode(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "deployment.md"))
	require.NoError(t, err)
	doc := string(raw)

	for _, b := range envBindings {
		assert.Containsf(t, doc, b.Env, "%s is read by the server but not documented", b.Env)
	}

	// And the reverse: every PORTAL_ variable the document names is either a
	// binding or one of the three environment-only secrets.
	secrets := map[string]bool{
		"PORTAL_PASSWORD_PEPPER":   true,
		"PORTAL_SMTP_PASSWORD":     true,
		"PORTAL_BACKUP_PASSPHRASE": true,
	}
	bound := map[string]bool{}
	for _, b := range envBindings {
		bound[b.Env] = true
	}
	for _, name := range documentedPortalVars(doc) {
		if secrets[name] {
			continue
		}
		assert.Truef(t, bound[name], "docs/deployment.md documents %s, which the server never reads", name)
	}
}

// documentedPortalVars extracts every PORTAL_* token the documentation names.
func documentedPortalVars(doc string) []string {
	var out []string
	seen := map[string]bool{}
	for _, field := range strings.FieldsFunc(doc, func(r rune) bool {
		return !(r == '_' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	}) {
		if strings.HasPrefix(field, "PORTAL_") && !seen[field] {
			seen[field] = true
			out = append(out, field)
		}
	}
	return out
}
