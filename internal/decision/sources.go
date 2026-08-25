package decision

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/sageox/ox/internal/config"
)

// resolvePaths returns the decision dirs/globs for this repo: the configured
// decision.paths, else the DefaultDecisionDirs that exist on disk. Empty means
// "no corpus here".
func resolvePaths(gitRoot string, cfg *config.DecisionConfig) []string {
	if gitRoot == "" {
		return nil
	}
	if !cfg.IsEmpty() {
		if err := config.ValidateDecisionConfig(cfg); err != nil {
			slog.Debug("decision: invalid decision config ignored", "error", err)
			return nil
		}
		var paths []string
		for _, p := range cfg.Paths {
			if p = strings.TrimSpace(p); p != "" {
				paths = append(paths, p)
			}
		}
		return paths
	}
	return existingDefaultDirs(gitRoot)
}

func existingDefaultDirs(gitRoot string) []string {
	var dirs []string
	for _, d := range config.DefaultDecisionDirs {
		if fi, err := os.Stat(filepath.Join(gitRoot, d)); err == nil && fi.IsDir() {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// CorpusDetected reports whether any decision corpus exists for this project —
// the cheap gate `ox agent prime` uses before spending tokens on DR guidance.
func CorpusDetected(gitRoot string) bool {
	if gitRoot == "" {
		return false
	}
	cfg, _ := config.LoadProjectConfig(gitRoot)
	if cfg != nil && !cfg.Decision.IsEmpty() {
		return len(resolvePaths(gitRoot, cfg.Decision)) > 0
	}
	return len(existingDefaultDirs(gitRoot)) > 0
}

// LoadCorpus discovers and parses every DR in the repo's decision paths,
// fresh per call — no persisted index. Corpora are hundreds of small files at
// most, so a walk+parse is a few milliseconds; the searchable full-text index
// already exists in codedb (`ox code search` reaches these same files).
// Fail-open throughout: unreadable files are skipped with a debug log.
func LoadCorpus(gitRoot string, cfg *config.DecisionConfig) []Record {
	return loadCorpus(gitRoot, cfg).records
}

type corpusLoadResult struct {
	records  []Record
	unparsed int
	err      error
}

// loadCorpus is the honesty-sensitive loader used by Enrich. LoadCorpus keeps
// its historical fail-open API for passive callers, while this variant reports
// configured-source discovery failures, unreadable files, and markdown that is
// clearly intended to be a DR but is too malformed to catalog. Ordinary notes,
// templates, and README files are normal exclusions, not source failures.
func loadCorpus(gitRoot string, cfg *config.DecisionConfig) corpusLoadResult {
	paths := resolvePaths(gitRoot, cfg)
	if len(paths) == 0 {
		return corpusLoadResult{}
	}

	discovery := discoverFiles(gitRoot, paths, cfg != nil && !cfg.IsEmpty())
	var records []Record
	var loadErrs []error
	if discovery.err != nil {
		loadErrs = append(loadErrs, discovery.err)
	}
	unparsed := 0
	for _, file := range discovery.files {
		data, err := os.ReadFile(file)
		if err != nil {
			slog.Debug("decision: unreadable file skipped", "path", file, "error", err)
			loadErrs = append(loadErrs, fmt.Errorf("read %s: %w", file, err))
			unparsed++
			continue
		}
		rec := ParseContent(file, string(data))
		if !rec.IsRecord() {
			if likelyDecisionRecord(file, string(data)) {
				unparsed++
			}
			continue
		}
		if fi, err := os.Stat(file); err == nil {
			rec.Mtime = fi.ModTime().Unix()
			rec.Size = fi.Size()
		}
		if rel, err := filepath.Rel(gitRoot, file); err == nil {
			rec.RelPath = rel
		}
		rec.Corpus = "repo"
		records = append(records, rec)
	}

	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Number != records[j].Number {
			return records[i].Number < records[j].Number
		}
		return records[i].RelPath < records[j].RelPath
	})
	return corpusLoadResult{records: records, unparsed: unparsed, err: errors.Join(loadErrs...)}
}

// listFiles expands dirs (recursive *.md) and doublestar globs into a deduped
// file list. README.md is excluded everywhere — corpus index tables, not DRs.
func listFiles(gitRoot string, patterns []string) []string {
	return discoverFiles(gitRoot, patterns, false).files
}

type fileDiscovery struct {
	files []string
	err   error
}

// discoverFiles is listFiles plus source-status reporting. When configured is
// true, a literal path was explicitly promised by decision.paths, so a missing
// or inaccessible path is an unavailable source rather than an empty corpus.
func discoverFiles(gitRoot string, patterns []string, configured bool) fileDiscovery {
	seen := make(map[string]struct{})
	var out []string
	var discoveryErrs []error
	add := func(p string) {
		if !strings.EqualFold(filepath.Ext(p), ".md") {
			return
		}
		if strings.EqualFold(filepath.Base(p), "README.md") {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}

	for _, pat := range patterns {
		full := filepath.Join(gitRoot, pat)
		fi, statErr := os.Stat(full)
		if statErr == nil && fi.IsDir() {
			walkErr := filepath.WalkDir(full, func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				add(p)
				return nil
			})
			if walkErr != nil {
				discoveryErrs = append(discoveryErrs, fmt.Errorf("walk decision path %q: %w", pat, walkErr))
			}
			continue
		}
		if statErr == nil {
			add(full)
			continue
		}
		if configured && !hasGlobMeta(pat) {
			discoveryErrs = append(discoveryErrs, fmt.Errorf("access configured decision path %q: %w", pat, statErr))
			continue
		}
		matches, err := doublestar.Glob(os.DirFS(gitRoot), pat)
		if err != nil {
			slog.Debug("decision: bad glob", "pattern", pat, "error", err)
			discoveryErrs = append(discoveryErrs, fmt.Errorf("expand decision path %q: %w", pat, err))
			continue
		}
		for _, m := range matches {
			add(filepath.Join(gitRoot, m))
		}
	}
	return fileDiscovery{files: out, err: errors.Join(discoveryErrs...)}
}

func hasGlobMeta(pattern string) bool {
	return strings.ContainsAny(pattern, `*?[{`)
}

var (
	likelyDecisionNameRe    = regexp.MustCompile(`(?i)^(?:adr|ddr)(?:[-_]|\.md$)|^\d{1,4}[-_]`)
	decisionSectionIntentRe = regexp.MustCompile(`(?im)^##\s+decision\b`)
	decisionSupportFiles    = map[string]struct{}{
		"contributing.md": {},
		"guide.md":        {},
		"index.md":        {},
		"notes.md":        {},
		"process.md":      {},
		"readme.md":       {},
		"template.md":     {},
	}
)

// likelyDecisionRecord distinguishes malformed DRs from intentional Markdown
// neighbors such as notes.md and TEMPLATE.md. Known support filenames are
// excluded; other Markdown with a title is conservatively treated as a broken
// DR so a descriptive filename cannot turn an unparseable decision into a
// falsely verified absence.
func likelyDecisionRecord(path, content string) bool {
	base := filepath.Base(path)
	if _, excluded := decisionSupportFiles[strings.ToLower(base)]; excluded {
		return false
	}
	if likelyDecisionNameRe.MatchString(base) || headIDRe.MatchString(firstH1Line(content)) {
		return true
	}
	lower := strings.ToLower(content)
	return strings.Contains(lower, "\ntype: adr") ||
		strings.Contains(lower, "\ntype: ddr") ||
		decisionSectionIntentRe.MatchString(content) ||
		firstH1Line(content) != ""
}

// PathMatcher returns a predicate reporting whether a repo-relative path lies
// inside this repo's decision corpus (configured decision.paths or the default
// dirs). Resolution happens once; matching is pure string work — cheap enough
// to tag every `ox code search` result at query time. Nil-safe: with no corpus
// the predicate is always false.
func PathMatcher(gitRoot string) func(relPath string) bool {
	var cfg *config.DecisionConfig
	if pc, _ := config.LoadProjectConfig(gitRoot); pc != nil {
		cfg = pc.Decision
	}
	patterns := resolvePaths(gitRoot, cfg)

	var dirs []string
	var globs []string
	for _, p := range patterns {
		p = filepath.ToSlash(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if fi, err := os.Stat(filepath.Join(gitRoot, p)); err == nil && fi.IsDir() {
			dirs = append(dirs, strings.TrimSuffix(p, "/")+"/")
			continue
		}
		globs = append(globs, p)
	}

	return func(relPath string) bool {
		rel := filepath.ToSlash(relPath)
		if !strings.EqualFold(filepath.Ext(rel), ".md") {
			return false
		}
		for _, d := range dirs {
			if strings.HasPrefix(rel, d) {
				return true
			}
		}
		for _, g := range globs {
			if ok, err := doublestar.Match(g, rel); err == nil && ok {
				return true
			}
		}
		return false
	}
}

// SearchPathPatterns returns repo-relative file patterns suitable for CodeDB's
// file filter. Directory entries become recursive markdown globs; explicit
// globs are passed through and checked precisely by PathMatcher after search.
func SearchPathPatterns(gitRoot string) []string {
	var cfg *config.DecisionConfig
	if pc, _ := config.LoadProjectConfig(gitRoot); pc != nil {
		cfg = pc.Decision
	}
	var patterns []string
	for _, p := range resolvePaths(gitRoot, cfg) {
		p = filepath.ToSlash(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if fi, err := os.Stat(filepath.Join(gitRoot, p)); err == nil && fi.IsDir() {
			patterns = append(patterns, strings.TrimSuffix(p, "/")+"/*.md")
			continue
		}
		patterns = append(patterns, p)
	}
	return patterns
}

// PrimaryDir returns the corpus dir new DRs should land in: the first
// configured/default path that is a plain directory. Empty when the corpus is
// glob-only or absent.
func PrimaryDir(gitRoot string, cfg *config.DecisionConfig) string {
	for _, p := range resolvePaths(gitRoot, cfg) {
		if fi, err := os.Stat(filepath.Join(gitRoot, p)); err == nil && fi.IsDir() {
			return p
		}
	}
	return ""
}
