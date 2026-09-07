package skillmanager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/require"
)

type fakeCatalog struct {
	revision string
	skill    skills.Skill
}

func (f fakeCatalog) Digest() (string, error) { return f.revision, nil }
func (f fakeCatalog) Select(string, DesiredSkills) ([]skills.Skill, error) {
	return []skills.Skill{f.skill}, nil
}

func sharedTarget() adapterprotocol.SkillTarget {
	return adapterprotocol.SkillTarget{
		Key: "agents-project", Root: ".agents/skills",
		Format: adapterprotocol.SkillFormatAgentSkillsV1, Scope: adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}
}

func desiredFor(target adapterprotocol.SkillTarget) DesiredSkills {
	return DesiredSkills{Bundles: []BundleRef{{ID: "core"}}, Targets: []string{target.Key}}
}

func fakeSkill(version string, suffix string) skills.Skill {
	files := []skills.File{
		{Path: "SKILL.md", Content: []byte("---\nname: test-skill\ndescription: test\n---\nbody " + suffix + "\n")},
		{Path: "assets/example.json", Content: []byte("{\"version\":\"" + suffix + "\"}\n")},
		{Path: "references/guide.md", Content: []byte("guide " + suffix + "\n")},
		{Path: "scripts/check.sh", Content: []byte("#!/bin/sh\necho " + suffix + "\n")},
	}
	return skills.Skill{Name: "test-skill", Content: files[0].Content, Files: files, Version: version}
}

func TestCanonicalizeTargetsDeduplicatesSharedProjection(t *testing.T) {
	repo := t.TempDir()
	codex := sharedTarget()
	gemini := sharedTarget()
	gemini.Key = "gemini-alias"
	targets, err := CanonicalizeTargets(repo, []adapterprotocol.SkillTarget{codex, gemini})
	require.NoError(t, err)
	require.Len(t, targets, 1)
	require.Equal(t, ".agents/skills", targets[0].Root)
	plan, err := planWithSource(repo, "1.0.0", DefaultDesired(targets), targets, fakeCatalog{revision: "rev-1", skill: fakeSkill("1.0.0", "one")})
	require.NoError(t, err)
	require.Equal(t, 1, plan.TargetCount)
	require.Len(t, plan.Creates, 4, "the shared projection must be planned exactly once")

	gemini.LinkPolicy = "follow"
	_, err = CanonicalizeTargets(repo, []adapterprotocol.SkillTarget{codex, gemini})
	require.ErrorContains(t, err, "unsupported link policy")
}

func TestSelectedTargetsPersistAndUnselectedTargetStaysAbsent(t *testing.T) {
	repo := t.TempDir()
	claude := adapterprotocol.SkillTarget{
		Key: "claude-project", Root: ".claude/skills",
		Format: adapterprotocol.SkillFormatAgentSkillsV1, Scope: adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}
	shared := sharedTarget()
	_, err := Reconcile(repo, "1.0.0", DefaultDesired([]adapterprotocol.SkillTarget{claude}), []adapterprotocol.SkillTarget{claude})
	require.NoError(t, err)

	desired, lockedTargets, err := LoadDesired(repo)
	require.NoError(t, err)
	require.Equal(t, []string{"claude-project"}, desired.Targets)
	require.Len(t, lockedTargets, 1)

	plan, err := Plan(repo, "1.0.0", desired, append(lockedTargets, shared))
	require.NoError(t, err)
	require.Equal(t, 1, plan.TargetCount)
	for _, action := range plan.Creates {
		require.NotContains(t, action.Path, ".agents/skills", "Doctor must not add an unselected detected target")
	}
	require.NoDirExists(t, filepath.Join(repo, ".agents", "skills"))
}

