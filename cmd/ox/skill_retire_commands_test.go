package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// writeStampedCommand writes a legacy Claude command file carrying a VALID ox
// stamp — the shape ox itself installed before the 0.15.0 fold.
func writeStampedCommand(t *testing.T, repoRoot, name, body string) string {
	t.Helper()
	dir := filepath.Join(repoRoot, ".claude", "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir commands: %v", err)
	}
	path := filepath.Join(dir, name+".md")
	if err := os.WriteFile(path, agentx.StampedContent([]byte(body), "0.14.0", "ox"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func claudeTargets() []adapterprotocol.SkillTarget {
	return []adapterprotocol.SkillTarget{{
		Key:        "claude-project",
		Root:       ".claude/skills",
		Format:     adapterprotocol.SkillFormatAgentSkillsV1,
		Scope:      adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}}
}

func retireCommandsRepo(t *testing.T) string {
	t.Helper()
	repoRoot := t.TempDir()
	retireCommandsGit(t, repoRoot, "init", "--initial-branch=main")
	retireCommandsGit(t, repoRoot, "config", "user.email", "test@example.com")
	retireCommandsGit(t, repoRoot, "config", "user.name", "Test User")
	return repoRoot
}

func retireCommandsGit(t *testing.T, repoRoot string, args ...string) string {
	t.Helper()
	out, err := runIsolatedGit(t, repoRoot, args...)
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return out
}

// TestRetireLegacyClaudeCommands_RemovesRetiredNamesNotCurrentOnes is the bug the
// fold introduces if the retired list is not wired in: after the rename the
// catalog knows "ox-cli-prime" while the file on disk is still "ox-prime.md", so
// a sweep keyed on current names matches nothing and every legacy command file
// survives forever. The user then has both /ox-prime and /ox-cli-prime, with the
// old one still serving stale guidance.
func TestRetireLegacyClaudeCommands_RemovesRetiredNamesNotCurrentOnes(t *testing.T) {
	repoRoot := retireCommandsRepo(t)
	retired := writeStampedCommand(t, repoRoot, "ox-prime", "legacy prime guidance\n")
	orphan := writeStampedCommand(t, repoRoot, "ox-session-resume", "orphan that no release ever removed\n")

	retireLegacyClaudeCommands(repoRoot, claudeTargets())

	if _, err := os.Stat(retired); err == nil {
		t.Errorf("legacy ox-prime.md survived the retirement sweep; /ox-prime would keep serving stale guidance beside /ox-cli-prime")
	}
	if _, err := os.Stat(orphan); err == nil {
		t.Errorf("orphaned ox-session-resume.md survived; this is the exact class of file that accumulated across releases")
	}
}

// TestRetireLegacyClaudeCommands_PreservesUserEditedAndUserAuthoredFiles is the
// data-loss guard. These are LEGACY files, written before the reserved-prefix
// contract existed, so their author never agreed to "ox owns this path".
//
// Two cases must survive: a file the user wrote themselves (no stamp at all), and
// an ox-stamped file the user has since edited (stamp present but no longer
// matching the body). Deleting either destroys work ox was never given.
func TestRetireLegacyClaudeCommands_PreservesUserEditedAndUserAuthoredFiles(t *testing.T) {
	repoRoot := retireCommandsRepo(t)

	// Stamped by ox, then edited by the user: the stamp no longer verifies.
	edited := writeStampedCommand(t, repoRoot, "ox-status", "original body\n")
	if err := os.WriteFile(edited, []byte("<!-- ox-hash: deadbeefcafe ver: 0.14.0 -->\nMY OWN NOTES\n"), 0o644); err != nil {
		t.Fatalf("simulate user edit: %v", err)
	}

	// Never touched by ox: a command the user authored under a retired name.
	dir := filepath.Join(repoRoot, ".claude", "commands")
	authored := filepath.Join(dir, "ox-doctor.md")
	if err := os.WriteFile(authored, []byte("my own doctor shortcut\n"), 0o644); err != nil {
		t.Fatalf("write user-authored command: %v", err)
	}

	before := map[string][]byte{}
	for _, p := range []string{edited, authored} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		before[p] = b
	}

	retireLegacyClaudeCommands(repoRoot, claudeTargets())

	for p, want := range before {
		got, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("%s was deleted; ox removed a legacy file whose author never agreed to ox ownership: %v", filepath.Base(p), err)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("%s was modified by the retirement sweep", filepath.Base(p))
		}
	}
}

// TestRetireLegacyClaudeCommands_NoOpWithoutAClaudeTarget: a project that never
// selected Claude has no .claude/commands surface to retire, and the sweep must
// not reach into a directory it does not own.
func TestRetireLegacyClaudeCommands_NoOpWithoutAClaudeTarget(t *testing.T) {
	repoRoot := retireCommandsRepo(t)
	kept := writeStampedCommand(t, repoRoot, "ox-prime", "legacy prime guidance\n")

	retireLegacyClaudeCommands(repoRoot, []adapterprotocol.SkillTarget{{
		Key:        "agents-project",
		Root:       ".agents/skills",
		Format:     adapterprotocol.SkillFormatAgentSkillsV1,
		Scope:      adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}})

	if _, err := os.Stat(kept); err != nil {
		t.Errorf("sweep removed a Claude command in a project with no Claude target: %v", err)
	}
}

func TestRetireLegacyClaudeCommands_PreservesTrackedFilesForMigration(t *testing.T) {
	repoRoot := retireCommandsRepo(t)
	if err := os.WriteFile(filepath.Join(repoRoot, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	retireCommandsGit(t, repoRoot, "add", "--", "README.md")
	retireCommandsGit(t, repoRoot, "commit", "-q", "-m", "seed")
	tracked := writeStampedCommand(t, repoRoot, "ox-prime", "legacy prime guidance\n")
	retireCommandsGit(t, repoRoot, "add", "--", ".claude/commands/ox-prime.md")
	retireCommandsGit(t, repoRoot, "commit", "-q", "-m", "track legacy command")

	retireLegacyClaudeCommands(repoRoot, claudeTargets())

	if _, err := os.Stat(tracked); err != nil {
		t.Fatalf("tracked command was deleted before the guarded migration could verify it: %v", err)
	}
	if status := retireCommandsGit(t, repoRoot, "status", "--porcelain", "--", ".claude/commands/ox-prime.md"); status != "" {
		t.Fatalf("init-time retirement dirtied the tracked command: %q", status)
	}
}
