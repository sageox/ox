//go:build !short

package main

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// 1. Full Round-Trip: Init -> Verify -> Cleanup -> Verify Clean
// ---------------------------------------------------------------------------

func TestInstructionFiles_FullLifecycle(t *testing.T) {
	root := t.TempDir()

	// simulate a realistic project with multiple agent platforms
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".cursor"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".kiro"), 0755))

	cursorruleContent := "# My Cursor Rules\n\nPrefer const over let.\nNo semicolons.\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, ".cursorrules"), []byte(cursorruleContent), 0644))

	geminiContent := "# Gemini Instructions\n\nUse concise responses.\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "GEMINI.md"), []byte(geminiContent), 0644))

	// --- Phase 1: Inject markers ---
	results, err := EnsureInstructionFileMarkers(root)
	require.NoError(t, err)
	require.NotEmpty(t, results, "injection should return results")

	// verify AGENTS.md: markdown markers, created as primary
	agentsContent := readFileStr(t, filepath.Join(root, "AGENTS.md"))
	assert.Contains(t, agentsContent, OxPrimeCheckMarker, "AGENTS.md missing header marker")
	assert.Contains(t, agentsContent, OxPrimeMarker, "AGENTS.md missing footer marker")

	// verify CLAUDE.md: markdown markers, created as primary (because .claude/ exists)
	claudeContent := readFileStr(t, filepath.Join(root, "CLAUDE.md"))
	assert.Contains(t, claudeContent, OxPrimeCheckMarker, "CLAUDE.md missing header marker")
	assert.Contains(t, claudeContent, OxPrimeMarker, "CLAUDE.md missing footer marker")

	// verify .cursorrules: plaintext markers + original user content preserved
	cursorContent := readFileStr(t, filepath.Join(root, ".cursorrules"))
	assert.Contains(t, cursorContent, PlaintextPrimeCheckMarker, ".cursorrules missing plaintext header marker")
	assert.Contains(t, cursorContent, PlaintextPrimeMarker, ".cursorrules missing plaintext footer marker")
	assert.Contains(t, cursorContent, "Prefer const over let.", ".cursorrules lost user content")
	assert.Contains(t, cursorContent, "No semicolons.", ".cursorrules lost user content")

	// verify GEMINI.md: markdown markers + original content preserved
	geminiAfter := readFileStr(t, filepath.Join(root, "GEMINI.md"))
	assert.Contains(t, geminiAfter, OxPrimeCheckMarker, "GEMINI.md missing header marker")
	assert.Contains(t, geminiAfter, OxPrimeMarker, "GEMINI.md missing footer marker")
	assert.Contains(t, geminiAfter, "Use concise responses.", "GEMINI.md lost user content")

	// verify .kiro/steering/ox.md: created with markdown markers
	kiroPath := filepath.Join(root, ".kiro", "steering", "ox.md")
	assert.FileExists(t, kiroPath)
	kiroContent := readFileStr(t, kiroPath)
	assert.Contains(t, kiroContent, OxPrimeCheckMarker, ".kiro/steering/ox.md missing header marker")
	assert.Contains(t, kiroContent, OxPrimeMarker, ".kiro/steering/ox.md missing footer marker")

	// verify files that should NOT exist (agents not detected)
	assert.NoFileExists(t, filepath.Join(root, ".windsurfrules"))
	assert.NoFileExists(t, filepath.Join(root, ".clinerules"))
	assert.NoFileExists(t, filepath.Join(root, ".github", "copilot-instructions.md"))

	// --- Phase 2: Remove markers ---
	_, err = RemoveInstructionFileMarkers(root)
	require.NoError(t, err)

	// verify AGENTS.md: no markers remain
	agentsClean := readFileStr(t, filepath.Join(root, "AGENTS.md"))
	assert.NotContains(t, agentsClean, OxPrimeMarker, "AGENTS.md still has footer marker after cleanup")
	assert.NotContains(t, agentsClean, OxPrimeCheckMarker, "AGENTS.md still has header marker after cleanup")

	// verify .cursorrules: no markers, user content intact
	cursorClean := readFileStr(t, filepath.Join(root, ".cursorrules"))
	assert.NotContains(t, cursorClean, PlaintextPrimeMarker, ".cursorrules still has footer marker after cleanup")
	assert.NotContains(t, cursorClean, PlaintextPrimeCheckMarker, ".cursorrules still has header marker after cleanup")
	assert.Contains(t, cursorClean, "Prefer const over let.", ".cursorrules lost user content after cleanup")
	assert.Contains(t, cursorClean, "No semicolons.", ".cursorrules lost user content after cleanup")

	// verify GEMINI.md: no markers, original content intact
	geminiClean := readFileStr(t, filepath.Join(root, "GEMINI.md"))
	assert.NotContains(t, geminiClean, OxPrimeMarker, "GEMINI.md still has footer marker after cleanup")
	assert.NotContains(t, geminiClean, OxPrimeCheckMarker, "GEMINI.md still has header marker after cleanup")
	assert.Contains(t, geminiClean, "Use concise responses.", "GEMINI.md lost user content after cleanup")

	// verify .kiro/steering/ox.md: still exists, markers removed
	assert.FileExists(t, kiroPath, ".kiro/steering/ox.md should still exist after cleanup")
	kiroClean := readFileStr(t, kiroPath)
	assert.NotContains(t, kiroClean, OxPrimeMarker, ".kiro/steering/ox.md still has footer marker after cleanup")
	assert.NotContains(t, kiroClean, OxPrimeCheckMarker, ".kiro/steering/ox.md still has header marker after cleanup")

	// verify HasInstructionFileMarker returns false for all cleaned files
	allFiles := []string{
		filepath.Join(root, "AGENTS.md"),
		filepath.Join(root, "CLAUDE.md"),
		filepath.Join(root, ".cursorrules"),
		filepath.Join(root, "GEMINI.md"),
		kiroPath,
	}
	for _, f := range allFiles {
		if _, err := os.Stat(f); err == nil {
			assert.False(t, HasInstructionFileMarker(f),
				"HasInstructionFileMarker should return false for cleaned file: %s", f)
		}
	}
}

