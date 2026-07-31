//go:build !short

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stagedFiles returns the repo-relative paths currently in the git index.
func stagedFiles(t *testing.T, repo string) []string {
	t.Helper()
	cmd := exec.Command("git", "diff", "--cached", "--name-only")
	cmd.Dir = repo
	out, err := cmd.Output()
	require.NoError(t, err)

	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths
}

// writeFileAt creates parents and writes content, returning the abs path.
func writeFileAt(t *testing.T, repo, rel, content string) string {
	t.Helper()
	abs := filepath.Join(repo, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
	require.NoError(t, os.WriteFile(abs, []byte(content), 0o644))
	return abs
}

// agentInstallTree lays down the files a real `ox init` produces for
// Claude Code, and returns them as repo-relative paths.
func agentInstallTree(t *testing.T, repo string) []string {
	t.Helper()
	rels := []string{
		".claude/settings.json",
		".claude/commands/ox.md",
		".claude/skills/ox-plan/SKILL.md",
		".claude/rules/ox.md",
		".claude/rules/sageox/use-team-context.md",
	}
	for _, rel := range rels {
		writeFileAt(t, repo, rel, "x")
	}
	return rels
}

// TestStageAll_StagesTheClaudeTree is the GH #731 acceptance test: after
// init writes .claude/**, those files must actually be in the git index,
// or the user's "git commit && git push" silently ships nothing.
//
// Nothing in this package asserted anything about the index before this.
func TestStageAll_StagesTheClaudeTree(t *testing.T) {
	repo := testGitRepo(t)
	rels := agentInstallTree(t, repo)

	tracker := newInitTracker(repo)
	for _, rel := range rels {
		tracker.trackForceStage(filepath.Join(repo, rel))
	}
	tracker.stageAll()

	staged := stagedFiles(t, repo)
	for _, rel := range rels {
		assert.Contains(t, staged, rel, "%s must reach the index", rel)
	}
}

// TestStageAll_OneBadPathDoesNotZeroTheBatch is the regression test for
// the actual #731 mechanism. git validates every pathspec up front and
// fails the whole invocation on the first bad one, so a single junk entry
// used to leave NOTHING staged — including the valid files beside it.
//
// This fails against the pre-fix batched `git add`.
func TestStageAll_OneBadPathDoesNotZeroTheBatch(t *testing.T) {
	repo := testGitRepo(t)
	rels := agentInstallTree(t, repo)

	tracker := newInitTracker(repo)
	for _, rel := range rels {
		tracker.trackForceStage(filepath.Join(repo, rel))
	}
	// exactly the shape adapters used to emit: a skills-dir-relative name
	// joined onto the repo root, pointing at a file that does not exist.
	tracker.trackForceStage(filepath.Join(repo, "ox-plan/SKILL.md"))
	tracker.trackForceStage(filepath.Join(repo, "ox.md"))

	tracker.stageAll()

	staged := stagedFiles(t, repo)
	assert.Contains(t, staged, ".claude/settings.json",
		"the valid files must still be staged despite a bad pathspec in the same batch")
	for _, rel := range rels {
		assert.Contains(t, staged, rel)
	}
}

// TestGitAdd_ReportsWhichPathsFailed — the old implementation used
// cmd.Run() and discarded git's stderr, so the warning said only
// "Could not stage hook/command files" and nobody could diagnose it.
func TestGitAdd_ReportsWhichPathsFailed(t *testing.T) {
	repo := testGitRepo(t)
	good := writeFileAt(t, repo, ".claude/settings.json", "{}")

	err := gitAddFilesForce(repo, []string{good, filepath.Join(repo, "nope.md")})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope.md", "the failing path must be named")
	assert.NotContains(t, err.Error(), "settings.json", "the path that worked must not be blamed")
	assert.Contains(t, stagedFiles(t, repo), ".claude/settings.json",
		"the good path is staged even though the call reports an error")
}

// TestGitAdd_UsesGitRootNotProcessCwd pins cmd.Dir. The helpers used to
// rely on the process working directory, which is only accidentally
// correct and impossible to test.
func TestGitAdd_UsesGitRootNotProcessCwd(t *testing.T) {
	repo := testGitRepo(t)
	writeFileAt(t, repo, ".claude/settings.json", "{}")

	t.Chdir(t.TempDir()) // somewhere else entirely, not even a git repo

	require.NoError(t, gitAddFilesForce(repo, []string{filepath.Join(repo, ".claude/settings.json")}))
	assert.Contains(t, stagedFiles(t, repo), ".claude/settings.json")
}

// TestStageAll_StagesNonClaudeInstructionFiles covers the second half of
// root cause B: init writes ox:prime markers into GEMINI.md, .cursorrules
// and friends, but stageAll only ever knew about AGENTS.md and CLAUDE.md,
// so those users' modified files were never committed.
//
// runInit registers each file it actually wrote via trackForceStage (see
// the EnsureInstructionFileMarkers loop) — staging by *detection* was
// rejected because presence doesn't imply ox touched it.
func TestStageAll_StagesNonClaudeInstructionFiles(t *testing.T) {
	repo := testGitRepo(t)
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".gemini"), 0o755))
	gemini := writeFileAt(t, repo, "GEMINI.md", "# instructions\n")
	agents := writeFileAt(t, repo, "AGENTS.md", "# instructions\n")

	tracker := newInitTracker(repo)
	// what the marker-injection loop does for each file it wrote
	tracker.trackForceStage(gemini)
	tracker.trackForceStage(agents)
	tracker.stageAll()

	staged := stagedFiles(t, repo)
	assert.Contains(t, staged, "GEMINI.md",
		"a Gemini user's ox-modified instruction file must be staged too")
	assert.Contains(t, staged, "AGENTS.md")
}

