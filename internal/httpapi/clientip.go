package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/netip"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// Client-address handling lives in its own file rather than in a session
// endpoint: the address belongs to the request, not to any one operation, and
// the previous placement is what let it degenerate into hashing a timestamp.
//
// KEYING DECISION
//
// The hash is an HMAC, not a bare digest. A bare SHA-256 of an IPv4 address is
// reversible by anyone willing to enumerate 2^32 inputs — about a second of
// work — so a bare hash in the database is a plaintext address wearing a hat.
// IPv6 is only marginally better once the /64 prefix is guessable.
//
// The key is DERIVED from the existing password pepper rather than read from a
// fourth secret environment variable, for three reasons:
//
//  1. Both secrets have exactly the same threat model (defend a stolen
//     database) and exactly the same lifecycle (set once, never rotated —
//     authn.BindPepper already refuses to start if the pepper changes). A
//     second secret with identical properties is a second thing to lose.
//  2. Every additional required secret is another way for a deployment to
//     start misconfigured, and this one would fail silently rather than
//     loudly.
//  3. Domain separation makes the reuse safe: the HMAC label below derives a
//     subkey that cannot be used to test candidate passwords, and a password
//     hash cannot be used to test candidate addresses.
//
// The cost is that the two are bound together — rotating the pepper would
// invalidate historical IP correlation. That is already true of every password
// hash in the database, so it changes nothing operationally.
//
// If no secret is configured (development, via -allow-empty-pepper), hashing
// is disabled and the stored value is empty. An empty requested_ip_hash is
// honest about knowing nothing; a bare or randomised one is not.

// clientIPHashLabel domain-separates the client-address subkey from every
// other use of the pepper.
const clientIPHashLabel = "bcars-portal/client-ip-hash/v1"

// clientIPHashBytes is the truncated HMAC length stored, in bytes. 16 bytes is
// far past any collision concern for this population and keeps the column
// narrow.
const clientIPHashBytes = 16

// clientIPCtxKey keys the resolved hash in the request context.
type clientIPCtxKey struct{}

// ClientIPConfig configures how the client address is obtained and hashed.
type ClientIPConfig struct {
	// TrustedProxyHeader names a request header carrying the original client
	// address, e.g. "X-Forwarded-For". The leftmost entry is used.
	//
	// Empty — the default — means the header is ignored entirely. This is
	// deliberate: a forwarding header is client-controlled input on any
	// deployment that is not actually behind a proxy that overwrites it, so
	// honouring it by default would let every caller choose its own recorded
	// source address and defeat the rate limiting this value exists to feed.
	TrustedProxyHeader string

	// HashKey is the secret the HMAC key is derived from. Empty disables
	// hashing; see the keying note above.
	HashKey []byte
}

// deriveClientIPKey turns a configured secret into the client-address subkey.
// Returns nil for an empty secret, which disables hashing.
func deriveClientIPKey(secret []byte) []byte {
	if len(secret) == 0 {
		return nil
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(clientIPHashLabel))
	return mac.Sum(nil)
}

// ClientIPMiddleware resolves the client address for every API request and
// stores its hash in the request context, where handlers read it with
// ClientIPHashFrom.
//
// It runs in the middleware layer because that is the only place with access
// to the request; handlers receive a bare context.Context.
func ClientIPMiddleware(cfg ClientIPConfig) func(huma.Context, func(huma.Context)) {
	key := deriveClientIPKey(cfg.HashKey)
	header := strings.TrimSpace(cfg.TrustedProxyHeader)
	return func(ctx huma.Context, next func(huma.Context)) {
		hash := hashClientIP(key, resolveClientIP(ctx, header))
		next(huma.WithValue(ctx, clientIPCtxKey{}, hash))
	}
}

// ClientIPHashFrom returns the hashed client address for the current request,
// or "" when no address was available or hashing is not configured.
//
// Callers must treat "" as "unknown" and store NULL. It is never a usable
// grouping key, which is the point: a value that is unique per call, as the
// previous implementation produced, looks like a working rate-limit key while
// grouping nothing.
func ClientIPHashFrom(ctx context.Context) string {
	h, _ := ctx.Value(clientIPCtxKey{}).(string)
	return h
}

// resolveClientIP returns the canonical client address as a string, or "" if
// it cannot be determined. header, when non-empty, names a trusted forwarding
// header that takes precedence over the transport peer address.
func resolveClientIP(ctx huma.Context, header string) string {
	if header != "" {
		if ip := normalizeIP(leftmostForwarded(ctx.Header(header))); ip != "" {
			return ip
		}
	}
	return normalizeIP(hostFromAddr(ctx.RemoteAddr()))
}

// leftmostForwarded returns the first entry of a comma-separated forwarding
// header value. The leftmost entry is the original client; entries to its
// right are the proxies it passed through.
func leftmostForwarded(v string) string {
	if v == "" {
		return ""
	}
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// hostFromAddr strips the port from a "host:port" remote address. A value with
// no port is returned unchanged so a bare address still works.
func hostFromAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// normalizeIP canonicalises an address so that equal sources always produce
// equal hashes: an IPv4-mapped IPv6 address hashes identically to the IPv4
// address it represents, and a zone identifier is dropped. Anything that is
// not a valid IP — including a hostname a forwarding header might carry —
// yields "", because an unparseable value must not become a fake identity.
func normalizeIP(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Some proxies emit bracketed IPv6 without a port.
	s = strings.TrimPrefix(strings.TrimSuffix(s, "]"), "[")
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return ""
	}
	return addr.Unmap().WithZone("").String()
}

// hashClientIP returns the keyed hash of ip, or "" when either the key or the
// address is missing.
func hashClientIP(key []byte, ip string) string {
	if len(key) == 0 || ip == "" {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(ip))
	return hex.EncodeToString(mac.Sum(nil)[:clientIPHashBytes])
}
