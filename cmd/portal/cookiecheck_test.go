package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/mail"
)

// The login loop with no error in it (bcars-portal-fmc.22).

func TestCookieReachabilityWarning(t *testing.T) {
	for _, tc := range []struct {
		name           string
		baseURL        string
		allowInsecure  bool
		expectWarning  bool
		mentionsRemedy bool
	}{
		{
			name:           "plaintext with secure cookies is the broken case",
			baseURL:        "http://localhost:8080",
			expectWarning:  true,
			mentionsRemedy: true,
		},
		{
			name:          "an address on the network is no different",
			baseURL:       "http://192.168.1.10:8080",
			expectWarning: true,
		},
		{
			name:          "https is the production shape",
			baseURL:       "https://portal.example.org",
			expectWarning: false,
		},
		{
			name:          "the opt-out is the operator having decided",
			baseURL:       "http://localhost:8080",
			allowInsecure: true,
			expectWarning: false,
		},
		{
			name:          "https with the opt-out is nobody's problem",
			baseURL:       "https://portal.example.org",
			allowInsecure: true,
			expectWarning: false,
		},
		{
			name:          "an unreadable base URL is a different problem",
			baseURL:       "://not a url",
			expectWarning: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := cookieReachabilityWarning(tc.baseURL, tc.allowInsecure)

			if !tc.expectWarning {
				assert.Empty(t, got)
				return
			}
			require.NotEmpty(t, got)
			if tc.mentionsRemedy {
				// A warning that describes a problem without naming the fix
				// leaves the reader where they started.
				assert.Contains(t, got, "-allow-insecure-cookies")
				assert.Contains(t, got, "https")
			}
		})
	}
}

// TestTheWarningReachesTheLog covers the wiring rather than the string: the
// check lives in buildHandler, which is what the binary and the smoke test both
// go through, so a check that existed and was never called would be invisible.
func TestTheWarningReachesTheLog(t *testing.T) {
	for _, tc := range []struct {
		name          string
		baseURL       string
		allowInsecure bool
		expect        bool
	}{
		{"plaintext base URL warns", "http://localhost:8080", false, true},
		{"https base URL is quiet", "https://portal.example.org", false, false},
		{"the opt-out is quiet", "http://localhost:8080", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

			d := newAssemblyDB(t)
			_, err := buildHandler(d, assemblyConfig{
				Logger:               logger,
				BaseURL:              tc.baseURL,
				AllowInsecureCookies: tc.allowInsecure,
				Pepper:               testPepper,
				Mailer:               mail.NewFilelogSender(t.TempDir()),
				CookieName:           "bcars_session",
			})
			require.NoError(t, err)

			logged := strings.Contains(buf.String(), "session cookies are Secure")
			assert.Equalf(t, tc.expect, logged, "log said: %s", buf.String())
		})
	}
}