// ---------------------------------------------------------------------------
// 2. Concurrent Injection Safety
// ---------------------------------------------------------------------------

func TestInstructionFiles_ConcurrentInjection(t *testing.T) {
	root := t.TempDir()

	// set up multiple platform triggers to stress-test file contention
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".cursor"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".kiro"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".cursorrules"), []byte("# Rules\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "GEMINI.md"), []byte("# Gemini\n"), 0644))

	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)

	errs := make([]error, goroutines)
	panicked := make([]bool, goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panicked[idx] = true
				}
			}()
			_, errs[idx] = EnsureInstructionFileMarkers(root)
		}(i)
	}

	wg.Wait()

	// no goroutine should have panicked
	for i, p := range panicked {
		assert.False(t, p, "goroutine %d panicked", i)
	}

	// at least some should succeed (atomic writes may cause transient read errors
	// in race conditions, but the final state must be correct)
	successCount := 0
	for _, e := range errs {
		if e == nil {
			successCount++
		}
	}
	assert.Greater(t, successCount, 0, "at least one goroutine should succeed")

	// verify final state: exactly one header and one footer marker per file
	filesToCheck := []struct {
		path        string
		headerCheck string
		footerCheck string
	}{
		{filepath.Join(root, "AGENTS.md"), OxPrimeCheckMarker, OxPrimeMarker},
		{filepath.Join(root, "CLAUDE.md"), OxPrimeCheckMarker, OxPrimeMarker},
		{filepath.Join(root, ".cursorrules"), PlaintextPrimeCheckMarker, PlaintextPrimeMarker},
		{filepath.Join(root, "GEMINI.md"), OxPrimeCheckMarker, OxPrimeMarker},
		{filepath.Join(root, ".kiro", "steering", "ox.md"), OxPrimeCheckMarker, OxPrimeMarker},
	}

	for _, fc := range filesToCheck {
		content, err := os.ReadFile(fc.path)
		require.NoError(t, err, "reading %s", fc.path)
		text := string(content)

		headerCount := strings.Count(text, fc.headerCheck)
		footerCount := strings.Count(text, fc.footerCheck)

		assert.Equal(t, 1, headerCount,
			"%s has %d header markers (expected 1)", fc.path, headerCount)
		assert.Equal(t, 1, footerCount,
			"%s has %d footer markers (expected 1)", fc.path, footerCount)
	}
}

