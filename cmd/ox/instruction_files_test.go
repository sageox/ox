//go:build !short

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// 1. Detection Tests
// ---------------------------------------------------------------------------

func TestDetectedInstructionFiles_EmptyDir(t *testing.T) {
	root := t.TempDir()

	targets := DetectedInstructionFiles(root)

	// agents with nil DetectFn (Codex, OpenCode, Amp, Goose) always pass detection,
	// so AGENTS.md appears via them even in an empty dir
	names := targetRelPaths(targets)
	assert.Contains(t, names, "AGENTS.md", "AGENTS.md should appear (shared by nil-DetectFn agents)")

	// platform-specific files should NOT appear without their trigger
	for _, tgt := range targets {
		assert.NotEqual(t, ".cursorrules", tgt.Path, ".cursorrules should not appear without .cursor/ dir")
		assert.NotEqual(t, ".windsurfrules", tgt.Path, ".windsurfrules should not appear without trigger")
		assert.NotEqual(t, "GEMINI.md", tgt.Path, "GEMINI.md should not appear without trigger")
	}
}

func TestDetectedInstructionFiles_WithCursorDir(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".cursor"), 0755))

	targets := DetectedInstructionFiles(root)

	names := targetRelPaths(targets)
	assert.Contains(t, names, ".cursorrules", ".cursorrules should appear when .cursor/ exists")
}

func TestDetectedInstructionFiles_WithGeminiMd(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "GEMINI.md"), []byte("# Gemini\n"), 0644))

	targets := DetectedInstructionFiles(root)

	names := targetRelPaths(targets)
	assert.Contains(t, names, "GEMINI.md", "GEMINI.md should appear when the file exists")
}

func TestDetectedInstructionFiles_WithCopilotInstructions(t *testing.T) {
	root := t.TempDir()
	copilotDir := filepath.Join(root, ".github")
	require.NoError(t, os.MkdirAll(copilotDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(copilotDir, "copilot-instructions.md"), []byte("# Copilot\n"), 0644))

	targets := DetectedInstructionFiles(root)

	names := targetRelPaths(targets)
	assert.Contains(t, names, filepath.Join(".github", "copilot-instructions.md"),
		"copilot-instructions.md should appear when .github/copilot-instructions.md exists")
}

func TestDetectedInstructionFiles_WithKiroDir(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".kiro"), 0755))

	targets := DetectedInstructionFiles(root)

	names := targetRelPaths(targets)
	assert.Contains(t, names, filepath.Join(".kiro", "steering", "ox.md"),
		".kiro/steering/ox.md should appear when .kiro/ exists")
}

func TestDetectedInstructionFiles_MultiplePlatforms(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".cursor"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".kiro"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "GEMINI.md"), []byte("# Gemini\n"), 0644))

	targets := DetectedInstructionFiles(root)

	names := targetRelPaths(targets)
	assert.Contains(t, names, "AGENTS.md")
	assert.Contains(t, names, ".cursorrules")
	assert.Contains(t, names, filepath.Join(".kiro", "steering", "ox.md"))
	assert.Contains(t, names, "GEMINI.md")
}

func TestDetectedInstructionFiles_DeduplicatesSharedFiles(t *testing.T) {
	root := t.TempDir()

	targets := DetectedInstructionFiles(root)

	// AGENTS.md is listed by Claude Code, Codex, OpenCode, Amp, and Goose.
	// Dedup should ensure it appears only once.
	names := targetRelPaths(targets)
	count := 0
	for _, n := range names {
		if n == "AGENTS.md" {
			count++
		}
	}
	assert.Equal(t, 1, count,
		"AGENTS.md should appear exactly once despite being listed by multiple agents, got %d", count)
}

// ---------------------------------------------------------------------------
// 2. Injection Safety Tests
// ---------------------------------------------------------------------------

func TestEnsureInstructionFileMarkers_NeverCreatesUndetectedFiles(t *testing.T) {
	root := t.TempDir()

	_, err := EnsureInstructionFileMarkers(root)
	require.NoError(t, err)

	// platform-specific files must NOT be created when their agent is not detected
	assert.NoFileExists(t, filepath.Join(root, ".cursorrules"),
		"must not create .cursorrules without .cursor/ dir")
	assert.NoFileExists(t, filepath.Join(root, "GEMINI.md"),
		"must not create GEMINI.md without trigger")
	assert.NoFileExists(t, filepath.Join(root, ".windsurfrules"),
		"must not create .windsurfrules without trigger")
	assert.NoFileExists(t, filepath.Join(root, ".github", "copilot-instructions.md"),
		"must not create copilot-instructions.md without trigger")
}

