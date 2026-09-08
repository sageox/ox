package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/version"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// installSkillsForTest materializes the real embedded catalog into a temp repo
// through the real installer, so the lockfile these tests manipulate is one ox
// actually wrote rather than a hand-built fixture that could drift from it.
func installSkillsForTest(t *testing.T) (repoRoot, managedFile string) {
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
		t.Fatalf("fixture installed no skills; the catalog or target shape changed")
	}
	managedFile = filepath.Join(repoRoot, filepath.FromSlash(plan.Creates[0].Path))
	if _, err := os.Stat(managedFile); err != nil {
		t.Fatalf("managed file missing after install: %v", err)
	}
	return repoRoot, managedFile
}

// removeManaged deletes a managed skill file so a reconcile has something
// visible to repair. Restored-or-not is the observable that separates "we
// planned" from "we took the cheap path" without reaching into internals.
//
// Deletion rather than modification is deliberate. A *modified* managed file is
// currently classified as a preserved Conflict, not an Update, so using
// modification here would couple this test to the ownership rule that task .14
// changes. A missing file is owned and restored under both the old
// preserve-on-edit regime and the new absolute-overwrite one, so these
// assertions stay true across that change and keep testing what they name.
func removeManaged(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove %s: %v", path, err)
	}
}

func managedExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

// setLockRevision rewrites only the recorded source revision, simulating the
// exact real-world trigger: the user upgraded ox, so the binary now ships a
// catalog whose digest differs from the one recorded at last install.
//
// The revision lives in the MACHINE-LOCAL half of the manifest
// (.sageox/cache/skills-state.json), not the committed lockfile — that split is
// what keeps a content-bearing release from producing a git diff. A fixture that
// edited the committed file would silently stop simulating anything.
func setLockRevision(t *testing.T, repoRoot, revision string) {
	t.Helper()
	path := skillmanager.StatePath(repoRoot)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read skills state: %v", err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("parse skills state: %v", err)
	}
	source, ok := state["source"].(map[string]any)
	if !ok {
		t.Fatalf("skills state has no source object: %s", data)
	}
	source["revision"] = revision
	out, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("marshal skills state: %v", err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		t.Fatalf("write skills state: %v", err)
	}
}

// TestReconcileSkillInventoryIfStale_RepairsAfterUpgrade is the customer promise:
// after an ox upgrade changes the shipped catalog, the next session start finds
// the materialized skills already current — no `ox doctor`, no commit, no
// manual step. Without this the agent reads the previous release's playbooks.
func TestReconcileSkillInventoryIfStale_RepairsAfterUpgrade(t *testing.T) {
	repoRoot, managedFile := installSkillsForTest(t)
	removeManaged(t, managedFile)
	setLockRevision(t, repoRoot, "revision-from-an-older-release")

	changed := reconcileSkillInventoryIfStale(repoRoot)
	if changed == 0 {
		t.Fatalf("stale revision did not trigger a reconcile")
	}
	if !managedExists(t, managedFile) {
		t.Errorf("managed file was not restored from the shipped catalog")
	}
}

// TestReconcileSkillInventoryIfStale_CurrentInventoryDoesNoWork pins the cost
// discipline that makes this safe on the session hot path. When the recorded
// revision already matches the binary, prime must not build a plan — which means
// reading and digesting every managed file across every target, on every session.
//
// The assertion is observable rather than introspective: a deleted managed file
// is left deleted. If this test ever fails by finding the file restored, the
// cheap path has been lost even though the user-visible outcome looks "better".
func TestReconcileSkillInventoryIfStale_CurrentInventoryDoesNoWork(t *testing.T) {
	repoRoot, managedFile := installSkillsForTest(t)
	removeManaged(t, managedFile)
	// Deliberately do NOT touch the lockfile: revision and version both match.

	if changed := reconcileSkillInventoryIfStale(repoRoot); changed != 0 {
		t.Errorf("prime planned work for an up-to-date inventory: changed=%d", changed)
	}
	if managedExists(t, managedFile) {
		t.Errorf("prime restored a file without a version/revision mismatch — the hot path is no longer cheap; " +
			"repairing local drift belongs to `ox doctor`, which reads every managed file by design")
	}
}

// TestReconcileSkillInventoryIfStale_NeverInstallsIntoAnUnselectedRepo guards the
// boundary that keeps prime from being an installer. A repo that never ran
// `ox init`, or whose owner deliberately selected no skill targets, must come out
// of prime with nothing written into it.
func TestReconcileSkillInventoryIfStale_NeverInstallsIntoAnUnselectedRepo(t *testing.T) {
	repoRoot := t.TempDir()

	if changed := reconcileSkillInventoryIfStale(repoRoot); changed != 0 {
		t.Errorf("prime installed into a repo with no selected targets: changed=%d", changed)
	}
	for _, dir := range []string{".claude", ".agents", ".sageox"} {
		if _, err := os.Stat(filepath.Join(repoRoot, dir)); err == nil {
			t.Errorf("prime created %s/ in a repo that never selected skills", dir)
		}
	}
}

// TestReconcileSkillInventoryIfStale_ToleratesMalformedLockfile: a corrupt
// lockfile is a degraded session, never a failed one. Prime must return quietly.
func TestReconcileSkillInventoryIfStale_ToleratesMalformedLockfile(t *testing.T) {
	repoRoot, _ := installSkillsForTest(t)
	if err := os.WriteFile(skillmanager.LockPath(repoRoot), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write malformed lockfile: %v", err)
	}
	// and the machine-local half, which is derived state and must also be
	// survivable rather than fatal
	if err := os.WriteFile(skillmanager.StatePath(repoRoot), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write malformed state: %v", err)
	}

	if changed := reconcileSkillInventoryIfStale(repoRoot); changed != 0 {
		t.Errorf("malformed lockfile should yield no work, got changed=%d", changed)
	}
}

// TestReconcileSkillInventoryIfStale_SelfHealsAMissingIgnoreRule covers the state
// a build that predated the ignore invariant left behind in real checkouts:
// reserved-prefix skills materialized on disk with NO rule hiding them.
//
// Such a repository is stuck without this. The staleness compare short-circuits
// because the recorded revision already matches, so Apply — where the ignore rule
// is written — is never reached, and nothing on the session path would ever
// notice. The files sit untracked and unignored, one `git add -A` from being
// committed into the customer's history.
func TestReconcileSkillInventoryIfStale_SelfHealsAMissingIgnoreRule(t *testing.T) {
	repoRoot, _ := installSkillsForTest(t)

	// Reproduce the damaged state: skills present and current, ignore rule gone.
	for _, dir := range []string{".claude", ".agents", ".factory"} {
		_ = os.Remove(filepath.Join(repoRoot, dir, ".gitignore"))
	}

	changed := reconcileSkillInventoryIfStale(repoRoot)
	if changed != 0 {
		t.Errorf("healing the ignore rule must not require a reconcile; got changed=%d", changed)
	}

	data, err := os.ReadFile(filepath.Join(repoRoot, ".claude", ".gitignore"))
	if err != nil {
		t.Fatalf("prime did not restore the ignore rule; the repository stays one `git add -A` from committing vendor files: %v", err)
	}
	if !strings.Contains(string(data), "skills/ox-cli-*/") {
		t.Errorf("ignore rule restored without the skills glob:\n%s", data)
	}
}
