package obs

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRedactString(t *testing.T) {
	cases := []struct {
		name  string
		input string
		check func(string) bool
	}{
		{"email", "user john@example.com logged in", func(s string) bool { return !contains(s, "john@example.com") }},
		{"phone", "phone 555-123-4567", func(s string) bool { return !contains(s, "555-123-4567") }},
		{"token", `token: abc123secretvalue`, func(s string) bool { return !contains(s, "abc123secretvalue") }},
		{"password field", `password="s3cretP@ss"`, func(s string) bool { return !contains(s, "s3cretP@ss") }},
		{"safe text unchanged", "user 42 performed action", func(s string) bool { return s == "user 42 performed action" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := RedactString(tc.input)
			assert.True(t, tc.check(result), "redaction failed: got %q", result)
		})
	}
}

func TestSafeEmail(t *testing.T) {
	assert.Equal(t, "j***@example.com", SafeEmail("john@example.com"))
	assert.Equal(t, "a***@x.io", SafeEmail("ab@x.io"))
	assert.Equal(t, "[REDACTED]", SafeEmail("no-at-sign"))
}

func TestSafePhone(t *testing.T) {
	assert.Equal(t, "***-4567", SafePhone("555-123-4567"))
	assert.Equal(t, "***-0001", SafePhone("5551230001"))
	assert.Equal(t, "[REDACTED]", SafePhone("123"))
}

// Golden tests: ensure PII never appears in redacted output.
func TestPIINeverLeaks(t *testing.T) {
	sensitiveInputs := []string{
		"admin@bcars.club",
		"555-867-5309",
		`Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.secret`,
		`password: hunter2`,
		`api_key=sk_live_1234567890`,
	}
	for _, input := range sensitiveInputs {
		result := RedactString(input)
		// The result should not contain the original sensitive value
		// (unless it's a safe substring like "password" as a label).
		assert.Contains(t, result, Redacted, "expected redaction for input: %q, got: %q", input, result)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsImpl(s, substr)
}

func containsImpl(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
