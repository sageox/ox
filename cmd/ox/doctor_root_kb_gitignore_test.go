//go:build !short

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/sageoxignore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kbCleanupRepo builds a git repo whose root .gitignore holds rootContent
// and whose .sageox/.gitignore holds sageoxContent, then chdirs into it so
// the CWD-based findGitRoot() resolves there. Either content may be "" to
// mean "this file does not exist".
func kbCleanupRepo(t *testing.T, rootContent, sageoxContent string) string {
	t.Helper()
	repo := testGitRepo(t)
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".sageox"), 0o755))

	if rootContent != "" {
		require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(rootContent), 0o644))
	}
	if sageoxContent != "" {
		require.NoError(t, os.WriteFile(filepath.Join(repo, ".sageox", ".gitignore"), []byte(sageoxContent), 0o644))
	}
	t.Chdir(repo)
	return repo
}

func readRootGitignore(t *testing.T, repo string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(repo, ".gitignore"))
	require.NoError(t, err)
	return string(body)
}

// TestCheckRootKBGitignore_RemovesStaleLine is the GH #732 cleanup for
// installs created before the rule moved into .sageox/.gitignore.
func TestCheckRootKBGitignore_RemovesStaleLine(t *testing.T) {
	repo := kbCleanupRepo(t,
		"node_modules/\n.sageox/kb/\ndist/\n",
		"cache/\nkb/\n",
	)

	result := checkRootKBGitignoreLine(true)

	assert.True(t, result.passed, "message=%q detail=%q", result.message, result.detail)
	assert.Equal(t, "node_modules/\ndist/\n", readRootGitignore(t, repo),
		"only the ox-written line goes; the developer's rules and ordering stay")
}

// TestCheckRootKBGitignore_WarnsWithoutFix pins the FixLevelSuggested
// behavior: a bare `ox doctor` reports the pollution but must not edit a
// file the developer owns. Silently editing their .gitignore is the exact
// complaint that opened #732 — the cleanup must not repeat it.
func TestCheckRootKBGitignore_WarnsWithoutFix(t *testing.T) {
	original := "node_modules/\n.sageox/kb/\ndist/\n"
	repo := kbCleanupRepo(t, original, "cache/\nkb/\n")

	result := checkRootKBGitignoreLine(false)

	assert.True(t, result.warning, "should surface as a warning, not a hard failure")
	assert.False(t, result.passed)
	assert.Equal(t, original, readRootGitignore(t, repo),
		"a non-fix run must leave the file byte-identical")
}

// TestCheckRootKBGitignore_SkipsWhenReplacementMissing is the safety gate.
// Removing the root line before `kb/` exists in .sageox/.gitignore would
// leave the symlink directory genuinely untracked-but-not-ignored, so the
// check must decline rather than guess.
func TestCheckRootKBGitignore_SkipsWhenReplacementMissing(t *testing.T) {
	original := "node_modules/\n.sageox/kb/\n"

	for _, tc := range []struct {
		name          string
		sageoxContent string
	}{
		{"no .sageox/.gitignore at all", ""},
		{"present but lacking kb/", "cache/\nlogs/\n"},
		{"kb/ only as a comment", "cache/\n# kb/\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := kbCleanupRepo(t, original, tc.sageoxContent)

			result := checkRootKBGitignoreLine(true)

			assert.True(t, result.skipped, "must decline: %q", result.message)
			assert.Equal(t, original, readRootGitignore(t, repo),
				"the root .gitignore must be untouched until the replacement is verified")
		})
	}
}

// TestCheckRootKBGitignore_PassesOnCleanRepo covers the overwhelmingly
// common case — a repo that never had the line, or never had a root
// .gitignore. Neither should produce noise in doctor output.
func TestCheckRootKBGitignore_PassesOnCleanRepo(t *testing.T) {
	t.Run("root .gitignore without the line", func(t *testing.T) {
		kbCleanupRepo(t, "node_modules/\ndist/\n", "cache/\nkb/\n")
		result := checkRootKBGitignoreLine(true)
		assert.True(t, result.passed)
		assert.Equal(t, "clean", result.message)
	})

	t.Run("no root .gitignore at all", func(t *testing.T) {
		repo := kbCleanupRepo(t, "", "cache/\nkb/\n")
		result := checkRootKBGitignoreLine(true)
		assert.True(t, result.passed)

		_, err := os.Stat(filepath.Join(repo, ".gitignore"))
		assert.True(t, os.IsNotExist(err), "must not create a .gitignore just to clean it")
	})
}

// TestCheckRootKBGitignore_IsIdempotent guards against doctor dirtying the
// worktree on every run.
func TestCheckRootKBGitignore_IsIdempotent(t *testing.T) {
	repo := kbCleanupRepo(t, "node_modules/\n.sageox/kb/\n", "cache/\nkb/\n")

	require.True(t, checkRootKBGitignoreLine(true).passed)
	first := readRootGitignore(t, repo)

	result := checkRootKBGitignoreLine(true)
	assert.True(t, result.passed)
	assert.Equal(t, "clean", result.message)
	assert.Equal(t, first, readRootGitignore(t, repo), "second run must be a true no-op")
}

// TestCheckRootKBGitignore_LeavesNeighboringSageoxRulesAlone restates the
// exact-match safety property at the doctor level: a user who ignores the
// whole .sageox directory, or who wrote a variant pattern, keeps it.
func TestCheckRootKBGitignore_LeavesNeighboringSageoxRulesAlone(t *testing.T) {
	original := ".sageox/\n.sageox/kb\n/.sageox/kb/\n!.sageox/kb/\n.sageox/kb/*\n"
	repo := kbCleanupRepo(t, original, "cache/\nkb/\n")

	result := checkRootKBGitignoreLine(true)

	assert.True(t, result.passed)
	assert.Equal(t, "clean", result.message, "none of these are the line we wrote")
	assert.Equal(t, original, readRootGitignore(t, repo))
}

// TestSageoxGitignoreHasKBRule covers the gate helper directly, including
// the comment case that must not count as the rule being present.
func TestSageoxGitignoreHasKBRule(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    bool
	}{
		{"present", "cache/\nkb/\nlogs/\n", true},
		{"absent", "cache/\nlogs/\n", false},
		{"commented", "cache/\n# kb/\n", false},
		{"negated", "cache/\n!kb/\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(repo, ".sageox"), 0o755))
			require.NoError(t, os.WriteFile(
				filepath.Join(repo, ".sageox", ".gitignore"), []byte(tc.content), 0o644))

			assert.Equal(t, tc.want, sageoxGitignoreHasKBRule(repo))
		})
	}

	t.Run("missing file", func(t *testing.T) {
		assert.False(t, sageoxGitignoreHasKBRule(t.TempDir()),
			"an unverifiable replacement must never authorize the cleanup")
	})
}

// TestSageoxGitignoreContentCarriesKBRule ties the shipped template to the
// gate: if kb/ ever drops out of sageoxGitignoreContent, the cleanup would
// silently stop running rather than fail loudly.
func TestSageoxGitignoreContentCarriesKBRule(t *testing.T) {
	assert.True(t, sageoxignore.HasEntry(sageoxGitignoreContent, sageoxignore.KBEntry),
		"the .sageox/.gitignore template must ship the kb/ rule")
	assert.Contains(t, requiredGitignoreEntries, sageoxignore.KBEntry,
		"kb/ must be a required entry so existing installs self-heal via mergeGitignoreEntries")
}