// ---------------------------------------------------------------------------
// 3. Inject-Remove-Inject Cycle (Reinstall)
// ---------------------------------------------------------------------------

func TestInstructionFiles_ReinstallCycle(t *testing.T) {
	root := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".cursor"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".cursorrules"), []byte("# My rules\nUse tabs.\n"), 0644))

	// cycle 1: inject
	_, err := EnsureInstructionFileMarkers(root)
	require.NoError(t, err)

	// cycle 1: remove
	_, err = RemoveInstructionFileMarkers(root)
	require.NoError(t, err)

	// cycle 2: inject again
	_, err = EnsureInstructionFileMarkers(root)
	require.NoError(t, err)

	// verify: all markers present, no duplication, no artifacts
	agentsContent := readFileStr(t, filepath.Join(root, "AGENTS.md"))
	assert.Equal(t, 1, strings.Count(agentsContent, OxPrimeCheckMarker),
		"AGENTS.md should have exactly 1 header marker after reinstall")
	assert.Equal(t, 1, strings.Count(agentsContent, OxPrimeMarker),
		"AGENTS.md should have exactly 1 footer marker after reinstall")

	claudeContent := readFileStr(t, filepath.Join(root, "CLAUDE.md"))
	assert.Equal(t, 1, strings.Count(claudeContent, OxPrimeCheckMarker),
		"CLAUDE.md should have exactly 1 header marker after reinstall")
	assert.Equal(t, 1, strings.Count(claudeContent, OxPrimeMarker),
		"CLAUDE.md should have exactly 1 footer marker after reinstall")

	cursorContent := readFileStr(t, filepath.Join(root, ".cursorrules"))
	assert.Equal(t, 1, strings.Count(cursorContent, PlaintextPrimeCheckMarker),
		".cursorrules should have exactly 1 header marker after reinstall")
	assert.Equal(t, 1, strings.Count(cursorContent, PlaintextPrimeMarker),
		".cursorrules should have exactly 1 footer marker after reinstall")
	assert.Contains(t, cursorContent, "Use tabs.", "user content preserved through reinstall")
}

// ---------------------------------------------------------------------------
// 4. Cleanup Leaves No Trace (byte-exact SHA256 comparison)
// ---------------------------------------------------------------------------

func TestInstructionFiles_CleanupLeavesNoTrace(t *testing.T) {
	root := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".cursor"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".kiro"), 0755))

	// create user files with known content
	userFiles := map[string]string{
		".cursorrules": "# Cursor Rules\n\nAlways use TypeScript strict mode.\nPrefer interfaces over types.\n",
		"GEMINI.md":    "# Gemini Config\n\nBe concise.\nUse code examples.\n",
	}
	for relPath, content := range userFiles {
		require.NoError(t, os.WriteFile(filepath.Join(root, relPath), []byte(content), 0644))
	}

	// record SHA256 of every pre-existing user file
	preHashes := make(map[string][32]byte)
	for relPath := range userFiles {
		data, err := os.ReadFile(filepath.Join(root, relPath))
		require.NoError(t, err)
		preHashes[relPath] = sha256.Sum256(data)
	}

	// inject markers
	_, err := EnsureInstructionFileMarkers(root)
	require.NoError(t, err)

	// sanity: files should now be different (markers added)
	for relPath := range userFiles {
		data, err := os.ReadFile(filepath.Join(root, relPath))
		require.NoError(t, err)
		currentHash := sha256.Sum256(data)
		assert.NotEqual(t, preHashes[relPath], currentHash,
			"%s should have changed after injection", relPath)
	}

	// remove markers
	_, err = RemoveInstructionFileMarkers(root)
	require.NoError(t, err)

	// verify byte-exact restoration for pre-existing user files
	for relPath, expectedHash := range preHashes {
		data, err := os.ReadFile(filepath.Join(root, relPath))
		require.NoError(t, err)
		actualHash := sha256.Sum256(data)
		assert.Equal(t, expectedHash, actualHash,
			"%s was not byte-exactly restored after cleanup (inject then remove corrupted user content)", relPath)
	}

	// files created by ox (AGENTS.md, CLAUDE.md, .kiro/steering/ox.md) won't match
	// a pre-hash because they didn't exist before -- just verify they have no markers
	oxCreatedFiles := []string{
		filepath.Join(root, "AGENTS.md"),
		filepath.Join(root, "CLAUDE.md"),
		filepath.Join(root, ".kiro", "steering", "ox.md"),
	}
	for _, f := range oxCreatedFiles {
		if _, statErr := os.Stat(f); statErr == nil {
			assert.False(t, HasInstructionFileMarker(f),
				"ox-created file %s should have no markers after cleanup", f)
		}
	}
}