func TestConcurrentTargetUpdatesDoNotLoseSelection(t *testing.T) {
	repo := t.TempDir()
	shared := sharedTarget()
	claude := adapterprotocol.SkillTarget{
		Key: "claude-project", Root: ".claude/skills",
		Format: adapterprotocol.SkillFormatAgentSkillsV1, Scope: adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, target := range []adapterprotocol.SkillTarget{shared, claude} {
		target := target
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := ReconcileUpdate(repo, "1.0.0", func(desired DesiredSkills, targets []adapterprotocol.SkillTarget) (DesiredSkills, []adapterprotocol.SkillTarget, error) {
				if len(desired.Bundles) == 0 {
					desired = DefaultDesired(nil)
				}
				desired = AddTargets(desired, target)
				targets = append(targets, target)
				return desired, targets, nil
			})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	desired, targets, err := LoadDesired(repo)
	require.NoError(t, err)
	require.Equal(t, []string{"agents-project", "claude-project"}, desired.Targets)
	require.Len(t, targets, 2)
}

func TestReconcileWritesManifestLastAndNoOpsWhenCurrent(t *testing.T) {
	repo := t.TempDir()
	target := sharedTarget()
	plan, err := Reconcile(repo, "1.0.0", desiredFor(target), []adapterprotocol.SkillTarget{target})
	require.NoError(t, err)
	require.NotEmpty(t, plan.Creates)
	require.True(t, plan.Converged())
	require.FileExists(t, LockPath(repo))
	require.NoFileExists(t, journalPath(repo))

	data, err := os.ReadFile(filepath.Join(repo, ".agents", "skills", "ox-cli-plan", "SKILL.md"))
	require.NoError(t, err)
	require.NotContains(t, string(data), "ox-hash", "new installs use the lockfile as the only ownership source")

	second, err := Plan(repo, "1.0.0", desiredFor(target), []adapterprotocol.SkillTarget{target})
	require.NoError(t, err)
	require.Empty(t, second.Creates)
	require.Empty(t, second.Updates)
	require.Empty(t, second.Removes)
	require.Empty(t, second.Conflicts)
	require.False(t, second.lockChanged)
}

// TestReservedNamespaceIsOwnedAbsolutely pins the 0.15.0 inversion of the
// preserve-on-edit rule, and the boundary that keeps it safe.
//
// Preserve-on-edit was correct while these files were TRACKED: an edit appeared
// in git diff, so it was visible, reviewable, and plausibly deliberate. Once the
// files are gitignored, a preserved edit becomes permanent SILENT drift — one
// machine quietly running a different playbook, invisible to git, unrepairable by
// ox, undiagnosable by a teammate reading the same repository. Overwriting is the
// safer failure mode.
//
// Two things must hold together, which is why they are asserted in one test:
// inside the reserved namespace ox restores truth; outside it ox touches nothing.
func TestReservedNamespaceIsOwnedAbsolutely(t *testing.T) {
	repo := t.TempDir()
	target := sharedTarget()
	skillsRoot := filepath.Join(repo, ".agents", "skills")

	// A file sitting at a RESERVED name that ox never recorded. Under the old rule
	// this was preserved forever as "not managed by ox"; it is now reclaimed,
	// because the prefix is the contract.
	squatted := filepath.Join(skillsRoot, "ox-cli-plan", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(squatted), 0o755))
	require.NoError(t, os.WriteFile(squatted, []byte("---\nname: ox-cli-plan\ndescription: mine\n---\nsquatted\n"), 0o644))

	// A skill the USER owns, in the same directory, outside the reserved prefixes.
	userSkill := filepath.Join(skillsRoot, "my-own-skill", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(userSkill), 0o755))
	userContent := []byte("---\nname: my-own-skill\ndescription: mine\n---\nuser owned\n")
	require.NoError(t, os.WriteFile(userSkill, userContent, 0o644))

	_, err := Install(adapterprotocol.SkillsParams{RepoRoot: repo, Version: "1.0.0"}, skillsRoot)
	require.NoError(t, err)

	got, err := os.ReadFile(squatted)
	require.NoError(t, err)
	require.NotContains(t, string(got), "squatted",
		"a reserved-prefix path must be reclaimed, not preserved: a gitignored local edit is invisible drift")

	untouched, err := os.ReadFile(userSkill)
	require.NoError(t, err)
	require.Equal(t, userContent, untouched,
		"a skill outside the reserved prefixes must never be touched")

	// A local edit to a genuinely managed reserved file is restored, not conflicted.
	managedPath := filepath.Join(skillsRoot, "ox-cli-consult", "SKILL.md")
	original, err := os.ReadFile(managedPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(managedPath, append(original, []byte("\nlocal edit\n")...), 0o644))

	plan, err := Plan(repo, "1.0.0", desiredFor(target), []adapterprotocol.SkillTarget{target})
	require.NoError(t, err)
	require.NotContains(t, conflictPaths(plan.Conflicts), filepath.FromSlash(".agents/skills/ox-cli-consult/SKILL.md"),
		"a reserved path must never be reported as a conflict")
	require.NoError(t, Apply(plan))

	after, err := os.ReadFile(managedPath)
	require.NoError(t, err)
	require.Equal(t, original, after, "ox must restore the shipped content of a reserved file")
}

