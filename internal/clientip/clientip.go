// Package clientip resolves and hashes the client address behind a request.
//
// It lives in its own package because two transports need the identical
// construction: the Huma API at /api/v1 and the server-rendered admin UI. When
// this logic lived in internal/httpapi, the admin UI could not reach it without
// an import cycle, so the UI passed an empty hash instead and every
// UI-initiated recovery stored NULL (bcars-portal-fmc.21). Two places obliged
// to agree, with nothing forcing them to, is how that gap opened; one package
// both call is what closes it.
//
// # KEYING DECISION
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
// is disabled and the stored value is empty. An empty hash is honest about
// knowing nothing; a bare or randomised one is not.
package clientip

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// hashLabel domain-separates the client-address subkey from every other use of
// the pepper.
const hashLabel = "bcars-portal/client-ip-hash/v1"

// hashBytes is the truncated HMAC length stored, in bytes. 16 bytes is far past
// any collision concern for this population and keeps the column narrow.
const hashBytes = 16

// ctxKey keys the resolved hash in the request context. One key, shared by
// every transport, so a handler reads the same value however it was reached.
type ctxKey struct{}

// Config configures how the client address is obtained and hashed.
type Config struct {
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

// Hasher resolves and hashes client addresses for one configuration. The zero
// value is usable and hashes nothing, which is the correct behaviour for a
// deployment with no configured secret.
type Hasher struct {
	key    []byte
	header string
}

// NewHasher derives the subkey once, so per-request work is a single HMAC.
func NewHasher(cfg Config) Hasher {
	return Hasher{
		key:    deriveKey(cfg.HashKey),
		header: strings.TrimSpace(cfg.TrustedProxyHeader),
	}
}

// Header returns the configured trusted forwarding header, or "" when none is
// trusted. Transports use it to read the right header from their own request
// representation.
func (h Hasher) Header() string { return h.header }

// Hash returns the keyed hash for a request whose trusted forwarding header
// carries forwarded (empty when unset or untrusted) and whose transport peer
// address is remoteAddr. Returns "" when no address is available or hashing is
// not configured.
//
// This is the single construction. Every transport funnels into it, so the API
// and the admin UI cannot drift into recording different values for the same
// caller.
func (h Hasher) Hash(forwarded, remoteAddr string) string {
	if h.header != "" {
		if ip := normalizeIP(leftmostForwarded(forwarded)); ip != "" {
			return h.hashIP(ip)
		}
	}
	return h.hashIP(normalizeIP(hostFromAddr(remoteAddr)))
}

// HashRequest is the net/http entry point, for the admin UI and any other
// handler holding a *http.Request.
func (h Hasher) HashRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	var forwarded string
	if h.header != "" {
		forwarded = r.Header.Get(h.header)
	}
	return h.Hash(forwarded, r.RemoteAddr)
}

// hashIP returns the keyed hash of ip, or "" when either the key or the address
// is missing.
func (h Hasher) hashIP(ip string) string {
	if len(h.key) == 0 || ip == "" {
		return ""
	}
	mac := hmac.New(sha256.New, h.key)
	mac.Write([]byte(ip))
	return hex.EncodeToString(mac.Sum(nil)[:hashBytes])
}

// ContextKey returns the opaque key under which the hash is stored in a request
// context. Transports that build their context through a foreign helper — Huma
// stores values via huma.WithValue — need the key itself; everything else
// should use WithHash and HashFrom.
func ContextKey() any { return ctxKey{} }

// WithHash returns ctx carrying the resolved hash.
func WithHash(ctx context.Context, hash string) context.Context {
	return context.WithValue(ctx, ctxKey{}, hash)
}

// HashFrom returns the hashed client address for the current request, or ""
// when no address was available or hashing is not configured.
//
// Callers must treat "" as "unknown" and store NULL. It is never a usable
// grouping key, which is the point: a value that is unique per call, as an
// earlier implementation produced by hashing a timestamp, looks like a working
// rate-limit key while grouping nothing.
func HashFrom(ctx context.Context) string {
	h, _ := ctx.Value(ctxKey{}).(string)
	return h
}

// deriveKey turns a configured secret into the client-address subkey. Returns
// nil for an empty secret, which disables hashing.
func deriveKey(secret []byte) []byte {
	if len(secret) == 0 {
		return nil
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(hashLabel))
	return mac.Sum(nil)
}

// leftmostForwarded returns the first entry of a comma-separated forwarding
// header value. The leftmost entry is the original client; entries to its right
// are the proxies it passed through.
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
// address it represents, and a zone identifier is dropped. Anything that is not
// a valid IP — including a hostname a forwarding header might carry — yields
// "", because an unparseable value must not become a fake identity.
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
