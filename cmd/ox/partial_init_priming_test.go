//go:build !short

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPartialInitPrimingMatrix verifies that ox agent prime is reachable
// across every partial ox init state in Claude Code.
//
// Three independent priming paths exist:
//  1. Hook       – SessionStart hook in .claude/settings.local.json
//  2. Header     – ox:prime-check marker at top of AGENTS.md / CLAUDE.md
//  3. Footer     – ox:prime marker at bottom of AGENTS.md / CLAUDE.md
//  4. UserMarker – ox:prime in ~/.claude/CLAUDE.md (global fallback)
//
// Each test case sets up a temp directory simulating a partial init state,
// then verifies: (a) which paths are available before anti-entropy,
// (b) that EnsureOxPrimeMarker repairs missing markers, and
// (c) post-repair state.
func TestPartialInitPrimingMatrix(t *testing.T) {
	tests := []struct {
		name string

		// setup flags: what to install before the test
		installHook   bool
		installHeader bool
		installFooter bool
		installUser   bool

		// expected state BEFORE anti-entropy
		wantHook       bool
		wantHeader     bool
		wantFooter     bool
		wantUserMarker bool
		wantPrimed     bool // at least one path reaches the agent

		// expected anti-entropy behavior
		wantRepairNeeded bool // EnsureOxPrimeMarker returns true (injected)

		// expected state AFTER anti-entropy
		wantPostHeader bool
		wantPostFooter bool
	}{
		{
			name:             "full init (hook + header + footer)",
			installHook:      true,
			installHeader:    true,
			installFooter:    true,
			wantHook:         true,
			wantHeader:       true,
			wantFooter:       true,
			wantPrimed:       true,
			wantRepairNeeded: false,
			wantPostHeader:   true,
			wantPostFooter:   true,
		},
		{
			name:             "hook only (bug #10373 scenario)",
			installHook:      true,
			wantHook:         true,
			wantHeader:       false,
			wantFooter:       false,
			wantPrimed:       true, // hook fires but may not reach agent (bug)
			wantRepairNeeded: true, // EnsureOxPrimeMarker creates AGENTS.md
			wantPostHeader:   true,
			wantPostFooter:   true,
		},
		{
			name:             "both markers only (no hook)",
			installHeader:    true,
			installFooter:    true,
			wantHeader:       true,
			wantFooter:       true,
			wantPrimed:       true, // self-check in header triggers prime
			wantRepairNeeded: false,
			wantPostHeader:   true,
			wantPostFooter:   true,
		},
		{
			name:             "footer only",
			installFooter:    true,
			wantFooter:       true,
			wantPrimed:       true, // instruction exists but weak
			wantRepairNeeded: true, // adds header
			wantPostHeader:   true,
			wantPostFooter:   true,
		},
		{
			name:             "header only",
			installHeader:    true,
			wantHeader:       true,
			wantPrimed:       true, // self-check triggers prime
			wantRepairNeeded: true, // adds footer
			wantPostHeader:   true,
			wantPostFooter:   true,
		},
		{
			name:             "user marker only (global fallback)",
			installUser:      true,
			wantUserMarker:   true,
			wantPrimed:       true,
			wantRepairNeeded: true, // creates AGENTS.md with both markers
			wantPostHeader:   true,
			wantPostFooter:   true,
		},
		{
			name:             "hook + user marker (double coverage)",
			installHook:      true,
			installUser:      true,
			wantHook:         true,
			wantUserMarker:   true,
			wantPrimed:       true,
			wantRepairNeeded: true, // creates AGENTS.md with both markers
			wantPostHeader:   true,
			wantPostFooter:   true,
		},
		{
			name:             "nothing installed",
			wantPrimed:       false, // no path to agent
			wantRepairNeeded: true,  // creates AGENTS.md
			wantPostHeader:   true,
			wantPostFooter:   true,
		},
		{
			name:             "hook + footer only",
			installHook:      true,
			installFooter:    true,
			wantHook:         true,
			wantFooter:       true,
			wantPrimed:       true,
			wantRepairNeeded: true, // adds header
			wantPostHeader:   true,
			wantPostFooter:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// isolated temp dir simulates a git repo
			gitRoot := t.TempDir()
			initGitRepo(t, gitRoot)

			// isolated HOME so user-level checks don't leak
			tmpHome := t.TempDir()
			t.Setenv("HOME", tmpHome)
			t.Setenv("AGENT_ENV", "claude-code")

			// --- setup ---

			if tt.installHook {
				require.NoError(t, InstallProjectClaudeHooks(gitRoot))
			}

			if tt.installHeader || tt.installFooter {
				agentsPath := filepath.Join(gitRoot, "AGENTS.md")
				content := ""
				if tt.installHeader {
					content += OxPrimeCheckBlock + "\n"
				}
				content += "# Agent Instructions\n\n"
				if tt.installFooter {
					content += OxPrimeLine + "\n"
				}
				require.NoError(t, os.WriteFile(agentsPath, []byte(content), 0644))
			}

			if tt.installUser {
				claudeDir := filepath.Join(tmpHome, ".claude")
				require.NoError(t, os.MkdirAll(claudeDir, 0755))
				userMD := filepath.Join(claudeDir, "CLAUDE.md")
				require.NoError(t, os.WriteFile(userMD, []byte("# User Config\n\n"+OxPrimeLine+"\n"), 0644))
			}

			// --- verify pre-anti-entropy state ---

			assert.Equal(t, tt.wantHook, HasProjectClaudeHooks(gitRoot),
				"HasProjectClaudeHooks before repair")
			assert.Equal(t, tt.wantHeader, HasOxPrimeCheckMarker(gitRoot),
				"HasOxPrimeCheckMarker before repair")
			assert.Equal(t, tt.wantFooter, HasOxPrimeMarker(gitRoot),
				"HasOxPrimeMarker before repair")
			assert.Equal(t, tt.wantUserMarker, hasUserLevelOxPrime(),
				"hasUserLevelOxPrime before repair")

			// at least one priming path exists
			anyPath := HasProjectClaudeHooks(gitRoot) ||
				HasOxPrimeCheckMarker(gitRoot) ||
				HasOxPrimeMarker(gitRoot) ||
				hasUserLevelOxPrime()
			assert.Equal(t, tt.wantPrimed, anyPath,
				"at least one priming path should exist")

			// --- run anti-entropy ---

			injected, err := EnsureOxPrimeMarker(gitRoot)
			require.NoError(t, err, "EnsureOxPrimeMarker should not error")
			assert.Equal(t, tt.wantRepairNeeded, injected,
				"EnsureOxPrimeMarker injection result")

			// --- verify post-anti-entropy state ---

			assert.Equal(t, tt.wantPostHeader, HasOxPrimeCheckMarker(gitRoot),
				"HasOxPrimeCheckMarker after repair")
			assert.Equal(t, tt.wantPostFooter, HasOxPrimeMarker(gitRoot),
				"HasOxPrimeMarker after repair")

			// after repair, both markers should always be present
			assert.True(t, HasBothPrimeMarkers(gitRoot),
				"HasBothPrimeMarkers should be true after EnsureOxPrimeMarker")
		})
	}
}

