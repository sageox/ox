//go:build !short

package main

import (
	"os"
	"path/filepath"
	"strings"
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
