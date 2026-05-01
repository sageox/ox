//go:build !short

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	claude "github.com/sageox/ox/internal/hooks/claude"
)

// TestInstallProjectClaudeHooks_ProducesValidSettings is the focused
// regression test for ox-9p1v.
//
// Story: the 2026-04-30 demo-ice-cream incident shipped a
// .claude/settings.json that current Claude Code rejected wholesale —
// every hook silently disabled. The bead asks for an end-to-end smoke
// test that would have failed before the broken file ever reached
// users. Full daemon-driven init→session-stop is a heavier harness
// (own bead — see follow-up notes); the LOAD-BEARING property the
// incident exposed is "the bytes ox writes for hooks are bytes Claude
// Code accepts." Pinning that contract closes the user-visible gap
// even before the broader smoke is in place.
//
// Failure prevented: a future change to InstallProjectClaudeHooks,
// MarshalSettings, ClaudeHookEntry shape, or the hook-command
// template emits a settings.json that Claude Code's parser rejects.
// The validator (internal/hooks/claude/validate.go) implements the
// same contract Claude Code enforces; if this test fails, the writer
// has drifted from the consumer.
func TestInstallProjectClaudeHooks_ProducesValidSettings(t *testing.T) {
	gitRoot := testGitRepo(t)
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	if err := os.Chdir(gitRoot); err != nil {
		t.Fatal(err)
	}

	// Seed the .claude dir as ox init would have done by this point.
	if err := os.MkdirAll(filepath.Join(gitRoot, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := InstallProjectClaudeHooks(gitRoot); err != nil {
		t.Fatalf("InstallProjectClaudeHooks: %v", err)
	}

	settingsPath := filepath.Join(gitRoot, ".claude", "settings.json")
	body, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}

	if err := claude.ValidateSettingsBytes(body); err != nil {
		t.Errorf("ox init produced a settings.json Claude Code would reject:\n%s\nerror: %v",
			body, err)
	}
}

// TestInstallProjectClaudeHooks_PreservesNonHookKeys guards a related
// invariant from the same incident: the hook installer must not drop
// pre-existing top-level keys like "permissions". Claude Code reads
// the same file for many concerns; clobbering one in service of
// another leaves the user with a different broken setup than the
// 2026-04-30 case but with the same blast radius (hooks or
// permissions silently disabled).
func TestInstallProjectClaudeHooks_PreservesNonHookKeys(t *testing.T) {
	gitRoot := testGitRepo(t)
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	if err := os.Chdir(gitRoot); err != nil {
		t.Fatal(err)
	}
	settingsDir := filepath.Join(gitRoot, ".claude")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(settingsDir, "settings.json")
	// Pre-existing "permissions" block — common in user repos.
	preexisting := `{
  "permissions": {"allow": ["Bash(git:*)", "Bash(go:*)"]},
  "hooks": {}
}`
	if err := os.WriteFile(settingsPath, []byte(preexisting), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := InstallProjectClaudeHooks(gitRoot); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := claude.ValidateSettingsBytes(body); err != nil {
		t.Errorf("post-install file rejected by validator:\n%s\nerror: %v", body, err)
	}
	// Must still contain the original permissions allowlist.
	got := string(body)
	if !strings.Contains(got, `"Bash(git:*)"`) || !strings.Contains(got, `"Bash(go:*)"`) {
		t.Errorf("hooks install dropped pre-existing permissions:\n%s", got)
	}
}