func TestPlanCoversAllSkillTreeFiles(t *testing.T) {
	repo := t.TempDir()
	target := sharedTarget()
	desired := desiredFor(target)
	v1 := fakeCatalog{revision: "rev-1", skill: fakeSkill("1.0.0", "one")}
	plan, err := planWithSource(repo, "1.0.0", desired, []adapterprotocol.SkillTarget{target}, v1)
	require.NoError(t, err)
	require.Len(t, plan.Creates, 4)
	require.NoError(t, Apply(plan))
	info, err := os.Stat(filepath.Join(repo, ".agents", "skills", "test-skill", "scripts", "check.sh"))
	require.NoError(t, err)
	// NTFS has no POSIX permission bits — Go synthesizes a mode and os.Chmod only
	// toggles the read-only attribute — so the executable bit is a POSIX-only
	// assertion. Asserting it everywhere is what made the installer look broken on
	// Windows when the real portability bug was elsewhere (see modeDrift).
	if runtime.GOOS != "windows" {
		require.Equal(t, os.FileMode(0o755), info.Mode().Perm())
	}

	missing := filepath.Join(repo, ".agents", "skills", "test-skill", "references", "guide.md")
	require.NoError(t, os.Remove(missing))
	plan, err = planWithSource(repo, "1.0.0", desired, []adapterprotocol.SkillTarget{target}, v1)
	require.NoError(t, err)
	require.Equal(t, ".agents/skills/test-skill/references/guide.md", plan.Creates[0].Path)
	require.NoError(t, Apply(plan))

	v2 := fakeCatalog{revision: "rev-2", skill: fakeSkill("2.0.0", "two")}
	plan, err = planWithSource(repo, "2.0.0", desired, []adapterprotocol.SkillTarget{target}, v2)
	require.NoError(t, err)
	require.Len(t, plan.Updates, 4, "SKILL.md, references, assets, and scripts all participate in drift")
	require.NoError(t, Apply(plan))
}

func TestUninstallRemovesOwnedTreeAndPreservesEditsAndAdditions(t *testing.T) {
	repo := t.TempDir()
	target := sharedTarget()
	desired := desiredFor(target)
	source := fakeCatalog{revision: "rev-1", skill: fakeSkill("1.0.0", "one")}
	plan, err := planWithSource(repo, "1.0.0", desired, []adapterprotocol.SkillTarget{target}, source)
	require.NoError(t, err)
	require.NoError(t, Apply(plan))

	modifiedPath := filepath.Join(repo, ".agents", "skills", "test-skill", "assets", "example.json")
	require.NoError(t, os.WriteFile(modifiedPath, []byte("user edit\n"), 0o644))
	addition := filepath.Join(repo, ".agents", "skills", "test-skill", "notes.txt")
	require.NoError(t, os.WriteFile(addition, []byte("keep\n"), 0o644))

	desired.Targets = nil
	plan, err = planWithSource(repo, "1.0.0", desired, []adapterprotocol.SkillTarget{target}, source)
	require.NoError(t, err)
	require.Len(t, plan.Removes, 3)
	require.Len(t, plan.Conflicts, 1)
	require.NoError(t, Apply(plan))
	require.FileExists(t, modifiedPath)
	require.FileExists(t, addition)

	next, err := planWithSource(repo, "1.0.0", desired, []adapterprotocol.SkillTarget{target}, source)
	require.NoError(t, err)
	require.Empty(t, next.Conflicts, "ownership is relinquished for a modified file that is no longer desired")
	require.Empty(t, next.Removes)
}

