package autofix

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/version"

	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

func driftRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.email", "test@sageox.example"},
		{"config", "user.name", "Test"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return root
}

// TestCheckSkillsInventoryDrift_NeverInstallsIntoAnUnselectedRepo: a repository
// that never ran `ox init` has no skill targets. Materializing into it would put
// vendor files in a project that never asked for them — the daemon must not make
// that choice on the user's behalf.
func TestCheckSkillsInventoryDrift_NeverInstallsIntoAnUnselectedRepo(t *testing.T) {
	root := driftRepo(t)

	res := checkSkillsInventoryDrift(context.Background(), root)

	if res.Status != StatusClean {
		t.Errorf("unselected repo produced status %v (%s)", res.Status, res.Summary)
	}
	for _, dir := range []string{".claude", ".agents", ".factory"} {
		if _, err := os.Stat(filepath.Join(root, dir)); err == nil {
			t.Errorf("the daemon created %s/ in a repository that never selected ox", dir)
		}
	}
}

// TestCheckSkillsInventoryDrift_EmptyRepoPathIsANoOp guards the daemon's own
// bookkeeping: an unresolved repo path must never be treated as the process cwd.
func TestCheckSkillsInventoryDrift_EmptyRepoPathIsANoOp(t *testing.T) {
	res := checkSkillsInventoryDrift(context.Background(), "")
	if res.Status != StatusClean || res.Summary != "" {
		t.Errorf("empty repo path produced work: status=%v summary=%q", res.Status, res.Summary)
	}
}

// TestCheckSkillsInventoryDrift_CanceledContextStopsBeforeAnyWork: the daemon
// cancels its tick on shutdown. The check must observe that before touching a
// repository, not midway through an apply.
func TestCheckSkillsInventoryDrift_CanceledContextStopsBeforeAnyWork(t *testing.T) {
	root := driftRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := checkSkillsInventoryDrift(ctx, root)

	if res.Status != StatusClean {
		t.Errorf("canceled tick reported work: status=%v (%s)", res.Status, res.Summary)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude")); err == nil {
		t.Error("the daemon wrote into a repository after its context was canceled")
	}
}

// TestCheckSkillsInventoryDrift_ReportsAnUnreadableLockfileInsteadOfGuessing:
// a corrupt lockfile must surface as an error, never be silently treated as
// "no targets" — that would let the daemon quietly stop maintaining a repo.
func TestCheckSkillsInventoryDrift_ReportsAnUnreadableLockfileInsteadOfGuessing(t *testing.T) {
	root := driftRepo(t)
	lockDir := filepath.Join(root, ".sageox")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lockDir, "skills.lock.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res := checkSkillsInventoryDrift(context.Background(), root)

	if res.Status != StatusError {
		t.Errorf("a corrupt lockfile was not reported: status=%v summary=%q", res.Status, res.Summary)
	}
	if !strings.Contains(res.Summary, "lockfile") {
		t.Errorf("summary does not name the problem: %q", res.Summary)
	}
}

// TestCheckSkillsInventoryDrift_MaterializesAndConverges is the daemon's job seen
// end to end: a selected repository whose managed tree has drifted gets it back,
// and a second tick reports nothing. A tick that reported work every time would
// mean the daemon never settles.
func TestCheckSkillsInventoryDrift_MaterializesAndConverges(t *testing.T) {
	root := driftRepo(t)
	targets := []adapterprotocol.SkillTarget{{
		Key:        "claude-project",
		Root:       ".claude/skills",
		Format:     adapterprotocol.SkillFormatAgentSkillsV1,
		Scope:      adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}}
	if _, err := skillmanager.ReconcileUpdate(root, version.Version,
		func(d skillmanager.DesiredSkills, ct []adapterprotocol.SkillTarget) (skillmanager.DesiredSkills, []adapterprotocol.SkillTarget, error) {
			return skillmanager.DefaultDesired(targets), targets, nil
		}); err != nil {
		t.Skipf("could not seed a selected repository: %v", err)
	}

	// Drift: the managed tree is deleted out from under the recorded state.
	if err := os.RemoveAll(filepath.Join(root, ".claude", "skills")); err != nil {
		t.Fatalf("simulate drift: %v", err)
	}

	res := checkSkillsInventoryDrift(context.Background(), root)
	if res.Status != StatusFixed {
		t.Fatalf("drift was not repaired: status=%v summary=%q", res.Status, res.Summary)
	}
	if entries, err := os.ReadDir(filepath.Join(root, ".claude", "skills")); err != nil || len(entries) == 0 {
		t.Errorf("the managed tree was not restored: err=%v", err)
	}

	// And the ignore rule must exist, or the restored files are visible to git.
	if _, err := os.Stat(filepath.Join(root, ".claude", ".gitignore")); err != nil {
		t.Errorf("the daemon materialized skills with no ignore rule: %v", err)
	}

	if again := checkSkillsInventoryDrift(context.Background(), root); again.Status != StatusClean {
		t.Errorf("a second tick still reported work: status=%v summary=%q", again.Status, again.Summary)
	}
}

