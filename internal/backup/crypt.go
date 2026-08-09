// Package backup encrypts and decrypts portal backup artifacts.
//
// A backup contains the entire member roster, so an unencrypted copy on a
// laptop or a cloud drive is a roster disclosure waiting to happen. Encryption
// is applied by the tool rather than left to the operator's storage, because
// "remember to encrypt it" is not a control.
package backup

import (
	"errors"
	"fmt"
	"io"
	"os"

	"filippo.io/age"
)

// PassphraseEnvVar is the only supported source for the backup passphrase.
// It is an environment variable rather than a flag so it stays out of the
// process table and shell history, matching how the SMTP password and the
// password pepper are supplied.
const PassphraseEnvVar = "PORTAL_BACKUP_PASSPHRASE"

// MinPassphraseLength is the shortest passphrase accepted. The passphrase is
// the only thing standing between a stolen backup file and the full roster.
const MinPassphraseLength = 12

// Extension is appended to encrypted backup artifacts.
const Extension = ".age"

var (
	// ErrNoPassphrase is returned when no passphrase is configured.
	ErrNoPassphrase = fmt.Errorf("backup: %s is not set", PassphraseEnvVar)

	// ErrPassphraseTooShort is returned for a configured but weak passphrase.
	ErrPassphraseTooShort = fmt.Errorf("backup: passphrase must be at least %d characters", MinPassphraseLength)

	// ErrDecrypt is returned when a backup cannot be decrypted, most often
	// because the passphrase is wrong or the file is not a backup.
	ErrDecrypt = errors.New("backup: cannot decrypt (wrong passphrase or corrupted file)")
)

// Passphrase reads and validates the configured passphrase.
func Passphrase() (string, error) {
	p := os.Getenv(PassphraseEnvVar)
	if p == "" {
		return "", ErrNoPassphrase
	}
	if len(p) < MinPassphraseLength {
		return "", ErrPassphraseTooShort
	}
	return p, nil
}

// EncryptFile writes an age-encrypted copy of src to dst.
func EncryptFile(src, dst, passphrase string) error {
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return fmt.Errorf("backup: prepare encryption: %w", err)
	}

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("backup: open plaintext: %w", err)
	}
	defer in.Close()

	// 0600: a backup is roster data even when encrypted.
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("backup: create ciphertext: %w", err)
	}
	defer out.Close()

	w, err := age.Encrypt(out, recipient)
	if err != nil {
		return fmt.Errorf("backup: start encryption: %w", err)
	}
	if _, err := io.Copy(w, in); err != nil {
		return fmt.Errorf("backup: encrypt: %w", err)
	}
	// Closing the age writer flushes the final chunk and its authentication
	// tag. Skipping it produces a file that looks written and cannot be read.
	if err := w.Close(); err != nil {
		return fmt.Errorf("backup: finalize encryption: %w", err)
	}
	return out.Close()
}

// DecryptFile writes the decrypted contents of src to dst.
func DecryptFile(src, dst, passphrase string) error {
	identity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return fmt.Errorf("backup: prepare decryption: %w", err)
	}

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("backup: open ciphertext: %w", err)
	}
	defer in.Close()

	r, err := age.Decrypt(in, identity)
	if err != nil {
		// age distinguishes several failures here; they all mean the same
		// thing operationally and none of them should hint at the passphrase.
		return ErrDecrypt
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("backup: create plaintext: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, r); err != nil {
		// A truncated or tampered payload fails here, at the authentication
		// tag, rather than at Decrypt.
		_ = os.Remove(dst)
		return ErrDecrypt
	}
	return out.Close()
}
