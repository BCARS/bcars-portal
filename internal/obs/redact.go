package obs

import (
	"log/slog"
	"regexp"
	"strings"
)

// Redacted is the replacement string for sensitive values in logs.
const Redacted = "[REDACTED]"

// redactPatterns matches common PII patterns for automated redaction.
var redactPatterns = []*regexp.Regexp{
	regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`),                             // email
	regexp.MustCompile(`\b\d{3}[-.]?\d{3}[-.]?\d{4}\b`),                                                // US phone
	regexp.MustCompile(`\b\d{5}(-\d{4})?\b`),                                                           // ZIP code
	regexp.MustCompile(`(?i)(password|secret|token|api_key|apikey|authorization)["\s:=]*[^\s,;"]{4,}`), // credentials
}

// RedactString replaces known PII patterns in a string.
func RedactString(s string) string {
	for _, p := range redactPatterns {
		s = p.ReplaceAllString(s, Redacted)
	}
	return s
}

// SafeEmail returns a redacted email suitable for logging.
// Shows the domain but masks the local part: "j***@example.com"
func SafeEmail(email string) string {
	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 {
		return Redacted
	}
	local := parts[0]
	if len(local) > 1 {
		local = string(local[0]) + "***"
	}
	return local + "@" + parts[1]
}

// SafePhone returns a redacted phone number showing only the last 4 digits.
func SafePhone(phone string) string {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, phone)
	if len(digits) < 4 {
		return Redacted
	}
	return "***-" + digits[len(digits)-4:]
}

// SafeAttr creates a slog.Attr with the value redacted.
func SafeAttr(key, value string) slog.Attr {
	return slog.String(key, RedactString(value))
}

// EmailAttr creates a slog.Attr with a safely masked email.
func EmailAttr(email string) slog.Attr {
	return slog.String("email", SafeEmail(email))
}

// PhoneAttr creates a slog.Attr with a safely masked phone.
func PhoneAttr(phone string) slog.Attr {
	return slog.String("phone", SafePhone(phone))
}