func TestEnsureInstructionFileMarkers_PreservesExistingContent(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".cursor"), 0755))

	userContent := "# My Custom Rules\n\nAlways use tabs.\nNever use semicolons.\n"
	cursorrules := filepath.Join(root, ".cursorrules")
	require.NoError(t, os.WriteFile(cursorrules, []byte(userContent), 0644))

	_, err := EnsureInstructionFileMarkers(root)
	require.NoError(t, err)

	after, err := os.ReadFile(cursorrules)
	require.NoError(t, err)

	assert.Contains(t, string(after), "Always use tabs.", "user content must be preserved")
	assert.Contains(t, string(after), "Never use semicolons.", "user content must be preserved")
}

func TestEnsureInstructionFileMarkers_Idempotent(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".cursor"), 0755))
	// pre-create .cursorrules so it's not skipped as non-primary
	require.NoError(t, os.WriteFile(filepath.Join(root, ".cursorrules"), []byte("# Rules\n"), 0644))

	results1, err := EnsureInstructionFileMarkers(root)
	require.NoError(t, err)

	// snapshot all files after first injection
	snapshots := snapshotDir(t, root)

	results2, err := EnsureInstructionFileMarkers(root)
	require.NoError(t, err)

	// second run should report already-present for every file that was injected
	for _, r := range results2 {
		if r.Status == injectedSkip {
			continue // non-primary files that don't exist
		}
		assert.Equal(t, alreadyPresent, r.Status,
			"second injection of %s should be already-present, got %s", r.File, r.Status)
	}

	// file contents must be byte-identical
	for relPath, before := range snapshots {
		afterBytes, err := os.ReadFile(filepath.Join(root, relPath))
		require.NoError(t, err, "reading %s after second injection", relPath)
		assert.Equal(t, string(before), string(afterBytes),
			"file %s changed on second injection", relPath)
	}

	// sanity: first run should have produced at least one create/update
	hasNew := false
	for _, r := range results1 {
		if r.Status == injectedNew || r.Status == injectedUpdate {
			hasNew = true
			break
		}
	}
	assert.True(t, hasNew, "first injection should have created at least one file")
}

func TestEnsureInstructionFileMarkers_AtomicWrite(t *testing.T) {
	root := t.TempDir()

	_, err := EnsureInstructionFileMarkers(root)
	require.NoError(t, err)

	agentsPath := filepath.Join(root, "AGENTS.md")
	info, err := os.Stat(agentsPath)
	require.NoError(t, err)

	assert.Equal(t, os.FileMode(0644), info.Mode().Perm(),
		"injected file should have 0644 permissions")
}

// TestEnsureInstructionFileMarkers_PreservesRestrictivePerms verifies that
// injecting markers into a pre-existing 0600 file does not broaden its perms
// to 0644 (a privacy regression — the atomic rename would otherwise reset mode).
func TestEnsureInstructionFileMarkers_PreservesRestrictivePerms(t *testing.T) {
	root := t.TempDir()

	agentsPath := filepath.Join(root, "AGENTS.md")
	require.NoError(t, os.WriteFile(agentsPath, []byte("# existing user content\n"), 0600))

	_, err := EnsureInstructionFileMarkers(root)
	require.NoError(t, err)

	info, err := os.Stat(agentsPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm(),
		"injecting markers must preserve the pre-existing 0600 permissions")

	// removal must also preserve the restrictive perms
	_, err = RemoveInstructionFileMarkers(root)
	require.NoError(t, err)

	info, err = os.Stat(agentsPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm(),
		"removing markers must preserve the pre-existing 0600 permissions")
}

func TestEnsureInstructionFileMarkers_SizeGuard(t *testing.T) {
	root := t.TempDir()

	// create a large AGENTS.md so the safety check threshold (50%) is meaningful
	bigContent := strings.Repeat("# Important user content line\n", 50)
	agentsPath := filepath.Join(root, "AGENTS.md")
	require.NoError(t, os.WriteFile(agentsPath, []byte(bigContent), 0644))

	_, err := EnsureInstructionFileMarkers(root)
	require.NoError(t, err)

	after, err := os.ReadFile(agentsPath)
	require.NoError(t, err)
	assert.True(t, len(after) >= len(bigContent),
		"injection should be additive: before=%d after=%d", len(bigContent), len(after))
}