func TestInterruptedApplyRecoversBeforeAndAfterLockCommit(t *testing.T) {
	repo := t.TempDir()
	target := sharedTarget()
	desired := desiredFor(target)
	source := fakeCatalog{revision: "rev-1", skill: fakeSkill("1.0.0", "one")}
	plan, err := planWithSource(repo, "1.0.0", desired, []adapterprotocol.SkillTarget{target}, source)
	require.NoError(t, err)
	require.NotEmpty(t, plan.Creates)

	// Simulate exit after the journal and first file, before lock commit.
	require.NoError(t, ensureDir(repo, filepath.Dir(journalPath(repo))))
	journal, err := json.MarshalIndent(plan.journal, "", "  ")
	require.NoError(t, err)
	require.NoError(t, atomicWriteNoSymlink(journalPath(repo), append(journal, '\n'), 0o600))
	first := plan.Creates[0]
	firstPath := filepath.Join(repo, filepath.FromSlash(first.Path))
	require.NoError(t, ensureDir(repo, filepath.Dir(firstPath)))
	require.NoError(t, atomicWriteNoSymlink(firstPath, first.Content, first.Mode))

	recovered, err := planWithSource(repo, "1.0.0", desired, []adapterprotocol.SkillTarget{target}, source)
	require.NoError(t, err)
	require.Empty(t, recovered.Conflicts)
	require.NoError(t, Apply(recovered))
	require.FileExists(t, LockPath(repo))

	// Simulate exit after lock commit but before deleting the old journal.
	journal, err = json.MarshalIndent(recovered.journal, "", "  ")
	require.NoError(t, err)
	require.NoError(t, atomicWriteNoSymlink(journalPath(repo), append(journal, '\n'), 0o600))
	final, err := planWithSource(repo, "1.0.0", desired, []adapterprotocol.SkillTarget{target}, source)
	require.NoError(t, err)
	require.Empty(t, final.Creates)
	require.Empty(t, final.Updates)
	require.Empty(t, final.Conflicts)
	require.NoError(t, Apply(final))
	require.NoFileExists(t, journalPath(repo))
}

func TestLegacyStampMigrationAndDowngradeGuard(t *testing.T) {
	repo := t.TempDir()
	target := sharedTarget()
	desired := desiredFor(target)
	source := fakeCatalog{revision: "rev-1", skill: fakeSkill("2.0.0", "one")}
	path := filepath.Join(repo, ".agents", "skills", "test-skill", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	canonical := source.skill.Content
	fmEnd := strings.Index(string(canonical), "\n---\n") + len("\n---\n")
	body := canonical[fmEnd:]
	legacy := append([]byte{}, canonical[:fmEnd]...)
	legacy = append(legacy, []byte("<!-- ox-hash: "+agentx.ContentHash(body)+" ver: 1.0.0 -->\n")...)
	legacy = append(legacy, body...)
	require.NoError(t, os.WriteFile(path, legacy, 0o644))

	plan, err := planWithSource(repo, "2.0.0", desired, []adapterprotocol.SkillTarget{target}, source)
	require.NoError(t, err)
	require.Empty(t, plan.Conflicts)
	require.Contains(t, actionPaths(plan.Updates), filepath.FromSlash(".agents/skills/test-skill/SKILL.md"))
	require.NoError(t, Apply(plan))

	downgrade, err := planWithSource(repo, "1.0.0", desired, []adapterprotocol.SkillTarget{target}, source)
	require.NoError(t, err)
	require.NotEmpty(t, downgrade.Warnings)
	require.Empty(t, downgrade.Updates)
}

func TestRetiredLegacyStampRemovesOnlyVerifiedManifest(t *testing.T) {
	repo := t.TempDir()
	target := sharedTarget()
	retiredDir := filepath.Join(repo, ".agents", "skills", "retired-skill")
	require.NoError(t, os.MkdirAll(retiredDir, 0o755))
	body := []byte("old body\n")
	legacy := []byte("---\nname: retired-skill\ndescription: old\n---\n<!-- ox-hash: " + agentx.ContentHash(body) + " ver: 0.9.0 -->\n")
	legacy = append(legacy, body...)
	manifest := filepath.Join(retiredDir, "SKILL.md")
	addition := filepath.Join(retiredDir, "notes.txt")
	require.NoError(t, os.WriteFile(manifest, legacy, 0o644))
	require.NoError(t, os.WriteFile(addition, []byte("keep\n"), 0o644))

	plan, err := planWithSource(repo, "1.0.0", desiredFor(target), []adapterprotocol.SkillTarget{target}, fakeCatalog{revision: "rev-1", skill: fakeSkill("1.0.0", "one")})
	require.NoError(t, err)
	require.Contains(t, actionPaths(plan.Removes), filepath.FromSlash(".agents/skills/retired-skill/SKILL.md"))
	require.NoError(t, Apply(plan))
	require.NoFileExists(t, manifest)
	require.FileExists(t, addition)
}

func TestSymlinkAndMalformedLockFailWithoutMutation(t *testing.T) {
	repo := t.TempDir()
	target := sharedTarget()
	external := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".agents"), 0o755))
	require.NoError(t, os.Symlink(external, filepath.Join(repo, ".agents", "skills")))
	_, err := Plan(repo, "1.0.0", desiredFor(target), []adapterprotocol.SkillTarget{target})
	require.ErrorContains(t, err, "symlink")

	reserved := sharedTarget()
	reserved.Root = ".git/skills"
	_, err = Plan(t.TempDir(), "1.0.0", desiredFor(reserved), []adapterprotocol.SkillTarget{reserved})
	require.ErrorContains(t, err, "reserved root")

	lockRepo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Dir(LockPath(lockRepo)), 0o755))
	require.NoError(t, os.WriteFile(LockPath(lockRepo), []byte("not json\n"), 0o644))
	_, err = Plan(lockRepo, "1.0.0", desiredFor(target), []adapterprotocol.SkillTarget{target})
	require.ErrorContains(t, err, "parse skills lockfile")

	symlinkLockRepo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Dir(LockPath(symlinkLockRepo)), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(symlinkLockRepo, "outside.json"), LockPath(symlinkLockRepo)))
	_, err = Plan(symlinkLockRepo, "1.0.0", desiredFor(target), []adapterprotocol.SkillTarget{target})
	require.ErrorContains(t, err, "symlink")
}

