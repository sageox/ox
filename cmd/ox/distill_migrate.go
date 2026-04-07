package main

import (
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// Legacy fact file patterns (no UUID7 segment).
var (
	legacyPRFactPattern    = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})-pr-(\d+)\.jsonl$`)
	legacyIssueFactPattern = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})-issue-(\d+)\.jsonl$`)
	legacyCommitsPattern   = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})-commits\.jsonl$`)

	// uuid7FactPattern detects already-migrated files with a UUID7 segment.
	uuid7FactPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[0-9a-f]{4}-[0-9a-f]{12}-`)
)

// MigrateLegacyFactFiles renames old-format fact files in memory/.github-facts/
// from {date}-pr-{number}.jsonl to {date}-{uuid7}-pr-{number}.jsonl.
// Only renames — does not re-extract. The source_hash in frontmatter remains valid.
// Returns count of migrated files.
func MigrateLegacyFactFiles(tcPath string, logger *slog.Logger) (int, error) {
	factsDir := filepath.Join(tcPath, "memory", ".github-facts")

	entries, err := os.ReadDir(factsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read github-facts dir: %w", err)
	}

	var migrated int
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()

		// skip already-migrated files
		if uuid7FactPattern.MatchString(name) {
			logger.Debug("skip uuid7-named file", "file", name)
			continue
		}

		var newName string
		uid, _ := uuid.NewV7()

		switch {
		case legacyPRFactPattern.MatchString(name):
			m := legacyPRFactPattern.FindStringSubmatch(name)
			newName = fmt.Sprintf("%s-%s-pr-%s.jsonl", m[1], uid.String(), m[2])
		case legacyIssueFactPattern.MatchString(name):
			m := legacyIssueFactPattern.FindStringSubmatch(name)
			newName = fmt.Sprintf("%s-%s-issue-%s.jsonl", m[1], uid.String(), m[2])
		case legacyCommitsPattern.MatchString(name):
			m := legacyCommitsPattern.FindStringSubmatch(name)
			newName = fmt.Sprintf("%s-%s-commits.jsonl", m[1], uid.String())
		default:
			continue
		}

		oldPath := filepath.Join(factsDir, name)
		newPath := filepath.Join(factsDir, newName)
		if err := os.Rename(oldPath, newPath); err != nil {
			return migrated, fmt.Errorf("rename %s to %s: %w", name, newName, err)
		}
		logger.Info("migrated legacy fact file", "from", name, "to", newName)
		migrated++
	}

	return migrated, nil
}

// UpdateDailySummaryRefs updates fact file references in daily summary frontmatter
// to point to the new UUID7-named files.
// Returns count of updated summaries.
func UpdateDailySummaryRefs(tcPath string, logger *slog.Logger) (int, error) {
	dailyDir := filepath.Join(tcPath, "memory", "daily")
	factsDir := filepath.Join(tcPath, "memory", ".github-facts")

	entries, err := os.ReadDir(dailyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read daily dir: %w", err)
	}

	var updated int
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		path := filepath.Join(dailyDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			logger.Debug("skip unreadable daily file", "path", path, "error", err)
			continue
		}

		content := string(data)
		sources := parseDailySources(content)
		if len(sources) == 0 {
			continue
		}

		// check each source for legacy naming
		changed := false
		newContent := content
		for _, src := range sources {
			base := filepath.Base(src)

			// only fix github-facts references with legacy naming
			if !strings.Contains(src, ".github-facts/") {
				continue
			}
			if uuid7FactPattern.MatchString(base) {
				continue
			}

			// try to find the renamed file
			newRef := findRenamedFactFile(factsDir, base, src)
			if newRef == "" {
				continue
			}

			newContent = strings.Replace(newContent, src, newRef, 1)
			changed = true
		}

		if changed {
			if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
				return updated, fmt.Errorf("write updated daily %s: %w", entry.Name(), err)
			}
			logger.Info("updated daily summary refs", "file", entry.Name())
			updated++
		}
	}

	// delete distill index cache so it rebuilds with new paths
	if updated > 0 {
		indexPath := filepath.Join(tcPath, "..", ".sageox", "cache", "distill-index.json")
		if err := os.Remove(indexPath); err != nil && !os.IsNotExist(err) {
			logger.Debug("failed to remove distill index cache", "error", err)
		}
	}

	return updated, nil
}

// findRenamedFactFile looks for a UUID7-named file in factsDir that matches
// the legacy filename pattern. Returns the new relative path (POSIX forward
// slashes for frontmatter storage) or empty string.
func findRenamedFactFile(factsDir, legacyBase, legacyRelPath string) string {
	// determine the glob pattern from the legacy filename
	var globPattern string
	// Use path.Dir (POSIX) — the returned ref is stored in markdown frontmatter,
	// so it must use forward slashes regardless of OS.
	dir := path.Dir(legacyRelPath) // e.g., "memory/.github-facts"

	switch {
	case legacyPRFactPattern.MatchString(legacyBase):
		m := legacyPRFactPattern.FindStringSubmatch(legacyBase)
		globPattern = fmt.Sprintf("%s-*-pr-%s.jsonl", m[1], m[2])
	case legacyIssueFactPattern.MatchString(legacyBase):
		m := legacyIssueFactPattern.FindStringSubmatch(legacyBase)
		globPattern = fmt.Sprintf("%s-*-issue-%s.jsonl", m[1], m[2])
	case legacyCommitsPattern.MatchString(legacyBase):
		m := legacyCommitsPattern.FindStringSubmatch(legacyBase)
		globPattern = fmt.Sprintf("%s-*-commits.jsonl", m[1])
	default:
		return ""
	}

	// Use filepath.Glob for filesystem operations (OS-native paths)
	matches, err := filepath.Glob(filepath.Join(factsDir, globPattern))
	if err != nil || len(matches) == 0 {
		return ""
	}

	// pick lexicographically last (UUID7 sorts chronologically)
	best := matches[0]
	for _, m := range matches[1:] {
		if m > best {
			best = m
		}
	}

	// Use path.Join (POSIX) for the returned ref stored in frontmatter
	return path.Join(dir, filepath.Base(best))
}
