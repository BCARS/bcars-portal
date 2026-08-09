package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testIPSecret = "client-ip-test-secret-value-32b!"

// hashFor drives the middleware over a synthetic request and returns the hash
// the handler would see. It goes through the real huma adapter so the test
// covers the actual RemoteAddr plumbing rather than a stand-in for it.
func hashFor(t *testing.T, cfg ClientIPConfig, remoteAddr string, headers map[string]string) string {
	t.Helper()

	r := httptest.NewRequest(http.MethodGet, "/api/v1/anything", nil)
	r.RemoteAddr = remoteAddr
	for k, v := range headers {
		r.Header.Set(k, v)
	}

	var got string
	var seen bool
	mux := http.NewServeMux()
	api := humago.NewWithPrefix(mux, "/api/v1", huma.DefaultConfig("test", "test"))
	api.UseMiddleware(ClientIPMiddleware(cfg))
	Register(api, huma.Operation{
		OperationID: "clientip-test-probe",
		Method:      http.MethodGet,
		Path:        "/anything",
	}, OperationMeta{
		RequiredCapability: PublicCapability,
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, _ *struct{}) (*struct{}, error) {
		got, seen = ClientIPHashFrom(ctx), true
		return &struct{}{}, nil
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	require.Less(t, rec.Code, 300, "probe request failed: %s", rec.Body.String())
	require.True(t, seen, "handler did not run")
	return got
}

func TestClientIPHash_StableForSameAddress(t *testing.T) {
	cfg := ClientIPConfig{HashKey: []byte(testIPSecret)}

	first := hashFor(t, cfg, "203.0.113.7:41234", nil)
	// A different ephemeral port is still the same source.
	second := hashFor(t, cfg, "203.0.113.7:55001", nil)

	assert.NotEmpty(t, first, "a real address must produce a hash")
	assert.Equal(t, first, second, "the same address must hash identically across requests")
	assert.NotContains(t, first, "203.0.113.7", "the address must not appear in its own hash")
}

func TestClientIPHash_DiffersByAddress(t *testing.T) {
	cfg := ClientIPConfig{HashKey: []byte(testIPSecret)}

	a := hashFor(t, cfg, "203.0.113.7:41234", nil)
	b := hashFor(t, cfg, "203.0.113.8:41234", nil)
	v6 := hashFor(t, cfg, "[2001:db8::1]:41234", nil)

	assert.NotEqual(t, a, b, "different addresses must hash differently")
	assert.NotEqual(t, a, v6)
	assert.NotEmpty(t, v6, "IPv6 sources must hash too")
}

// TestClientIPHash_KeyedNotBare is the regression that keeps the construction
// keyed: with a bare digest the hash would not depend on the secret, and an
// attacker holding the database could enumerate the whole IPv4 space.
func TestClientIPHash_KeyedNotBare(t *testing.T) {
	one := hashFor(t, ClientIPConfig{HashKey: []byte(testIPSecret)}, "203.0.113.7:1234", nil)
	two := hashFor(t, ClientIPConfig{HashKey: []byte("a-different-secret-of-length-32!")}, "203.0.113.7:1234", nil)

	assert.NotEqual(t, one, two, "the hash must depend on the configured secret")
}

// TestClientIPHash_ForgedHeaderIgnored is the core anti-forgery property: with
// no trusted-proxy header configured, a client that sets X-Forwarded-For is
// still recorded under its real transport address.
func TestClientIPHash_ForgedHeaderIgnored(t *testing.T) {
	cfg := ClientIPConfig{HashKey: []byte(testIPSecret)}

	real := hashFor(t, cfg, "203.0.113.7:41234", nil)
	forged := hashFor(t, cfg, "203.0.113.7:41234", map[string]string{
		"X-Forwarded-For": "198.51.100.1",
	})
	assert.Equal(t, real, forged, "a forwarding header must be ignored unless configured")

	// Two different forged values must not let one client masquerade as two
	// sources, which is what would defeat per-source rate limiting.
	forgedTwo := hashFor(t, cfg, "203.0.113.7:41234", map[string]string{
		"X-Forwarded-For": "198.51.100.2",
	})
	assert.Equal(t, forged, forgedTwo)
}

func TestClientIPHash_TrustedHeaderHonoredWhenConfigured(t *testing.T) {
	cfg := ClientIPConfig{HashKey: []byte(testIPSecret), TrustedProxyHeader: "X-Forwarded-For"}

	// Leftmost entry wins: it is the original client, the rest are proxies.
	viaProxy := hashFor(t, cfg, "10.0.0.1:5000", map[string]string{
		"X-Forwarded-For": "198.51.100.1, 10.0.0.9",
	})
	direct := hashFor(t, ClientIPConfig{HashKey: []byte(testIPSecret)}, "198.51.100.1:9999", nil)

	assert.Equal(t, direct, viaProxy,
		"a forwarded client must hash the same as the same client seen directly")

	// A garbage header value falls back to the peer address rather than
	// inventing an identity.
	garbage := hashFor(t, cfg, "10.0.0.1:5000", map[string]string{
		"X-Forwarded-For": "not-an-ip",
	})
	peer := hashFor(t, ClientIPConfig{HashKey: []byte(testIPSecret)}, "10.0.0.1:5000", nil)
	assert.Equal(t, peer, garbage)
}

func TestClientIPHash_EmptyWhenAddressUnavailable(t *testing.T) {
	cfg := ClientIPConfig{HashKey: []byte(testIPSecret)}

	assert.Empty(t, hashFor(t, cfg, "", nil),
		"no address must yield an empty hash, not a fabricated one")
	assert.Empty(t, hashFor(t, cfg, "not-an-address", nil))
}

func TestClientIPHash_EmptyWhenUnkeyed(t *testing.T) {
	assert.Empty(t, hashFor(t, ClientIPConfig{}, "203.0.113.7:41234", nil),
		"without a secret the hash would be reversible, so none is stored")
}
