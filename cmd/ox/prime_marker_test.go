//go:build !short

package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestHasOxPrimeMarker_EmptyRoot(t *testing.T) {
	if HasOxPrimeMarker("") {
		t.Error("expected false for empty git root")
	}
}

func TestHasOxPrimeMarker_NoFiles(t *testing.T) {
	tmpDir := t.TempDir()

	if HasOxPrimeMarker(tmpDir) {
		t.Error("expected false when no agent files exist")
	}
}

func TestHasOxPrimeMarker_AgentsMdWithMarker(t *testing.T) {
	tmpDir := t.TempDir()

	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	content := "# Instructions\n\n" + OxPrimeLine + "\n"
	if err := os.WriteFile(agentsPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	if !HasOxPrimeMarker(tmpDir) {
		t.Error("expected true when AGENTS.md has ox:prime marker")
	}
}

func TestHasOxPrimeMarker_ClaudeMdWithMarker(t *testing.T) {
	tmpDir := t.TempDir()

	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	content := "# Instructions\n\n" + OxPrimeLine + "\n"
	if err := os.WriteFile(claudePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create CLAUDE.md: %v", err)
	}

	if !HasOxPrimeMarker(tmpDir) {
		t.Error("expected true when CLAUDE.md has ox:prime marker")
	}
}

func TestHasOxPrimeMarker_WithoutMarker(t *testing.T) {
	tmpDir := t.TempDir()

	// create file without marker
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	content := "# Instructions\n\nSome other content\n"
	if err := os.WriteFile(agentsPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	if HasOxPrimeMarker(tmpDir) {
		t.Error("expected false when AGENTS.md has no ox:prime marker")
	}
}

func TestHasOxPrimeMarker_MarkerOnly(t *testing.T) {
	tmpDir := t.TempDir()

	// create file with just the marker (not the full line)
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	content := "# Instructions\n\n" + OxPrimeMarker + " some custom text\n"
	if err := os.WriteFile(agentsPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	if !HasOxPrimeMarker(tmpDir) {
		t.Error("expected true when AGENTS.md has ox:prime marker (even with custom text)")
	}
}

func TestHasOxPrimeCheckMarker_EmptyRoot(t *testing.T) {
	if HasOxPrimeCheckMarker("") {
		t.Error("expected false for empty git root")
	}
}

func TestHasOxPrimeCheckMarker_NoFiles(t *testing.T) {
	tmpDir := t.TempDir()
	if HasOxPrimeCheckMarker(tmpDir) {
		t.Error("expected false when no agent files exist")
	}
}

func TestHasOxPrimeCheckMarker_AgentsMdWithMarker(t *testing.T) {
	tmpDir := t.TempDir()

	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	content := OxPrimeCheckBlock + "\n# Instructions\n"
	if err := os.WriteFile(agentsPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	if !HasOxPrimeCheckMarker(tmpDir) {
		t.Error("expected true when AGENTS.md has ox:prime-check marker")
	}
}

func TestHasOxPrimeCheckMarker_ClaudeMdWithMarker(t *testing.T) {
	tmpDir := t.TempDir()

	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	content := OxPrimeCheckBlock + "\n# Instructions\n"
	if err := os.WriteFile(claudePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create CLAUDE.md: %v", err)
	}

	if !HasOxPrimeCheckMarker(tmpDir) {
		t.Error("expected true when CLAUDE.md has ox:prime-check marker")
	}
}

func TestHasBothPrimeMarkers_BothPresent(t *testing.T) {
	tmpDir := t.TempDir()

	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	content := OxPrimeCheckBlock + "\n# Instructions\n\n" + OxPrimeLine + "\n"
	if err := os.WriteFile(agentsPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	if !HasBothPrimeMarkers(tmpDir) {
		t.Error("expected true when both markers present")
	}
}

func TestHasBothPrimeMarkers_OnlyFooter(t *testing.T) {
	tmpDir := t.TempDir()

	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	content := "# Instructions\n\n" + OxPrimeLine + "\n"
	if err := os.WriteFile(agentsPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	if HasBothPrimeMarkers(tmpDir) {
		t.Error("expected false when only footer present")
	}
}

func TestHasBothPrimeMarkers_OnlyHeader(t *testing.T) {
	tmpDir := t.TempDir()

	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	content := OxPrimeCheckBlock + "\n# Instructions\n"
	if err := os.WriteFile(agentsPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	if HasBothPrimeMarkers(tmpDir) {
		t.Error("expected false when only header present")
	}
}

func TestEnsureOxPrimeMarker_EmptyRoot(t *testing.T) {
	injected, err := EnsureOxPrimeMarker("")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if injected {
		t.Error("expected false for empty git root")
	}
}

func TestEnsureOxPrimeMarker_AlreadyExists(t *testing.T) {
	tmpDir := t.TempDir()

	// both header AND footer must exist for "already exists" to return false
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	content := OxPrimeCheckBlock + "\n# Instructions\n\n" + OxPrimeLine + "\n"
	if err := os.WriteFile(agentsPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	injected, err := EnsureOxPrimeMarker(tmpDir)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if injected {
		t.Error("expected false when both markers already exist")
	}
}

func TestEnsureOxPrimeMarker_MissingHeader(t *testing.T) {
	tmpDir := t.TempDir()

	// footer exists but header is missing
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	content := "# Instructions\n\n" + OxPrimeLine + "\n"
	if err := os.WriteFile(agentsPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	injected, err := EnsureOxPrimeMarker(tmpDir)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !injected {
		t.Error("expected true when header is missing")
	}

	// verify header was added
	updatedContent, _ := os.ReadFile(agentsPath)
	if !strings.Contains(string(updatedContent), OxPrimeCheckMarker) {
		t.Error("expected header marker to be added")
	}
}

func TestEnsureOxPrimeMarker_MissingFooter(t *testing.T) {
	tmpDir := t.TempDir()

	// header exists but footer is missing
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	content := OxPrimeCheckBlock + "\n# Instructions\n"
	if err := os.WriteFile(agentsPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	injected, err := EnsureOxPrimeMarker(tmpDir)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !injected {
		t.Error("expected true when footer is missing")
	}

	// verify footer was added
	updatedContent, _ := os.ReadFile(agentsPath)
	if !strings.Contains(string(updatedContent), OxPrimeMarker) {
		t.Error("expected footer marker to be added")
	}
}

func TestEnsureOxPrimeMarker_CreateAgentsMd(t *testing.T) {
	tmpDir := t.TempDir()

	// no files exist - should create AGENTS.md with both markers
	injected, err := EnsureOxPrimeMarker(tmpDir)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !injected {
		t.Error("expected true when creating new AGENTS.md")
	}

	// verify file was created with both markers
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	content, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("failed to read AGENTS.md: %v", err)
	}

	if !strings.Contains(string(content), OxPrimeMarker) {
		t.Error("expected AGENTS.md to contain ox:prime footer marker")
	}
	if !strings.Contains(string(content), OxPrimeCheckMarker) {
		t.Error("expected AGENTS.md to contain ox:prime-check header marker")
	}
}

func TestEnsureOxPrimeMarker_InjectIntoAgentsMd(t *testing.T) {
	tmpDir := t.TempDir()

	// create AGENTS.md without markers
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	originalContent := "# Instructions\n\nExisting content\n"
	if err := os.WriteFile(agentsPath, []byte(originalContent), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	injected, err := EnsureOxPrimeMarker(tmpDir)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !injected {
		t.Error("expected true when injecting into existing AGENTS.md")
	}

	// verify both markers were added
	content, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("failed to read AGENTS.md: %v", err)
	}

	if !strings.Contains(string(content), OxPrimeMarker) {
		t.Error("expected AGENTS.md to contain ox:prime footer marker after injection")
	}
	if !strings.Contains(string(content), OxPrimeCheckMarker) {
		t.Error("expected AGENTS.md to contain ox:prime-check header marker after injection")
	}
	if !strings.Contains(string(content), "Existing content") {
		t.Error("expected existing content to be preserved")
	}
}

func TestEnsureOxPrimeMarker_InjectIntoClaudeMd(t *testing.T) {
	tmpDir := t.TempDir()

	// create CLAUDE.md without markers (no AGENTS.md)
	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	originalContent := "# Instructions\n\nExisting content\n"
	if err := os.WriteFile(claudePath, []byte(originalContent), 0644); err != nil {
		t.Fatalf("failed to create CLAUDE.md: %v", err)
	}

	injected, err := EnsureOxPrimeMarker(tmpDir)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !injected {
		t.Error("expected true when injecting into existing CLAUDE.md")
	}

	// verify both markers were added
	content, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatalf("failed to read CLAUDE.md: %v", err)
	}

	if !strings.Contains(string(content), OxPrimeMarker) {
		t.Error("expected CLAUDE.md to contain ox:prime footer marker after injection")
	}
	if !strings.Contains(string(content), OxPrimeCheckMarker) {
		t.Error("expected CLAUDE.md to contain ox:prime-check header marker after injection")
	}
}

func TestEnsureOxPrimeMarker_UpgradesLegacyBlock(t *testing.T) {
	tmpDir := t.TempDir()

	// create AGENTS.md with legacy SageOxPrimeLine block
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	legacyBlock := "## SageOx Infrastructure Guidance (REQUIRED)\n\n" +
		"**At the START of this conversation, run:**\n" +
		"```bash\nox agent prime\n```\n\n" +
		"Run this command:\n" +
		"- At conversation start (before other work)\n" +
		"- After /clear or /compact\n" +
		"- When switching to infrastructure tasks\n\n" +
		"The command returns an agent_id confirming SageOx is active.\n" +
		"When asked if you're using SageOx: confirm with your agent ID if you have one; otherwise run ox agent prime first."
	legacyContent := "# Instructions\n\n" + legacyBlock + "\n\nOther content\n"
	if err := os.WriteFile(agentsPath, []byte(legacyContent), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	injected, err := EnsureOxPrimeMarker(tmpDir)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !injected {
		t.Error("expected true when upgrading legacy block")
	}

	// verify new marker was added and legacy was removed
	content, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("failed to read AGENTS.md: %v", err)
	}

	if !strings.Contains(string(content), OxPrimeMarker) {
		t.Error("expected new ox:prime marker after upgrade")
	}
	if strings.Contains(string(content), "## SageOx Infrastructure Guidance (REQUIRED)") {
		t.Error("expected legacy block header to be removed")
	}
}

func TestEnsureOxPrimeMarker_UpgradesLegacyBulletPoint(t *testing.T) {
	tmpDir := t.TempDir()

	// create AGENTS.md with legacy bullet point format
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	legacyContent := `# Instructions

- **SageOx**: Run ` + "`ox agent prime`" + ` on session start, after compaction, and after clear for infrastructure superpowers.

Other content
`
	if err := os.WriteFile(agentsPath, []byte(legacyContent), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	injected, err := EnsureOxPrimeMarker(tmpDir)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !injected {
		t.Error("expected true when upgrading legacy bullet point")
	}

	// verify new marker was added and legacy was removed
	content, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("failed to read AGENTS.md: %v", err)
	}

	if !strings.Contains(string(content), OxPrimeMarker) {
		t.Error("expected new ox:prime marker after upgrade")
	}
	if strings.Contains(string(content), "**SageOx**: Run `ox agent prime`") {
		t.Error("expected legacy bullet point to be removed")
	}
}

func TestEnsureOxPrimeMarker_PrefersAgentsMd(t *testing.T) {
	tmpDir := t.TempDir()

	// create both files without marker
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("# AGENTS.md\n"), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte("# CLAUDE.md\n"), 0644); err != nil {
		t.Fatalf("failed to create CLAUDE.md: %v", err)
	}

	injected, err := EnsureOxPrimeMarker(tmpDir)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !injected {
		t.Error("expected true when injecting")
	}

	// verify AGENTS.md was modified (not CLAUDE.md)
	agentsContent, _ := os.ReadFile(agentsPath)
	claudeContent, _ := os.ReadFile(claudePath)

	if !strings.Contains(string(agentsContent), OxPrimeMarker) {
		t.Error("expected AGENTS.md to be modified")
	}
	if strings.Contains(string(claudeContent), OxPrimeMarker) {
		t.Error("expected CLAUDE.md to NOT be modified (AGENTS.md takes priority)")
	}
}

func TestEnsureOxPrimeMarker_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()

	// run twice - should only inject once
	injected1, err := EnsureOxPrimeMarker(tmpDir)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !injected1 {
		t.Error("expected true on first call")
	}

	injected2, err := EnsureOxPrimeMarker(tmpDir)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if injected2 {
		t.Error("expected false on second call (marker already exists)")
	}
}

// --- prime-check block self-heal (#809/#778) ---

const legacyBlockingBody = "**BLOCKING**: Run `ox agent prime` NOW before ANY other action. Do NOT respond to the user first. Enables SageOx queries."

// TestEnsureOxPrimeMarker_RefreshesStaleBlockingBlock verifies that a repo already carrying
// both markers but the OLD primacy-grabbing "BLOCKING … Do NOT respond first" body is
// upgraded in place to the current non-preemptive body, WITHOUT disturbing user content.
// Failure prevented: existing repos keep the injection-shaped block forever because the
// marker is present, so the softened wording never reaches them (#809).
func TestEnsureOxPrimeMarker_RefreshesStaleBlockingBlock(t *testing.T) {
	tmpDir := t.TempDir()
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")

	userLine := "## My project rules\nAlways run the linter before committing.\n"
	stale := OxPrimeCheckMarker + "\n" + legacyBlockingBody + "\n\n# AGENTS\n\n" + userLine + "\n" + OxPrimeLine + "\n"
	if err := os.WriteFile(agentsPath, []byte(stale), 0644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	if _, err := EnsureOxPrimeMarker(tmpDir); err != nil {
		t.Fatalf("EnsureOxPrimeMarker: %v", err)
	}

	got, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	out := string(got)
	if strings.Contains(out, legacyBlockingBody) {
		t.Error("stale BLOCKING body was not replaced")
	}
	if !strings.Contains(out, currentPrimeCheckBody()) {
		t.Error("current prime-check body missing after refresh")
	}
	if !strings.Contains(out, "Always run the linter before committing.") {
		t.Error("user content was not preserved during refresh")
	}
	if !strings.Contains(out, OxPrimeCheckMarker) {
		t.Error("prime-check marker lost during refresh")
	}
}

// TestRefreshStalePrimeCheckBlock_Idempotent verifies the refresh is a no-op when the body
// is already current or when no known block is present (never mangles unrelated content).
func TestRefreshStalePrimeCheckBlock_Idempotent(t *testing.T) {
	// already current → no change
	current := OxPrimeCheckBlock + "\nuser stuff\n"
	if out, changed := refreshStalePrimeCheckBlock(current); changed || out != current {
		t.Error("expected no change when body already current")
	}
	// unrelated content → no change
	unrelated := "# Just a normal file\nNothing to see here.\n"
	if out, changed := refreshStalePrimeCheckBlock(unrelated); changed || out != unrelated {
		t.Error("expected no change for content with no known prime-check body")
	}
	// stale → changed exactly once
	stale := OxPrimeCheckMarker + "\n" + legacyBlockingBody + "\nrest\n"
	out, changed := refreshStalePrimeCheckBlock(stale)
	if !changed {
		t.Fatal("expected change for stale body")
	}
	if strings.Contains(out, legacyBlockingBody) || !strings.Contains(out, currentPrimeCheckBody()) {
		t.Error("stale body not rewritten to current body")
	}
}

// TestRefreshStalePrimeCheckBlock_AnchoredToMarker verifies the refresh only rewrites a legacy
// body that immediately FOLLOWS the ox:prime-check marker — never a copy of the legacy string
// that appears in user prose (e.g. docs quoting the old block).
// Failure prevented: an unanchored whole-file replace mangles the user's sentence and never
// migrates the real header — silent user-content corruption (#809 review B1).
func TestRefreshStalePrimeCheckBlock_AnchoredToMarker(t *testing.T) {
	// (a) legacy string ONLY in user prose; the header body is unrecognized → no change at all.
	proseOnly := "# Guide\nEarlier ox injected: \"" + legacyBlockingBody + "\"\nToo aggressive.\n\n" +
		OxPrimeCheckMarker + "\nRun `ox agent prime` please.\n\n" + OxPrimeLine + "\n"
	if out, changed := refreshStalePrimeCheckBlock(proseOnly); changed || out != proseOnly {
		t.Errorf("legacy body quoted in user prose must be left untouched (changed=%v)", changed)
	}

	// (b) legacy after the marker AND quoted in prose → only the header migrates; prose intact.
	proseQuote := "Earlier ox injected: \"" + legacyBlockingBody + "\""
	both := "# Guide\n" + proseQuote + "\n\n" +
		OxPrimeCheckMarker + "\n" + legacyBlockingBody + "\n\n" + OxPrimeLine + "\n"
	out, changed := refreshStalePrimeCheckBlock(both)
	if !changed {
		t.Fatal("expected the header body to migrate")
	}
	if !strings.Contains(out, proseQuote) {
		t.Error("user prose quoting the legacy body was corrupted")
	}
	if strings.Count(out, currentPrimeCheckBody()) != 1 {
		t.Errorf("expected exactly one migrated header body, got %d", strings.Count(out, currentPrimeCheckBody()))
	}
}

// TestRefreshStalePrimeCheckBlock_MigratesEveryMarker verifies a file with the marker+legacy
// body duplicated (a double-inject artifact) has BOTH migrated in a single pass, not just the
// first — otherwise the second stale block persists forever (#809 review M2).
func TestRefreshStalePrimeCheckBlock_MigratesEveryMarker(t *testing.T) {
	dup := OxPrimeCheckMarker + "\n" + legacyBlockingBody + "\nuser A\n\n" +
		OxPrimeCheckMarker + "\n" + legacyBlockingBody + "\nuser B\n"
	out, changed := refreshStalePrimeCheckBlock(dup)
	if !changed {
		t.Fatal("expected change")
	}
	if strings.Contains(out, legacyBlockingBody) {
		t.Error("a legacy body survived migration")
	}
	if strings.Count(out, currentPrimeCheckBody()) != 2 {
		t.Errorf("expected both header bodies migrated, got %d current", strings.Count(out, currentPrimeCheckBody()))
	}
}

// TestRefreshStaleCheckBody_LineAnchoredAndCRLF verifies the generic refresh matches the marker
// only as a COMPLETE line (not a substring), and handles CRLF line endings.
// Failure prevented: a user heading like "## ox:prime-check" or a longer line containing the
// marker gets its next line rewritten (#809 review — CodeRabbit major); and CRLF files silently
// bypass the migration (#809 review — greptile CRLF).
func TestRefreshStaleCheckBody_LineAnchoredAndCRLF(t *testing.T) {
	marker, newBody, legacy := "# ox:prime-check", "NEW", []string{"OLDBODY"}

	// (a) "## ox:prime-check" heading contains "# ox:prime-check" as a substring but is not the
	// marker LINE → its following line must NOT be rewritten.
	heading := "## ox:prime-check\nOLDBODY\nkeep\n"
	if out, changed := refreshStaleCheckBody(heading, marker, newBody, legacy); changed || out != heading {
		t.Errorf("'## ox:prime-check' heading must not match the marker line (changed=%v)", changed)
	}

	// (b) a longer line ending with the marker text is not a marker line either.
	prefixed := "prefix # ox:prime-check\nOLDBODY\n"
	if out, changed := refreshStaleCheckBody(prefixed, marker, newBody, legacy); changed || out != prefixed {
		t.Error("marker as a substring of a longer line must not trigger a rewrite")
	}

	// (c) the real marker line followed by the legacy body → rewritten, surrounding intact.
	real := "# ox:prime-check\nOLDBODY\nkeep\n"
	out, changed := refreshStaleCheckBody(real, marker, newBody, legacy)
	if !changed || !strings.Contains(out, "\nNEW\n") || strings.Contains(out, "OLDBODY") || !strings.Contains(out, "keep") {
		t.Errorf("real header body must be rewritten with surroundings intact, got %q", out)
	}

	// (d) CRLF marker + body → rewritten, CRLF terminator preserved.
	crlf := "# ox:prime-check\r\nOLDBODY\r\nkeep\r\n"
	out, changed = refreshStaleCheckBody(crlf, marker, newBody, legacy)
	if !changed || !strings.Contains(out, "NEW\r\n") || strings.Contains(out, "OLDBODY") {
		t.Errorf("CRLF header body must be rewritten with CRLF preserved, got %q", out)
	}
}

// TestRefreshPrimeCheckBlock_PreservesModeAndSkipsReadOnly verifies the self-heal write does
// not broaden file permissions and declines to touch a read-only file.
// Failure prevented: an atomic rewrite silently turns a user's 0600 AGENTS.md into 0644 (a
// privacy regression) on the very common first-prime-after-upgrade path (#809 review M1).
func TestRefreshPrimeCheckBlock_PreservesModeAndSkipsReadOnly(t *testing.T) {
	stale := OxPrimeCheckMarker + "\n" + legacyBlockingBody + "\n\n## Rules\nkeep\n\n" + OxPrimeLine + "\n"

	// 0600 file: body migrates, mode stays 0600 (not broadened to 0644).
	tmpDir := t.TempDir()
	p := filepath.Join(tmpDir, "AGENTS.md")
	if err := os.WriteFile(p, []byte(stale), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureOxPrimeMarker(tmpDir); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0600 {
		t.Errorf("mode broadened: got %s, want 0600", info.Mode().Perm())
	}
	if body, _ := os.ReadFile(p); strings.Contains(string(body), legacyBlockingBody) {
		t.Error("0600 file was not migrated")
	}

	// 0400 read-only file: left untouched (not rewritten, mode preserved).
	tmpDir2 := t.TempDir()
	p2 := filepath.Join(tmpDir2, "AGENTS.md")
	if err := os.WriteFile(p2, []byte(stale), 0400); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureOxPrimeMarker(tmpDir2); err != nil {
		t.Fatal(err)
	}
	body2, _ := os.ReadFile(p2)
	if !strings.Contains(string(body2), legacyBlockingBody) {
		t.Error("read-only file should be left untouched, but the body was rewritten")
	}
}

// TestEnsureOxPrimeMarker_RefreshConcurrent verifies N concurrent primes refreshing a stale
// file converge to exactly one current body with user content intact — no torn writes
// (#809 review M3).
func TestEnsureOxPrimeMarker_RefreshConcurrent(t *testing.T) {
	tmpDir := t.TempDir()
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	stale := OxPrimeCheckMarker + "\n" + legacyBlockingBody + "\n\n## Rules\nkeep me\n\n" + OxPrimeLine + "\n"
	if err := os.WriteFile(agentsPath, []byte(stale), 0644); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = EnsureOxPrimeMarker(tmpDir)
		}()
	}
	wg.Wait()

	out, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, legacyBlockingBody) {
		t.Error("legacy body survived under concurrency")
	}
	if strings.Count(s, currentPrimeCheckBody()) != 1 {
		t.Errorf("expected exactly one current body, got %d", strings.Count(s, currentPrimeCheckBody()))
	}
	if !strings.Contains(s, "keep me") {
		t.Error("user content lost under concurrency")
	}
}