// TestPartialInitPrimingMatrix_AntiEntropyIdempotent verifies that running
// EnsureOxPrimeMarker twice is a no-op the second time.
func TestPartialInitPrimingMatrix_AntiEntropyIdempotent(t *testing.T) {
	gitRoot := t.TempDir()
	initGitRepo(t, gitRoot)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// first run: should inject
	injected, err := EnsureOxPrimeMarker(gitRoot)
	require.NoError(t, err)
	assert.True(t, injected, "first EnsureOxPrimeMarker should inject")

	// capture file content
	agentsPath := filepath.Join(gitRoot, "AGENTS.md")
	contentAfterFirst, err := os.ReadFile(agentsPath)
	require.NoError(t, err)

	// second run: should be a no-op
	injected, err = EnsureOxPrimeMarker(gitRoot)
	require.NoError(t, err)
	assert.False(t, injected, "second EnsureOxPrimeMarker should be no-op")

	// content should be identical
	contentAfterSecond, err := os.ReadFile(agentsPath)
	require.NoError(t, err)
	assert.Equal(t, string(contentAfterFirst), string(contentAfterSecond),
		"file content should not change on idempotent call")
}

// TestPartialInitPrimingMatrix_RepairPreservesExistingContent verifies that
// EnsureOxPrimeMarker adds markers without destroying existing AGENTS.md content.
func TestPartialInitPrimingMatrix_RepairPreservesExistingContent(t *testing.T) {
	gitRoot := t.TempDir()
	initGitRepo(t, gitRoot)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// write AGENTS.md with custom user content but no markers
	agentsPath := filepath.Join(gitRoot, "AGENTS.md")
	userContent := "# My Custom Instructions\n\nDo not touch my settings.\n"
	require.NoError(t, os.WriteFile(agentsPath, []byte(userContent), 0644))

	injected, err := EnsureOxPrimeMarker(gitRoot)
	require.NoError(t, err)
	assert.True(t, injected)

	// verify both markers added
	assert.True(t, HasBothPrimeMarkers(gitRoot))

	// verify original content preserved
	content, err := os.ReadFile(agentsPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "My Custom Instructions",
		"user content should be preserved")
	assert.Contains(t, string(content), "Do not touch my settings.",
		"user content should be preserved")
}
