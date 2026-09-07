//go:build !short

package main

import (
	"os"
	"path/filepath"
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
