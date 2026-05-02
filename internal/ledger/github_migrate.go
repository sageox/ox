package ledger

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

			// Check ALL files for conflict markers first — even hash-named
			// files can be corrupted by git merge conflicts.
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				logger.Debug("skip unreadable file", "path", path, "error", readErr)
				continue
			}

			if strings.Contains(string(data), "<<<<<<<") {
				logger.Warn("deleting corrupted file with conflict markers", "path", path)
				if rmErr := os.Remove(path); rmErr != nil {
					return migrated, deleted, fmt.Errorf("remove corrupted file %s: %w", path, rmErr)
				}
				deleted++
				continue
			}

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

// ScanLegacyGitHubFiles reports legacy-format and corrupted files without modifying them.
// Returns lists of files that would be renamed and deleted.
// Paths are relative to ledgerPath.
func ScanLegacyGitHubFiles(ledgerPath string) (legacyFiles, corruptedFiles []string, err error) {
	for _, dataType := range []string{"pr", "issue"} {
		files, listErr := ListGitHubDataFiles(ledgerPath, dataType)
		if listErr != nil {
			return legacyFiles, corruptedFiles, fmt.Errorf("list %s files: %w", dataType, listErr)
		}

		for _, path := range files {
			name := filepath.Base(path)

			data, readErr := os.ReadFile(path)
			if readErr != nil {
				continue
			}

			rel, _ := filepath.Rel(ledgerPath, path)
			if rel == "" {
				rel = path
			}

			if strings.Contains(string(data), "<<<<<<<") {
				corruptedFiles = append(corruptedFiles, rel)
				continue
			}

			if parseHashFilename(name) >= 0 {
				continue
			}

			if !legacyNumberPattern.MatchString(name) {
				continue
			}

			legacyFiles = append(legacyFiles, rel)
		}
	}

	return legacyFiles, corruptedFiles, nil
}
