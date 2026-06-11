package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/facts"
)

// distillIndex is a performance cache that mirrors metadata already stored
// in memory files. It is NOT the source of truth — the files are.
// If missing or corrupt, it is rebuilt by reading file headers.
//
// Every field here corresponds 1:1 to metadata in the files:
//   - FactMeta: _meta header from .jsonl fact files (source_hash, query_since, query_until)
//   - DailySources: sources frontmatter from daily .md files
type distillIndex struct {
	// FactMeta caches the _meta header of each fact file.
	// Key: relative path (e.g., "memory/.github-facts/2026-03-30-xxx.jsonl").
	// Value: the FileMeta from the file's first line — same fields, same values.
	FactMeta map[string]facts.FileMeta `json:"fact_meta"`

	// DailySources caches the sources frontmatter from each daily summary.
	// Key: relative path (e.g., "memory/daily/2026-03-30-xxx.md").
	// Value: list of fact file paths from the file's YAML frontmatter.
	DailySources map[string][]string `json:"daily_sources"`
}

const distillIndexFile = "distill-index.json"

// loadDistillIndex loads the cache index, or rebuilds it from memory files.
// Returns the index and whether it was rebuilt (vs loaded from cache).
func loadDistillIndex(projectRoot, tcPath string) (*distillIndex, bool) {
	indexPath := filepath.Join(projectRoot, ".sageox", "cache", distillIndexFile)

	if data, err := os.ReadFile(indexPath); err == nil {
		var idx distillIndex
		if err := json.Unmarshal(data, &idx); err != nil {
			slog.Warn("corrupt distill index, rebuilding", "error", err)
		} else if idx.FactMeta != nil {
			return &idx, false
		} else {
			slog.Warn("distill index missing FactMeta, rebuilding")
		}
	}

	idx := rebuildDistillIndex(tcPath)
	return idx, true
}

// saveDistillIndex persists the index to disk.
func saveDistillIndex(projectRoot string, idx *distillIndex) error {
	cacheDir := filepath.Join(projectRoot, ".sageox", "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(cacheDir, distillIndexFile), append(data, '\n'), 0o644)
}

// rebuildDistillIndex scans all memory files and builds the index from scratch.
func rebuildDistillIndex(tcPath string) *distillIndex {
	idx := &distillIndex{
		FactMeta:     make(map[string]facts.FileMeta),
		DailySources: make(map[string][]string),
	}

	// scan fact files (.github-facts, .session-facts, .discussion-facts)
	scanFactDir(idx, tcPath, filepath.Join("memory", ".github-facts"))
	scanFactDir(idx, tcPath, filepath.Join("memory", ".discussion-facts"))
	scanSessionFactDirs(idx, tcPath)

	// scan daily summaries for sources frontmatter
	scanDailySummaries(idx, tcPath)

	slog.Info("rebuilt distill index", "facts", len(idx.FactMeta), "dailies", len(idx.DailySources))
	return idx
}

// updateDistillIndex scans for new files not in the index and merges them in.
// Returns the number of new files discovered.
func updateDistillIndex(idx *distillIndex, tcPath string) int {
	var newFiles int
	newFiles += scanFactDirUpdate(idx, tcPath, filepath.Join("memory", ".github-facts"))
	newFiles += scanFactDirUpdate(idx, tcPath, filepath.Join("memory", ".discussion-facts"))
	newFiles += scanSessionFactDirsUpdate(idx, tcPath)
	newFiles += scanDailySummariesUpdate(idx, tcPath)

	if newFiles > 0 {
		slog.Info("updated distill index", "new_files", newFiles)
	}
	return newFiles
}

// --- scan helpers ---

// scanFactDir reads all .jsonl files in a flat directory and caches their _meta.
func scanFactDir(idx *distillIndex, tcPath, relDir string) {
	absDir := filepath.Join(tcPath, relDir)
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		relPath := filepath.Join(relDir, entry.Name())
		meta, err := readFactFileMeta(filepath.Join(absDir, entry.Name()))
		if err != nil {
			continue
		}
		idx.FactMeta[relPath] = meta
	}
}

