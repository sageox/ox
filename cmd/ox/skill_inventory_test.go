package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/teamskills"
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
	setStateSource(t, repoRoot, "revision", revision)
}

// setStateSource rewrites one field of the recorded source in the machine-local
// state, leaving every other recorded byte alone.
func setStateSource(t *testing.T, repoRoot, field, value string) {
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
	source[field] = value
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

	changed, _ := reconcileSkillInventoryIfStale(repoRoot)
	if changed == 0 {
		t.Fatalf("stale revision did not trigger a reconcile")
	}
	if !managedExists(t, managedFile) {
		t.Errorf("managed file was not restored from the shipped catalog")
	}
}

func TestReconcileSkillInventoryIfStale_LeavesGitStatusClean(t *testing.T) {
	repoRoot, managedFile := installSkillsForTest(t)
	gitInitRepo(t, repoRoot)
	if err := os.WriteFile(filepath.Join(repoRoot, ".gitignore"), []byte(".sageox/cache/\n"), 0o644); err != nil {
		t.Fatalf("write fixture ignore: %v", err)
	}
	gitOutput(t, repoRoot, "add", "-A")
	gitOutput(t, repoRoot, "commit", "-q", "-m", "baseline")

	removeManaged(t, managedFile)
	setLockRevision(t, repoRoot, "revision-from-an-older-release")
	if status := gitOutput(t, repoRoot, "status", "--porcelain"); status != "" {
		t.Fatalf("fixture is dirty before prime: %q", status)
	}

	if changed, _ := reconcileSkillInventoryIfStale(repoRoot); changed == 0 {
		t.Fatal("fixture did not exercise automatic projection repair")
	}
	if status := gitOutput(t, repoRoot, "status", "--porcelain"); status != "" {
		t.Fatalf("session-start reconciliation changed tracked repository state: %q", status)
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

	if changed, _ := reconcileSkillInventoryIfStale(repoRoot); changed != 0 {
		t.Errorf("prime planned work for an up-to-date inventory: changed=%d", changed)
	}
	if managedExists(t, managedFile) {
		t.Errorf("prime restored a file without a version/revision mismatch — the hot path is no longer cheap; " +
			"repairing local drift belongs to `ox doctor`, which reads every managed file by design")
	}
}

// TestReconcileSkillInventoryIfStale_TwoUnchangedPrimesBuildOnePlan proves the
// complete session sequence, not just the steady-state half: the first prime
// sees an old recorded revision and plans/applies once; the next prime sees the
// revision that apply recorded and returns before planning.
//
// Deleting the restored file between calls is intentional observability. A
// second plan would notice and restore it, while the revision-only fast path
// leaves local drift for doctor/daemon repair.
func TestReconcileSkillInventoryIfStale_TwoUnchangedPrimesBuildOnePlan(t *testing.T) {
	repoRoot, managedFile := installSkillsForTest(t)
	removeManaged(t, managedFile)
	setLockRevision(t, repoRoot, "revision-from-an-older-release")

	if changed, _ := reconcileSkillInventoryIfStale(repoRoot); changed == 0 {
		t.Fatal("first prime did not plan the stale inventory")
	}
	if !managedExists(t, managedFile) {
		t.Fatal("first prime did not apply its plan")
	}

	removeManaged(t, managedFile)
	if changed, _ := reconcileSkillInventoryIfStale(repoRoot); changed != 0 {
		t.Fatalf("second unchanged prime planned work: changed=%d", changed)
	}
	if managedExists(t, managedFile) {
		t.Fatal("second unchanged prime rebuilt the plan instead of taking the revision fast path")
	}
}

// TestReconcileSkillInventoryIfStale_NeverInstallsIntoAnUnselectedRepo guards the
// boundary that keeps prime from being an installer. A repo that never ran
// `ox init`, or whose owner deliberately selected no skill targets, must come out
// of prime with nothing written into it.
func TestReconcileSkillInventoryIfStale_NeverInstallsIntoAnUnselectedRepo(t *testing.T) {
	repoRoot := t.TempDir()

	if changed, _ := reconcileSkillInventoryIfStale(repoRoot); changed != 0 {
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

	if changed, _ := reconcileSkillInventoryIfStale(repoRoot); changed != 0 {
		t.Errorf("malformed lockfile should yield no work, got changed=%d", changed)
	}
}

// TestReconcileSkillInventoryIfStale_DoesNotRepairTrackedIgnoreAtSessionStart
// pins the session-start boundary: prime may refresh gitignored projections, but
// repository policy belongs to explicit lifecycle commands such as init/doctor.
func TestReconcileSkillInventoryIfStale_DoesNotRepairTrackedIgnoreAtSessionStart(t *testing.T) {
	repoRoot, _ := installSkillsForTest(t)

	// Reproduce the damaged state: skills present and current, ignore rule gone.
	for _, dir := range []string{".claude", ".agents", ".factory"} {
		_ = os.Remove(filepath.Join(repoRoot, dir, ".gitignore"))
	}

	changed, _ := reconcileSkillInventoryIfStale(repoRoot)
	if changed != 0 {
		t.Errorf("healing the ignore rule must not require a reconcile; got changed=%d", changed)
	}

	for _, dir := range []string{".claude", ".agents", ".factory"} {
		if _, err := os.Stat(filepath.Join(repoRoot, dir, ".gitignore")); !os.IsNotExist(err) {
			t.Errorf("prime recreated tracked %s/.gitignore: %v", dir, err)
		}
	}
}

// commitTeamCheckout commits everything in the team checkout so its HEAD moves.
// The planner keys the recorded catalog revision on that HEAD, so a team revision
// that is not a real commit is not a revision change at all.
func commitTeamCheckout(t *testing.T, team, message string) {
	t.Helper()
	gitOutput(t, team, "add", "-A")
	gitOutput(t, team, "commit", "-q", "-m", message)
}

// stageWithheldOnlyRevision builds a repository whose team's NEXT revision adds
// ONLY a skill whose manifest grants tools. ox withholds that skill outright, so
// the resulting plan has no file to create, update, or remove — the shape that
// exposes whether a revision ox evaluated but wrote nothing for is remembered.
//
// It returns with the baseline settled and the new revision published but not yet
// primed, so each caller decides what the next session start looks like.
func stageWithheldOnlyRevision(t *testing.T) (repo, installedNotes string) {
	t.Helper()
	repo, team := stageApprovalRepo(t, "notes", nil)
	gitInitRepo(t, team)
	commitTeamCheckout(t, team, "publish notes")

	// settle the baseline: the first real team commit is itself a revision change.
	if _, withheld := reconcileSkillInventoryIfStale(repo); len(withheld) != 0 {
		t.Fatalf("the prose-only baseline reported something withheld: %+v", withheld)
	}
	installedNotes = filepath.Join(installedSkillDir(repo, "notes"), "SKILL.md")
	if _, err := os.Stat(installedNotes); err != nil {
		t.Fatalf("baseline team skill did not reach disk: %v", err)
	}

	grants := filepath.Join(team, "agents", "skills", "grants")
	if err := os.MkdirAll(grants, 0o755); err != nil {
		t.Fatalf("stage withheld skill: %v", err)
	}
	manifest := "---\nname: grants\ndescription: Needs a tool grant.\nallowed-tools: Bash\n---\n\nbody\n"
	if err := os.WriteFile(filepath.Join(grants, "SKILL.md"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("stage withheld skill: %v", err)
	}
	commitTeamCheckout(t, team, "publish a skill that grants tools")
	return repo, installedNotes
}

// TestReconcileSkillInventoryIfStale_WithheldOnlyRevisionIsReportedOnce is the
// customer promise behind "tell the coworker when part of a team skill was held
// back": the coworker is told ONCE, when the team publishes the skill — not in
// every session start afterward.
//
// Nothing is written to disk for a withheld-only revision, but the revision was
// still successfully evaluated. If it is not recorded, the next session start
// sees the same stale revision, rebuilds the whole plan, and repeats the same
// warning forever.
func TestReconcileSkillInventoryIfStale_WithheldOnlyRevisionIsReportedOnce(t *testing.T) {
	repo, installedNotes := stageWithheldOnlyRevision(t)

	wantRevision, err := skillmanager.ExpectedRevision(repo)
	if err != nil {
		t.Fatalf("expected revision: %v", err)
	}
	if recorded, _, _ := skillmanager.InstalledSource(repo); recorded == wantRevision {
		t.Fatalf("fixture did not move the team revision: %q", recorded)
	}

	changed, withheld := reconcileSkillInventoryIfStale(repo)
	if changed != 0 {
		t.Fatalf("fixture is no longer withheld-only; the plan mutated %d files", changed)
	}
	if len(withheld) != 1 || withheld[0].Name != "grants" {
		t.Fatalf("first prime after the publish did not report the withheld skill: %+v", withheld)
	}
	if recorded, _, _ := skillmanager.InstalledSource(repo); recorded != wantRevision {
		t.Errorf("a successfully evaluated revision was not recorded:\n got  %q\n want %q", recorded, wantRevision)
	}

	// delete a managed file so a second plan, if built, is observable: it would
	// restore it, while the recorded-revision fast path leaves it alone.
	removeManaged(t, installedNotes)
	changed, withheld = reconcileSkillInventoryIfStale(repo)
	if changed != 0 || len(withheld) != 0 {
		t.Errorf("second prime on the same revision repeated itself: changed=%d withheld=%+v", changed, withheld)
	}
	if managedExists(t, installedNotes) {
		t.Errorf("second prime rebuilt the plan instead of taking the revision fast path")
	}
}

// TestReconcileSkillInventoryIfStale_RefusedPlanReportsNothing guards the other
// half of "reported on change": a reconcile that was REFUSED decided nothing.
//
// When a newer ox installed these skills, Apply refuses to downgrade them and
// writes nothing, so no revision is ever recorded and every session start
// re-plans. The planner still carries the team decisions on that refused plan,
// so without this guard the coworker is told the same skill is held back at the
// start of every session — while the real fact is that this binary is too old to
// act on the repository at all.
func TestReconcileSkillInventoryIfStale_RefusedPlanReportsNothing(t *testing.T) {
	repo, _ := stageWithheldOnlyRevision(t)
	setStateSource(t, repo, "version", "999.0.0")

	for prime := 1; prime <= 2; prime++ {
		changed, withheld := reconcileSkillInventoryIfStale(repo)
		if changed != 0 || len(withheld) != 0 {
			t.Errorf("prime %d reported a decision from a refused reconcile: changed=%d withheld=%+v", prime, changed, withheld)
		}
	}
}

// TestReconcileSkillInventoryIfStale_FailedReconcileReportsNothingAndRecoversLater
// pins the invariant that makes recording an evaluated revision safe: a plan ox
// could not build must not reach the session as a decision, and must not
// advance the recorded revision — otherwise the failure would be remembered as
// success and the team's skill would never be reported once the cause is fixed.
//
// An unreadable approvals store is the cause used here because ox treats it as
// "I cannot tell", never as "nothing is approved": materializing an executable
// skill off a store it could not parse is the fail-open shape to avoid.
func TestReconcileSkillInventoryIfStale_FailedReconcileReportsNothingAndRecoversLater(t *testing.T) {
	repo, installedNotes := stageWithheldOnlyRevision(t)
	staleRevision, _, _ := skillmanager.InstalledSource(repo)

	approvals := teamskills.ApprovalPath(repo)
	if err := os.MkdirAll(filepath.Dir(approvals), 0o755); err != nil {
		t.Fatalf("stage approvals dir: %v", err)
	}
	if err := os.WriteFile(approvals, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("corrupt approvals store: %v", err)
	}

	changed, withheld := reconcileSkillInventoryIfStale(repo)
	if changed != 0 || len(withheld) != 0 {
		t.Errorf("a reconcile that failed still reported a decision: changed=%d withheld=%+v", changed, withheld)
	}
	if recorded, _, _ := skillmanager.InstalledSource(repo); recorded != staleRevision {
		t.Errorf("a failed reconcile advanced the recorded revision:\n got  %q\n want %q", recorded, staleRevision)
	}
	if !managedExists(t, installedNotes) {
		t.Errorf("a failed reconcile disturbed an already-installed team skill")
	}

	// the cause is fixed; the very next session start must tell the coworker.
	if err := os.Remove(approvals); err != nil {
		t.Fatalf("repair approvals store: %v", err)
	}
	if _, withheld := reconcileSkillInventoryIfStale(repo); len(withheld) != 1 || withheld[0].Name != "grants" {
		t.Errorf("after the store was repaired the held-back skill was not reported: %+v", withheld)
	}
}
