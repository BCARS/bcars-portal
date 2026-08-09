package smoke

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/mail"
)

var (
	buildOnce sync.Once
	binDir    string
	buildErr  error
)

// binPath builds both binaries once per test run and returns the path to one.
// Building from source is the point: the smoke test must exercise the artifact
// the Makefile produces, not a library reconstruction of it.
func binPath(t *testing.T, name string) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "bcars-smoke-bin")
		if err != nil {
			buildErr = err
			return
		}
		binDir = dir
		for _, target := range []string{"portal", "portalctl"} {
			cmd := exec.Command("go", "build", "-o", filepath.Join(dir, target), "./cmd/"+target)
			cmd.Dir = repoRoot(t)
			if out, err := cmd.CombinedOutput(); err != nil {
				buildErr = fmt.Errorf("build %s: %w\n%s", target, err, out)
				return
			}
		}
	})
	require.NoError(t, buildErr)
	return filepath.Join(binDir, name)
}

// repoRoot walks up from the test's working directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "go.mod not found above the test directory")
		dir = parent
	}
}

// start migrates a temporary database, runs the server, and waits for it to
// report ready. The server is stopped and its log dumped on failure.
func start(t *testing.T) *env {
	t.Helper()

	tmp := t.TempDir()
	e := &env{
		t:       t,
		dbPath:  filepath.Join(tmp, "smoke.db"),
		mailDir: filepath.Join(tmp, "mail"),
		client: &http.Client{
			Timeout: 10 * time.Second,
			// Do not follow redirects: a redirect is a meaningful outcome
			// here, not something to transparently resolve.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	e.mailer = mail.NewFilelogSender(e.mailDir)

	port := freePort(t)
	e.baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)

	// Migrate with the real binary rather than the library, so the smoke test
	// also covers the documented `portal -migrate-only` deployment step.
	e.run(binPath(t, "portal"), "-migrate-only", "-db", e.dbPath)

	cmd := exec.Command(binPath(t, "portal"),
		"-db", e.dbPath,
		"-addr", fmt.Sprintf("127.0.0.1:%d", port),
		"-base-url", e.baseURL,
		"-mail-transport", "filelog",
		"-mail-dir", e.mailDir,
		"-log-level", "warn",
	)
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

	e.waitForListening(port, &logBuf)
	return e
}

func (e *env) waitForListening(port int, logBuf *syncBuffer) {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 250*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.t.Fatalf("server never started listening on %d:\n%s", port, logBuf.String())
}

// requireReady asserts /readyz reports ok, which covers DB reachability, the
// required SQLite pragmas, and the migration version the binary expects.
func (e *env) requireReady() {
	e.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		resp, err := e.client.Get(e.baseURL + "/readyz")
		if err == nil {
			body := readBody(resp)
			if resp.StatusCode == http.StatusOK {
				return
			}
			last = fmt.Sprintf("status %d: %s", resp.StatusCode, body)
		}
		time.Sleep(200 * time.Millisecond)
	}
	e.t.Fatalf("/readyz never reported ok: %s", last)
}

// --- helpers ---

func (e *env) do(method, path string, cookie *http.Cookie, body string) *http.Response {
	e.t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, e.baseURL+path, rdr)
	require.NoError(e.t, err)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := e.client.Do(req)
	require.NoError(e.t, err)

	// Buffer the body so it can be read more than once. Assertion messages
	// like `require.Equal(..., "%s", readBody(resp))` are evaluated eagerly by
	// testify even when the assertion passes, which would otherwise drain the
	// body before the caller decodes it.
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.NoError(e.t, err)
	resp.Body = &replayBody{data: raw}
	return resp
}

// replayBody serves the same bytes on every read, so a response body can be
// inspected and then decoded.
type replayBody struct {
	data []byte
	pos  int
}

func (b *replayBody) Read(p []byte) (int, error) {
	if b.pos >= len(b.data) {
		b.pos = 0 // rewind for the next reader
		return 0, io.EOF
	}
	n := copy(p, b.data[b.pos:])
	b.pos += n
	return n, nil
}

func (b *replayBody) Close() error { b.pos = 0; return nil }

// run executes a command and returns its combined output, failing the test on
// a non-zero exit.
func (e *env) run(name string, args ...string) string {
	e.t.Helper()
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	require.NoError(e.t, err, "%s %s failed:\n%s", filepath.Base(name), strings.Join(args, " "), out)
	return string(out)
}

func (e *env) mailCount() int {
	sent, err := e.mailer.ReadAll()
	if err != nil {
		return 0
	}
	return len(sent)
}

func sessionCookie(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == "bcars_session" {
			return c
		}
	}
	return nil
}

// tokenFromURL extracts the invitation token from portalctl's printed URL,
// which is the only way an operator obtains it.
func tokenFromURL(t *testing.T, out string) string {
	t.Helper()
	idx := strings.Index(out, "token=")
	require.GreaterOrEqual(t, idx, 0, "no invitation URL in output:\n%s", out)
	rest := out[idx+len("token="):]
	if nl := strings.IndexAny(rest, "\r\n"); nl >= 0 {
		rest = rest[:nl]
	}
	token := strings.TrimSpace(rest)
	require.NotEmpty(t, token)
	return token
}

func readBody(resp *http.Response) string {
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// syncBuffer is a concurrency-safe buffer for the server's log output.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