// TestForeignSymlinkDoesNotAbortSkillDiscovery pins the defect that made ox
// skill rollout silently dead in any repo that keeps its own skills beside
// ox's.
//
// `.claude/skills/` is SHARED. The sageox monorepo generates agent-parity
// mirrors there as symlinks, and both whole-directory scans — LegacyBundles
// (which bundles are installed?) and retiredLegacyFiles (which ox skills should
// be removed?) — refused to read them and returned the error for the entire
// scan. `ox doctor` then reported "cannot inspect managed skills" and
// reconciled nothing, so every later ox release failed to reach the repo. That
// is how a repo ends up without ox-cli-pr-header while prime tells the agent to
// "see the ox-cli-pr-header skill".
//
// A symlinked SKILL.md cannot carry a valid ox stamp, so it is definitionally
// not ox's. Skipping it is the correct answer; the hard refusal stays on the
// write path, where following a symlink is the path-escape this guards.
func TestForeignSymlinkDoesNotAbortSkillDiscovery(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	root := filepath.Join(repo, ".claude", "skills")

	// One real ox-stamped skill, so discovery has something to find.
	managed := filepath.Join(root, "ox-cli-plan")
	if err := os.MkdirAll(managed, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("# ox-cli-plan\n")
	stamped := append([]byte("<!-- ox-hash: "+agentx.ContentHash(body)+" ver: 0.0.1 -->\n"), body...)
	if err := os.WriteFile(filepath.Join(managed, "SKILL.md"), stamped, 0o644); err != nil {
		t.Fatal(err)
	}

	// A foreign skill whose SKILL.md is a symlink — the agent-parity shape.
	foreign := filepath.Join(root, "bdd-compile")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	realFile := filepath.Join(repo, "tests", "bdd", "skills", "bdd-compile", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(realFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realFile, []byte("# bdd-compile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realFile, filepath.Join(foreign, "SKILL.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	target := adapterprotocol.SkillTarget{
		Key: "claude", Root: ".claude/skills",
		Format: adapterprotocol.SkillFormatAgentSkillsV1, Scope: adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}
	bundles, err := LegacyBundles(repo, target)
	if err != nil {
		t.Fatalf("LegacyBundles aborted on a foreign symlink: %v", err)
	}
	if len(bundles) == 0 {
		t.Error("discovery skipped the foreign symlink but also lost the real stamped skill beside it")
	}
}

// TestReservedSweepNeverDestroysUnrecognizedContent settles a contradiction in
// the design and pins the safe side of it in code.
//
// "Reserved prefixes are absolute" could be read two ways: ox OVERWRITES the
// names it ships (correct, see TestReservedNamespaceIsOwnedAbsolutely), or ox
// DELETES anything wearing the prefix. The second reading is unrecoverable: once
// `skills/ox-cli-*/` is in .gitignore, a user directory that happens to match —
// "ox-cli-mine" is a plausible name for a skill ABOUT the ox CLI — is not in git,
// not in the lockfile, and not in the apply journal. A sweep would destroy it with
// no way back.
//
// So ox only ever removes what it can prove it wrote. Content it does not
// recognize is left alone even inside a reserved namespace.
func TestReservedSweepNeverDestroysUnrecognizedContent(t *testing.T) {
	repo := t.TempDir()
	target := sharedTarget()
	skillsRoot := filepath.Join(repo, ".agents", "skills")

	// A populated directory ox never installed, wearing the reserved prefix.
	mine := filepath.Join(skillsRoot, "ox-cli-mine")
	require.NoError(t, os.MkdirAll(filepath.Join(mine, "references"), 0o755))
	skillBody := []byte("---\nname: ox-cli-mine\ndescription: my own notes\n---\nmy content\n")
	notes := []byte("months of my own research\n")
	require.NoError(t, os.WriteFile(filepath.Join(mine, "SKILL.md"), skillBody, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(mine, "references", "notes.md"), notes, 0o644))

	// A full install + a second reconcile: retirement runs on the second pass, so
	// one pass alone would not exercise the removal path at all.
	_, err := Install(adapterprotocol.SkillsParams{RepoRoot: repo, Version: "1.0.0"}, skillsRoot)
	require.NoError(t, err)
	plan, err := Plan(repo, "1.0.0", desiredFor(target), []adapterprotocol.SkillTarget{target})
	require.NoError(t, err)
	require.NoError(t, Apply(plan))

	gotSkill, err := os.ReadFile(filepath.Join(mine, "SKILL.md"))
	require.NoError(t, err, "an unrecognized reserved-prefix skill was deleted and is unrecoverable")
	require.Equal(t, skillBody, gotSkill)
	gotNotes, err := os.ReadFile(filepath.Join(mine, "references", "notes.md"))
	require.NoError(t, err, "a populated directory was removed wholesale")
	require.Equal(t, notes, gotNotes)
}

// TestPlanToleratesJournalFromAnotherSchema is the wedge guard.
//
// The apply journal is a crash-recovery hint, not a source of truth. When a
// schema-mismatched or corrupt journal made Plan return an error, every consumer
// degraded silently: `ox doctor` said "cannot inspect managed skills", prime
// logged at debug and returned, the daemon tick errored quietly. The journal lives
// in gitignored cache/, so nobody could see the cause, and that repository stopped
// receiving skill updates permanently.
//
// Failure prevented: one crash before an upgrade freezes a machine's skills
// forever, with no visible symptom.
func TestPlanToleratesJournalFromAnotherSchema(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
	}{
		{"schema from another release", `{"schema_version": 999, "actions": []}`},
		{"truncated by a crash mid-write", `{"schema_version": 1, "acti`},
		{"empty file", ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			target := sharedTarget()
			skillsRoot := filepath.Join(repo, ".agents", "skills")
			_, err := Install(adapterprotocol.SkillsParams{RepoRoot: repo, Version: "1.0.0"}, skillsRoot)
			require.NoError(t, err)

			jp := filepath.Join(repo, ".sageox", "cache", "skills-apply.json")
			require.NoError(t, os.MkdirAll(filepath.Dir(jp), 0o755))
			require.NoError(t, os.WriteFile(jp, []byte(tc.content), 0o600))

			plan, err := Plan(repo, "1.0.0", desiredFor(target), []adapterprotocol.SkillTarget{target})
			require.NoError(t, err, "an unreadable recovery hint must never block planning")
			require.NotNil(t, plan)
		})
	}
}

// TestContentReleaseDoesNotTouchTheCommittedLockfile is the reason the manifest
// was split in two.
//
// Gitignoring the skill files is not enough on its own. Before the split, the
// committed lockfile carried source.version, source.revision, and a digest for
// every managed file — so every content-bearing release rewrote a TRACKED file
// and produced a diff in someone's pull request, even though every file it
// described was invisible to git. The manifest would have become the last
// remaining source of exactly the churn this rework exists to remove.
//
// After the split the committed half holds only the project's selection — which
// bundles, which agent targets — and changes only when a human changes it.
func TestContentReleaseDoesNotTouchTheCommittedLockfile(t *testing.T) {
	repo := t.TempDir()
	target := sharedTarget()
	targets := []adapterprotocol.SkillTarget{target}
	desired := desiredFor(target)

	v1 := fakeCatalog{revision: "rev-1", skill: fakeSkill("1.0.0", "one")}
	plan, err := planWithSource(repo, "1.0.0", desired, targets, v1)
	require.NoError(t, err)
	require.NoError(t, Apply(plan))

	committedBefore, err := os.ReadFile(LockPath(repo))
	require.NoError(t, err)
	stateBefore, err := os.ReadFile(StatePath(repo))
	require.NoError(t, err)

	// A new ox version shipping new skill content: different revision, different
	// version, identical project selection.
	v2 := fakeCatalog{revision: "rev-2", skill: fakeSkill("2.0.0", "two")}
	plan, err = planWithSource(repo, "2.0.0", desired, targets, v2)
	require.NoError(t, err)
	require.NotEmpty(t, plan.Updates, "precondition: the release must actually change skill content")
	require.NoError(t, Apply(plan))

	committedAfter, err := os.ReadFile(LockPath(repo))
	require.NoError(t, err)
	require.Equal(t, string(committedBefore), string(committedAfter),
		"a content-bearing release rewrote the COMMITTED lockfile; it would appear in the customer's pull request")

	stateAfter, err := os.ReadFile(StatePath(repo))
	require.NoError(t, err)
	require.NotEqual(t, string(stateBefore), string(stateAfter),
		"machine-local state must record the new revision, or prime's fast path can never detect staleness")
	require.Contains(t, string(stateAfter), "rev-2")
}

// TestInstalledSourceSurvivesTheSchemaSplit guards the interaction that would
// silently destroy prime's fast path: source.revision moved out of the committed
// file, so a reader that only looked there would report an empty revision, never
// match, and run a full plan on EVERY session start.
func TestInstalledSourceSurvivesTheSchemaSplit(t *testing.T) {
	repo := t.TempDir()
	target := sharedTarget()
	targets := []adapterprotocol.SkillTarget{target}

	plan, err := planWithSource(repo, "1.2.3", desiredFor(target), targets,
		fakeCatalog{revision: "rev-abc", skill: fakeSkill("1.2.3", "one")})
	require.NoError(t, err)
	require.NoError(t, Apply(plan))

	revision, version, selected := InstalledSource(repo)
	require.True(t, selected, "targets are recorded in the committed half and must still be visible")
	require.Equal(t, "rev-abc", revision, "revision lives in the machine-local half and must be merged back in")
	require.Equal(t, "1.2.3", version)
}

// TestSchemaOneLockfileMigratesOnRead: an existing repository carries everything
// in one committed file. Reading it must work without a separate migration step,
// and the next write must split it — otherwise every pre-0.15.0 repository would
// need a flag day.
func TestSchemaOneLockfileMigratesOnRead(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Dir(LockPath(repo)), 0o755))
	legacy := `{
  "schema_version": 1,
  "source": {"kind": "builtin", "revision": "old-rev", "version": "0.14.0"},
  "desired": {"bundles": ["core"], "targets": ["agents-project"]},
  "targets": [{"key": "agents-project", "root": ".agents/skills", "format": "agent-skills-v1", "scope": "project", "link_policy": "reject"}],
  "managed_files": []
}`
	require.NoError(t, os.WriteFile(LockPath(repo), []byte(legacy), 0o644))

	revision, version, selected := InstalledSource(repo)
	require.True(t, selected, "a schema-1 repository must still report its selected targets")
	require.Equal(t, "old-rev", revision, "inline schema-1 source must be readable without a migration step")
	require.Equal(t, "0.14.0", version)
}