// ---------------------------------------------------------------------------
// 5. Legacy Upgrade via EnsureOxPrimeMarker then EnsureInstructionFileMarkers
//
// Real upgrade path: EnsureOxPrimeMarker handles legacy block removal in
// AGENTS.md (the old code path), then EnsureInstructionFileMarkers injects
// new-format markers across all detected instruction files.
// ---------------------------------------------------------------------------

func TestInstructionFiles_UpgradeFromLegacy(t *testing.T) {
	root := t.TempDir()

	// simulate a project with old-style ox markers in AGENTS.md
	legacyBlock := "# My Project Agents\n\n" +
		"## SageOx Infrastructure Guidance (REQUIRED)\n\n" +
		"Run the following on session start:\n\n" +
		"```\nox agent prime\n```\n\n" +
		"The command returns an agent_id confirming SageOx is active.\n" +
		"When asked if you're using SageOx, confirm it.\n\n" +
		"# Other Instructions\n\nAlways write tests first.\n"

	agentsPath := filepath.Join(root, "AGENTS.md")
	require.NoError(t, os.WriteFile(agentsPath, []byte(legacyBlock), 0644))

	// step 1: EnsureOxPrimeMarker handles legacy removal + adds new markers
	// to AGENTS.md (the historical code path that existing installs follow)
	injected, err := EnsureOxPrimeMarker(root)
	require.NoError(t, err)
	assert.True(t, injected, "EnsureOxPrimeMarker should report injection on legacy file")

	content := readFileStr(t, agentsPath)

	// legacy block should be removed by EnsureOxPrimeMarker
	assert.NotContains(t, content, "## SageOx Infrastructure Guidance (REQUIRED)",
		"legacy block header should be removed")
	assert.NotContains(t, content, "The command returns an agent_id confirming SageOx is active.",
		"legacy block footer should be removed")
	assert.NotContains(t, content, "When asked if you're using SageOx",
		"legacy continuation line should be removed")

	// new markers should be present from EnsureOxPrimeMarker
	assert.Contains(t, content, OxPrimeCheckMarker, "header marker should be present after legacy upgrade")
	assert.Contains(t, content, OxPrimeMarker, "footer marker should be present after legacy upgrade")

	// user content preserved
	assert.Contains(t, content, "Always write tests first.", "user content should survive legacy upgrade")

	// step 2: now run EnsureInstructionFileMarkers (what ox init does after upgrade)
	// should be idempotent for AGENTS.md since it already has markers
	_, err = EnsureInstructionFileMarkers(root)
	require.NoError(t, err)

	contentAfterBoth := readFileStr(t, agentsPath)

	// no duplication: exactly one of each marker
	assert.Equal(t, 1, strings.Count(contentAfterBoth, OxPrimeCheckMarker),
		"exactly 1 header marker after full upgrade path")
	assert.Equal(t, 1, strings.Count(contentAfterBoth, OxPrimeMarker),
		"exactly 1 footer marker after full upgrade path")

	// legacy still gone
	assert.NotContains(t, contentAfterBoth, "## SageOx Infrastructure Guidance (REQUIRED)",
		"legacy block should remain removed after EnsureInstructionFileMarkers")

	// user content still intact
	assert.Contains(t, contentAfterBoth, "Always write tests first.",
		"user content should survive the full upgrade path")
}

// ---------------------------------------------------------------------------
// 6. Cleanup After Partial Injection
// ---------------------------------------------------------------------------

