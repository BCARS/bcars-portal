package authn

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

// PepperEnvVar is the only supported source for the password pepper. It is an
// environment variable rather than a flag so it does not appear in the process
// table, and it is never written to a log or an error message.
const PepperEnvVar = "PORTAL_PASSWORD_PEPPER"

// MinPepperLength is the shortest pepper accepted. A pepper defends a stolen
// database, so a guessable one defends nothing.
const MinPepperLength = 16

// pepperFingerprintKey is the app_settings row holding the fingerprint.
const pepperFingerprintKey = "password_pepper_fingerprint"

// fingerprintLabel domain-separates the fingerprint from any other use of the
// pepper. The fingerprint is an HMAC rather than a bare hash of the pepper so
// that a database reader cannot test candidate peppers any faster than they
// could test candidate passwords.
const fingerprintLabel = "bcars-portal/password-pepper-fingerprint/v1"

var (
	// ErrPepperMissing is returned when no pepper is configured and the caller
	// has not explicitly opted out.
	ErrPepperMissing = errors.New("authn: no password pepper configured")

	// ErrPepperTooShort is returned for a configured but inadequate pepper.
	ErrPepperTooShort = fmt.Errorf("authn: password pepper must be at least %d bytes", MinPepperLength)

	// ErrPepperChanged is returned when the configured pepper does not match
	// the one this database's password hashes were created with.
	ErrPepperChanged = errors.New("authn: configured password pepper does not match this database")
)

// PepperFingerprint returns the stable fingerprint of a pepper.
func PepperFingerprint(pepper []byte) string {
	mac := hmac.New(sha256.New, pepper)
	mac.Write([]byte(fingerprintLabel))
	return hex.EncodeToString(mac.Sum(nil))
}

// ValidatePepper checks a configured pepper before it is used.
//
// allowEmpty exists for development and tests. It is an explicit opt-out
// rather than a default so that forgetting to configure a pepper in production
// fails at startup instead of silently producing unpeppered hashes that look
// exactly like peppered ones.
func ValidatePepper(pepper []byte, allowEmpty bool) error {
	if len(pepper) == 0 {
		if allowEmpty {
			return nil
		}
		return ErrPepperMissing
	}
	if len(pepper) < MinPepperLength {
		return ErrPepperTooShort
	}
	return nil
}

// BindPepper records the pepper's fingerprint on first use and verifies it on
// every subsequent start.
//
// A pepper that is changed or lost does not fail loudly on its own: Argon2id
// hashes carry no record of the pepper that made them, so every password
// verification simply returns "invalid credentials" — for every account, with
// a message that reads like user error. This turns that into a refusal to
// start. It is called before the server accepts traffic.
func BindPepper(db *sql.DB, pepper []byte) error {
	want := PepperFingerprint(pepper)

	var have string
	err := db.QueryRow(`SELECT value FROM app_settings WHERE key = ?`, pepperFingerprintKey).Scan(&have)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// First start against this database: record it.
		_, err = db.Exec(
			`INSERT INTO app_settings (key, value) VALUES (?, ?)`,
			pepperFingerprintKey, want,
		)
		if err != nil {
			return fmt.Errorf("authn: record pepper fingerprint: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("authn: read pepper fingerprint: %w", err)
	}

	if !hmac.Equal([]byte(have), []byte(want)) {
		// Deliberately says nothing about either pepper's value.
		return ErrPepperChanged
	}
	return nil
}
