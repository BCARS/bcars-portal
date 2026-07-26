// Package obs provides structured logging, request-id middleware, and
// PII-safe helpers for the BCARS portal.
//
// WS1.4 landed the request-id middleware and slog wiring. Redaction helpers
// used by the audit writer live here so both the log and the audit path can
// share one definition of "sensitive".
package obs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

// RequestIDHeader is the canonical HTTP header carrying the request id.
const RequestIDHeader = "X-Request-Id"

// contextKey is unexported so callers must use the helpers below.
type contextKey struct{ name string }

var (
	requestIDKey = &contextKey{"request-id"}
	loggerKey    = &contextKey{"logger"}
)

// NewLogger builds a JSON slog logger at the requested level. Level strings
// are debug|info|warn|error; anything else falls back to info.
func NewLogger(w io.Writer, level string) *slog.Logger {
	if w == nil {
		w = os.Stderr
	}
	lvl := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})
	return slog.New(h)
}

// WithLogger returns a context that carries the given logger. The middleware
// stores a per-request logger already annotated with the request id.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	if l == nil {
		return ctx
	}
	return context.WithValue(ctx, loggerKey, l)
}

// LoggerFrom returns the request-scoped logger, or slog.Default() if none is
// set. It never returns nil.
func LoggerFrom(ctx context.Context) *slog.Logger {
	if v, ok := ctx.Value(loggerKey).(*slog.Logger); ok && v != nil {
		return v
	}
	return slog.Default()
}

// WithRequestID stores a request id in ctx.
func WithRequestID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFrom returns the request id, or the empty string if none is set.
func RequestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

// NewRequestID returns a fresh 128-bit random id encoded as 32 hex chars.
// It uses crypto/rand and panics only if the entropy source is unavailable,
// which on any supported platform indicates a broken host.
func NewRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand is documented never to fail on modern OSes; if it
		// does, we truly cannot continue.
		panic("obs: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// RequestIDMiddleware ensures every request has a request id, both in its
// context and on the response as X-Request-Id. When trustInboundHeader is
// true, an incoming X-Request-Id is preserved (and lightly validated);
// otherwise a fresh id is always minted.
//
// The middleware also attaches a per-request logger derived from base with a
// "request_id" attribute so downstream handlers just call obs.LoggerFrom(ctx).
func RequestIDMiddleware(base *slog.Logger, trustInboundHeader bool) func(http.Handler) http.Handler {
	if base == nil {
		base = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := ""
			if trustInboundHeader {
				id = sanitizeRequestID(r.Header.Get(RequestIDHeader))
			}
			if id == "" {
				id = NewRequestID()
			}
			w.Header().Set(RequestIDHeader, id)

			ctx := WithRequestID(r.Context(), id)
			ctx = WithLogger(ctx, base.With(slog.String("request_id", id)))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// sanitizeRequestID accepts a small subset of characters. Anything else is
// dropped and the caller mints a fresh id. This prevents log/header injection
// and keeps ids compact.
func sanitizeRequestID(s string) string {
	s = strings.TrimSpace(s)
	if len(s) == 0 || len(s) > 128 {
		return ""
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r == '-' || r == '_':
		default:
			return ""
		}
	}
	return s
}