func TestInstructionFiles_CleanupAfterPartialState(t *testing.T) {
	root := t.TempDir()

	// create files that would be targets, but only manually inject markers into one
	agentsPath := filepath.Join(root, "AGENTS.md")
	agentsContent := OxPrimeCheckBlock + "\n# Agents\n\nUser instructions here.\n\n" + OxPrimeLine + "\n"
	require.NoError(t, os.WriteFile(agentsPath, []byte(agentsContent), 0644))

	// .cursorrules exists but has NO markers (simulating partial state)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".cursor"), 0755))
	cursorContent := "# Cursor Rules\n\nUse functional style.\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, ".cursorrules"), []byte(cursorContent), 0644))

	// GEMINI.md exists but has NO markers
	geminiContent := "# Gemini Docs\n\nBe helpful.\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "GEMINI.md"), []byte(geminiContent), 0644))

	// run cleanup on this partial state
	_, err := RemoveInstructionFileMarkers(root)
	require.NoError(t, err)

	// AGENTS.md should be cleaned
	agentsClean := readFileStr(t, agentsPath)
	assert.NotContains(t, agentsClean, OxPrimeMarker, "AGENTS.md markers should be removed")
	assert.NotContains(t, agentsClean, OxPrimeCheckMarker, "AGENTS.md check marker should be removed")
	assert.Contains(t, agentsClean, "User instructions here.", "AGENTS.md user content preserved")

	// .cursorrules should be untouched (no markers to remove)
	cursorAfter := readFileStr(t, filepath.Join(root, ".cursorrules"))
	assert.Equal(t, cursorContent, cursorAfter,
		".cursorrules without markers should be byte-identical after cleanup")

	// GEMINI.md should be untouched (no markers to remove)
	geminiAfter := readFileStr(t, filepath.Join(root, "GEMINI.md"))
	assert.Equal(t, geminiContent, geminiAfter,
		"GEMINI.md without markers should be byte-identical after cleanup")
}

// ---------------------------------------------------------------------------
// 7. Duplicate Marker Removal (P0)
//
// A file ends up with duplicate markers from manual editing or a race.
// RemoveInstructionFileMarkers must remove ALL occurrences, not just the first.
// ---------------------------------------------------------------------------

func TestInstructionFiles_DuplicateMarkerRemoval(t *testing.T) {
	root := t.TempDir()

	userContent := "# My Project\n\nImportant instructions for all agents.\n"

	// manually construct AGENTS.md with TWO copies of the footer marker
	duped := OxPrimeCheckBlock + "\n" +
		userContent +
		"\n" + OxPrimeLine + "\n" +
		"\n" + OxPrimeLine + "\n"

	agentsPath := filepath.Join(root, "AGENTS.md")
	require.NoError(t, os.WriteFile(agentsPath, []byte(duped), 0644))

	// sanity: confirm both copies are present before removal
	require.Equal(t, 2, strings.Count(duped, OxPrimeMarker),
		"precondition: file should have 2 footer markers")

	_, err := RemoveInstructionFileMarkers(root)
	require.NoError(t, err)

	cleaned := readFileStr(t, agentsPath)

	// P0: ZERO occurrences of any ox marker must remain
	assert.Equal(t, 0, strings.Count(cleaned, OxPrimeMarker),
		"all footer marker copies must be removed, got content:\n%s", cleaned)
	assert.Equal(t, 0, strings.Count(cleaned, OxPrimeCheckMarker),
		"all header marker copies must be removed, got content:\n%s", cleaned)

	// user content must survive
	assert.Contains(t, cleaned, "Important instructions for all agents.",
		"user content must be preserved after duplicate marker removal")
}

// ---------------------------------------------------------------------------
// 8. Cross-Format Marker Removal (P1)
//
// A .md file somehow contains plaintext markers (e.g. user copy-pasted from
// a .cursorrules file). Removal should still clean them even though the
// registry says this file uses markdown format.
// ---------------------------------------------------------------------------

