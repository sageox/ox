//go:build !short

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHasUserLevelOxPrime_RequiresMarker verifies that hasUserLevelOxPrime
// checks for the canonical <!-- ox:prime --> marker, not just "ox agent prime".
//
// Bug being caught: The old implementation checked for "ox agent prime" in the
// file content. Users who have the marker will pass. Users who only have
// "ox agent prime" text (without the HTML marker) will now fail. This test
// documents the intentional behavior change.
func TestHasUserLevelOxPrime_RequiresMarker(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "canonical marker present",
			content: "# My Config\n\n<!-- ox:prime --> Run SageOx `ox agent prime` on session start.\n",
			want:    true,
		},
		{
			name:    "marker as HTML comment only",
			content: "# Config\n<!-- ox:prime -->\n",
			want:    true,
		},
		{
			name:    "legacy text without marker",
			content: "# Config\nRun `ox agent prime` on session start\n",
			want:    false, // intentional: legacy text alone no longer counts
		},
		{
			name:    "empty file",
			content: "",
			want:    false,
		},
		{
			name:    "unrelated content",
			content: "# My CLAUDE.md\nSome instructions here.\n",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpHome := t.TempDir()
			claudeDir := filepath.Join(tmpHome, ".claude")
			require.NoError(t, os.MkdirAll(claudeDir, 0755))

			claudeMDPath := filepath.Join(claudeDir, "CLAUDE.md")
			require.NoError(t, os.WriteFile(claudeMDPath, []byte(tt.content), 0644))

			t.Setenv("HOME", tmpHome)
			t.Setenv("AGENT_ENV", "claude-code")

			got := hasUserLevelOxPrime()
			assert.Equal(t, tt.want, got, "hasUserLevelOxPrime() with content: %q", tt.content)
		})
	}
}

// TestHasUserLevelOxPrime_NoFile verifies behavior when CLAUDE.md doesn't exist.
func TestHasUserLevelOxPrime_NoFile(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("AGENT_ENV", "claude-code")

	assert.False(t, hasUserLevelOxPrime(), "should return false when ~/.claude/CLAUDE.md doesn't exist")
}

// TestCheckSessionStartHookBug_ProjectHooksPresent verifies the check warns when
// project hooks exist but no fallback marker is configured.
func TestCheckSessionStartHookBug_ProjectHooksPresent(t *testing.T) {
	// set up git repo BEFORE changing HOME (git needs real HOME for config)
	gitRoot := t.TempDir()
	initGitRepo(t, gitRoot)

	// install project hooks so HasProjectClaudeHooks returns true
	require.NoError(t, InstallProjectClaudeHooks(gitRoot))

	// now set fake HOME (no ~/.claude/CLAUDE.md = no user-level fallback)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	origDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(gitRoot))
	defer func() { _ = os.Chdir(origDir) }()

	result := checkSessionStartHookBug()

	// should warn because hooks exist but no AGENTS.md/CLAUDE.md fallback
	assert.False(t, result.skipped, "should not be skipped when project hooks exist")
	assert.True(t, result.warning, "should warn when no fallback is configured")
	assert.Contains(t, result.detail, "#10373", "should reference the Claude Code bug")
}

// TestCheckSessionStartHookBug_WithFallbackMarker verifies check passes when
// project hooks exist AND the ox:prime marker is in AGENTS.md.
func TestCheckSessionStartHookBug_WithFallbackMarker(t *testing.T) {
	// set up git repo BEFORE changing HOME
	gitRoot := t.TempDir()
	initGitRepo(t, gitRoot)
	require.NoError(t, InstallProjectClaudeHooks(gitRoot))

	// add the fallback marker to AGENTS.md
	agentsPath := filepath.Join(gitRoot, "AGENTS.md")
	require.NoError(t, os.WriteFile(agentsPath, []byte("# Agents\n\n"+OxPrimeLine+"\n"), 0644))

	// set fake HOME (hasUserLevelOxPrime will return false, but HasOxPrimeMarker checks project)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	origDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(gitRoot))
	defer func() { _ = os.Chdir(origDir) }()

	result := checkSessionStartHookBug()

	assert.True(t, result.passed, "should pass when fallback marker exists")
	assert.False(t, result.warning, "should not warn when fallback is configured")
}

// TestCheckSessionStartHookBug_NoProjectHooks verifies check is skipped when
// no project-level hooks are configured.
func TestCheckSessionStartHookBug_NoProjectHooks(t *testing.T) {
	// set up git repo BEFORE changing HOME
	gitRoot := t.TempDir()
	initGitRepo(t, gitRoot)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	origDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(gitRoot))
	defer func() { _ = os.Chdir(origDir) }()

	result := checkSessionStartHookBug()

	assert.True(t, result.skipped, "should be skipped when no project hooks exist")
}

// TestCheckProjectHookCommands_ValidHooks verifies that project-level hook
// validation passes for properly installed hooks.
func TestCheckProjectHookCommands_ValidHooks(t *testing.T) {
	gitRoot := t.TempDir()
	initGitRepo(t, gitRoot)

	// install valid project hooks
	require.NoError(t, InstallProjectClaudeHooks(gitRoot))

	origDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(gitRoot))
	defer func() { _ = os.Chdir(origDir) }()

	result := checkProjectHookCommands()
	assert.True(t, result.passed, "should pass for properly installed hooks, got message=%q detail=%q", result.message, result.detail)
	assert.False(t, result.warning, "should not warn for valid hooks")
}

// TestCheckProjectHookCommands_InvalidCommand verifies that project-level hook
// validation catches invalid ox commands.
func TestCheckProjectHookCommands_InvalidCommand(t *testing.T) {
	gitRoot := t.TempDir()
	initGitRepo(t, gitRoot)

	claudeDir := filepath.Join(gitRoot, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0755))

	// write settings with an invalid ox command
	settings := ClaudeSettings{
		Hooks: map[string][]ClaudeHookEntry{
			"SessionStart": {
				{
					Matcher: "",
					Hooks: []ClaudeHook{
						{Type: "command", Command: "ox prime"},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(settings, "", "  ")
	settingsPath := filepath.Join(claudeDir, "settings.local.json")
	require.NoError(t, os.WriteFile(settingsPath, data, 0644))

	origDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(gitRoot))
	defer func() { _ = os.Chdir(origDir) }()

	result := checkProjectHookCommands()
	assert.True(t, result.warning, "should warn about invalid command 'ox prime'")
	assert.Contains(t, result.message, "1 invalid", "should report 1 invalid command")
}

// initGitRepo creates a minimal git repo in the given directory.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.name", "Dev"},
		{"git", "config", "user.email", "dev@example.com"},
		{"git", "config", "commit.gpgsign", "false"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "failed: %v: %s", args, string(out))
	}

	// create an initial commit so git operations work
	readme := filepath.Join(dir, "README.md")
	require.NoError(t, os.WriteFile(readme, []byte("repo"), 0644))
	cmd := exec.Command("git", "add", "README.md")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add: %s", string(out))
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = dir
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit: %s", string(out))
}
