package ledger

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const CurrentDataVersion = 2

// DataVersionFile tracks which data migrations have been applied.
type DataVersionFile struct {
	Version    int                  `json:"version"`
	Migrations map[string]time.Time `json:"migrations"`
}

// Migration name constants.
const (
	MigrationContentHashFilenames = "content_hash_filenames"
	MigrationUUID7FactFilenames   = "uuid7_fact_filenames"
	MigrationDailySummaryRefs     = "daily_summary_refs"
)

// legacyNumberPattern matches old-format filenames: NNN.json (no hash suffix).
var legacyNumberPattern = regexp.MustCompile(`^(\d+)\.json$`)

// RepairConflictMarkerFiles scans PR and issue JSON files in the ledger for
// git merge conflict markers. Corrupted files are deleted (the next sync will
// recreate them cleanly with content-hash filenames).
// Returns the count of repaired files.
func RepairConflictMarkerFiles(ledgerPath string, logger *slog.Logger) (int, error) {
	var repaired int

	for _, dataType := range []string{"pr", "issue"} {
		files, err := ListGitHubDataFiles(ledgerPath, dataType)
		if err != nil {
			return repaired, fmt.Errorf("list %s files: %w", dataType, err)
		}

		for _, path := range files {
			data, err := os.ReadFile(path)
			if err != nil {
				logger.Debug("skip unreadable file", "path", path, "error", err)
				continue
			}

			if !strings.Contains(string(data), "<<<<<<<") {
				continue
			}

			logger.Warn("deleting corrupted file with conflict markers", "path", path)
			if err := os.Remove(path); err != nil {
				return repaired, fmt.Errorf("remove corrupted file %s: %w", path, err)
			}
			repaired++
		}
	}

	return repaired, nil
}

// MigrateLegacyGitHubFiles renames old-format files (NNN.json) to content-hash
// format (NNN-{hash8}.json). Files that are already hash-named are skipped.
// Corrupted files (conflict markers) are deleted.
// Returns counts of migrated and deleted files.
func MigrateLegacyGitHubFiles(ledgerPath string, logger *slog.Logger) (migrated, deleted int, err error) {
	for _, dataType := range []string{"pr", "issue"} {
		files, listErr := ListGitHubDataFiles(ledgerPath, dataType)
		if listErr != nil {
			return migrated, deleted, fmt.Errorf("list %s files: %w", dataType, listErr)
		}

		for _, path := range files {
			name := filepath.Base(path)

			// skip files already in content-hash format
			if parseHashFilename(name) >= 0 {
				logger.Debug("skip hash-named file", "path", path)
				continue
			}

			// check for legacy pattern
			if !legacyNumberPattern.MatchString(name) {
				logger.Debug("skip non-legacy file", "path", path)
				continue
			}

			data, readErr := os.ReadFile(path)
			if readErr != nil {
				logger.Debug("skip unreadable file", "path", path, "error", readErr)
				continue
			}

			// check for conflict markers
			if strings.Contains(string(data), "<<<<<<<") {
				logger.Warn("deleting corrupted legacy file", "path", path)
				if rmErr := os.Remove(path); rmErr != nil {
					return migrated, deleted, fmt.Errorf("remove corrupted file %s: %w", path, rmErr)
				}
				deleted++
				continue
			}

			// compute content hash and rename
			hash := contentHash(data)
			m := legacyNumberPattern.FindStringSubmatch(name)
			newName := fmt.Sprintf("%s-%s.json", m[1], hash)
			newPath := filepath.Join(filepath.Dir(path), newName)

			if err := os.Rename(path, newPath); err != nil {
				return migrated, deleted, fmt.Errorf("rename %s to %s: %w", path, newPath, err)
			}
			logger.Info("migrated legacy file", "from", name, "to", newName)
			migrated++
		}
	}

	return migrated, deleted, nil
}

// dataVersionPath returns the path to the data version file.
func dataVersionPath(ledgerPath string) string {
	return filepath.Join(GitHubSyncCacheDir(ledgerPath), "data_version.json")
}

// ReadDataVersion reads the migration version from the ledger cache.
// Returns version 0 if the file doesn't exist.
func ReadDataVersion(ledgerPath string) (*DataVersionFile, error) {
	path := dataVersionPath(ledgerPath)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &DataVersionFile{
				Version:    0,
				Migrations: make(map[string]time.Time),
			}, nil
		}
		return nil, fmt.Errorf("read data version: %w", err)
	}

	var v DataVersionFile
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("unmarshal data version: %w", err)
	}
	if v.Migrations == nil {
		v.Migrations = make(map[string]time.Time)
	}
	return &v, nil
}

// WriteDataVersion writes the migration version to the ledger cache.
func WriteDataVersion(ledgerPath string, v *DataVersionFile) error {
	dir := GitHubSyncCacheDir(ledgerPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}

	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal data version: %w", err)
	}

	path := dataVersionPath(ledgerPath)
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write data version: %w", err)
	}
	return nil
}

// NeedsMigration returns true if any migrations haven't been applied yet.
func NeedsMigration(ledgerPath string) bool {
	v, err := ReadDataVersion(ledgerPath)
	if err != nil {
		return true // assume migration needed on read error
	}
	return v.Version < CurrentDataVersion
}

// MarkMigration records that a migration was applied and bumps the version
// if all known migrations are complete.
func MarkMigration(ledgerPath string, name string) error {
	v, err := ReadDataVersion(ledgerPath)
	if err != nil {
		v = &DataVersionFile{
			Version:    0,
			Migrations: make(map[string]time.Time),
		}
	}

	v.Migrations[name] = time.Now().UTC()

	// bump version when all migrations are applied
	allMigrations := []string{MigrationContentHashFilenames, MigrationUUID7FactFilenames, MigrationDailySummaryRefs}
	allDone := true
	for _, m := range allMigrations {
		if _, ok := v.Migrations[m]; !ok {
			allDone = false
			break
		}
	}
	if allDone {
		v.Version = CurrentDataVersion
	}

	return WriteDataVersion(ledgerPath, v)
}
