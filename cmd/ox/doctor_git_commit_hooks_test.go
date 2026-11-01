package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: doctor blames prepare-commit-msg for another missing or invalid hook.
func TestCheckGitCommitHooks_NamesMissingHooks(t *testing.T) {
	skipIntegration(t) // Multiple real git worktree fixtures and hook installations.
	for _, tt := range []struct {
		name     string
		missing  []string
		invalid  bool
		worktree bool
		fix      bool
		message  string
	}{
		{name: "all_present"},
		{name: "post_commit_missing", missing: []string{postCommitHookName}, message: "post-commit hook not installed"},
		{name: "post_commit_invalid", missing: []string{postCommitHookName}, invalid: true, message: "post-commit hook not installed"},
		{name: "multiple_missing", missing: []string{postCommitHookName, postRewriteHookName}, message: "post-commit, post-rewrite hooks not installed"},
		{name: "pre_push_missing", missing: []string{prePushHookName}, message: "pre-push hook not installed"},
		{name: "worktree_all_present", worktree: true},
		{name: "worktree_repair", missing: []string{postCommitHookName}, worktree: true, fix: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mainRoot, worktreeRoot := gitHooksWorktree(t)
			root := mainRoot
			if tt.worktree {
				root = worktreeRoot
			}
			require.NoError(t, config.SaveProjectConfig(root, config.GetDefaultProjectConfig()))
			require.NoError(t, InstallGitHooks(mainRoot))
			for _, name := range tt.missing {
				path := filepath.Join(mainRoot, ".git", "hooks", name)
				if tt.invalid {
					require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n# incomplete hook\n"), 0755))
				} else {
					require.NoError(t, os.Remove(path))
				}
			}
			t.Chdir(root)
			result := checkGitCommitHooks(tt.fix)
			assert.False(t, result.skipped)
			assert.Equal(t, len(tt.missing) == 0 || tt.fix, result.passed)
			if result.passed {
				for _, spec := range allHookSpecs() {
					assert.Contains(t, result.message, spec.name)
				}
				assert.True(t, HasGitHooks(mainRoot))
				assert.True(t, HasGitHooks(worktreeRoot))
			} else {
				assert.Equal(t, tt.message, result.message)
				assert.NotContains(t, result.message, prepareCommitMsgHookName)
				assert.Contains(t, result.detail, "ox doctor --fix")
			}
		})
	}
}