func TestEnsureInstructionFileMarkers_MarkdownFormat(t *testing.T) {
	root := t.TempDir()

	_, err := EnsureInstructionFileMarkers(root)
	require.NoError(t, err)

	agentsPath := filepath.Join(root, "AGENTS.md")
	content, err := os.ReadFile(agentsPath)
	require.NoError(t, err)
	text := string(content)

	assert.Contains(t, text, OxPrimeCheckMarker,
		"AGENTS.md should use markdown header marker")
	assert.Contains(t, text, OxPrimeMarker,
		"AGENTS.md should use markdown footer marker")
}

func TestEnsureInstructionFileMarkers_PlaintextFormat(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".cursor"), 0755))

	// pre-create .cursorrules so it's injected (not skipped as non-primary non-creatable)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".cursorrules"), []byte("# Rules\n"), 0644))

	_, err := EnsureInstructionFileMarkers(root)
	require.NoError(t, err)

	cursorrules := filepath.Join(root, ".cursorrules")
	content, err := os.ReadFile(cursorrules)
	require.NoError(t, err)
	text := string(content)

	assert.Contains(t, text, PlaintextPrimeMarker,
		".cursorrules should use plaintext footer marker")
	assert.Contains(t, text, PlaintextPrimeCheckMarker,
		".cursorrules should use plaintext header marker")
	assert.NotContains(t, text, "<!--",
		".cursorrules should not contain HTML comment markers")
}

// ---------------------------------------------------------------------------
// 3. Removal / Cleanup Tests
// ---------------------------------------------------------------------------

func TestRemoveInstructionFileMarkers_CleansMarkdown(t *testing.T) {
	root := t.TempDir()

	userContent := "# My Project\n\nUse Go 1.22.\n"
	agentsPath := filepath.Join(root, "AGENTS.md")
	require.NoError(t, os.WriteFile(agentsPath, []byte(userContent), 0644))

	_, err := EnsureInstructionFileMarkers(root)
	require.NoError(t, err)

	injected, _ := os.ReadFile(agentsPath)
	require.Contains(t, string(injected), OxPrimeMarker, "setup: markers should be present")

	_, err = RemoveInstructionFileMarkers(root)
	require.NoError(t, err)

	after, err := os.ReadFile(agentsPath)
	require.NoError(t, err)
	text := string(after)

	assert.NotContains(t, text, OxPrimeMarker, "footer marker should be removed")
	assert.NotContains(t, text, OxPrimeCheckMarker, "header marker should be removed")
	assert.Contains(t, text, "Use Go 1.22.", "user content must survive removal")
}

func TestRemoveInstructionFileMarkers_CleansPlaintext(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".cursor"), 0755))

	userContent := "# Cursor Rules\n\nPrefer functional style.\n"
	cursorrules := filepath.Join(root, ".cursorrules")
	require.NoError(t, os.WriteFile(cursorrules, []byte(userContent), 0644))

	_, err := EnsureInstructionFileMarkers(root)
	require.NoError(t, err)

	injected, _ := os.ReadFile(cursorrules)
	require.Contains(t, string(injected), PlaintextPrimeMarker, "setup: plaintext markers should be present")

	_, err = RemoveInstructionFileMarkers(root)
	require.NoError(t, err)

	after, err := os.ReadFile(cursorrules)
	require.NoError(t, err)
	text := string(after)

	assert.NotContains(t, text, PlaintextPrimeMarker, "plaintext footer marker should be removed")
	assert.NotContains(t, text, PlaintextPrimeCheckMarker, "plaintext header marker should be removed")
	assert.Contains(t, text, "Prefer functional style.", "user content must survive removal")
}

