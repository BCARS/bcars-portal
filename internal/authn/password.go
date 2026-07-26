// Package authn provides password hashing, session management, and
// authentication primitives.
package authn

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters. Stored alongside each hash so they can be rotated
// without breaking existing passwords.
type Params struct {
	Memory      uint32 `json:"memory"`
	Iterations  uint32 `json:"iterations"`
	Parallelism uint8  `json:"parallelism"`
	SaltLength  uint32 `json:"salt_length"`
	KeyLength   uint32 `json:"key_length"`
}

// DefaultParams returns secure defaults for argon2id.
// OWASP recommended: 19 MiB, 2 iterations, 1 thread.
func DefaultParams() Params {
	return Params{
		Memory:      19 * 1024, // 19 MiB
		Iterations:  2,
		Parallelism: 1,
		SaltLength:  16,
		KeyLength:   32,
	}
}

// HashPassword creates an argon2id hash of password with the given pepper.
// The returned string is the PHC format:
// $argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>
func HashPassword(password string, pepper []byte, params Params) (string, error) {
	salt := make([]byte, params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("authn: generate salt: %w", err)
	}

	peppered := applyPepper([]byte(password), pepper)
	hash := argon2.IDKey(peppered, salt, params.Iterations, params.Memory, params.Parallelism, params.KeyLength)

	b64Salt := base64.RawStdEncoding.EncodeToString(salt)
	b64Hash := base64.RawStdEncoding.EncodeToString(hash)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, params.Memory, params.Iterations, params.Parallelism,
		b64Salt, b64Hash), nil
}

// VerifyPassword checks password against a PHC-format argon2id hash.
// Uses constant-time comparison to prevent timing attacks.
func VerifyPassword(password string, encodedHash string, pepper []byte) (bool, error) {
	params, salt, hash, err := decodeHash(encodedHash)
	if err != nil {
		return false, err
	}

	peppered := applyPepper([]byte(password), pepper)
	otherHash := argon2.IDKey(peppered, salt, params.Iterations, params.Memory, params.Parallelism, params.KeyLength)

	return subtle.ConstantTimeCompare(hash, otherHash) == 1, nil
}

// NeedsRehash returns true if the hash was created with different parameters
// than the provided ones (indicating a rotation is needed).
func NeedsRehash(encodedHash string, desired Params) (bool, error) {
	params, _, _, err := decodeHash(encodedHash)
	if err != nil {
		return false, err
	}
	return params.Memory != desired.Memory ||
		params.Iterations != desired.Iterations ||
		params.Parallelism != desired.Parallelism ||
		params.KeyLength != desired.KeyLength, nil
}

func applyPepper(password, pepper []byte) []byte {
	if len(pepper) == 0 {
		return password
	}
	out := make([]byte, len(password)+len(pepper))
	copy(out, password)
	copy(out[len(password):], pepper)
	return out
}

var (
	ErrInvalidHash = errors.New("authn: invalid argon2id hash format")
)

func decodeHash(encodedHash string) (Params, []byte, []byte, error) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 {
		return Params{}, nil, nil, ErrInvalidHash
	}

	if parts[1] != "argon2id" {
		return Params{}, nil, nil, fmt.Errorf("%w: unsupported algorithm %q", ErrInvalidHash, parts[1])
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return Params{}, nil, nil, fmt.Errorf("%w: version: %v", ErrInvalidHash, err)
	}

	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Iterations, &p.Parallelism); err != nil {
		return Params{}, nil, nil, fmt.Errorf("%w: params: %v", ErrInvalidHash, err)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return Params{}, nil, nil, fmt.Errorf("%w: salt: %v", ErrInvalidHash, err)
	}
	p.SaltLength = uint32(len(salt))

	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return Params{}, nil, nil, fmt.Errorf("%w: hash: %v", ErrInvalidHash, err)
	}
	p.KeyLength = uint32(len(hash))

	return p, salt, hash, nil
}
