package clientip

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecret = "client-ip-test-secret-value-32b!"

func TestNormalizeIP_CanonicalForms(t *testing.T) {
	// An IPv4-mapped IPv6 peer and the plain IPv4 peer are one source.
	assert.Equal(t, normalizeIP("::ffff:203.0.113.7"), normalizeIP("203.0.113.7"))
	// A zone identifier must not split one source into many.
	assert.Equal(t, normalizeIP("fe80::1"), normalizeIP("fe80::1%eth0"))
	assert.Equal(t, "2001:db8::1", normalizeIP("[2001:db8::1]"))
	assert.Empty(t, normalizeIP("example.com"))
	assert.Empty(t, normalizeIP(""))
}

func TestLeftmostForwarded(t *testing.T) {
	assert.Equal(t, "198.51.100.1", leftmostForwarded("198.51.100.1, 10.0.0.9, 10.0.0.10"))
	assert.Equal(t, "198.51.100.1", leftmostForwarded("  198.51.100.1  "))
	assert.Empty(t, leftmostForwarded(""))
}

func TestHostFromAddr(t *testing.T) {
	assert.Equal(t, "203.0.113.7", hostFromAddr("203.0.113.7:41234"))
	assert.Equal(t, "2001:db8::1", hostFromAddr("[2001:db8::1]:41234"))
	assert.Equal(t, "203.0.113.7", hostFromAddr("203.0.113.7"))
	assert.Empty(t, hostFromAddr(""))
}

// TestHashRequestMatchesHash proves the net/http entry point is the same
// construction as the transport-neutral one, not a parallel implementation.
func TestHashRequestMatchesHash(t *testing.T) {
	h := NewHasher(Config{HashKey: []byte(testSecret)})

	r := httptest.NewRequest(http.MethodPost, "/forgot-password", nil)
	r.RemoteAddr = "203.0.113.7:41234"

	assert.Equal(t, h.Hash("", "203.0.113.7:41234"), h.HashRequest(r))
	assert.NotEmpty(t, h.HashRequest(r))
}

// TestHashRequestForgedHeaderIgnored proves an untrusted forwarding header
// cannot let a caller choose its own recorded source address.
func TestHashRequestForgedHeaderIgnored(t *testing.T) {
	h := NewHasher(Config{HashKey: []byte(testSecret)})

	forge := func(header string) string {
		r := httptest.NewRequest(http.MethodPost, "/forgot-password", nil)
		r.RemoteAddr = "203.0.113.7:41234"
		r.Header.Set("X-Forwarded-For", header)
		return h.HashRequest(r)
	}

	assert.Equal(t, forge("198.51.100.1"), forge("198.51.100.2"),
		"an unconfigured forwarding header must not change the recorded source")
}

// TestHashRequestTrustedHeaderHonored proves the header IS used once the
// deployment declares it trusted.
func TestHashRequestTrustedHeaderHonored(t *testing.T) {
	h := NewHasher(Config{HashKey: []byte(testSecret), TrustedProxyHeader: "X-Forwarded-For"})

	withHeader := func(v string) string {
		r := httptest.NewRequest(http.MethodPost, "/forgot-password", nil)
		r.RemoteAddr = "10.0.0.9:41234"
		r.Header.Set("X-Forwarded-For", v)
		return h.HashRequest(r)
	}

	assert.NotEqual(t, withHeader("198.51.100.1"), withHeader("198.51.100.2"))
	// The leftmost entry is the original client.
	assert.Equal(t, withHeader("198.51.100.1"), withHeader("198.51.100.1, 10.0.0.9"))
	// A garbage header falls back to the peer address rather than inventing an
	// identity from it.
	assert.Equal(t, h.Hash("", "10.0.0.9:41234"), withHeader("not-an-address"))
}

// TestHashRequestNilAndUnkeyed proves the empty cases stay empty rather than
// becoming a fake grouping key.
func TestHashRequestNilAndUnkeyed(t *testing.T) {
	keyed := NewHasher(Config{HashKey: []byte(testSecret)})
	assert.Empty(t, keyed.HashRequest(nil))

	r := httptest.NewRequest(http.MethodPost, "/forgot-password", nil)
	r.RemoteAddr = "203.0.113.7:41234"
	assert.Empty(t, Hasher{}.HashRequest(r), "the zero hasher records nothing")
	assert.Empty(t, NewHasher(Config{}).HashRequest(r),
		"without a secret the hash would be reversible, so none is stored")

	r.RemoteAddr = ""
	assert.Empty(t, keyed.HashRequest(r), "no address means no hash")
}

// TestContextRoundTrip proves both transports read one value through one key.
func TestContextRoundTrip(t *testing.T) {
	ctx := WithHash(context.Background(), "abc123")
	assert.Equal(t, "abc123", HashFrom(ctx))
	assert.Empty(t, HashFrom(context.Background()))

	// The exported key is the same one WithHash uses, which is what lets the
	// Huma middleware store a value this package's reader can find.
	ctx = context.WithValue(context.Background(), ContextKey(), "via-key")
	require.Equal(t, "via-key", HashFrom(ctx))
}