func TestRemoveInstructionFileMarkers_LeavesEmptyFileWarning(t *testing.T) {
	root := t.TempDir()

	// let injection create AGENTS.md from scratch (only markers, no user content)
	_, err := EnsureInstructionFileMarkers(root)
	require.NoError(t, err)

	results, err := RemoveInstructionFileMarkers(root)
	require.NoError(t, err)

	agentsPath := filepath.Join(root, "AGENTS.md")
	if _, statErr := os.Stat(agentsPath); statErr == nil {
		content, _ := os.ReadFile(agentsPath)
		assert.NotContains(t, string(content), OxPrimeMarker,
			"markers should be gone even from marker-only file")
		assert.NotContains(t, string(content), OxPrimeCheckMarker,
			"markers should be gone even from marker-only file")
	}

	assert.NotEmpty(t, results, "removal should return results even for marker-only files")
}

func TestRemoveInstructionFileMarkers_NoOpOnCleanFile(t *testing.T) {
	root := t.TempDir()

	cleanContent := "# Clean File\n\nNo markers here.\n"
	agentsPath := filepath.Join(root, "AGENTS.md")
	require.NoError(t, os.WriteFile(agentsPath, []byte(cleanContent), 0644))

	results, err := RemoveInstructionFileMarkers(root)
	require.NoError(t, err)

	// file should be unchanged
	after, err := os.ReadFile(agentsPath)
	require.NoError(t, err)
	assert.Equal(t, cleanContent, string(after), "clean file should not be modified")

	// results should indicate no-op
	for _, r := range results {
		if r.File == "AGENTS.md" {
			assert.Equal(t, alreadyPresent, r.Status,
				"clean file should report already-present (no markers to remove)")
		}
	}
}

func TestRemoveInstructionFileMarkers_HandlesAllFormats(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".cursor"), 0755))

	// pre-create .cursorrules so injection proceeds
	require.NoError(t, os.WriteFile(filepath.Join(root, ".cursorrules"), []byte("# Rules\n"), 0644))

	_, err := EnsureInstructionFileMarkers(root)
	require.NoError(t, err)

	// verify injection happened
	agentsContent, _ := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	require.Contains(t, string(agentsContent), OxPrimeMarker)

	cursorContent, _ := os.ReadFile(filepath.Join(root, ".cursorrules"))
	require.Contains(t, string(cursorContent), PlaintextPrimeMarker)

	// remove all
	_, err = RemoveInstructionFileMarkers(root)
	require.NoError(t, err)

	// verify all markers gone
	agentsAfter, _ := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	assert.NotContains(t, string(agentsAfter), OxPrimeMarker)
	assert.NotContains(t, string(agentsAfter), OxPrimeCheckMarker)

	cursorAfter, _ := os.ReadFile(filepath.Join(root, ".cursorrules"))
	assert.NotContains(t, string(cursorAfter), PlaintextPrimeMarker)
	assert.NotContains(t, string(cursorAfter), PlaintextPrimeCheckMarker)
}

func TestRemoveInstructionFileMarkers_Idempotent(t *testing.T) {
	root := t.TempDir()

	_, err := EnsureInstructionFileMarkers(root)
	require.NoError(t, err)

	_, err = RemoveInstructionFileMarkers(root)
	require.NoError(t, err)

	snapshot := snapshotDir(t, root)

	// second removal should be a no-op
	_, err = RemoveInstructionFileMarkers(root)
	require.NoError(t, err)

	for relPath, before := range snapshot {
		afterBytes, err := os.ReadFile(filepath.Join(root, relPath))
		require.NoError(t, err)
		assert.Equal(t, string(before), string(afterBytes),
			"file %s changed on second removal", relPath)
	}
}

// ---------------------------------------------------------------------------
// 4. HasInstructionFileMarker Tests
// ---------------------------------------------------------------------------

func TestHasInstructionFileMarker_MarkdownMarker(t *testing.T) {
	root := t.TempDir()
	fp := filepath.Join(root, "AGENTS.md")
	content := "# Agents\n\n" + OxPrimeMarker + "\n"
	require.NoError(t, os.WriteFile(fp, []byte(content), 0644))

	assert.True(t, HasInstructionFileMarker(fp),
		"file with markdown marker should return true")
}

func TestHasInstructionFileMarker_PlaintextMarker(t *testing.T) {
	root := t.TempDir()
	fp := filepath.Join(root, ".cursorrules")
	content := "# Rules\n\n" + PlaintextPrimeMarker + "\n"
	require.NoError(t, os.WriteFile(fp, []byte(content), 0644))

	assert.True(t, HasInstructionFileMarker(fp),
		"file with plaintext marker should return true")
}

