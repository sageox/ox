//go:build !short

package main

import (
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
// root cause B: init can write markers into GEMINI.md, .cursorrules and
// friends, but stageAll only ever knew about AGENTS.md and CLAUDE.md.
func TestStageAll_StagesNonClaudeInstructionFiles(t *testing.T) {
	repo := testGitRepo(t)
	// .gemini/ presence is what makes GEMINI.md a detected target.
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".gemini"), 0o755))
	writeFileAt(t, repo, "GEMINI.md", "# instructions\n")
	writeFileAt(t, repo, "AGENTS.md", "# instructions\n")

	tracker := newInitTracker(repo)
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
