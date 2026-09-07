package skillmanager

import (
	"encoding/json"
	"os"
	"path/filepath"
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

	data, err := os.ReadFile(filepath.Join(repo, ".agents", "skills", "ox-plan", "SKILL.md"))
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

func TestUnknownAndModifiedFilesAreTruthfulConflicts(t *testing.T) {
	repo := t.TempDir()
	target := sharedTarget()
	userPath := filepath.Join(repo, ".agents", "skills", "ox-plan", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(userPath), 0o755))
	user := []byte("---\nname: ox-plan\ndescription: mine\n---\nuser owned\n")
	require.NoError(t, os.WriteFile(userPath, user, 0o644))

	response, err := Install(adapterprotocol.SkillsParams{RepoRoot: repo, Version: "1.0.0"}, filepath.Join(repo, ".agents", "skills"))
	require.NoError(t, err)
	require.False(t, response.Installed)
	require.NotEmpty(t, response.Conflicts)
	after, err := os.ReadFile(userPath)
	require.NoError(t, err)
	require.Equal(t, user, after)

	managedPath := filepath.Join(repo, ".agents", "skills", "ox-consult", "SKILL.md")
	original, err := os.ReadFile(managedPath)
	require.NoError(t, err)
	modified := append(original, []byte("\nlocal edit\n")...)
	require.NoError(t, os.WriteFile(managedPath, modified, 0o644))
	plan, err := Plan(repo, "1.0.0", desiredFor(target), []adapterprotocol.SkillTarget{target})
	require.NoError(t, err)
	require.Contains(t, conflictPaths(plan.Conflicts), filepath.FromSlash(".agents/skills/ox-consult/SKILL.md"))
	require.NoError(t, Apply(plan))
	after, err = os.ReadFile(managedPath)
	require.NoError(t, err)
	require.Equal(t, modified, after)
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
	require.Equal(t, os.FileMode(0o755), info.Mode().Perm())

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
// is how a repo ends up without ox-pr-header while prime tells the agent to
// "see the ox-pr-header skill".
//
// A symlinked SKILL.md cannot carry a valid ox stamp, so it is definitionally
// not ox's. Skipping it is the correct answer; the hard refusal stays on the
// write path, where following a symlink is the path-escape this guards.
func TestForeignSymlinkDoesNotAbortSkillDiscovery(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	root := filepath.Join(repo, ".claude", "skills")

	// One real ox-stamped skill, so discovery has something to find.
	managed := filepath.Join(root, "ox-plan")
	if err := os.MkdirAll(managed, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("# ox-plan\n")
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