// scanFactDirUpdate is like scanFactDir but only processes files not already in the index.
func scanFactDirUpdate(idx *distillIndex, tcPath, relDir string) int {
	absDir := filepath.Join(tcPath, relDir)
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return 0
	}
	var newFiles int
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		relPath := filepath.Join(relDir, entry.Name())
		if _, known := idx.FactMeta[relPath]; known {
			continue
		}
		meta, err := readFactFileMeta(filepath.Join(absDir, entry.Name()))
		if err != nil {
			continue
		}
		idx.FactMeta[relPath] = meta
		newFiles++
	}
	return newFiles
}

// scanSessionFactDirs reads .session-facts/<date>/<file>.jsonl (nested structure).
func scanSessionFactDirs(idx *distillIndex, tcPath string) {
	sessDir := filepath.Join(tcPath, "memory", ".session-facts")
	dateDirs, err := os.ReadDir(sessDir)
	if err != nil {
		return
	}
	for _, dateDir := range dateDirs {
		if !dateDir.IsDir() {
			continue
		}
		scanFactDir(idx, tcPath, filepath.Join("memory", ".session-facts", dateDir.Name()))
	}
}

// scanSessionFactDirsUpdate is like scanSessionFactDirs but only processes new files.
func scanSessionFactDirsUpdate(idx *distillIndex, tcPath string) int {
	sessDir := filepath.Join(tcPath, "memory", ".session-facts")
	dateDirs, err := os.ReadDir(sessDir)
	if err != nil {
		return 0
	}
	var newFiles int
	for _, dateDir := range dateDirs {
		if !dateDir.IsDir() {
			continue
		}
		newFiles += scanFactDirUpdate(idx, tcPath, filepath.Join("memory", ".session-facts", dateDir.Name()))
	}
	return newFiles
}

// scanDailySummaries reads daily .md files and caches their sources frontmatter.
func scanDailySummaries(idx *distillIndex, tcPath string) {
	dailyDir := filepath.Join(tcPath, "memory", "daily")
	entries, err := os.ReadDir(dailyDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		relPath := filepath.Join("memory", "daily", entry.Name())
		data, err := os.ReadFile(filepath.Join(dailyDir, entry.Name()))
		if err != nil {
			continue
		}
		if sources := parseDailySources(string(data)); len(sources) > 0 {
			idx.DailySources[relPath] = sources
		}
	}
}

// scanDailySummariesUpdate is like scanDailySummaries but only processes new files.
func scanDailySummariesUpdate(idx *distillIndex, tcPath string) int {
	dailyDir := filepath.Join(tcPath, "memory", "daily")
	entries, err := os.ReadDir(dailyDir)
	if err != nil {
		return 0
	}
	var newFiles int
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		relPath := filepath.Join("memory", "daily", entry.Name())
		if _, known := idx.DailySources[relPath]; known {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dailyDir, entry.Name()))
		if err != nil {
			continue
		}
		if sources := parseDailySources(string(data)); len(sources) > 0 {
			idx.DailySources[relPath] = sources
			newFiles++
		}
	}
	return newFiles
}

// --- index query helpers ---

// readFactFileMeta reads the _meta header from the first line of a JSONL fact file.
func readFactFileMeta(path string) (facts.FileMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return facts.FileMeta{}, err
	}
	firstLine, _, _ := strings.Cut(string(data), "\n")
	firstLine = strings.TrimSpace(firstLine)
	if firstLine == "" {
		return facts.FileMeta{}, fmt.Errorf("empty file")
	}
	var header facts.FileHeader
	if err := json.Unmarshal([]byte(firstLine), &header); err != nil {
		return facts.FileMeta{}, err
	}
	return header.Meta, nil
}

// distilledSources returns the set of fact file paths already included in daily summaries.
func (idx *distillIndex) distilledSources() map[string]bool {
	result := make(map[string]bool)
	for _, sources := range idx.DailySources {
		for _, src := range sources {
			result[src] = true
		}
	}
	return result
}

// addDailySummary records a newly created daily summary and its sources.
func (idx *distillIndex) addDailySummary(relPath string, sources []string) {
	idx.DailySources[relPath] = sources
}
