package authn

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidatePepper(t *testing.T) {
	long := []byte(strings.Repeat("x", MinPepperLength))

	t.Run("missing is refused by default", func(t *testing.T) {
		assert.ErrorIs(t, ValidatePepper(nil, false), ErrPepperMissing)
	})
	t.Run("missing is allowed only on explicit opt-out", func(t *testing.T) {
		assert.NoError(t, ValidatePepper(nil, true))
	})
	t.Run("short is refused even with the opt-out", func(t *testing.T) {
		assert.ErrorIs(t, ValidatePepper([]byte("tooshort"), true), ErrPepperTooShort)
	})
	t.Run("adequate is accepted", func(t *testing.T) {
		assert.NoError(t, ValidatePepper(long, false))
	})
}

func TestPepperFingerprintIsStableAndDistinct(t *testing.T) {
	a := PepperFingerprint([]byte("pepper-value-one-long-enough"))
	b := PepperFingerprint([]byte("pepper-value-one-long-enough"))
	c := PepperFingerprint([]byte("pepper-value-two-long-enough"))

	assert.Equal(t, a, b, "the same pepper must fingerprint identically")
	assert.NotEqual(t, a, c)
	assert.NotContains(t, a, "pepper-value", "the fingerprint must not reveal the pepper")
}

// TestBindPepperDetectsChange is the point of the fingerprint: without it, a
// changed pepper makes every sign-in fail as "invalid credentials" with no
// indication that the configuration is at fault.
func TestBindPepperDetectsChange(t *testing.T) {
	env := setupEnv(t)
	original := []byte("original-pepper-long-enough-ok")

	require.NoError(t, BindPepper(env.db, original), "first bind records the fingerprint")
	require.NoError(t, BindPepper(env.db, original), "rebinding the same pepper is fine")

	err := BindPepper(env.db, []byte("a-different-pepper-long-enough"))
	require.ErrorIs(t, err, ErrPepperChanged)
	assert.NotContains(t, err.Error(), "pepper-long-enough",
		"the error must not disclose either pepper")
}

func TestBindPepperTreatsEmptyAsItsOwnIdentity(t *testing.T) {
	env := setupEnv(t)

	// A database created without a pepper must not silently accept one later:
	// the existing hashes would all stop verifying.
	require.NoError(t, BindPepper(env.db, nil))
	assert.ErrorIs(t, BindPepper(env.db, []byte("newly-added-pepper-long-enough")), ErrPepperChanged)
}

// TestPepperChangesTheHash confirms the pepper actually participates in
// hashing, so the fingerprint guard is protecting something real.
func TestPepperChangesTheHash(t *testing.T) {
	pepper := []byte("a-real-pepper-long-enough-yes")

	hash, err := HashPassword("hunter2hunter2", pepper, DefaultParams())
	require.NoError(t, err)

	ok, err := VerifyPassword("hunter2hunter2", hash, pepper)
	require.NoError(t, err)
	assert.True(t, ok, "the correct pepper must verify")

	ok, err = VerifyPassword("hunter2hunter2", hash, nil)
	require.NoError(t, err)
	assert.False(t, ok, "the wrong pepper must not verify")
}
