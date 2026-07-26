package authn

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHashAndVerify(t *testing.T) {
	pepper := []byte("test-pepper-32-bytes-exactly!!!!!")
	params := DefaultParams()

	hash, err := HashPassword("correct-horse-battery-staple", pepper, params)
	require.NoError(t, err)

	// Correct password verifies.
	ok, err := VerifyPassword("correct-horse-battery-staple", hash, pepper)
	require.NoError(t, err)
	assert.True(t, ok, "correct password should verify")

	// Wrong password fails.
	ok, err = VerifyPassword("wrong-password", hash, pepper)
	require.NoError(t, err)
	assert.False(t, ok, "wrong password should not verify")
}

func TestHashFormat(t *testing.T) {
	hash, err := HashPassword("pw", nil, DefaultParams())
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(hash, "$argon2id$v=19$"), "should be PHC format, got: %s", hash)
	assert.Equal(t, 6, len(strings.Split(hash, "$")), "PHC format has 6 $-separated parts")
}

func TestDifferentSalts(t *testing.T) {
	params := DefaultParams()
	h1, err := HashPassword("same", nil, params)
	require.NoError(t, err)
	h2, err := HashPassword("same", nil, params)
	require.NoError(t, err)
	assert.NotEqual(t, h1, h2, "same password should produce different hashes (random salt)")
}

func TestPepperChangeFails(t *testing.T) {
	pepper1 := []byte("pepper-one-32-bytes!!!!!!!!!!!!!!")
	pepper2 := []byte("pepper-two-32-bytes!!!!!!!!!!!!!!")

	hash, err := HashPassword("pw", pepper1, DefaultParams())
	require.NoError(t, err)

	ok, err := VerifyPassword("pw", hash, pepper2)
	require.NoError(t, err)
	assert.False(t, ok, "wrong pepper should fail verification")
}

func TestNoPepper(t *testing.T) {
	hash, err := HashPassword("pw", nil, DefaultParams())
	require.NoError(t, err)

	ok, err := VerifyPassword("pw", hash, nil)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestNeedsRehash(t *testing.T) {
	params := DefaultParams()
	hash, err := HashPassword("pw", nil, params)
	require.NoError(t, err)

	// Same params → no rehash needed.
	needs, err := NeedsRehash(hash, params)
	require.NoError(t, err)
	assert.False(t, needs)

	// Different params → rehash needed.
	newParams := params
	newParams.Memory = 32 * 1024
	needs, err = NeedsRehash(hash, newParams)
	require.NoError(t, err)
	assert.True(t, needs)
}

func TestInvalidHash(t *testing.T) {
	_, err := VerifyPassword("pw", "not-a-hash", nil)
	assert.ErrorIs(t, err, ErrInvalidHash)

	_, err = VerifyPassword("pw", "$bcrypt$v=19$m=19456,t=2,p=1$salt$hash", nil)
	assert.ErrorIs(t, err, ErrInvalidHash)
}

func TestEmptyPassword(t *testing.T) {
	hash, err := HashPassword("", nil, DefaultParams())
	require.NoError(t, err)

	ok, err := VerifyPassword("", hash, nil)
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = VerifyPassword("not-empty", hash, nil)
	require.NoError(t, err)
	assert.False(t, ok)
}
