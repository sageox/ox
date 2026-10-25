package skillmanager

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/require"
)

// commitInRepo stages and commits paths with an identity local to the fixture.
//
// The identity is set with -c rather than `git config`, so a developer running
// the suite never has their own user.name or user.email touched. That rule is not
// stylistic here: this package's tests run against real repositories.
func commitInRepo(t *testing.T, repo, message string, paths ...string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run(append([]string{"add", "-f", "--"}, paths...)...)
	run("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
		"-c", "commit.gpgsign=false", "commit", "-q", "-m", message)
}

// TestReconcile_CheckedInSkillOfTheSameNameIsNeverOverwritten is the customer
// promise that made the "-team" suffix safe to adopt.
//
// Under the old "sageox-team-" PREFIX, ox owned the namespace by declaration: no
// human was ever going to name a directory that, so claiming one on sight was
// defensible. A suffix cannot make that claim — "-team" is ordinary English, and
// `notify-team`, `onboard-team`, `deploy-team` are all names somebody might
// reasonably pick.
//
// The committed file here is a VALID ox projection, stamp and all, and that is
// the whole point of the fixture. An unstamped stranger is already refused by the
// not-managed-by-ox arm (the test below pins that), so staging one would let this
// test pass with the tracked-path check deleted. A stamped projection satisfies
// every ownership arm ox has; the ONLY thing left standing between it and an
// overwrite is "git tracks this path, so a human put it there on purpose."
//
// That state is ordinary, not exotic: any repository that committed its skills
// before the ignore rules landed, or anyone who ran `git add -f`, is in it. And
// an overwrite there is not a lost file but a silent uncommitted diff against
// somebody's committed work, with nothing scheduled to ever revisit it.
func TestReconcile_CheckedInSkillOfTheSameNameIsNeverOverwritten(t *testing.T) {
	const skillName = "deploy"
	installedDir := filepath.Join(".agents", "skills", skillName+TeamSuffix)

	repo := t.TempDir()
	team := t.TempDir()
	writeTeamSkill(t, team, skillName, "", nil)
	stageTeamWiredProject(t, repo, team)

	// A previous, pinned version of the team's own skill — committed. It verifies
	// as ox's, so nothing but its tracked status can protect it.
	mine := string(TeamSkillStamp.Apply(
		[]byte("---\nname: deploy\ndescription: the version we pinned\n---\n\nour body\n")))
	require.True(t, TeamSkillStamp.Verifies([]byte(mine)),
		"fixture must be a VALID projection, or this test passes with the guard deleted")

	manifest := filepath.Join(repo, installedDir, "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(manifest), 0o755))
	require.NoError(t, os.WriteFile(manifest, []byte(mine), 0o644))
	commitInRepo(t, repo, "pin the deploy-team skill", filepath.ToSlash(filepath.Join(installedDir, "SKILL.md")))

	target := sharedTarget()
	plan, err := Reconcile(repo, "1.0.0", desiredFor(target), []adapterprotocol.SkillTarget{target})
	require.NoError(t, err, "one conflicting skill must not fail the whole reconcile")

	// The assertion that carries the promise: the committed bytes, unchanged.
	after, readErr := os.ReadFile(manifest)
	require.NoError(t, readErr)
	require.Equal(t, mine, string(after),
		"ox rewrote a file git tracks, leaving an uncommitted diff against committed work that nothing will revisit")

	// Silence would be nearly as bad as an overwrite: the team author needs to
	// learn why their skill is not here.
	var reasons []string
	for _, conflict := range plan.Conflicts {
		if strings.Contains(conflict.Path, skillName+TeamSuffix) {
			reasons = append(reasons, conflict.Reason)
		}
	}
	require.NotEmpty(t, reasons, "the refusal was silent; nothing told anyone the team's skill did not land")
	require.Contains(t, strings.Join(reasons, " "), "checked-in",
		"the reason does not name the cause a human can act on: %v", reasons)

	// The rest of the catalog is unaffected — one contested name must not take
	// every other skill offline.
	require.FileExists(t, filepath.Join(repo, ".agents", "skills", "ox-cli-plan", "SKILL.md"))
}

// TestReconcile_UnstampedTeamNamedDirectoryIsLeftAlone covers the same hazard
// without git in the picture.
//
// A skill someone wrote by hand and never committed is the commonest shape of
// all — gitignored working-tree content. It has no tracked path to protect it, so
// the only thing standing between it and deletion is that ox refuses to claim a
// "-team" directory it cannot prove it wrote.
func TestReconcile_UnstampedTeamNamedDirectoryIsLeftAlone(t *testing.T) {
	repo := t.TempDir()
	team := t.TempDir()
	// The team publishes nothing at all, so the ONLY thing that could touch this
	// directory is a name-based sweep.
	require.NoError(t, os.MkdirAll(filepath.Join(team, "agents", "skills"), 0o755))
	stageTeamWiredProject(t, repo, team)

	const mine = "---\nname: notify-team\ndescription: pings the on-call channel\n---\n\nbody\n"
	manifest := filepath.Join(repo, ".agents", "skills", "notify"+TeamSuffix, "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(manifest), 0o755))
	require.NoError(t, os.WriteFile(manifest, []byte(mine), 0o644))

	target := sharedTarget()
	_, err := Reconcile(repo, "1.0.0", desiredFor(target), []adapterprotocol.SkillTarget{target})
	require.NoError(t, err)

	after, readErr := os.ReadFile(manifest)
	require.NoError(t, readErr, "ox deleted a hand-authored skill because its name ends in -team")
	require.Equal(t, mine, string(after))
}

// TestReconcile_LegacyPrefixedTeamSkillIsSweptAndReinstalledUnderTheSuffix is the
// migration, end to end.
//
// Every repository on the old scheme holds `sageox-team-<name>` directories that
// nothing will ever install again. Leaving them would give an AI coworker two
// copies of the same skill under two names — and the stale one would keep
// answering, forever, because nothing retires it.
//
// The sweep is safe precisely BECAUSE the old name is a prefix ox declared: it
// can be reclaimed on sight, which is what the suffix deliberately gave up.
func TestReconcile_LegacyPrefixedTeamSkillIsSweptAndReinstalledUnderTheSuffix(t *testing.T) {
	const skillName = "deploy"

	repo := t.TempDir()
	team := t.TempDir()
	writeTeamSkill(t, team, skillName, "", nil)
	stageTeamWiredProject(t, repo, team)

	// The state an upgrading repository is actually in: the previous release's
	// projection, on disk, under the old prefix.
	legacyDir := filepath.Join(repo, ".agents", "skills", LegacyTeamPrefix+skillName)
	require.NoError(t, os.MkdirAll(legacyDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(legacyDir, "SKILL.md"),
		[]byte("---\nname: deploy\ndescription: the old projection\n---\n\nbody\n"), 0o644))

	target := sharedTarget()
	_, err := Reconcile(repo, "1.0.0", desiredFor(target), []adapterprotocol.SkillTarget{target})
	require.NoError(t, err)

	require.NoFileExists(t, filepath.Join(legacyDir, "SKILL.md"),
		"the pre-suffix projection survived; an AI coworker now sees the same skill twice, and the stale copy never retires")
	require.NoDirExists(t, legacyDir)

	suffixed := filepath.Join(repo, ".agents", "skills", skillName+TeamSuffix, "SKILL.md")
	require.FileExists(t, suffixed, "the team's skill did not reinstall under its real name")

	// And the new projection carries the proof that replaces the old prefix.
	installed, readErr := os.ReadFile(suffixed)
	require.NoError(t, readErr)
	require.True(t, TeamSkillStamp.Verifies(installed),
		"the projection carries no verifiable stamp, so the next reconcile on a machine with no cache cannot tell it from a stranger")
}

// TestTrackedSkillDirs_AnswersWithoutGuessing covers the probe's edges directly,
// because every one of them decides whether ox may WRITE.
//
// The "not a git repository" case is the one worth stating out loud: it returns
// empty AND no error, which reads like the fail-open this codebase keeps
// stamping out. It is not. Nothing can be tracked where there is no index, so
// empty is the correct answer rather than a guess — and the alternative, failing
// the plan, would break `ox` in a plain directory for no safety gained. Every
// OTHER failure does propagate, so a broken git never masquerades as "clean."
func TestTrackedSkillDirs_AnswersWithoutGuessing(t *testing.T) {
	t.Parallel()
	target := sharedTarget()
	targets := []adapterprotocol.SkillTarget{target}

	t.Run("a directory that is not a git repository has nothing tracked", func(t *testing.T) {
		got, err := trackedSkillDirs(t.TempDir(), targets)
		require.NoError(t, err, "a plain directory must not fail the plan")
		require.Empty(t, got)
	})

	t.Run("no skill targets means no git call at all", func(t *testing.T) {
		got, err := trackedSkillDirs(t.TempDir(), nil)
		require.NoError(t, err)
		require.Empty(t, got)
	})

	t.Run("a tracked file names its skill directory, not itself", func(t *testing.T) {
		repo := t.TempDir()
		stageTeamWiredProject(t, repo, t.TempDir())

		// Two skills, and a file sitting directly in the root that belongs to no
		// skill — the last one must not invent a directory entry.
		for _, rel := range []string{
			".agents/skills/deploy-team/SKILL.md",
			".agents/skills/deploy-team/references/notes.md",
			".agents/skills/mine/SKILL.md",
			".agents/skills/README.md",
		} {
			path := filepath.Join(repo, filepath.FromSlash(rel))
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.WriteFile(path, []byte("x\n"), 0o644))
			commitInRepo(t, repo, "add "+rel, rel)
		}

		got, err := trackedSkillDirs(repo, targets)
		require.NoError(t, err)
		require.Equal(t, map[string]struct{}{
			".agents/skills/deploy-team": {},
			".agents/skills/mine":        {},
		}, got, "a loose file in the skills root must not register as a skill directory")
	})
}

// TestTrackedSkillDirs_FindsTrackedDirsFromANestedProjectRoot pins the discovery
// question the probe has to ask git rather than answer itself.
//
// A stat for `.git` at repoRoot answers "is this the work-tree ROOT", which is a
// different question. A project root nested below the work-tree root has no `.git`
// of its own, so a stat-based gate reports "not a repository", the tracked set
// comes back EMPTY, and reconciliation proceeds to overwrite committed skills
// believing nothing is tracked — the exact fail-open this probe exists to prevent,
// reintroduced by the gate standing in front of it.
func TestTrackedSkillDirs_FindsTrackedDirsFromANestedProjectRoot(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	stageTeamWiredProject(t, worktree, t.TempDir())

	// The project lives BELOW the git root — a monorepo package, a nested service.
	project := filepath.Join(worktree, "services", "api")
	rel := filepath.Join("services", "api", ".agents", "skills", "deploy"+TeamSuffix, "SKILL.md")
	path := filepath.Join(worktree, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("x\n"), 0o644))
	commitInRepo(t, worktree, "add nested skill", filepath.ToSlash(rel))

	got, err := trackedSkillDirs(project, []adapterprotocol.SkillTarget{sharedTarget()})
	require.NoError(t, err)
	require.Contains(t, got, ".agents/skills/deploy"+TeamSuffix,
		"a project root below the work-tree root reported nothing tracked, so reconcile would overwrite committed work")
}