// multiCatalog serves a named set of skills, so a test can drop one between
// reconciles the way a release drops a skill from the catalog.
type multiCatalog struct {
	revision string
	skills   []skills.Skill
}

func (m multiCatalog) Digest() (string, error) { return m.revision, nil }
func (m multiCatalog) Select(string, DesiredSkills) ([]skills.Skill, error) {
	return m.skills, nil
}

func namedSkill(name, body string) skills.Skill {
	files := []skills.File{
		{Path: "SKILL.md", Content: []byte("---\nname: " + name + "\ndescription: d\n---\n" + body + "\n")},
		{Path: "references/guide.md", Content: []byte("guide for " + name + "\n")},
	}
	return skills.Skill{Name: name, Content: files[0].Content, Files: files, Version: "1.0.0"}
}

// TestRetiringASkillRemovesItCompletely is the lifecycle proof.
//
// Retirement is the half of the model that has NEVER worked. Under the old design
// removing a file meant asking every customer to accept a deletion commit, so in
// practice nobody ever did it — five orphaned command files survived in ox's own
// repository across releases, shipped by no version and removed by none.
//
// This drives the whole cycle: install two skills, drop one from the catalog the
// way a release would, reconcile, and assert the retired skill is gone from disk
// entirely — SKILL.md, its reference files, and the directory itself — while the
// surviving skill is untouched.
//
// Failure prevented: a retired skill lingers forever on every machine, and agents
// keep reading a playbook the product no longer ships.
func TestRetiringASkillRemovesItCompletely(t *testing.T) {
	repo := t.TempDir()
	target := sharedTarget()
	targets := []adapterprotocol.SkillTarget{target}
	desired := desiredFor(target)

	before := multiCatalog{revision: "rev-1", skills: []skills.Skill{
		namedSkill("ox-cli-keeper", "keep me"),
		namedSkill("ox-cli-doomed", "retire me"),
	}}
	plan, err := planWithSource(repo, "1.0.0", desired, targets, before)
	require.NoError(t, err)
	require.NoError(t, Apply(plan))

	doomedDir := filepath.Join(repo, ".agents", "skills", "ox-cli-doomed")
	keeperDir := filepath.Join(repo, ".agents", "skills", "ox-cli-keeper")
	require.FileExists(t, filepath.Join(doomedDir, "SKILL.md"), "precondition: the skill must be installed before it can be retired")
	require.FileExists(t, filepath.Join(doomedDir, "references", "guide.md"))

	// The release that retires it: same project selection, one fewer skill.
	after := multiCatalog{revision: "rev-2", skills: []skills.Skill{
		namedSkill("ox-cli-keeper", "keep me"),
	}}
	plan, err = planWithSource(repo, "1.1.0", desired, targets, after)
	require.NoError(t, err)
	require.NotEmpty(t, plan.Removes, "the retired skill must appear as a removal, not be silently forgotten")
	require.NoError(t, Apply(plan))

	require.NoDirExists(t, doomedDir,
		"the retired skill's directory survived; agents would keep reading a playbook ox no longer ships")

	require.FileExists(t, filepath.Join(keeperDir, "SKILL.md"), "retiring one skill must not disturb another")
	require.FileExists(t, filepath.Join(keeperDir, "references", "guide.md"))

	// Retirement must converge: a second pass has nothing left to do.
	plan, err = planWithSource(repo, "1.1.0", desired, targets, after)
	require.NoError(t, err)
	require.Empty(t, plan.Removes, "retirement did not converge; the plan still wants to remove something")
}

