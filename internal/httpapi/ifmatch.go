package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// VersionHeader is the response header that carries the current row version.
// Clients send it back in If-Match to enable optimistic concurrency.
const VersionHeader = "ETag"

// IfMatchHeader is the request header clients use to declare the version they
// last saw.
const IfMatchHeader = "If-Match"

// FormatETag formats a row version as an HTTP ETag value: a quoted integer.
//
//	w.Header().Set(VersionHeader, FormatETag(row.Version))
func FormatETag(version int64) string {
	return fmt.Sprintf(`"%d"`, version)
}

// ParseIfMatch extracts the version integer from an If-Match header value.
// It accepts the quoted form returned by FormatETag ("42") as well as the
// unquoted form (42) for convenience.
//
// Returns (0, nil) when the header is absent (caller may treat that as
// "skip optimistic concurrency check"). Returns a non-nil error when the
// header is present but malformed, which should surface as a 400.
func ParseIfMatch(r *http.Request) (version int64, err error) {
	raw := strings.TrimSpace(r.Header.Get(IfMatchHeader))
	if raw == "" {
		return 0, nil
	}
	// Strip optional quotes.
	raw = strings.Trim(raw, `"`)
	v, convErr := strconv.ParseInt(raw, 10, 64)
	if convErr != nil {
		return 0, fmt.Errorf("If-Match: expected a quoted integer (e.g. \"42\"), got %q", r.Header.Get(IfMatchHeader))
	}
	return v, nil
}

// WriteETag sets the ETag response header to the formatted version.
func WriteETag(w http.ResponseWriter, version int64) {
	w.Header().Set(VersionHeader, FormatETag(version))
}
