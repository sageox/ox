package manifest

import (
	"log/slog"
	"strings"
)

// DefaultResolveRules is the default set of resolve rules used when:
//   - No sync.manifest exists (fallback config)
//   - Manifest exists but has no `resolve` directives
//
// The default covers all data/ subdirectories because they contain
// derived/imported content (GitHub PRs, Linear issues, murmurs) that
// will be re-fetched on the next sync cycle. Last-write-wins is correct
// for idempotent data — a code change and CLI release should not be
// required to add a new data integration like data/jira/.
var DefaultResolveRules = []ResolveRule{
	{Mode: ResolveModeAuto, Path: "data/"},
}

// resolveHardDenyList contains paths that must NEVER have mode=auto,
// regardless of what the manifest says. These are either human-authored,
// session-critical, or candidates for smarter merge strategies in the future.
//
// Categories:
//   - Hard deny (human-authored, conflicts need human review):
//     SOUL.md, AGENTS.md, TEAM.md, .sageox/, docs/, coworkers/, sessions/
//   - Future: LLM semantic merge (agent-written, both sides have value):
//     MEMORY.md, memory/ — agents write these via `ox remember`; concurrent
//     writes from multiple agents should be merged, not silently dropped
//   - Future: likely auto-resolvable (cloud-authored, remote is source of truth):
//     discussions/ — distilled by cloud pipeline, latest version is canonical.
//     Kept on deny list for safety until we have confidence in the pipeline.
var resolveHardDenyList = []string{
	// hard deny: human-authored content
	"SOUL.md",
	"AGENTS.md",
	"TEAM.md",
	".sageox/",
	"docs/",
	"coworkers/",
	"sessions/",

	// future: LLM semantic merge candidates (agent-written, both sides valuable)
	"MEMORY.md",
	"memory/",

	// future: likely auto-resolvable (cloud pipeline is source of truth)
	"discussions/",
}

// ValidateResolveRules filters resolve rules, rejecting any `auto` rule
// whose path overlaps with the hardcoded deny list. Non-auto rules (like
// `none`) are always allowed — they're explicit opt-outs.
// Returns only valid rules. Logs a warning for each rejected rule.
func ValidateResolveRules(rules []ResolveRule) []ResolveRule {
	var result []ResolveRule
	for _, rule := range rules {
		if rule.Mode == ResolveModeAuto && isHardDenied(rule.Path) {
			slog.Warn("manifest: resolve auto rejected by hard deny list",
				"path", rule.Path)
			continue
		}
		result = append(result, rule)
	}
	return result
}

// ResolveModeFor returns the applicable resolve mode for a file path.
// Most specific prefix wins (longest match). On equal-length ties,
// none wins over auto (deny is the safer default). Returns ResolveModeNone
// if no rule matches.
func ResolveModeFor(filePath string, rules []ResolveRule) ResolveMode {
	bestLen := 0
	bestMode := ResolveModeNone
	for _, rule := range rules {
		if !strings.HasPrefix(filePath, rule.Path) {
			continue
		}
		if len(rule.Path) > bestLen {
			bestLen = len(rule.Path)
			bestMode = rule.Mode
		} else if len(rule.Path) == bestLen && rule.Mode == ResolveModeNone {
			// deny wins ties — safer default
			bestMode = ResolveModeNone
		}
	}
	return bestMode
}

// AutoResolvePaths extracts paths with mode=auto from resolve rules.
// Used by callers that need a flat []string for ResolveRebaseAcceptTheirs.
func AutoResolvePaths(rules []ResolveRule) []string {
	var paths []string
	for _, rule := range rules {
		if rule.Mode == ResolveModeAuto {
			paths = append(paths, rule.Path)
		}
	}
	return paths
}

// AutoResolveDenyPaths extracts paths with mode=none from resolve rules.
// Used by callers that need explicit deny paths for ResolveRebaseAcceptTheirs.
func AutoResolveDenyPaths(rules []ResolveRule) []string {
	var paths []string
	for _, rule := range rules {
		if rule.Mode == ResolveModeNone {
			paths = append(paths, rule.Path)
		}
	}
	return paths
}

// isHardDenied checks if a path overlaps with any entry in the hardcoded
// deny list. Overlap means either is a prefix of the other.
// Empty paths are always denied — they would match every file.
func isHardDenied(path string) bool {
	if path == "" {
		return true
	}
	for _, denied := range resolveHardDenyList {
		if path == denied {
			return true
		}
		// path is parent of denied path (e.g., "" or "/" would match everything)
		if strings.HasSuffix(path, "/") && strings.HasPrefix(denied, path) {
			return true
		}
		// denied path is parent of path (e.g., deny "memory/" blocks "memory/shared/")
		if strings.HasSuffix(denied, "/") && strings.HasPrefix(path, denied) {
			return true
		}
	}
	return false
}
