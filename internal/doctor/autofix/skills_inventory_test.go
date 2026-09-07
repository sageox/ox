package autofix

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/version"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// installSkillsFixture materializes the real embedded catalog through the real
// installer, so these tests exercise a lockfile ox actually wrote.
func installSkillsFixture(t *testing.T) (repoRoot, managedFile string) {
	t.Helper()
	repoRoot = t.TempDir()

	targets, err := skillmanager.CanonicalizeTargets(repoRoot, []adapterprotocol.SkillTarget{{
		Key:        "claude-project",
		Root:       ".claude/skills",
		Format:     adapterprotocol.SkillFormatAgentSkillsV1,
		Scope:      adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}})
	if err != nil {
		t.Fatalf("canonicalize targets: %v", err)
	}
	plan, err := skillmanager.Reconcile(repoRoot, version.Version, skillmanager.DefaultDesired(targets), targets)
	if err != nil {
		t.Fatalf("install skills: %v", err)
	}
	if len(plan.Creates) == 0 {
		t.Fatalf("fixture installed no skills; catalog or target shape changed")
	}
	return repoRoot, filepath.Join(repoRoot, filepath.FromSlash(plan.Creates[0].Path))
}

// TestSkillsInventoryDrift_RepairsLocallyDeletedFile is the gap this check
// exists to close. `ox agent prime` compares only the recorded revision against
// the binary, so a managed file deleted or damaged on disk — with the lockfile
// still reporting everything current — is invisible to it forever. The daemon
// tick is what notices, because it can afford to read every managed file.
//
// Failure prevented: an agent silently missing a skill for days because a stray
// `rm -rf`, a bad merge, or an editor removed it and nothing ever looked.
func TestSkillsInventoryDrift_RepairsLocallyDeletedFile(t *testing.T) {
	repoRoot, managedFile := installSkillsFixture(t)
	if err := os.Remove(managedFile); err != nil {
		t.Fatalf("remove managed file: %v", err)
	}

	res := checkSkillsInventoryDrift(context.Background(), repoRoot)
	if res.Status != StatusFixed {
		t.Fatalf("expected StatusFixed for a deleted managed file, got status=%v summary=%q", res.Status, res.Summary)
	}
	if _, err := os.Stat(managedFile); err != nil {
		t.Errorf("managed file not restored: %v", err)
	}
}

// TestSkillsInventoryDrift_CleanInventoryIsClean is the control. Without it, a
// check that reported StatusFixed unconditionally would pass the repair test
// above and nobody would notice the daemon rewriting files on every tick.
func TestSkillsInventoryDrift_CleanInventoryIsClean(t *testing.T) {
	repoRoot, _ := installSkillsFixture(t)

	res := checkSkillsInventoryDrift(context.Background(), repoRoot)
	if res.Status != StatusClean {
		t.Errorf("healthy inventory should be clean, got status=%v summary=%q", res.Status, res.Summary)
	}
}

// TestSkillsInventoryDrift_NeverInstallsIntoAnUnselectedRepo guards the boundary
// that keeps the daemon from making a decision the user never made. Selecting an
// AI coworker is `ox init`'s job; a background process must not decide that a
// repository should suddenly acquire a skills directory.
func TestSkillsInventoryDrift_NeverInstallsIntoAnUnselectedRepo(t *testing.T) {
	repoRoot := t.TempDir()

	res := checkSkillsInventoryDrift(context.Background(), repoRoot)
	if res.Status != StatusClean {
		t.Errorf("unselected repo should be clean, got status=%v summary=%q", res.Status, res.Summary)
	}
	for _, dir := range []string{".claude", ".agents", ".sageox"} {
		if _, err := os.Stat(filepath.Join(repoRoot, dir)); err == nil {
			t.Errorf("daemon created %s/ in a repo that never selected skills", dir)
		}
	}
}

