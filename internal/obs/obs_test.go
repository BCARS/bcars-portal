package obs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bcars/bcars-portal/internal/obs"
)

func TestNewRequestID_UniqueAndHex(t *testing.T) {
	a := obs.NewRequestID()
	b := obs.NewRequestID()
	if a == b {
		t.Fatal("expected two request ids to differ")
	}
	if len(a) != 32 {
		t.Fatalf("expected 32 hex chars, got %d (%q)", len(a), a)
	}
	for _, r := range a {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Fatalf("non-hex char in id: %q", a)
		}
	}
}

func TestRequestIDMiddleware_MintsWhenAbsent(t *testing.T) {
	var buf bytes.Buffer
	logger := obs.NewLogger(&buf, "info")

	mw := obs.RequestIDMiddleware(logger, false)
	var seenID string
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenID = obs.RequestIDFrom(r.Context())
		obs.LoggerFrom(r.Context()).Info("hello")
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if seenID == "" {
		t.Fatal("handler did not observe a request id in context")
	}
	if got := rr.Header().Get(obs.RequestIDHeader); got != seenID {
		t.Fatalf("response header %q != context id %q", got, seenID)
	}
	// The logger should have carried request_id into the JSON line.
	line := firstLine(buf.String())
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("log line was not JSON: %v — %q", err, line)
	}
	if m["request_id"] != seenID {
		t.Fatalf("log line missing request_id=%q, got %#v", seenID, m["request_id"])
	}
}

func TestRequestIDMiddleware_TrustInboundWhenEnabled(t *testing.T) {
	logger := obs.NewLogger(nil, "warn")
	mw := obs.RequestIDMiddleware(logger, true)

	var seenID string
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenID = obs.RequestIDFrom(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(obs.RequestIDHeader, "abc-123_ok")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if seenID != "abc-123_ok" {
		t.Fatalf("expected inbound id preserved, got %q", seenID)
	}
}

func TestRequestIDMiddleware_RejectsBadInbound(t *testing.T) {
	logger := obs.NewLogger(nil, "warn")
	mw := obs.RequestIDMiddleware(logger, true)

	var seenID string
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenID = obs.RequestIDFrom(r.Context())
	}))

	// Contains a newline; must not be trusted.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(obs.RequestIDHeader, "bad\nvalue")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if seenID == "" || seenID == "bad\nvalue" {
		t.Fatalf("expected freshly minted id, got %q", seenID)
	}
}

func TestLoggerFrom_DefaultsWhenMissing(t *testing.T) {
	if obs.LoggerFrom(context.Background()) == nil {
		t.Fatal("LoggerFrom should never return nil")
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
