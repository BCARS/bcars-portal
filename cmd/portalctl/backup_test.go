package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bcars/bcars-portal/internal/backup"
)

const testPassphrase = "a-sufficiently-long-backup-passphrase"

// seededDB returns a migrated database file on disk with one recognisable row,
// so a restore can be shown to have recovered actual content rather than just
// a well-formed empty database.
func seededDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "source.db")

	d := newMigratedDBAt(t, path)
	_, err := d.Exec(`INSERT INTO users (email, password_hash, is_active) VALUES ('restore-marker@bcars.org', 'x', 1)`)
	require.NoError(t, err)
	require.NoError(t, d.Close())
	return path
}

func withPassphrase(t *testing.T, value string) {
	t.Helper()
	t.Setenv(backup.PassphraseEnvVar, value)
}

// backupOnce runs a backup and returns the artifact and manifest paths.
func backupOnce(t *testing.T, dbPath string) (artifact, manifest string) {
	t.Helper()
	toDir := t.TempDir()
	captureStdout(t, func() {
		require.NoError(t, runBackup([]string{"--db", dbPath, "--to", toDir}))
	})

	entries, err := os.ReadDir(toDir)
	require.NoError(t, err)
	for _, e := range entries {
		switch {
		case strings.HasSuffix(e.Name(), ".manifest.json"):
			manifest = filepath.Join(toDir, e.Name())
		case strings.HasSuffix(e.Name(), backup.Extension):
			artifact = filepath.Join(toDir, e.Name())
		}
	}
	require.NotEmpty(t, artifact, "no encrypted artifact produced")
	require.NotEmpty(t, manifest, "no manifest produced")
	return artifact, manifest
}

// TestBackupRestoreRoundTrip is the acceptance path.
func TestBackupRestoreRoundTrip(t *testing.T) {
	withPassphrase(t, testPassphrase)
	src := seededDB(t)

	artifact, _ := backupOnce(t, src)
	intoDir := filepath.Join(t.TempDir(), "restored")

	captureStdout(t, func() {
		require.NoError(t, runRestore([]string{"--from", artifact, "--into", intoDir}))
	})

	d := openExisting(t, filepath.Join(intoDir, "portal.db"))
	var email string
	require.NoError(t, d.QueryRow(`SELECT email FROM users WHERE id = 1`).Scan(&email))
	assert.Equal(t, "restore-marker@bcars.org", email, "the restore must recover actual content")
}

// TestBackupIsNotReadableWithoutThePassphrase is the point of the whole bead:
// the artifact must not be a plain SQLite file.
func TestBackupIsNotReadableWithoutThePassphrase(t *testing.T) {
	withPassphrase(t, testPassphrase)
	src := seededDB(t)

	artifact, _ := backupOnce(t, src)

	raw, err := os.ReadFile(artifact)
	require.NoError(t, err)
	assert.NotContains(t, string(raw[:min(16, len(raw))]), "SQLite format",
		"the artifact must not be a readable SQLite database")
	assert.NotContains(t, string(raw), "restore-marker@bcars.org",
		"member data must not appear in the encrypted artifact")
}

// TestBackupLeavesNoPlaintext guards the temporary VACUUM INTO output.
func TestBackupLeavesNoPlaintext(t *testing.T) {
	withPassphrase(t, testPassphrase)
	src := seededDB(t)
	toDir := t.TempDir()

	captureStdout(t, func() {
		require.NoError(t, runBackup([]string{"--db", src, "--to", toDir}))
	})

	entries, err := os.ReadDir(toDir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasSuffix(e.Name(), ".db"),
			"unencrypted %s was left behind in the backup directory", e.Name())
	}
}

func TestBackupRequiresPassphrase(t *testing.T) {
	withPassphrase(t, "")
	src := seededDB(t)

	err := runBackup([]string{"--db", src, "--to", t.TempDir()})
	require.ErrorIs(t, err, backup.ErrNoPassphrase)
}