// TestSkillsInventoryDrift_SkipsWhileASessionIsRecording pins the rule that a
// background tick must not change the instructions an agent is working from
// mid-turn. Swapping a skill body under a live session is a correctness problem,
// not a latency one: the agent has already read the old text and may act on it
// while the file now says something else.
func TestSkillsInventoryDrift_SkipsWhileASessionIsRecording(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(xdg, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(xdg, "data"))
	repoRoot, managedFile := installSkillsFixture(t)
	if err := os.Remove(managedFile); err != nil {
		t.Fatalf("remove managed file: %v", err)
	}

	// A recording state only resolves for a project that carries a repo id and an
	// endpoint — that pair is what names the per-repo sessions directory. Writing
	// the real config is what makes this exercise the production guard rather than
	// a stub of it.
	cfg, err := json.Marshal(map[string]string{
		"repo_id":  "repo_01jfk3mabskilldrift",
		"endpoint": "https://sageox.example",
	})
	if err != nil {
		t.Fatalf("marshal project config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".sageox", "config.json"), cfg, 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	// The state must live in a session folder under the per-repo sessions
	// directory, which is where LoadRecordingState looks for it.
	const repoID = "repo_01jfk3mabskilldrift"
	sessionPath := filepath.Join(paths.SessionCacheDir(repoID), "sessions", "2026-09-07-skill-drift")
	if err := session.SaveRecordingState(repoRoot, &session.RecordingState{
		AgentID:     "TestAg",
		SessionPath: sessionPath,
		StartedAt:   time.Now(),
		AdapterName: "claude-code",
	}); err != nil {
		t.Fatalf("stage recording state: %v", err)
	}
	if !session.IsRecording(repoRoot) {
		t.Fatal("fixture did not produce an active recording; the guard would not be exercised")
	}

	res := checkSkillsInventoryDrift(context.Background(), repoRoot)
	if res.Status != StatusClean {
		t.Errorf("expected the check to stand down during a session, got status=%v summary=%q", res.Status, res.Summary)
	}
	if _, err := os.Stat(managedFile); err == nil {
		t.Errorf("daemon rewrote a managed skill file while a session was recording")
	}
}

// TestSkillsInventoryDrift_RegisteredInDefaultRegistry makes the wiring itself a
// tested property: an unregistered check is dead code that every other test in
// this file would still pass.
func TestSkillsInventoryDrift_RegisteredInDefaultRegistry(t *testing.T) {
	var found *Check
	for _, c := range Default().All() {
		if c.Slug == "skills-inventory-drift" {
			found = c
			break
		}
	}
	if found == nil {
		t.Fatal("skills-inventory-drift is not registered in the default registry; the daemon will never run it")
	}
	if found.MinInterval <= 0 {
		t.Error("check must declare a MinInterval or it runs on every tick")
	}
	if found.BlastRadius == "" {
		t.Error("check must declare a BlastRadius for ops review")
	}
}

// TestSkillsInventoryDrift_NeverRewritesATrackedFile is #732 at a different path.
//
// The committed `sageox` on-ramp skill is tracked on purpose — it is the one file
// that must reach teammates and fresh clones. When its content changes in a
// release, a background tick that applied the update would modify a tracked file
// in the developer's working tree, so they would find a modified file in
// `git status` having done nothing. That is exactly the intrusion the team ruled
// out when it refused to let a background process edit the root .gitignore.
func TestSkillsInventoryDrift_NeverRewritesATrackedFile(t *testing.T) {
	repoRoot, managedFile := installSkillsFixture(t)

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run(repoRoot, "init", "--initial-branch=main")
	run(repoRoot, "config", "user.email", "test@sageox.example")
	run(repoRoot, "config", "user.name", "Test")
	run(repoRoot, "add", "--force", "--", filepath.ToSlash(mustRel(t, repoRoot, managedFile)))
	run(repoRoot, "commit", "-q", "-m", "track a managed file")

	// Now make it drift, the way a release changes shipped content.
	if err := os.Remove(managedFile); err != nil {
		t.Fatalf("remove managed file: %v", err)
	}

	res := checkSkillsInventoryDrift(context.Background(), repoRoot)

	// Assert the WORKING TREE, not just the reported status. A status-only
	// assertion passes even when the write already happened — which is exactly how
	// this guard came to enforce nothing while its test stayed green: the check ran
	// after Apply, reported StatusFound, and the file on disk had already changed.
	if _, err := os.Stat(managedFile); err == nil {
		t.Errorf("the background tick RESTORED a tracked file on disk; the developer would find a change they did not make")
	}
	if res.Status == StatusFixed {
		t.Errorf("status reported a fix for a tracked path")
	}
	if res.Status != StatusFound {
		t.Errorf("expected StatusFound reporting the tracked path, got %v (%s)", res.Status, res.Summary)
	}
}

func mustRel(t *testing.T, root, abs string) string {
	t.Helper()
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		t.Fatalf("rel: %v", err)
	}
	return rel
}

// TestSkillsInventoryDrift_FailedTrackedLookupVetoesTheApply closes the hole where
// an UNANSWERED question read as "no".
//
// trackedPlanPaths used to turn every git error into an empty result, so a failing
// `git ls-files` looked exactly like "nothing is tracked" and the gate let the
// apply proceed — rewriting the very tracked file it exists to protect. Standing
// down is the only safe reading of a lookup that did not answer.
//
// The failure is induced by corrupting .git/index rather than by canceling the
// context: the check returns early on a canceled context, so a canceled-ctx
// fixture would never reach the lookup and the test would pass for the wrong
// reason. It did, on the first attempt.
func TestSkillsInventoryDrift_FailedTrackedLookupVetoesTheApply(t *testing.T) {
	repoRoot, managedFile := installSkillsFixture(t)

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "--initial-branch=main")

	if err := os.Remove(managedFile); err != nil {
		t.Fatalf("remove managed file: %v", err)
	}
	// A corrupt index makes `git ls-files` fail while the directory is still very
	// much a git repository, which is the shape the veto has to handle.
	if err := os.WriteFile(filepath.Join(repoRoot, ".git", "index"), []byte("not an index"), 0o644); err != nil {
		t.Fatalf("corrupt index: %v", err)
	}

	res := checkSkillsInventoryDrift(context.Background(), repoRoot)

	if _, err := os.Stat(managedFile); err == nil {
		t.Error("the apply proceeded despite an unanswered tracked-path lookup")
	}
	if res.Status == StatusFixed {
		t.Errorf("expected the check to stand down, got StatusFixed (%s)", res.Summary)
	}
}