// TestNormalizeAdapterFilesWritten covers the boundary defense. Adapters
// are separate binaries, so ox cannot enforce the FilesWritten contract —
// it can only refuse to hand git something broken.
func TestNormalizeAdapterFilesWritten(t *testing.T) {
	repo := testGitRepo(t)
	settings := writeFileAt(t, repo, ".claude/settings.json", "{}")
	skill := writeFileAt(t, repo, ".claude/skills/ox-plan/SKILL.md", "x")
	outside := filepath.Join(t.TempDir(), "elsewhere.json")
	require.NoError(t, os.WriteFile(outside, []byte("{}"), 0o644))

	tests := []struct {
		name string
		in   []string
		want []string
		why  string
	}{
		{
			"repo-relative existing", []string{".claude/settings.json"}, []string{settings},
			"the convention the built-in Claude hook installer uses",
		},
		{
			"absolute inside repo", []string{settings}, []string{settings},
			"codex/droid/gemini adapters return absolute paths — also valid",
		},
		{
			"already-absolute must not be re-joined", []string{settings}, []string{settings},
			"double-joining produced <root><root>/... and broke the whole batch",
		},
		{
			"skills-dir-relative is dropped", []string{"ox-plan/SKILL.md"}, nil,
			"resolves to <root>/ox-plan/SKILL.md which does not exist; dropping " +
				"beats guessing, and the adapter fix is what makes it arrive correctly",
		},
		{
			"rules-dir-relative is dropped", []string{"ox.md", "sageox/use-team-context.md"}, nil,
			"same class as above",
		},
		{
			"path outside the repo is dropped", []string{outside}, nil,
			"user-scope installs (~/.codex/hooks.json) are real but git cannot stage them",
		},
		{
			"traversal escape is dropped", []string{"../escape.json"}, nil,
			"containment is checked after joining, not by string prefix",
		},
		{"nonexistent is dropped", []string{".claude/ghost.json"}, nil, "never stage what was not written"},
		{"empty and blank are skipped", []string{"", "   "}, nil, "defensive against sloppy adapters"},
		{
			"duplicates collapse",
			[]string{".claude/settings.json", settings, ".claude/settings.json"},
			[]string{settings},
			"the same file reported by two capabilities must be staged once",
		},
		{
			"forward slashes are accepted",
			[]string{".claude/skills/ox-plan/SKILL.md"}, []string{skill},
			"adapters emit JSON paths with forward slashes regardless of platform",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeAdapterFilesWritten(repo, tt.in)
			if tt.want == nil {
				assert.Empty(t, got, tt.why)
				return
			}
			assert.Equal(t, tt.want, got, tt.why)
		})
	}
}

