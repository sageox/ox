package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/sageoxignore"
	"github.com/sageox/ox/internal/skillmanager"
)

// scopedIgnoreFile is one agent directory ox writes into, plus the ignore rules
// that keep ox's own files out of the customer's pull requests.
//
// The rules live in a .gitignore INSIDE each agent directory rather than in the
// repository root. Two constraints force that:
//
//   - A .gitignore under .sageox/ cannot reach .claude/ — a gitignore only governs
//     its own subtree — so the existing ox-owned ignore file is not an option.
//   - GH #732 ruled that ox must not write into the developer's root .gitignore.
//     "Don't pollute developers repos" was the words; the remedy was "put it in a
//     file ox owns". A four-line file inside a directory ox already populates is
//     the nearest thing to that which can actually reach these paths.
//
// The file is committed, deliberately: a teammate's fresh clone must inherit the
// rule BEFORE any ox file can appear there, or the first person to run ox in that
// clone commits vendor files by accident.
type scopedIgnoreFile struct {
	// dir is repo-relative, e.g. ".claude".
	dir string
	// entries are relative to dir, as gitignore patterns always are.
	entries []string
}

// scopedIgnoreFiles returns the ignore files for the agent directories ox writes.
//
// The sageox-team-* globs ship from day one even though that source is not
// implemented yet. Reserving a namespace before anything can occupy it is free,
// and it means the team-sync release never has to touch a customer's ignore file
// again — one fewer commit into somebody's repository, forever.
func scopedIgnoreFiles() []scopedIgnoreFile {
	skillGlob := "skills/" + skillmanager.CLIPrefix + "*/"
	teamSkillGlob := "skills/" + skillmanager.TeamPrefix + "*/"
	teamRuleGlob := "rules/" + skillmanager.TeamPrefix + "*"
	// Two rule patterns, not one. The primary rule is named exactly "ox-cli.md",
	// which "ox-cli-*" does NOT match — and the miss would be silent, leaving that
	// one file visible in every pull request. A bare "ox-cli*" would match, but it
	// would also claim a user's "ox-client-notes.md", so the exact name is listed
	// explicitly instead of widening the glob.
	ruleExact := "rules/" + skillmanager.CLIBase + ".md"
	ruleGlob := "rules/" + skillmanager.CLIPrefix + "*"

	return []scopedIgnoreFile{
		{dir: ".claude", entries: []string{
			skillGlob, ruleExact, ruleGlob,
			// The command surface folded into skills in 0.15.0; the glob stays so a
			// repository still holding pre-fold files keeps them out of diffs until
			// the retirement sweep reaches it.
			"commands/" + skillmanager.CLIPrefix + "*",
			teamSkillGlob, teamRuleGlob,
		}},
		{dir: ".agents", entries: []string{skillGlob, teamSkillGlob}},
		{dir: ".factory", entries: []string{ruleExact, ruleGlob, teamRuleGlob}},
	}
}

// ensureScopedIgnoreFiles writes the ox-managed ignore block into every agent
// directory that EXISTS in the repository, and returns the repo-relative paths it
// created or changed.
//
// Existence is the gate on purpose. Writing .agents/.gitignore into a repository
// that has no .agents/ directory would add vendor footprint to a project that
// never asked for that agent — the same complaint this whole rework answers.
// ignoreFileResult is one ignore file ox wrote, and whether ox created it.
//
// The distinction is load-bearing for rollback: a file ox CREATED is removed on
// rollback, while a file that already existed must be restored to its previous
// contents. Treating an existing file as created would delete a file the user
// wrote, which is strictly worse than the failure being rolled back.
type ignoreFileResult struct {
	Rel     string
	Created bool
}

func ensureScopedIgnoreFiles(repoRoot string) ([]ignoreFileResult, error) {
	var written []ignoreFileResult
	for _, f := range scopedIgnoreFiles() {
		dirPath := filepath.Join(repoRoot, f.dir)
		// Lstat, not Stat: Stat follows symlinks, so a repository that makes an
		// agent directory a symlink could steer ox into writing outside repoRoot.
		// This mirrors skillmanager's refusal to write through a symlinked parent.
		info, err := os.Lstat(dirPath)
		if err != nil || !info.IsDir() {
			continue // absent, a symlink, or not a directory: ox writes nothing here
		}
		path := filepath.Join(dirPath, ".gitignore")
		if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			// A symlinked .gitignore would have ox edit whatever it points at.
			continue
		}
		changed, created, err := sageoxignore.EnsureBlock(path, f.entries)
		if err != nil {
			return written, err
		}
		if changed {
			written = append(written, ignoreFileResult{Rel: filepath.Join(f.dir, ".gitignore"), Created: created})
		}
	}
	return written, nil
}

// isReservedManagedPath reports whether an absolute path inside repoRoot is an
// ox-owned, gitignored artifact — a skill directory, rule, or command carrying a
// reserved prefix.
//
// It is the gate that stops `ox init` from staging its own files. Until 0.15.0
// init force-staged everything the adapters wrote, deliberately defeating
// .gitignore (gitAddFilesForce, "force-stage files that may be gitignored") —
// which is the single mechanism that put vendor files into customers' pull
// requests. Ignoring a path is useless while something still force-adds it.
//
// The committed on-ramp (sageox) is NOT reserved, so it keeps being staged: it is
// the one file that must reach teammates and fresh clones.
func isReservedManagedPath(repoRoot, absPath string) bool {
	rel, err := filepath.Rel(repoRoot, absPath)
	if err != nil {
		return false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 3 {
		return false
	}
	switch parts[0] + "/" + parts[1] {
	case ".claude/skills", ".agents/skills":
		// <agent>/skills/<name>/SKILL.md — ownership is decided by the directory.
		return skillmanager.IsReservedName(parts[2])
	case ".claude/rules", ".factory/rules", ".agents/rules", ".claude/commands":
		// A rule or command file: ownership is decided by the basename. A nested
		// legacy path (rules/sageox/foo.md) is deliberately NOT reserved — it is
		// retired by the adapter's legacy sweep, not hidden from git.
		if len(parts) != 3 {
			return false
		}
		return skillmanager.IsReservedName(parts[2])
	}
	return false
}

// stageableInstalledPaths filters adapter-written paths down to the ones that
// belong in git.
//
// Reserved ox-cli-* / sageox-team-* artifacts are gitignored working-tree state,
// materialized locally on every machine from the binary's own catalog. Staging
// them is exactly what put vendor files into customers' pull requests on every
// release, and an ignore rule is useless while something still force-adds the
// path. Everything else — hook settings, the committed sageox on-ramp, instruction
// files — still needs to reach the index.
func stageableInstalledPaths(repoRoot string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if isReservedManagedPath(repoRoot, p) {
			continue
		}
		out = append(out, p)
	}
	return out
}
