package smoke

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServerIsConfigurableEntirelyByEnvironment starts the shipped binary with
// no flags at all — the way a container or a process supervisor starts it — and
// asserts it honours what the environment told it.
//
// The package tests in cmd/portal cover the precedence rules against the real
// flag set, but they call applyEnv themselves. Nothing there notices if main
// stops calling it, which would leave every documented variable inert while the
// unit tests stayed green. This starts the artifact and looks at where it
// listens and what it wrote (bcars-portal-fmc.8).
func TestServerIsConfigurableEntirelyByEnvironment(t *testing.T) {
	if testing.Short() {
		t.Skip("smoke test builds binaries and starts a server; skipped in -short")
	}

	tmp := t.TempDir()
	runDir := filepath.Join(tmp, "elsewhere")
	require.NoError(t, os.MkdirAll(runDir, 0o750))
	requireOutsideRepo(t, runDir)

	dbPath := filepath.Join(tmp, "env-configured.db")
	mailDir := filepath.Join(tmp, "env-mail")
	port := freePort(t)

	// Every setting arrives as an environment variable, including the one that
	// runs migrations — so readiness, which is 503 until the schema matches,
	// is itself the assertion that PORTAL_MIGRATE was read.
	cmd := exec.Command(binPath(t, "portal"))
	cmd.Env = append(os.Environ(),
		smokePepperEnv,
		"PORTAL_DB="+dbPath,
		fmt.Sprintf("PORTAL_ADDR=127.0.0.1:%d", port),
		"PORTAL_BASE_URL="+fmt.Sprintf("http://127.0.0.1:%d", port),
		"PORTAL_MAIL_TRANSPORT=filelog",
		"PORTAL_MAIL_DIR="+mailDir,
		"PORTAL_LOG_LEVEL=warn",
		"PORTAL_MIGRATE=true",
	)
	cmd.Dir = runDir
	var logBuf syncBuffer
	cmd.Stdout = &logBuf
	cmd.Stderr = &logBuf
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		if t.Failed() {
			t.Logf("portal server log:\n%s", logBuf.String())
		}
	})

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 5 * time.Second}

	var ready bool
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(base + "/readyz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.True(t, ready,
		"the server never became ready on the address PORTAL_ADDR named; log:\n%s", logBuf.String())

	// PORTAL_DB decided where the data went. A server that ignored it would
	// have created a database next to its working directory instead.
	_, err := os.Stat(dbPath)
	assert.NoError(t, err, "PORTAL_DB did not decide the database path")

	entries, err := os.ReadDir(runDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "the server wrote into its working directory rather than the configured paths")
}

// TestDevelopmentSwitchesCannotBeSetByEnvironment pins the deliberate omission:
// the two flags that weaken a production deployment must require a command
// line. An environment variable is what gets copied from a development manifest
// into a production one.
func TestDevelopmentSwitchesCannotBeSetByEnvironment(t *testing.T) {
	if testing.Short() {
		t.Skip("smoke test builds binaries and starts a server; skipped in -short")
	}

	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "no-pepper.db")

	// No pepper, and an attempt to relax that requirement through the
	// environment. The server must still refuse to start.
	cmd := exec.Command(binPath(t, "portal"), "-db", dbPath, "-migrate-only")
	cmd.Env = append(os.Environ(),
		"PORTAL_PASSWORD_PEPPER=",
		"PORTAL_ALLOW_EMPTY_PEPPER=true",
		"PORTAL_ALLOW_INSECURE_COOKIES=true",
	)
	out, err := cmd.CombinedOutput()

	// -migrate-only exits before the pepper is checked, so the meaningful
	// assertion is on a server start: do that too.
	require.NoError(t, err, "migrate-only should still work: %s", out)

	port := freePort(t)
	srv := exec.Command(binPath(t, "portal"),
		"-db", dbPath,
		"-addr", fmt.Sprintf("127.0.0.1:%d", port),
	)
	srv.Env = append(os.Environ(),
		"PORTAL_PASSWORD_PEPPER=",
		"PORTAL_ALLOW_EMPTY_PEPPER=true",
	)
	out, err = srv.CombinedOutput()
	require.Error(t, err,
		"the server started without a pepper because an environment variable was honoured: %s", out)
	assert.Contains(t, string(out), "pepper",
		"the refusal must name the missing secret: %s", out)
}