// TestRetiringAUserModifiedSkillPreservesTheirWork bounds the removal above.
// A retired skill the user edited is left on disk and ox relinquishes ownership,
// because the edit is theirs and retirement is not a license to delete it.
func TestRetiringAUserModifiedSkillPreservesTheirWork(t *testing.T) {
	repo := t.TempDir()
	target := sharedTarget()
	targets := []adapterprotocol.SkillTarget{target}
	desired := desiredFor(target)

	before := multiCatalog{revision: "rev-1", skills: []skills.Skill{namedSkill("ox-cli-doomed", "original")}}
	plan, err := planWithSource(repo, "1.0.0", desired, targets, before)
	require.NoError(t, err)
	require.NoError(t, Apply(plan))

	guide := filepath.Join(repo, ".agents", "skills", "ox-cli-doomed", "references", "guide.md")
	mine := []byte("my own notes, months of them\n")
	require.NoError(t, os.WriteFile(guide, mine, 0o644))

	after := multiCatalog{revision: "rev-2", skills: []skills.Skill{namedSkill("ox-cli-keeper", "keep me")}}
	plan, err = planWithSource(repo, "1.1.0", desired, targets, after)
	require.NoError(t, err)
	require.NoError(t, Apply(plan))

	got, err := os.ReadFile(guide)
	require.NoError(t, err, "retirement destroyed a file the user had edited")
	require.Equal(t, mine, got)
}
