package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bcars/bcars-portal/internal/db"
	"github.com/bcars/bcars-portal/internal/version"
)

// BackupManifest records metadata about a backup for restore verification.
type BackupManifest struct {
	Timestamp     string `json:"timestamp"`
	AppVersion    string `json:"app_version"`
	SchemaVersion int    `json:"schema_version"`
	SizeBytes     int64  `json:"size_bytes"`
	SHA256        string `json:"sha256"`
	SourcePath    string `json:"source_path"`
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

	// Backup filename with timestamp.
	ts := time.Now().UTC()
	backupName := fmt.Sprintf("portal-backup-%s.db", ts.Format("20060102-150405"))
	backupPath := filepath.Join(*toDir, backupName)

	// Use VACUUM INTO for a WAL-safe atomic backup.
	if _, err := src.Exec(fmt.Sprintf(`VACUUM INTO '%s'`, strings.ReplaceAll(backupPath, "'", "''"))); err != nil {
		return fmt.Errorf("VACUUM INTO backup: %w", err)
	}

	// Verify the backup can be opened and passes integrity check.
	check, err := db.Open(backupPath)
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

	// Compute SHA-256 of the backup file (after WAL checkpoint).
	hash, size, err := fileSHA256(backupPath)
	if err != nil {
		return fmt.Errorf("compute checksum: %w", err)
	}

	// Write manifest.
	manifest := BackupManifest{
		Timestamp:     ts.Format(time.RFC3339),
		AppVersion:    version.Get().Short(),
		SchemaVersion: schemaVersion,
		SizeBytes:     size,
		SHA256:        hash,
		SourcePath:    *dbPath,
	}

	manifestPath := backupPath + ".manifest.json"
	manifestJSON, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(manifestPath, manifestJSON, 0o640); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	fmt.Printf("Backup complete.\n")
	fmt.Printf("  File:     %s\n", backupPath)
	fmt.Printf("  Manifest: %s\n", manifestPath)
	fmt.Printf("  Size:     %d bytes\n", size)
	fmt.Printf("  SHA-256:  %s\n", hash)
	fmt.Printf("  Schema:   v%d\n", schemaVersion)
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

	// Check manifest if it exists.
	manifestPath := *fromPath + ".manifest.json"
	if _, err := os.Stat(manifestPath); err == nil {
		manifestData, err := os.ReadFile(manifestPath)
		if err == nil {
			var manifest BackupManifest
			if err := json.Unmarshal(manifestData, &manifest); err == nil {
				// Verify SHA-256.
				hash, _, err := fileSHA256(*fromPath)
				if err == nil && hash != manifest.SHA256 {
					return fmt.Errorf("SHA-256 mismatch: expected %s, got %s — backup may be corrupted", manifest.SHA256, hash)
				}
				fmt.Printf("Manifest verified: schema v%d, %d bytes\n", manifest.SchemaVersion, manifest.SizeBytes)
			}
		}
	}

	// Create destination directory.
	if err := os.MkdirAll(*intoDir, 0o750); err != nil {
		return fmt.Errorf("create restore directory: %w", err)
	}

	// Copy backup to destination.
	if err := copyFile(*fromPath, destPath); err != nil {
		return fmt.Errorf("copy backup: %w", err)
	}

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

	// Verify foreign keys.
	var fkViolations int
	if err := restored.QueryRow(`PRAGMA foreign_key_check`).Scan(&fkViolations); err == nil && fkViolations > 0 {
		fmt.Printf("WARNING: %d foreign key violations detected\n", fkViolations)
	}

	fmt.Printf("Restore complete.\n")
	fmt.Printf("  Restored to: %s\n", destPath)
	fmt.Printf("  Integrity:   %s\n", integrity)
	fmt.Println("\nTo use this database, update your portal configuration to point to the restored path.")
	return nil
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

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