// TestCheckSkillsInventoryDrift_NeverRewritesATrackedManagedFile is the #732
// boundary. The daemon runs with nobody at the keyboard, so finding a modified
// tracked file in `git status` afterwards is exactly the intrusion that ruling
// was about. The gate must veto the apply, not merely report afterwards.
func TestCheckSkillsInventoryDrift_NeverRewritesATrackedManagedFile(t *testing.T) {
	root := driftRepo(t)
	targets := []adapterprotocol.SkillTarget{{
		Key:        "claude-project",
		Root:       ".claude/skills",
		Format:     adapterprotocol.SkillFormatAgentSkillsV1,
		Scope:      adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}}
	if _, err := skillmanager.ReconcileUpdate(root, version.Version,
		func(d skillmanager.DesiredSkills, ct []adapterprotocol.SkillTarget) (skillmanager.DesiredSkills, []adapterprotocol.SkillTarget, error) {
			return skillmanager.DefaultDesired(targets), targets, nil
		}); err != nil {
		t.Skipf("could not seed a selected repository: %v", err)
	}

	// Someone force-added a managed file, so it is now TRACKED.
	skillsDir := filepath.Join(root, ".claude", "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil || len(entries) == 0 {
		t.Skipf("no managed skills materialized: %v", err)
	}
	rel := filepath.Join(".claude", "skills", entries[0].Name(), "SKILL.md")
	abs := filepath.Join(root, rel)
	if _, err := os.Stat(abs); err != nil {
		t.Skipf("unexpected skill layout: %v", err)
	}
	add := exec.Command("git", "add", "--force", "--", filepath.ToSlash(rel))
	add.Dir = root
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	// Perturb it so a reconcile would want to rewrite it.
	if err := os.WriteFile(abs, []byte("locally edited\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	before, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	res := checkSkillsInventoryDrift(context.Background(), root)

	after, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("the daemon rewrote a TRACKED managed file with nobody at the keyboard (status=%v, %q)",
			res.Status, res.Summary)
	}
}

// TestCheckSkillsInventoryDrift_NonGitWorkspaceStillGetsItsSkills: the
// tracked-path gate must not turn "not a git repository" into a veto. Managed
// workspaces without git are a normal state, and vetoing there would stop the
// daemon reconciling them at all.
func TestCheckSkillsInventoryDrift_NonGitWorkspaceStillGetsItsSkills(t *testing.T) {
	root := t.TempDir() // deliberately NOT a git repository
	targets := []adapterprotocol.SkillTarget{{
		Key:        "claude-project",
		Root:       ".claude/skills",
		Format:     adapterprotocol.SkillFormatAgentSkillsV1,
		Scope:      adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}}
	if _, err := skillmanager.ReconcileUpdate(root, version.Version,
		func(d skillmanager.DesiredSkills, ct []adapterprotocol.SkillTarget) (skillmanager.DesiredSkills, []adapterprotocol.SkillTarget, error) {
			return skillmanager.DefaultDesired(targets), targets, nil
		}); err != nil {
		t.Skipf("could not seed: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(root, ".claude", "skills")); err != nil {
		t.Fatalf("simulate drift: %v", err)
	}

	res := checkSkillsInventoryDrift(context.Background(), root)

	if res.Status != StatusFixed {
		t.Errorf("a non-git workspace was not reconciled: status=%v summary=%q", res.Status, res.Summary)
	}
	if entries, err := os.ReadDir(filepath.Join(root, ".claude", "skills")); err != nil || len(entries) == 0 {
		t.Errorf("skills were not restored in a non-git workspace: %v", err)
	}
}