// TestNormalizeAdapterFilesWritten_KeepsGoodEntriesAlongsideBadOnes is the
// property that actually mattered in #731: a mixed list must not be
// discarded wholesale just because part of it is malformed.
func TestNormalizeAdapterFilesWritten_KeepsGoodEntriesAlongsideBadOnes(t *testing.T) {
	repo := testGitRepo(t)
	settings := writeFileAt(t, repo, ".claude/settings.json", "{}")
	command := writeFileAt(t, repo, ".claude/commands/ox.md", "x")

	got := normalizeAdapterFilesWritten(repo, []string{
		".claude/settings.json",  // valid, repo-relative
		"ox-plan/SKILL.md",       // junk, skills-dir-relative
		command,                  // valid, absolute
		"ox.md",                  // junk, rules-dir-relative
		"/tmp/definitely/absent", // junk, absolute and outside
	})

	assert.Equal(t, []string{settings, command}, got,
		"the two real files survive; only the junk is dropped")
}

// TestNormalizeAdapterFilesWritten_RejectsRepoRootAndDirs covers the
// over-staging hazard. An adapter entry of "." — or gitRoot absolute —
// passes both containment and Lstat, and `git add --force -- <root>`
// stages the ENTIRE worktree, sweeping in every unrelated change the user
// had in progress. That is the exact outcome init's own next-steps note
// warns against, so it must never come from an adapter's return value.
func TestNormalizeAdapterFilesWritten_RejectsRepoRootAndDirs(t *testing.T) {
	repo := testGitRepo(t)
	settings := writeFileAt(t, repo, ".claude/settings.json", "{}")

	tests := []struct {
		name  string
		entry string
		why   string
	}{
		{"dot", ".", "resolves to the repo root"},
		{"dot-slash", "./", "same, trailing separator"},
		{"absolute root", repo, "adapter returned gitRoot itself"},
		{"nested directory", ".claude", "staging a dir pulls in files ox never wrote"},
		{"deep directory", ".claude/skills", "same, one level down"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Empty(t, normalizeAdapterFilesWritten(repo, []string{tt.entry}), tt.why)
		})
	}

	// and a mixed list must still keep the legitimate file
	got := normalizeAdapterFilesWritten(repo, []string{".", ".claude", ".claude/settings.json"})
	assert.Equal(t, []string{settings}, got,
		"rejecting the root and the dir must not take the real file with them")
}

// TestStageAll_RepoRootEntryDoesNotStageEverything is the end-to-end
// version: an adapter reporting "." must not cause init to stage a file
// the user was independently working on.
func TestStageAll_RepoRootEntryDoesNotStageEverything(t *testing.T) {
	repo := testGitRepo(t)
	writeFileAt(t, repo, ".claude/settings.json", "{}")
	// an unrelated in-progress edit the user has NOT asked to commit
	writeFileAt(t, repo, "src/work_in_progress.go", "package main\n")

	tracker := newInitTracker(repo)
	for _, p := range normalizeAdapterFilesWritten(repo, []string{".", ".claude/settings.json"}) {
		tracker.trackForceStage(p)
	}
	tracker.stageAll()

	staged := stagedFiles(t, repo)
	assert.Contains(t, staged, ".claude/settings.json")
	assert.NotContains(t, staged, "src/work_in_progress.go",
		"a rogue adapter entry must never sweep the user's unrelated work into the commit")
}