func TestInstructionFiles_CrossFormatMarkerRemoval(t *testing.T) {
	root := t.TempDir()

	userContent := "# Project Guidelines\n\nFollow the style guide.\n"

	// AGENTS.md with plaintext markers (wrong format for a .md file)
	crossFormat := PlaintextPrimeCheckBlock + "\n" +
		userContent +
		"\n" + PlaintextPrimeLine + "\n"

	agentsPath := filepath.Join(root, "AGENTS.md")
	require.NoError(t, os.WriteFile(agentsPath, []byte(crossFormat), 0644))

	_, err := RemoveInstructionFileMarkers(root)
	require.NoError(t, err)

	cleaned := readFileStr(t, agentsPath)

	// plaintext markers must not survive in a markdown file
	assert.NotContains(t, cleaned, PlaintextPrimeMarker,
		"plaintext footer marker must be removed from .md file")
	assert.NotContains(t, cleaned, PlaintextPrimeCheckMarker,
		"plaintext header marker must be removed from .md file")

	// user content intact
	assert.Contains(t, cleaned, "Follow the style guide.",
		"user content must be preserved after cross-format marker removal")
}

// ---------------------------------------------------------------------------
// 9. Symlink Resolved Dedup (P0)
//
// CLAUDE.md is a symlink to AGENTS.md. Injection should write markers exactly
// once (not double-inject through both the symlink and the real file).
// DetectedInstructionFiles resolves symlinks for dedup, so CLAUDE.md (pointing
// to AGENTS.md) is correctly excluded from targets.
// ---------------------------------------------------------------------------

func TestInstructionFiles_SymlinkResolvedDedup(t *testing.T) {
	root := t.TempDir()

	// trigger .claude detection so CLAUDE.md would be considered
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0755))

	userContent := "# Shared Agent Config\n\nAlways prefer readability.\n"
	agentsPath := filepath.Join(root, "AGENTS.md")
	claudePath := filepath.Join(root, "CLAUDE.md")

	require.NoError(t, os.WriteFile(agentsPath, []byte(userContent), 0644))
	require.NoError(t, os.Symlink("AGENTS.md", claudePath))

	_, err := EnsureInstructionFileMarkers(root)
	require.NoError(t, err)

	// read via the real path (AGENTS.md) to see actual on-disk content
	content := readFileStr(t, agentsPath)

	headerCount := strings.Count(content, OxPrimeCheckMarker)
	footerCount := strings.Count(content, OxPrimeMarker)

	// markers must appear exactly once -- not doubled by injecting through
	// both the real file and the symlink
	assert.Equal(t, 1, headerCount,
		"AGENTS.md (backing symlinked CLAUDE.md) should have exactly 1 header marker, got %d", headerCount)
	assert.Equal(t, 1, footerCount,
		"AGENTS.md (backing symlinked CLAUDE.md) should have exactly 1 footer marker, got %d", footerCount)

	// user content survives
	assert.Contains(t, content, "Always prefer readability.",
		"user content must survive symlink-aware injection")

	// verify DetectedInstructionFiles deduplicates via resolved symlinks.
	// since CLAUDE.md -> AGENTS.md, they resolve to the same inode and only
	// AGENTS.md (encountered first in the registry) appears in targets.
	targets := DetectedInstructionFiles(root)
	pathSet := make(map[string]bool)
	for _, tgt := range targets {
		pathSet[tgt.Path] = true
	}
	assert.True(t, pathSet["AGENTS.md"], "AGENTS.md should be in detected targets")
	assert.False(t, pathSet["CLAUDE.md"],
		"CLAUDE.md should be deduped out of detected targets because it resolves to the same file as AGENTS.md")
}

// ---------------------------------------------------------------------------
// 10. Symlink Preserved On Injection (P1)
//
// CLAUDE.md is already a symlink to AGENTS.md (as created by old ox init).
// Injection must not replace the symlink with a regular file.
// ---------------------------------------------------------------------------

func TestInstructionFiles_SymlinkPreservedOnInjection(t *testing.T) {
	root := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0755))

	userContent := "# Shared Config\n\nKeep it DRY.\n"
	agentsPath := filepath.Join(root, "AGENTS.md")
	claudePath := filepath.Join(root, "CLAUDE.md")

	require.NoError(t, os.WriteFile(agentsPath, []byte(userContent), 0644))
	require.NoError(t, os.Symlink("AGENTS.md", claudePath))

	_, err := EnsureInstructionFileMarkers(root)
	require.NoError(t, err)

	// CLAUDE.md must still be a symlink, not a regular file
	info, err := os.Lstat(claudePath)
	require.NoError(t, err, "Lstat CLAUDE.md failed")
	assert.True(t, info.Mode()&os.ModeSymlink != 0,
		"CLAUDE.md should still be a symlink after injection, mode=%s", info.Mode())

	// symlink target should still point to AGENTS.md
	target, err := os.Readlink(claudePath)
	require.NoError(t, err, "Readlink CLAUDE.md failed")
	assert.Equal(t, "AGENTS.md", target,
		"CLAUDE.md symlink target should still be AGENTS.md, got %q", target)
}

