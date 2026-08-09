package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bcars/bcars-portal/internal/authn"
	"github.com/bcars/bcars-portal/internal/backup"
	"github.com/bcars/bcars-portal/internal/db"
	"github.com/bcars/bcars-portal/internal/version"
)

// BackupManifest records metadata about a backup for restore verification.
//
// SizeBytes and SHA256 describe the PLAINTEXT database, so they can be checked
// after decryption and prove the restored file is byte-identical to what was
// backed up. Hashing the ciphertext would only prove the file downloaded
// intact, which age's authentication tag already guarantees.
type BackupManifest struct {
	Timestamp     string `json:"timestamp"`
	AppVersion    string `json:"app_version"`
	SchemaVersion int    `json:"schema_version"`
	SizeBytes     int64  `json:"size_bytes"`
	SHA256        string `json:"sha256"`
	SourcePath    string `json:"source_path"`
	Encrypted     bool   `json:"encrypted"`
}

func runBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	dbPath := fs.String("db", "", "Path to live SQLite database (required)")
	toDir := fs.String("to", "", "Destination directory for backup (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *dbPath == "" || *toDir == "" {
		fs.Usage()
		return fmt.Errorf("--db and --to are required")
	}

	// Verify source exists.
	if _, err := os.Stat(*dbPath); err != nil {
		return fmt.Errorf("source database not found: %w", err)
	}

	// Create destination directory.
	if err := os.MkdirAll(*toDir, 0o750); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}

	// Open source to perform WAL-safe backup via VACUUM INTO.
	src, err := db.Open(*dbPath)
	if err != nil {
		return fmt.Errorf("open source database: %w", err)
	}
	defer src.Close()

	// Get schema version.
	var schemaVersion int
	if err := src.QueryRow(`SELECT version_id FROM goose_db_version ORDER BY id DESC LIMIT 1`).Scan(&schemaVersion); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	passphrase, err := backup.Passphrase()
	if err != nil {
		return err
	}

	// Backup filename with timestamp.
	ts := time.Now().UTC()
	backupName := fmt.Sprintf("portal-backup-%s.db", ts.Format("20060102-150405"))
	plaintextPath := filepath.Join(*toDir, backupName)
	backupPath := plaintextPath + backup.Extension

	// Use VACUUM INTO for a WAL-safe atomic backup. The plaintext is written
	// to the destination directory only briefly; it is encrypted and removed
	// before this function returns, including on every error path.
	if _, err := src.Exec(fmt.Sprintf(`VACUUM INTO '%s'`, strings.ReplaceAll(plaintextPath, "'", "''"))); err != nil {
		return fmt.Errorf("VACUUM INTO backup: %w", err)
	}
	defer func() {
		if err := os.Remove(plaintextPath); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "WARNING: could not remove temporary plaintext %s: %v\n", plaintextPath, err)
		}
	}()

	// Verify the backup can be opened and passes integrity check.
	check, err := db.Open(plaintextPath)
	if err != nil {
		return fmt.Errorf("verify backup open: %w", err)
	}
	var integrity string
	if err := check.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		check.Close()
		return fmt.Errorf("backup integrity check failed: %s", integrity)
	}
	// Checkpoint WAL so the .db file is self-contained for hashing.
	_, _ = check.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	check.Close()

	// Compute SHA-256 of the plaintext (after WAL checkpoint), so a restore
	// can prove it recovered exactly these bytes.
	hash, size, err := fileSHA256(plaintextPath)
	if err != nil {
		return fmt.Errorf("compute checksum: %w", err)
	}

	if err := backup.EncryptFile(plaintextPath, backupPath, passphrase); err != nil {
		return err
	}

	// Write manifest.
	manifest := BackupManifest{
		Timestamp:     ts.Format(time.RFC3339),
		AppVersion:    version.Get().Short(),
		SchemaVersion: schemaVersion,
		SizeBytes:     size,
		SHA256:        hash,
		SourcePath:    *dbPath,
		Encrypted:     true,
	}

	manifestPath := backupPath + ".manifest.json"
	manifestJSON, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(manifestPath, manifestJSON, 0o640); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	fmt.Printf("Backup complete.\n")
	fmt.Printf("  File:      %s (age-encrypted)\n", backupPath)
	fmt.Printf("  Manifest:  %s\n", manifestPath)
	fmt.Printf("  Size:      %d bytes (plaintext)\n", size)
	fmt.Printf("  SHA-256:   %s (plaintext)\n", hash)
	fmt.Printf("  Schema:    v%d\n", schemaVersion)
	fmt.Printf("\nThis backup does NOT contain %s. Restoring a working instance\n", authn.PepperEnvVar)
	fmt.Printf("requires both this file's passphrase and that pepper.\n")
	return nil
}

func runRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	fromPath := fs.String("from", "", "Path to backup .db file (required)")
	intoDir := fs.String("into", "", "Directory to restore into (required; must not contain portal.db)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *fromPath == "" || *intoDir == "" {
		fs.Usage()
		return fmt.Errorf("--from and --into are required")
	}

	// Verify backup exists.
	if _, err := os.Stat(*fromPath); err != nil {
		return fmt.Errorf("backup file not found: %w", err)
	}

	// Safety: refuse to restore into a directory that already has portal.db.
	destPath := filepath.Join(*intoDir, "portal.db")
	if _, err := os.Stat(destPath); err == nil {
		return fmt.Errorf("destination %s already exists — refusing to overwrite. Remove or rename it first", destPath)
	}

	// The manifest is required, and every failure to read it is fatal.
	//
	// Previously a missing, unreadable, or malformed manifest was silently
	// skipped and the restore proceeded with no integrity verification at all
	// — the one moment when silently skipping a check is least acceptable.
	manifest, err := readManifest(*fromPath + ".manifest.json")
	if err != nil {
		return err
	}

	passphrase, err := backup.Passphrase()
	if err != nil {
		return err
	}

	// Create destination directory.
	if err := os.MkdirAll(*intoDir, 0o750); err != nil {
		return fmt.Errorf("create restore directory: %w", err)
	}

	if err := backup.DecryptFile(*fromPath, destPath, passphrase); err != nil {
		return err
	}

	// Verify the decrypted bytes are exactly what was backed up.
	hash, size, err := fileSHA256(destPath)
	if err != nil {
		return fmt.Errorf("checksum restored file: %w", err)
	}
	if hash != manifest.SHA256 {
		_ = os.Remove(destPath)
		return fmt.Errorf("SHA-256 mismatch: manifest says %s, restored file is %s — backup or manifest is corrupted",
			manifest.SHA256, hash)
	}
	if size != manifest.SizeBytes {
		_ = os.Remove(destPath)
		return fmt.Errorf("size mismatch: manifest says %d bytes, restored file is %d", manifest.SizeBytes, size)
	}
	fmt.Printf("Manifest verified: schema v%d, %d bytes, SHA-256 matches.\n",
		manifest.SchemaVersion, manifest.SizeBytes)

	// Open and verify the restored database.
	restored, err := db.Open(destPath)
	if err != nil {
		return fmt.Errorf("open restored database: %w", err)
	}
	defer restored.Close()

	// Integrity check.
	var integrity string
	if err := restored.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		return fmt.Errorf("restored database integrity check failed: %s", integrity)
	}

	// Run migrations to bring schema up to date.
	if err := db.Migrate(restored); err != nil {
		return fmt.Errorf("migrate restored database: %w", err)
	}

	// Verify foreign keys. PRAGMA foreign_key_check returns one ROW per
	// violation (table, rowid, parent, fkid) and no rows when clean — the
	// previous QueryRow(...).Scan(&int) could never succeed in either case,
	// so this check silently never ran.
	violations, err := foreignKeyViolations(restored)
	if err != nil {
		return fmt.Errorf("foreign key check: %w", err)
	}
	if violations > 0 {
		return fmt.Errorf("restored database has %d foreign key violations; refusing to present it as good", violations)
	}

	fmt.Printf("Restore complete.\n")
	fmt.Printf("  Restored to:  %s\n", destPath)
	fmt.Printf("  Integrity:    %s\n", integrity)
	fmt.Printf("  Foreign keys: ok\n")
	fmt.Printf("\nPoint the portal at %s to use it.\n", destPath)
	fmt.Printf("This backup did NOT contain %s. Sign-in will fail for every account\n", authn.PepperEnvVar)
	fmt.Printf("until the ORIGINAL pepper is supplied — a different one is refused at startup.\n")
	return nil
}

// readManifest loads and validates the manifest beside a backup.
func readManifest(path string) (*BackupManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("manifest %s not found; a backup cannot be verified without it", path)
		}
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}

	var m BackupManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest %s is malformed: %w", path, err)
	}
	if m.SHA256 == "" {
		return nil, fmt.Errorf("manifest %s has no sha256; cannot verify the backup", path)
	}
	if m.SizeBytes <= 0 {
		return nil, fmt.Errorf("manifest %s has no size; cannot verify the backup", path)
	}
	return &m, nil
}

// foreignKeyViolations counts rows returned by PRAGMA foreign_key_check.
func foreignKeyViolations(d *sql.DB) (int, error) {
	rows, err := d.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	n := 0
	for rows.Next() {
		n++
	}
	return n, rows.Err()
}

func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}