// TestReconcile_CommittedTeamSkillIsNeverRETIRED covers the deleting half of this
// package, which had no tracked check at all.
//
// Being recorded in the lockfile proves ox WROTE a file. It says nothing about
// whether somebody has since committed it. A team skill that was installed,
// committed, and then retired upstream — or filtered out by a `repos:` change —
// is exactly that shape, and without this guard reconcile removes it, leaving an
// uncommitted deletion in somebody's index that nothing is scheduled to revisit.
func TestReconcile_CommittedTeamSkillIsNeverRetired(t *testing.T) {
	const skillName = "deploy"
	installedDir := filepath.Join(".agents", "skills", skillName+TeamSuffix)
	manifest := filepath.Join(installedDir, "SKILL.md")

	repo := t.TempDir()
	team := t.TempDir()
	writeTeamSkill(t, team, skillName, "", nil)
	stageTeamWiredProject(t, repo, team)

	target := sharedTarget()
	targets := []adapterprotocol.SkillTarget{target}

	// Install it for real, so the lockfile records ox's ownership.
	_, err := Reconcile(repo, "1.0.0", desiredFor(target), targets)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(repo, manifest))

	// The team commits the projection — plausible in any repo that wants its
	// coworkers' skills reviewable, and the state `git add -f` produces.
	commitInRepo(t, repo, "commit the team skill", filepath.ToSlash(manifest))
	before, readErr := os.ReadFile(filepath.Join(repo, manifest))
	require.NoError(t, readErr)

	// Upstream retires it: the team's copy is gone, so it leaves desired state.
	require.NoError(t, os.RemoveAll(filepath.Join(team, "agents", "skills", skillName)))

	plan, err := Reconcile(repo, "1.0.0", desiredFor(target), targets)
	require.NoError(t, err)

	after, readErr := os.ReadFile(filepath.Join(repo, manifest))
	require.NoError(t, readErr,
		"ox deleted a skill git tracks; the deletion is now staged in somebody's index with nothing to revisit it")
	require.Equal(t, string(before), string(after))

	var reasons []string
	for _, conflict := range plan.Conflicts {
		if strings.Contains(conflict.Path, skillName+TeamSuffix) {
			reasons = append(reasons, conflict.Reason)
		}
	}
	require.NotEmpty(t, reasons, "the refusal to retire a tracked skill was silent")
	require.Contains(t, strings.Join(reasons, " "), "checked-in")
}
