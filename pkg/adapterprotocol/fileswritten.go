package adapterprotocol

import (
	"path/filepath"
	"strings"
)

// FilesWritten contract
//
// Every install-* response carries a FilesWritten list. ox stages those
// paths into git so the files an adapter creates actually reach the
// user's commit.
//
// # The contract
//
// Each entry MUST be either an absolute path, or a path relative to the
// RepoRoot the adapter was given. Nothing else. In particular it must not
// be relative to some intermediate directory the adapter happens to be
// writing into (`.claude/skills/`, `.claude/rules/`), because ox has no
// way to know what that directory was.
//
// ox normalizes and verifies every entry, and silently drops any that do
// not resolve to an existing path inside the repo. A dropped entry is not
// staged, so violating the contract means the adapter's files quietly
// never get committed.
//
// # Why the rule exists
//
// GH #731: four in-tree adapters used four different conventions —
// repo-relative, skills-dir-relative, rules-dir-relative, and absolute.
// ox joined them all onto RepoRoot, producing paths like
// `<root>/ox-plan/SKILL.md` that did not exist. `git add` validates every
// pathspec up front and fails the whole invocation on the first bad one,
// so a single malformed entry stopped ox from staging *anything* —
// including the valid `.claude/settings.json`. On a fresh `ox init` that
// meant the entire `.claude/` tree was created and then never committed.
//
// Adapters are separate binaries resolved at runtime, so ox cannot force
// compliance; it defends at the boundary instead. Use RepoRelativePaths
// below rather than hand-rolling the conversion.

// RepoRelativePaths converts names that are relative to baseDir into
// paths relative to repoRoot, satisfying the FilesWritten contract.
//
// baseDir may be absolute or relative to repoRoot.
//
// A path that does not live under repoRoot is returned ABSOLUTE, not as a
// `../..` traversal. Both forms are technically valid per the contract,
// but the absolute form is unambiguous and survives being resolved from
// any working directory — whereas a traversal only means the right thing
// relative to repoRoot, and ox's staging boundary drops it either way
// (git cannot stage a file outside the repo). User-scope installs like
// ~/.codex/hooks.json are the real case here.
func RepoRelativePaths(repoRoot, baseDir string, names []string) []string {
	if !filepath.IsAbs(baseDir) {
		baseDir = filepath.Join(repoRoot, baseDir)
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		abs := name
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(baseDir, name)
		}
		rel, err := filepath.Rel(repoRoot, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			out = append(out, filepath.Clean(abs))
			continue
		}
		out = append(out, rel)
	}
	return out
}