func TestHasInstructionFileMarker_NoMarker(t *testing.T) {
	root := t.TempDir()
	fp := filepath.Join(root, "AGENTS.md")
	content := "# Agents\n\nNo markers here.\n"
	require.NoError(t, os.WriteFile(fp, []byte(content), 0644))

	assert.False(t, HasInstructionFileMarker(fp),
		"file without any marker should return false")
}

func TestHasInstructionFileMarker_NonexistentFile(t *testing.T) {
	root := t.TempDir()
	fp := filepath.Join(root, "does-not-exist.md")

	assert.False(t, HasInstructionFileMarker(fp),
		"nonexistent file should return false")
}

// ---------------------------------------------------------------------------
// 5. Edge Cases
// ---------------------------------------------------------------------------

func TestEnsureInstructionFileMarkers_EmptyExistingFile(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".cursor"), 0755))

	// create an empty .cursorrules (user ran `touch .cursorrules`)
	cursorrules := filepath.Join(root, ".cursorrules")
	require.NoError(t, os.WriteFile(cursorrules, []byte(""), 0644))

	_, err := EnsureInstructionFileMarkers(root)
	require.NoError(t, err)

	content, err := os.ReadFile(cursorrules)
	require.NoError(t, err)

	assert.Contains(t, string(content), PlaintextPrimeMarker,
		"empty existing file should get markers injected")
}

func TestEnsureInstructionFileMarkers_ReadOnlyFile(t *testing.T) {
	root := t.TempDir()

	agentsPath := filepath.Join(root, "AGENTS.md")
	require.NoError(t, os.WriteFile(agentsPath, []byte("# Read Only\n"), 0444))

	t.Cleanup(func() {
		_ = os.Chmod(agentsPath, 0644)
	})

	_, err := EnsureInstructionFileMarkers(root)

	// should return an error, not panic
	assert.Error(t, err, "writing to a read-only file should produce an error")
}

func TestEnsureInstructionFileMarkers_SymlinkedFile(t *testing.T) {
	root := t.TempDir()

	agentsPath := filepath.Join(root, "AGENTS.md")
	require.NoError(t, os.WriteFile(agentsPath, []byte("# Agents\n"), 0644))

	// CLAUDE.md is a symlink to AGENTS.md
	claudePath := filepath.Join(root, "CLAUDE.md")
	require.NoError(t, os.Symlink("AGENTS.md", claudePath))

	_, err := EnsureInstructionFileMarkers(root)
	require.NoError(t, err)

	agentsContent, err := os.ReadFile(agentsPath)
	require.NoError(t, err)

	// markers should not be doubled from injecting into both the real file and the symlink
	markerCount := strings.Count(string(agentsContent), OxPrimeMarker)
	assert.Equal(t, 1, markerCount,
		"symlink should not cause double injection: found %d markers", markerCount)

	checkCount := strings.Count(string(agentsContent), OxPrimeCheckMarker)
	assert.Equal(t, 1, checkCount,
		"symlink should not cause double check-marker injection: found %d markers", checkCount)
}

func TestEnsureInstructionFileMarkers_KiroCreatesSubdir(t *testing.T) {
	root := t.TempDir()

	// .kiro/ exists but .kiro/steering/ does not
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".kiro"), 0755))

	_, err := EnsureInstructionFileMarkers(root)
	require.NoError(t, err)

	// Kiro spec has Creatable=true, so ox should create .kiro/steering/ox.md
	kiroPath := filepath.Join(root, ".kiro", "steering", "ox.md")
	assert.FileExists(t, kiroPath, ".kiro/steering/ox.md should be created (Creatable=true)")

	// verify the intermediate directory was created
	assert.DirExists(t, filepath.Join(root, ".kiro", "steering"),
		".kiro/steering/ directory should be created")

	content, err := os.ReadFile(kiroPath)
	require.NoError(t, err)

	// kiro uses markdown format per the registry
	assert.Contains(t, string(content), OxPrimeMarker,
		".kiro/steering/ox.md should contain the markdown prime marker")
	assert.Contains(t, string(content), OxPrimeCheckMarker,
		".kiro/steering/ox.md should contain the markdown check marker")
}

// ---------------------------------------------------------------------------
// 6. Registry integrity tests
// ---------------------------------------------------------------------------