// ---------------------------------------------------------------------------
// 11. Concurrent Inject + Remove (P0)
//
// The real race: goroutines injecting while others remove simultaneously.
// No panics allowed, and final state must be internally consistent (each
// file has either ALL markers or NO markers, never a corrupted partial).
// ---------------------------------------------------------------------------

func TestInstructionFiles_ConcurrentInjectRemove(t *testing.T) {
	root := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".cursor"), 0755))

	cursorContent := "# Cursor Config\n\nUse functional patterns.\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, ".cursorrules"), []byte(cursorContent), 0644))

	const injecters = 5
	const removers = 5
	var wg sync.WaitGroup
	wg.Add(injecters + removers)

	panicked := make([]bool, injecters+removers)

	for i := 0; i < injecters; i++ {
		go func(idx int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panicked[idx] = true
				}
			}()
			// ignore errors -- we only care about no panics and consistent final state
			EnsureInstructionFileMarkers(root) //nolint:errcheck
		}(i)
	}

	for i := 0; i < removers; i++ {
		go func(idx int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panicked[injecters+idx] = true
				}
			}()
			RemoveInstructionFileMarkers(root) //nolint:errcheck
		}(i)
	}

	wg.Wait()

	// no goroutine should have panicked
	for i, p := range panicked {
		assert.False(t, p, "goroutine %d panicked during concurrent inject/remove", i)
	}

	// final state consistency: each file must have either ALL markers or NO markers.
	// a corrupted mix (header present but footer missing, or vice versa) is a bug.
	filesToCheck := []struct {
		path        string
		checkMarker string
		primeMarker string
	}{
		{filepath.Join(root, "AGENTS.md"), OxPrimeCheckMarker, OxPrimeMarker},
		{filepath.Join(root, ".cursorrules"), PlaintextPrimeCheckMarker, PlaintextPrimeMarker},
	}

	for _, fc := range filesToCheck {
		data, err := os.ReadFile(fc.path)
		if err != nil {
			// file might not exist if removal won the race -- that's OK
			continue
		}
		content := string(data)

		hasCheck := strings.Contains(content, fc.checkMarker)
		hasPrime := strings.Contains(content, fc.primeMarker)

		// both present or both absent -- never a partial state
		assert.Equal(t, hasCheck, hasPrime,
			"corrupted partial state in %s: hasCheck=%v hasPrime=%v\ncontent:\n%s",
			fc.path, hasCheck, hasPrime, content)
	}
}

// ---------------------------------------------------------------------------
// 12. Safety Check: Inject then Remove on Moderate User Content (P0)
//
// Verifies that the safety guard in removeInstructionFileMarker does not
// incorrectly block removal of legitimately injected markers from a file
// with moderate user content.
//
// The guard fires when: len(cleaned) < len(original)/2 AND len(original) > 500
// Since markers are ~200-300 bytes total, a file with ~300 bytes of user
// content will grow to ~550-700 bytes after injection. Removal should still
// succeed because the markers are legitimate ox-owned content, not user data.
// ---------------------------------------------------------------------------