// TestStageAll_DoesNotStageUntouchedInstructionFiles — a file merely
// EXISTING says nothing about whether ox changed it. Staging every
// detected instruction file would sweep a user's own uncommitted edits to
// GEMINI.md / .cursorrules into the init commit.
func TestStageAll_DoesNotStageUntouchedInstructionFiles(t *testing.T) {
	repo := testGitRepo(t)
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".gemini"), 0o755))
	writeFileAt(t, repo, "GEMINI.md", "# the user's own in-progress edit\n")
	touched := writeFileAt(t, repo, "AGENTS.md", "# ox wrote a marker here\n")

	tracker := newInitTracker(repo)
	// only the file ox actually wrote to is registered for staging
	tracker.trackForceStage(touched)
	tracker.stageAll()

	staged := stagedFiles(t, repo)
	assert.Contains(t, staged, "AGENTS.md", "the file ox wrote must be staged")
	assert.NotContains(t, staged, "GEMINI.md",
		"a detected-but-untouched instruction file must not be staged — those are the user's edits")
}

// TestRollback_UnstagesEverythingItStaged — rollback restores file CONTENT,
// but stageAll runs before API registration, so without unstaging the
// user's next `git commit` silently includes changes they were just told
// were rolled back. The root .gitignore cleanup is the sharp case: a
// pre-existing tracked file that ox modified.
func TestRollback_UnstagesEverythingItStaged(t *testing.T) {
	repo := testGitRepo(t)

	// a tracked file with committed content
	gitignore := filepath.Join(repo, ".gitignore")
	require.NoError(t, os.WriteFile(gitignore, []byte("node_modules/\n.sageox/kb/\n"), 0o644))
	for _, args := range [][]string{{"add", ".gitignore"}, {"commit", "-m", "baseline"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		require.NoError(t, cmd.Run(), "git %v", args)
	}

	tracker := newInitTracker(repo)
	// ox modifies it (the #732 cleanup), snapshotting first
	tracker.trackModifiedFile(gitignore)
	require.NoError(t, os.WriteFile(gitignore, []byte("node_modules/\n"), 0o644))
	tracker.trackForceStage(gitignore)

	// ...and creates a new file
	created := writeFileAt(t, repo, ".claude/settings.json", "{}")
	tracker.trackCreatedFile(created)
	tracker.trackForceStage(created)

	tracker.stageAll()
	require.NotEmpty(t, stagedFiles(t, repo), "precondition: something is staged")

	tracker.rollback(true)

	assert.Empty(t, stagedFiles(t, repo),
		"rollback must leave a clean index — restoring content while leaving the edit "+
			"staged means the next commit silently ships it")

	body, err := os.ReadFile(gitignore)
	require.NoError(t, err)
	assert.Equal(t, "node_modules/\n.sageox/kb/\n", string(body), "content restored too")
}

// TestRollback_PreservesPreExistingStagedChanges — a user may have already
// `git add`ed their own edits to a file ox is about to touch. Rollback
// restores the working tree from a content snapshot, but the INDEX needs
// its own snapshot: a plain `git reset HEAD` would silently throw the
// user's staged work away while leaving the working tree untouched.
func TestRollback_PreservesPreExistingStagedChanges(t *testing.T) {
	repo := testGitRepo(t)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		require.NoError(t, cmd.Run(), "git %v", args)
	}

	agents := filepath.Join(repo, "AGENTS.md")
	require.NoError(t, os.WriteFile(agents, []byte("# committed\n"), 0o644))
	git("add", "AGENTS.md")
	git("commit", "-m", "baseline")

	// the user stages their OWN edit, before ox runs
	require.NoError(t, os.WriteFile(agents, []byte("# committed\n# the user's staged work\n"), 0o644))
	git("add", "AGENTS.md")

	stagedBlob := func() string {
		cmd := exec.Command("git", "ls-files", "--stage", "--", "AGENTS.md")
		cmd.Dir = repo
		out, err := cmd.Output()
		require.NoError(t, err)
		return strings.TrimSpace(string(out))
	}
	before := stagedBlob()
	require.NotEmpty(t, before)

	// ox now modifies and stages the same file, then rolls back
	tracker := newInitTracker(repo)
	tracker.trackModifiedFile(agents)
	require.NoError(t, os.WriteFile(agents, []byte("# committed\n# ox marker\n"), 0o644))
	tracker.trackForceStage(agents)
	tracker.stageAll()
	require.NotEqual(t, before, stagedBlob(), "precondition: ox changed the index entry")

	tracker.rollback(true)

	assert.Equal(t, before, stagedBlob(),
		"the user's pre-existing staged edit must survive rollback — resetting to HEAD "+
			"would destroy work ox was never asked to touch")

	body, err := os.ReadFile(agents)
	require.NoError(t, err)
	assert.Equal(t, "# committed\n# the user's staged work\n", string(body),
		"working tree restored to the user's content, not HEAD's")
}

