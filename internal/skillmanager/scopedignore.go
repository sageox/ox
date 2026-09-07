package skillmanager

import (
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/sageoxignore"
)

// ScopedIgnoreFile is one agent directory ox writes into, plus the ignore rules
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
type ScopedIgnoreFile struct {
	// dir is repo-relative, e.g. ".claude".
	Dir string
	// entries are relative to dir, as gitignore patterns always are.
	Entries []string
}

// ScopedIgnoreFiles returns the ignore files for the agent directories ox writes.
//
// The sageox-team-* globs ship from day one even though that source is not
// implemented yet. Reserving a namespace before anything can occupy it is free,
// and it means the team-sync release never has to touch a customer's ignore file
// again — one fewer commit into somebody's repository, forever.
func ScopedIgnoreFiles() []ScopedIgnoreFile {
	skillGlob := "skills/" + CLIPrefix + "*/"
	teamSkillGlob := "skills/" + TeamPrefix + "*/"
	teamRuleGlob := "rules/" + TeamPrefix + "*"
	// Two rule patterns, not one. The primary rule is named exactly "ox-cli.md",
	// which "ox-cli-*" does NOT match — and the miss would be silent, leaving that
	// one file visible in every pull request. A bare "ox-cli*" would match, but it
	// would also claim a user's "ox-client-notes.md", so the exact name is listed
	// explicitly instead of widening the glob.
	ruleExact := "rules/" + CLIBase + ".md"
	ruleGlob := "rules/" + CLIPrefix + "*"

	return []ScopedIgnoreFile{
		{Dir: ".claude", Entries: []string{
			skillGlob, ruleExact, ruleGlob,
			// The command surface folded into skills in 0.15.0; the glob stays so a
			// repository still holding pre-fold files keeps them out of diffs until
			// the retirement sweep reaches it.
			"commands/" + CLIPrefix + "*",
			teamSkillGlob, teamRuleGlob,
		}},
		{Dir: ".agents", Entries: []string{skillGlob, teamSkillGlob}},
		{Dir: ".factory", Entries: []string{ruleExact, ruleGlob, teamRuleGlob}},
	}
}

// EnsureScopedIgnoreFiles writes the ox-managed ignore block into every agent
// directory that EXISTS in the repository, and returns the repo-relative paths it
// created or changed.
//
// Existence is the gate on purpose. Writing .agents/.gitignore into a repository
// that has no .agents/ directory would add vendor footprint to a project that
// never asked for that agent — the same complaint this whole rework answers.
// IgnoreFileResult is one ignore file ox wrote, and whether ox created it.
//
// The distinction is load-bearing for rollback: a file ox CREATED is removed on
// rollback, while a file that already existed must be restored to its previous
// contents. Treating an existing file as created would delete a file the user
// wrote, which is strictly worse than the failure being rolled back.
type IgnoreFileResult struct {
	Rel     string
	Created bool
}

func EnsureScopedIgnoreFiles(repoRoot string) ([]IgnoreFileResult, error) {
	return ensureScopedIgnoreFilesIn(repoRoot, nil)
}

// EnsureScopedIgnoreFilesForDirs also writes the block into agent directories
// that do NOT exist yet, named by force.
//
// Apply needs this because it writes the rule BEFORE materializing the files:
// at that moment `.claude/` or `.agents/` may not exist, and an existence gate
// would skip exactly the directory about to be filled with reserved-prefix
// files. The gate still applies everywhere else, so a repository never sprouts
// an agent directory it does not use.
func EnsureScopedIgnoreFilesForDirs(repoRoot string, force map[string]bool) ([]IgnoreFileResult, error) {
	return ensureScopedIgnoreFilesIn(repoRoot, force)
}

func ensureScopedIgnoreFilesIn(repoRoot string, force map[string]bool) ([]IgnoreFileResult, error) {
	var written []IgnoreFileResult
	for _, f := range ScopedIgnoreFiles() {
		dirPath := filepath.Join(repoRoot, f.Dir)
		// Lstat, not Stat: Stat follows symlinks, so a repository that makes an
		// agent directory a symlink could steer ox into writing outside repoRoot.
		// This mirrors skillmanager's refusal to write through a symlinked parent.
		info, err := os.Lstat(dirPath)
		switch {
		case err == nil && info.Mode()&os.ModeSymlink != 0:
			continue // symlinked agent dir: refuse, it can point outside the repo
		case err == nil && !info.IsDir():
			continue
		case err != nil:
			if !force[f.Dir] {
				continue // ox writes nothing here
			}
			if mkErr := os.MkdirAll(dirPath, 0o755); mkErr != nil {
				continue
			}
		}
		path := filepath.Join(dirPath, ".gitignore")
		if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			// A symlinked .gitignore would have ox edit whatever it points at.
			continue
		}
		changed, created, err := sageoxignore.EnsureBlock(path, f.Entries)
		if err != nil {
			return written, err
		}
		if changed {
			written = append(written, IgnoreFileResult{Rel: filepath.Join(f.Dir, ".gitignore"), Created: created})
		}
	}
	return written, nil
}