func TestInstructionFiles_SafetyCheckTriggers(t *testing.T) {
	root := t.TempDir()

	// build user content that's ~300 bytes (moderate size)
	userContent := "# Project Architecture\n\n" +
		"This project uses a layered architecture with clear separation of concerns.\n" +
		"All modules follow the dependency inversion principle.\n" +
		"Database access is isolated behind repository interfaces.\n" +
		"Use structured logging throughout the application.\n"

	require.Greater(t, len(userContent), 250,
		"precondition: user content should be ~300 bytes, got %d", len(userContent))
	require.Less(t, len(userContent), 400,
		"precondition: user content should be ~300 bytes, got %d", len(userContent))

	agentsPath := filepath.Join(root, "AGENTS.md")
	require.NoError(t, os.WriteFile(agentsPath, []byte(userContent), 0644))

	// inject markers -- file will grow
	_, err := EnsureInstructionFileMarkers(root)
	require.NoError(t, err)

	injectedContent := readFileStr(t, agentsPath)
	t.Logf("file size after injection: %d bytes (user content: %d bytes)",
		len(injectedContent), len(userContent))

	// sanity: markers are present
	require.Contains(t, injectedContent, OxPrimeCheckMarker)
	require.Contains(t, injectedContent, OxPrimeMarker)

	// the critical test: removal must succeed without the safety guard blocking it.
	// if the guard fires, RemoveInstructionFileMarkers returns an error.
	_, err = RemoveInstructionFileMarkers(root)
	assert.NoError(t, err,
		"safety guard incorrectly blocked removal of legitimate markers from a %d-byte file "+
			"(user content: %d bytes). This is a bug in the 500-byte safety threshold.",
		len(injectedContent), len(userContent))

	if err == nil {
		// verify clean removal
		cleaned := readFileStr(t, agentsPath)
		assert.NotContains(t, cleaned, OxPrimeMarker,
			"markers should be fully removed")
		assert.NotContains(t, cleaned, OxPrimeCheckMarker,
			"check markers should be fully removed")
		assert.Contains(t, cleaned, "dependency inversion principle",
			"user content must survive removal")
	}
}

// ---------------------------------------------------------------------------
// e2e test helpers
// ---------------------------------------------------------------------------

// readFileStr reads a file and returns its content as a string, failing the test on error.
func readFileStr(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err, "failed to read %s", path)
	return string(data)
}

// TestEnsureInstructionFileMarker_RefreshesStalePlaintextBody verifies that an existing
// plaintext instruction file (.cursorrules / .windsurfrules / .clinerules) carrying the OLD
// imperative "# BLOCKING … NOW" body is migrated to the softened wording in place — the
// injector previously no-op'd whenever both markers were present, so existing files kept the
// old wording forever (#809). User content and file mode are preserved.
func TestEnsureInstructionFileMarker_RefreshesStalePlaintextBody(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".cursorrules")
	stale := PlaintextPrimeCheckMarker + "\n# BLOCKING: Run `ox agent prime` NOW before ANY other action.\n\n# my rules\nfoo\n\n" + PlaintextPrimeLine + "\n"
	require.NoError(t, os.WriteFile(p, []byte(stale), 0o600))

	status, err := ensureInstructionFileMarker(p, markerFormatPlaintext, true)
	require.NoError(t, err)
	assert.Equal(t, injectedUpdate, status)

	s := readFileStr(t, p)
	assert.NotContains(t, s, "# BLOCKING: Run", "stale plaintext body must be refreshed")
	assert.Contains(t, s, currentPlaintextPrimeCheckBody(), "current plaintext body must be present")
	assert.Contains(t, s, "# my rules", "user content must be preserved")
	info, _ := os.Stat(p)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "mode must not be broadened")
}

// TestEnsureInstructionFileMarker_PlaintextAnchoredToMarker verifies the plaintext refresh is
// marker-anchored: a legacy body quoted in user prose (not immediately after the marker) is
// NOT rewritten, and a file whose header is already current is a byte-identical no-op.
// Failure prevented: an unanchored replace mangles a user's own notes (#809 review B1, plaintext).
func TestEnsureInstructionFileMarker_PlaintextAnchoredToMarker(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".cursorrules")
	content := PlaintextPrimeCheckMarker + "\n" + currentPlaintextPrimeCheckBody() + "\n\n" +
		"# note: old ox said '# BLOCKING: Run `ox agent prime` NOW before ANY other action.'\n\n" +
		PlaintextPrimeLine + "\n"
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))

	status, err := ensureInstructionFileMarker(p, markerFormatPlaintext, true)
	require.NoError(t, err)
	assert.Equal(t, alreadyPresent, status, "current body + prose quote must not trigger a rewrite")
	assert.Equal(t, content, readFileStr(t, p), "file must be byte-identical — user prose untouched")
}