func TestInstructionFileRegistry_AllSpecsHaveRequiredFields(t *testing.T) {
	for _, spec := range InstructionFileRegistry {
		assert.NotEmpty(t, spec.AgentType, "every spec must have AgentType")
		assert.NotEmpty(t, spec.DisplayName, "every spec must have DisplayName")
		assert.NotEmpty(t, spec.ProjectFiles, "every spec must list at least one ProjectFile")
		assert.Contains(t, []string{markerFormatMarkdown, markerFormatPlaintext}, spec.MarkerFormat,
			"spec %s has invalid MarkerFormat: %s", spec.AgentType, spec.MarkerFormat)
	}
}

// TestInstructionFileRegistry_GooseSharesAgentsMd pins where the Goose marker
// goes. Goose loads AGENTS.md BEFORE .goosehints, so marking AGENTS.md reaches
// it first and a second .goosehints copy would only duplicate the instruction.
// Goose must also carry no DetectFn — an AGENTS.md-sharing agent that gated on
// detection would suppress the marker in repos where no Goose-specific file
// exists, which is every repo, since Goose creates none.
func TestInstructionFileRegistry_GooseSharesAgentsMd(t *testing.T) {
	var goose *InstructionFileSpec
	for i := range InstructionFileRegistry {
		if InstructionFileRegistry[i].AgentType == agentGoose {
			goose = &InstructionFileRegistry[i]
			break
		}
	}

	require.NotNil(t, goose, "Goose must be registered — it reads AGENTS.md natively")
	assert.Equal(t, []string{"AGENTS.md"}, goose.ProjectFiles)
	assert.Equal(t, markerFormatMarkdown, goose.MarkerFormat)
	assert.Nil(t, goose.DetectFn, "Goose shares AGENTS.md; gating on detection would suppress the marker")
	assert.False(t, goose.Creatable, "AGENTS.md is a primary file; Creatable would be redundant")
}

// TestInstructionFileRegistry_AiderIsAdapterOwned pins Aider OUT of this
// registry. Aider loads only what .aider.conf.yml's `read:` names —
// CONVENTIONS.md by default — so an AGENTS.md entry marked a file Aider never
// reads while README.md and docs/guides/agent-compatibility.md both promised
// CONVENTIONS.md. ox-adapter-aider owns CONVENTIONS.md end to end under its own
// ox:prime:aider markers, so a CONVENTIONS.md entry here is equally wrong: it
// would stack a second, differently-marked prime block on the adapter's.
//
// Failure prevented (the general class, not just Aider): a registry spec that
// names an instruction file its agent does not actually load, or that the
// agent's own adapter already owns — either way ox reports the agent as primed
// when it is not, or double-primes it.
func TestInstructionFileRegistry_AiderIsAdapterOwned(t *testing.T) {
	for _, spec := range InstructionFileRegistry {
		assert.NotEqual(t, "aider", spec.AgentType,
			"Aider must not be in the instruction-file registry — ox-adapter-aider owns CONVENTIONS.md")
		assert.NotContains(t, spec.ProjectFiles, "CONVENTIONS.md",
			"spec %s lists CONVENTIONS.md, which ox-adapter-aider already writes under its own markers", spec.AgentType)
	}
}

func TestInstructionFileRegistry_PrimaryFilesAreMarkdown(t *testing.T) {
	// AGENTS.md and CLAUDE.md must use markdown format -- they support HTML comments
	for _, spec := range InstructionFileRegistry {
		for _, f := range spec.ProjectFiles {
			if primaryInstructionFiles[f] {
				assert.Equal(t, markerFormatMarkdown, spec.MarkerFormat,
					"primary file %s (via %s) must use markdown format", f, spec.AgentType)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// targetRelPaths extracts relative paths from a slice of InstructionFileTarget.
// Path is already relative to git root per the DetectedInstructionFiles contract.
func targetRelPaths(targets []InstructionFileTarget) []string {
	paths := make([]string, len(targets))
	for i, tgt := range targets {
		paths[i] = tgt.Path
	}
	return paths
}

// snapshotDir reads all regular files under root into a map[relPath]content.
func snapshotDir(t *testing.T, root string) map[string][]byte {
	t.Helper()
	snap := make(map[string][]byte)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		// skip symlinks -- read only real files
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		snap[rel] = data
		return nil
	})
	require.NoError(t, err, "snapshotDir walk failed")
	return snap
}
