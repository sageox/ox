//go:build !short

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/skillmanager"
)

// TestHasLegacyOxCommands_RequiresAVerifyingStamp: the selection signal must not
// fire on a file the user owns, or ox would keep "detecting" work it must never do.
func TestHasLegacyOxCommands_RequiresAVerifyingStamp(t *testing.T) {
	root := t.TempDir()
	cmds := filepath.Join(root, ".claude", "commands")
	if err := os.MkdirAll(cmds, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cmds, "ox-status.md"),
		[]byte("<!-- ox-hash: deadbeefcafe ver: 0.14.0 -->\nmine now\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if hasLegacyOxCommands(root) {
		t.Error("a user-edited command counted as a legacy ox command")
	}

	if err := os.WriteFile(filepath.Join(cmds, "ox-prime.md"),
		agentx.StampedContent([]byte("legacy\n"), "0.14.0", "ox"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !hasLegacyOxCommands(root) {
		t.Error("a stamp-verified legacy command was not detected")
	}
}

// TestReconcileCommittedSkills_ReAssertsDefaultBundles covers the failure that
// kept the ONE committed file out of a real repository.
//
// A lockfile records the bundles chosen at `ox init`. Without re-asserting the
// defaults, a bundle added by a later release never reaches an existing project —
// which is exactly how the `sageox` on-ramp failed to install during the first
// end-to-end run.
func TestReconcileCommittedSkills_ReAssertsDefaultBundles(t *testing.T) {
	root := migrationRepo(t)

	if _, err := reconcileCommittedSkills(root); err != nil {
		t.Fatalf("reconcileCommittedSkills: %v", err)
	}

	desired, _, err := skillmanager.LoadDesired(root)
	if err != nil {
		t.Fatalf("LoadDesired: %v", err)
	}
	have := map[string]bool{}
	for _, b := range desired.Bundles {
		have[b.ID] = true
	}
	if !have["onramp"] {
		t.Errorf("the onramp bundle was not re-asserted; the one committed file never installs. bundles=%v", desired.Bundles)
	}

	onramp := filepath.Join(root, ".claude", "skills", skillmanager.CommittedOnRamp, "SKILL.md")
	if _, err := os.Stat(onramp); err != nil {
		t.Errorf("the committed on-ramp was not materialized: %v", err)
	}
}

// TestBootstrapLegacySkillState_LegacyCommandsAloneEarnTheReplacement is the
// guard against making the command→skill fold a pure regression.
//
// A project initialized before skills existed has ox-stamped /ox* command files
// and NO recorded skill target. The migration removes those command files; if
// nothing recorded a target, the reconcile installs no replacement, and the user
// loses /ox-prime and every other lifecycle surface while gaining nothing. That
// was observed on a real repository, which is why the presence of a stamped
// legacy command is itself treated as evidence that this project wants the
// surface.
func TestBootstrapLegacySkillState_LegacyCommandsAloneEarnTheReplacement(t *testing.T) {
	root := t.TempDir()
	cmds := filepath.Join(root, ".claude", "commands")
	if err := os.MkdirAll(cmds, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cmds, "ox-prime.md"),
		agentx.StampedContent([]byte("legacy prime\n"), "0.14.0", "ox"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	desired, targets, err := bootstrapLegacySkillState(root, skillmanager.DesiredSkills{}, nil)
	if err != nil {
		t.Fatalf("bootstrapLegacySkillState: %v", err)
	}
	if len(targets) == 0 || len(desired.Targets) == 0 {
		t.Fatal("a project with stamped legacy commands got no skill target; the fold would delete its surface and install nothing")
	}
	if len(desired.Bundles) == 0 {
		t.Error("targets were selected but no default bundle was, so nothing would install")
	}
}

// TestBootstrapLegacySkillState_NoLegacyEvidenceSelectsNothing is the other half.
// Selecting targets for a project that shows no sign of wanting them would push
// vendor files into a repository that never asked.
func TestBootstrapLegacySkillState_NoLegacyEvidenceSelectsNothing(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".claude", "commands"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A command the user wrote themselves is not evidence.
	if err := os.WriteFile(filepath.Join(root, ".claude", "commands", "ox-kb.md"),
		[]byte("mine\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	desired, targets, err := bootstrapLegacySkillState(root, skillmanager.DesiredSkills{}, nil)
	if err != nil {
		t.Fatalf("bootstrapLegacySkillState: %v", err)
	}
	if len(targets) != 0 || len(desired.Targets) != 0 {
		t.Errorf("ox selected itself into a project with no evidence it wants skills: targets=%v", desired.Targets)
	}
}

// TestDetectedOrClaudeTargets_FallsBackToTheClaudeProjection: the evidence that
// reaches this path is an ox-stamped .claude/commands file, which is
// Claude-specific — so the fallback must be the Claude projection, not nothing.
func TestDetectedOrClaudeTargets_FallsBackToTheClaudeProjection(t *testing.T) {
	root := t.TempDir()
	got := detectedOrClaudeTargets(root)
	if len(got) == 0 {
		t.Fatal("no fallback target: a legacy-command repo would get no replacement surface")
	}
	var haveClaude bool
	for _, tgt := range got {
		if strings.Contains(tgt.Root, ".claude") {
			haveClaude = true
		}
	}
	if !haveClaude {
		t.Errorf("fallback did not include the Claude projection: %+v", got)
	}
}