// TestUnstageAll_RestoresConflictStagesWithoutLeavingUnmerged covers the
// index-restore path for a path that was ALREADY CONFLICTED when init ran.
//
// stageAll's `git add --force` collapses such a path to a single stage-0
// entry. Replaying the recorded stages 1-3 without first clearing stage 0
// leaves both in the index, so rollback hands the user an unmerged path
// (`git status` shows UU) rather than the conflict they started with.
func TestUnstageAll_RestoresConflictStagesWithoutLeavingUnmerged(t *testing.T) {
	repo := testGitRepo(t)
	rel := "conflicted.txt"
	abs := writeFileAt(t, repo, rel, "working copy\n")

	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return string(out)
	}

	// Build three real blobs and install them as conflict stages 1/2/3.
	stage := func(n int, content string) string {
		h := exec.Command("git", "hash-object", "-w", "--stdin")
		h.Dir = repo
		h.Stdin = strings.NewReader(content)
		out, err := h.Output()
		require.NoError(t, err)
		sha := strings.TrimSpace(string(out))
		return fmt.Sprintf("100644 %s %d\t%s", sha, n, rel)
	}
	conflict := strings.Join([]string{stage(1, "base\n"), stage(2, "ours\n"), stage(3, "theirs\n")}, "\n")

	install := exec.Command("git", "update-index", "--index-info")
	install.Dir = repo
	install.Stdin = strings.NewReader(conflict + "\n")
	require.NoError(t, install.Run())
	require.Contains(t, git("status", "--porcelain"), "UU "+rel, "precondition: path must start out conflicted")

	// init records the pre-existing index entry, then stages over it.
	tracker := newInitTracker(repo)
	tracker.trackForceStage(abs)
	tracker.stageAll()
	require.NotContains(t, git("status", "--porcelain"), "UU "+rel,
		"precondition: git add --force must have collapsed the conflict to stage 0")

	tracker.unstageAll(true)

	assert.Contains(t, git("status", "--porcelain"), "UU "+rel,
		"rollback must restore the conflict exactly as it was, not leave a half-merged index")
	assert.Equal(t, 3, strings.Count(git("ls-files", "--stage", "--", rel), rel),
		"exactly stages 1-3 should remain — a leftover stage 0 means the index is still unmerged")
}

// TestStageAll_StagesClaudePrimaryInstructionFiles is the regression test
// for the files ox writes via injectOxPrime. The later marker pass reports
// them as alreadyPresent (injectOxPrime just wrote the marker), so gating
// staging on marker results alone dropped AGENTS.md and CLAUDE.md from the
// init commit — the GH #731 symptom for the two files it matters most for.
func TestStageAll_StagesClaudePrimaryInstructionFiles(t *testing.T) {
	repo := testGitRepo(t)
	writeFileAt(t, repo, "AGENTS.md", "# instructions\n")
	writeFileAt(t, repo, "CLAUDE.md", "# instructions\n")

	tracker := newInitTracker(repo)
	// what runInit records for a non-alreadyPresent injection result
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		tracker.trackForceStage(filepath.Join(repo, name))
	}
	tracker.stageAll()

	staged := stagedFiles(t, repo)
	assert.Contains(t, staged, "AGENTS.md")
	assert.Contains(t, staged, "CLAUDE.md")
}