func TestBackupRejectsShortPassphrase(t *testing.T) {
	withPassphrase(t, "short")
	src := seededDB(t)

	err := runBackup([]string{"--db", src, "--to", t.TempDir()})
	require.ErrorIs(t, err, backup.ErrPassphraseTooShort)
}

func TestRestoreRejectsWrongPassphrase(t *testing.T) {
	withPassphrase(t, testPassphrase)
	src := seededDB(t)
	artifact, _ := backupOnce(t, src)

	withPassphrase(t, "a-different-long-passphrase-here")
	err := runRestore([]string{"--from", artifact, "--into", filepath.Join(t.TempDir(), "restored")})
	require.ErrorIs(t, err, backup.ErrDecrypt)
}

// --- manifest failures, all of which were previously ignored ---

func TestRestoreRefusesMissingManifest(t *testing.T) {
	withPassphrase(t, testPassphrase)
	src := seededDB(t)
	artifact, manifest := backupOnce(t, src)
	require.NoError(t, os.Remove(manifest))

	err := runRestore([]string{"--from", artifact, "--into", filepath.Join(t.TempDir(), "restored")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestRestoreRefusesMalformedManifest(t *testing.T) {
	withPassphrase(t, testPassphrase)
	src := seededDB(t)
	artifact, manifest := backupOnce(t, src)
	require.NoError(t, os.WriteFile(manifest, []byte("{not json"), 0o600))

	err := runRestore([]string{"--from", artifact, "--into", filepath.Join(t.TempDir(), "restored")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed")
}

func TestRestoreRefusesManifestWithoutChecksum(t *testing.T) {
	withPassphrase(t, testPassphrase)
	src := seededDB(t)
	artifact, manifest := backupOnce(t, src)

	var m BackupManifest
	raw, err := os.ReadFile(manifest)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &m))
	m.SHA256 = ""
	out, err := json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(manifest, out, 0o600))

	err = runRestore([]string{"--from", artifact, "--into", filepath.Join(t.TempDir(), "restored")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sha256")
}

// TestRestoreDetectsChecksumMismatch covers a manifest that describes a
// different database than the one being restored.
func TestRestoreDetectsChecksumMismatch(t *testing.T) {
	withPassphrase(t, testPassphrase)
	src := seededDB(t)
	artifact, manifest := backupOnce(t, src)

	var m BackupManifest
	raw, err := os.ReadFile(manifest)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &m))
	m.SHA256 = strings.Repeat("0", 64)
	out, err := json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(manifest, out, 0o600))

	intoDir := filepath.Join(t.TempDir(), "restored")
	err = runRestore([]string{"--from", artifact, "--into", intoDir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SHA-256 mismatch")

	_, statErr := os.Stat(filepath.Join(intoDir, "portal.db"))
	assert.True(t, os.IsNotExist(statErr),
		"a failed restore must not leave a database behind that looks usable")
}

// TestRestoreDetectsCorruptedPayload covers a tampered or truncated artifact.
func TestRestoreDetectsCorruptedPayload(t *testing.T) {
	withPassphrase(t, testPassphrase)
	src := seededDB(t)
	artifact, _ := backupOnce(t, src)

	raw, err := os.ReadFile(artifact)
	require.NoError(t, err)
	// Truncate the ciphertext: age fails at the authentication tag.
	require.NoError(t, os.WriteFile(artifact, raw[:len(raw)-32], 0o600))

	err = runRestore([]string{"--from", artifact, "--into", filepath.Join(t.TempDir(), "restored")})
	require.ErrorIs(t, err, backup.ErrDecrypt)
}

func TestRestoreRefusesToOverwrite(t *testing.T) {
	withPassphrase(t, testPassphrase)
	src := seededDB(t)
	artifact, _ := backupOnce(t, src)

	intoDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(intoDir, "portal.db"), []byte("existing"), 0o600))

	err := runRestore([]string{"--from", artifact, "--into", intoDir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to overwrite")
}
