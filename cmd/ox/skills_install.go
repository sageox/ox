package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/teamdocs"
	"github.com/sageox/ox/internal/version"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/spf13/cobra"
)

// skills_install.go — `ox skills install` and `ox skills uninstall`.
//
// The write half of the catalog surface. `ox skills catalog` can now report a
// skill as available rather than installed — the `team` bundle is the first that
// does not default on — and a report with no way to act on it is the same
// dead-end `ox skills approve` was built to remove.
//
// Two things keep these commands honest:
//
//   - They go through the SAME reconcile entry point every other install path
//     uses. A second installer would be a second definition of what ox owns, and
//     the lockfile is the only record that lets ox later remove what it wrote.
//   - They finish the job in one command. Recording a selection and then telling
//     the human to run `ox doctor --fix` leaves the repository in exactly the
//     state the install was meant to end, which reads as the install not having
//     worked.
//
// `--team` is deliberately NOT symmetric with the rest. It seeds a copy into the
// Team Context and then stops owning it: the team edits it, the team's history
// carries it, and ox never writes over it again. Treating it as a managed
// install would mean overwriting a teammate's edits on every publish.

const (
	skillChangeInstalled    = "installed"
	skillChangeAlready      = "already-installed"
	skillChangePublished    = "published"
	skillChangeUninstalled  = "uninstalled"
	skillChangeNotInstalled = "not-installed"
)

// teamSkillsPublishRoot is the CANONICAL skills root inside a Team Context.
// Discovery also walks the legacy coworkers/skills, but nothing new is ever
// written there — see teamdocs.SkillRoots.
const teamSkillsPublishRoot = "agents/skills"

// teamPublishTimeout bounds the whole Team Context transaction: the wait for the
// repository lock, the file writes it guards, and the git invocations that record
// them. Matches the other two commands that write into that checkout (ox coworker
// add, ox memory put); it is generous for a local commit and short enough that a
// wedged index does not hang a terminal indefinitely.
//
// Deliberately shorter than gitutil.RepoLockTimeout, which is sized for a daemon
// that can afford to wait two minutes for a peer. A human at a terminal cannot:
// "the Team Context is busy, try again" after 30 seconds is a better answer than
// a prompt that appears to have hung.
const teamPublishTimeout = 30 * time.Second

var skillsInstallCmd = &cobra.Command{
	Use:     "install <name>...",
	Aliases: []string{"add"},
	Short:   "Install skills ox ships into this repository",
	Long: `Install skills ox ships into this repository.

Run ` + "`ox skills catalog`" + ` to see the names. Default bundles are already
installed; the ones marked available are what this command is for.

The files land immediately, and your selection is recorded in
.sageox/skills.lock.json. That file is part of the repository on purpose: the
choice belongs to the project, so your coworkers get the same skills.

With --team, the skill is copied into your Team Context instead, where it becomes
your team's to edit and applies to every repository on the team. That is a SEED,
not a managed install — ox writes the copy once and never overwrites it, so if
the skill is already published this refuses rather than discarding your team's
edits. Names are refused all-or-nothing: a typo cannot leave half a run applied.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runSkillsInstall,
}

var skillsUninstallCmd = &cobra.Command{
	Use:     "uninstall <name>...",
	Aliases: []string{"remove"},
	Short:   "Remove skills ox installed in this repository",
	Long: `Remove skills ox installed in this repository.

The files go away and the name is dropped from .sageox/skills.lock.json, so the
next reconcile does not put them back.

Only skills ox owns can be removed this way. A skill you authored yourself is
refused: ox has no record of it, no way to restore it, and no claim on it. A
skill your team publishes is refused too — remove it in the Team Context, where
it lives.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runSkillsUninstall,
}

func init() {
	skillsInstallCmd.Flags().Bool("team", false,
		"Publish the skill to your Team Context instead, as a one-time seed the team then owns")
	skillsInstallCmd.Flags().Bool("json", false, "Emit machine-readable JSON")
	skillsUninstallCmd.Flags().Bool("json", false, "Emit machine-readable JSON")
	skillsCmd.AddCommand(skillsInstallCmd, skillsUninstallCmd)
}

// skillChangeRow is one name's outcome, in both renderings.
type skillChangeRow struct {
	Name  string `json:"name"`
	State string `json:"state"`
	// Bundle names the selection responsible when a skill is already installed
	// without having been asked for by name. Without it, "already-installed" is
	// indistinguishable from a repeated install.
	Bundle string `json:"bundle,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// skillsChangeOutput is the shape both commands emit.
//
// One shape for install and uninstall on purpose: they are symmetric operations
// and a reader should not need two parsers. Every array is present and `[]` when
// empty — a key that appears only sometimes forces the reader to guess whether
// its absence means "none" or "this ox does not report that."
type skillsChangeOutput struct {
	Skills  []skillChangeRow `json:"skills"`
	Written []string         `json:"written"`
	Removed []string         `json:"removed"`
	// TeamContext is the checkout a --team publish wrote into, and empty
	// otherwise. Always present, so a reader can tell "published nowhere" from
	// "this ox does not report where."
	TeamContext string `json:"team_context"`
	Guidance    string `json:"guidance"`
}

func newSkillsChangeOutput() skillsChangeOutput {
	return skillsChangeOutput{Skills: []skillChangeRow{}, Written: []string{}, Removed: []string{}}
}

func runSkillsInstall(cmd *cobra.Command, args []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	toTeam, _ := cmd.Flags().GetBool("team")

	gitRoot := findGitRoot()
	if gitRoot == "" {
		return fmt.Errorf("not inside a git repository")
	}

	change := installCatalogSkills
	if toTeam {
		change = publishCatalogSkillsToTeam
	}
	out, err := change(gitRoot, args)
	if err != nil {
		return err
	}
	return emitSkillsChange(cmd.OutOrStdout(), out, asJSON)
}

func runSkillsUninstall(cmd *cobra.Command, args []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")

	gitRoot := findGitRoot()
	if gitRoot == "" {
		return fmt.Errorf("not inside a git repository")
	}

	out, err := uninstallCatalogSkills(gitRoot, args)
	if err != nil {
		return err
	}
	return emitSkillsChange(cmd.OutOrStdout(), out, asJSON)
}

// installCatalogSkills adds names to this repository's committed selection and
// materializes them. repoRoot is a parameter rather than something this goes
// looking for, so the decision is testable without faking a command.
func installCatalogSkills(repoRoot string, names []string) (skillsChangeOutput, error) {
	out := newSkillsChangeOutput()
	names, err := validateCatalogNames(names)
	if err != nil {
		return out, err
	}

	desired, _, err := skillmanager.LoadDesired(repoRoot)
	if err != nil {
		return out, err
	}
	if len(desired.Targets) == 0 {
		return out, fmt.Errorf("this repository has not selected an AI coworker, so there is nowhere to put a skill — run `ox init`")
	}
	selected, err := selectedCatalogNames(desired)
	if err != nil {
		return out, err
	}

	var add []string
	for _, name := range names {
		if selected[name] {
			out.Skills = append(out.Skills, skillChangeRow{
				Name: name, State: skillChangeAlready, Bundle: bundleSelecting(desired, name),
			})
			continue
		}
		add = append(add, name)
		out.Skills = append(out.Skills, skillChangeRow{Name: name, State: skillChangeInstalled})
	}

	if len(add) > 0 {
		// The existing reconcile entry point, with a mutation that only appends
		// names. A second installer would be a second definition of what ox owns,
		// and the lockfile is the only record that lets ox later remove what it
		// wrote.
		plan, reconcileErr := skillmanager.ReconcileUpdateGated(repoRoot, version.Version,
			func(current skillmanager.DesiredSkills, targets []adapterprotocol.SkillTarget) (skillmanager.DesiredSkills, []adapterprotocol.SkillTarget, error) {
				current.Names = append(current.Names, add...)
				return current, targets, nil
			}, func(plan *skillmanager.ReconcilePlan) error {
				for _, conflict := range plan.Conflicts {
					for _, name := range add {
						needle := "/skills/" + name + "/"
						if strings.Contains("/"+filepath.ToSlash(conflict.Path), needle) {
							return fmt.Errorf("%q conflicts with existing content at %s; nothing was installed", name, conflict.Path)
						}
					}
				}
				return nil
			})
		if reconcileErr != nil {
			return out, fmt.Errorf("install %s: %w", strings.Join(add, ", "), reconcileErr)
		}
		out.Written = orEmptyStrings(plan.WrittenPaths())
		out.Removed = orEmptyStrings(plan.RemovedPaths())
	}

	out.Guidance = skillsChangeGuidance(out)
	return out, nil
}

// uninstallCatalogSkills drops names from the committed selection and lets
// reconcile remove the files ox owns.
func uninstallCatalogSkills(repoRoot string, names []string) (skillsChangeOutput, error) {
	out := newSkillsChangeOutput()

	desired, targets, err := skillmanager.LoadDesired(repoRoot)
	if err != nil {
		return out, err
	}
	roots := make([]string, 0, len(targets))
	for _, target := range targets {
		roots = append(roots, target.Root)
	}
	// Classified BEFORE the catalog check, because "you wrote this yourself" is a
	// far more useful answer than "ox ships no such skill" — and it is the one
	// that stops a name collision from deleting a human's work.
	if err := refuseSkillsOxDoesNotOwn(repoRoot, roots, names); err != nil {
		return out, err
	}

	names, err = validateCatalogNames(names)
	if err != nil {
		return out, err
	}
	selected, err := selectedCatalogNames(desired)
	if err != nil {
		return out, err
	}

	var drop []string
	for _, name := range names {
		if !selected[name] {
			out.Skills = append(out.Skills, skillChangeRow{
				Name: name, State: skillChangeNotInstalled,
				Detail: "this repository has not selected it",
			})
			continue
		}
		// Dropping the NAME of a bundle-selected skill changes nothing: the bundle
		// still selects it and reconcile reinstalls it on the spot. Reporting
		// success would be the worst outcome available — the command says the skill
		// is gone while the file is still on disk. The lockfile has no per-skill
		// exclusion, so the bundle really is the unit, and saying so beats
		// inventing a flag that cannot work.
		if bundle := bundleSelecting(desired, name); bundle != "" {
			return out, fmt.Errorf("%q is part of the %q bundle, which this repository selected as a whole — ox has no way to drop one skill out of a bundle, so the file would come straight back%s",
				name, bundle, nothingChangedSuffix(names))
		}
		drop = append(drop, name)
		out.Skills = append(out.Skills, skillChangeRow{Name: name, State: skillChangeUninstalled})
	}

	if len(drop) > 0 {
		plan, reconcileErr := skillmanager.ReconcileUpdate(repoRoot, version.Version,
			func(current skillmanager.DesiredSkills, currentTargets []adapterprotocol.SkillTarget) (skillmanager.DesiredSkills, []adapterprotocol.SkillTarget, error) {
				current.Names = slices.DeleteFunc(current.Names, func(n string) bool { return slices.Contains(drop, n) })
				return current, currentTargets, nil
			})
		if reconcileErr != nil {
			return out, fmt.Errorf("uninstall %s: %w", strings.Join(drop, ", "), reconcileErr)
		}
		out.Written = orEmptyStrings(plan.WrittenPaths())
		out.Removed = orEmptyStrings(plan.RemovedPaths())
	}

	out.Guidance = skillsChangeGuidance(out)
	return out, nil
}

// refuseSkillsOxDoesNotOwn stops an uninstall that would delete a file ox has no
// claim on.
//
// A hand-authored skill is the majority of what sits in a real repository's
// skills directory. ox has no record of it, no copy to restore from, and no
// ownership stamp on it — deleting one because the name matched is unrecoverable
// loss from a command the human believed was scoped to ox's own files.
func refuseSkillsOxDoesNotOwn(repoRoot string, roots, names []string) error {
	if len(roots) == 0 {
		return nil
	}
	installed := map[string]installedSkillRow{}
	for _, row := range collectInstalledSkills(repoRoot, roots).Skills {
		installed[row.Name] = row
	}
	for _, name := range names {
		row, ok := installed[name]
		if !ok {
			continue
		}
		switch row.Provenance {
		case provenanceLocal:
			where := name
			if len(row.Roots) > 0 {
				where = path.Join(row.Roots[0], name)
			}
			return fmt.Errorf("%q is your own skill, not one ox installed — ox never removes a skill it does not own. Delete %s yourself if you want it gone%s",
				name, where, nothingChangedSuffix(names))
		case provenanceTeam:
			return fmt.Errorf("%q came from your team's Team Context, not from ox's catalog — remove it there and it leaves every repository on the team. Run `ox skills status` to see where it comes from%s",
				name, nothingChangedSuffix(names))
		}
	}
	return nil
}

// publishCatalogSkillsToTeam seeds ox's copy of each skill into the Team Context.
//
// A seed, not a managed install: after this the team owns the copy. That is why
// an existing directory is refused rather than overwritten — a publish that
// clobbered a teammate's edits would destroy the only thing publishing is for.
func publishCatalogSkillsToTeam(repoRoot string, names []string) (skillsChangeOutput, error) {
	out := newSkillsChangeOutput()
	names, err := validateCatalogNames(names)
	if err != nil {
		return out, err
	}

	tc := config.FindRepoTeamContext(repoRoot)
	if tc == nil || tc.Path == "" {
		return out, fmt.Errorf("no Team Context is configured for this project, so there is nowhere to publish — run `ox skills status` to see why")
	}
	out.TeamContext = tc.Path

	// Resolve every catalog input before taking the Team Context lock. On-disk
	// collision checks happen again inside the lock below: two publishers can both
	// observe an absent directory before either has acquired the lock.
	type seed struct {
		name   string
		relDir string
		files  []skills.File
	}
	seeds := make([]seed, 0, len(names))
	for _, name := range names {
		if !teamdocs.ValidTeamSkillName(name) {
			// Defense in depth: the name becomes a directory inside someone else's
			// checkout, and the team side will refuse to install what it cannot name.
			return out, fmt.Errorf("%q is not a name a Team Context can carry", name)
		}
		relDir := path.Join(teamSkillsPublishRoot, name)
		files, readErr := catalogSkillFiles(name)
		if readErr != nil {
			return out, readErr
		}
		if err := refuseUnpublishableDescription(name, files); err != nil {
			return out, err
		}
		seeds = append(seeds, seed{name: name, relDir: relDir, files: files})
	}

	ctx, cancel := context.WithTimeout(context.Background(), teamPublishTimeout)
	defer cancel()

	// ONE critical section, from the first byte written to the commit that records
	// it — the advisory lock every mutating operation on a managed clone takes
	// (ADR-030 D1), including the daemon's own fetch, pull and rebase of this same
	// Team Context.
	//
	// It starts at the WRITES, not at the first `git add`, for two reasons. A
	// daemon rebase landing in the gap would be reconciling a tree already full of
	// untracked files it knows nothing about. And the rollback below has to put
	// the index back under the same lock it was disturbed under — WithRepoLock is
	// not re-entrant, so a rollback that acquired it again would block on itself
	// until the deadline and then leave the mess it was called to clean up.
	acquired := false
	lockErr := gitutil.WithRepoLock(ctx, tc.Path, func() error {
		acquired = true

		// Every write below goes through a handle rooted at the checkout, never
		// through a joined absolute path. A Team Context is a remote-controlled
		// clone: if "agents" or "agents/skills" is a symlink pointing out of the
		// tree, MkdirAll and WriteFile would follow it and deposit the embedded
		// skill outside the checkout entirely — the staging failure afterwards
		// cleans up the wrong place. os.Root refuses to traverse out and is the
		// same defense teamsource.go already applies on the read side.
		root, rootErr := os.OpenRoot(tc.Path)
		if rootErr != nil {
			return fmt.Errorf("open team context: %w", rootErr)
		}
		defer func() { _ = root.Close() }()

		// This check is part of the critical section, not just a pre-flight. If two
		// publishers queued for the same name, the second must see the first one's
		// commit and refuse before writing. Lstat also treats a broken symlink as an
		// existing path instead of following it into a write outside the checkout.
		//
		// The worktree alone is not the answer. A Team Context is a SPARSE checkout,
		// so a skill that is tracked in git can be absent from disk — and then a
		// worktree-only check calls the destination free, and the path-scoped commit
		// below replaces the team's existing skill in history. Ask git too.
		for _, s := range seeds {
			switch _, statErr := root.Lstat(filepath.FromSlash(s.relDir)); {
			case statErr == nil:
				return fmt.Errorf("%q is already published at %s in your Team Context — ox seeds a team copy once and never writes over it. Edit it there, or delete it first%s",
					s.name, s.relDir, nothingChangedSuffix(names))
			case !errors.Is(statErr, fs.ErrNotExist):
				return fmt.Errorf("inspect %s: %w", s.relDir, statErr)
			}
			// Reuses init.go's helper: `ls-files --error-unmatch` over a directory
			// pathspec succeeds when anything under it is tracked, which is exactly
			// the sparse-excluded case os.Lstat cannot see.
			tracked, trackedErr := gitTracksPath(tc.Path, s.relDir)
			if trackedErr != nil {
				return trackedErr
			}
			if tracked {
				return fmt.Errorf("%q is already published at %s in your Team Context — it is tracked in git but not checked out here (a sparse checkout). ox seeds a team copy once and never writes over it%s",
					s.name, s.relDir, nothingChangedSuffix(names))
			}
		}

		// Past this point the Team Context holds bytes no commit carries yet. A
		// seed left behind is not a stray file: the pre-flight above refuses a
		// directory that already exists, so the next attempt is told the skill is
		// "already published" to a team that has never seen it. The deferred
		// rollback, rather than a call at each return, is what makes that true of
		// every failure path below — including ones added later.
		//
		// seededDirs is appended to BEFORE the first byte of each directory is
		// written, so a failure part-way through one seed still takes it back.
		seededDirs := make([]string, 0, len(seeds))
		committed := false
		defer func() {
			if !committed {
				rollbackTeamSeeds(tc.Path, seededDirs)
			}
		}()

		for _, s := range seeds {
			seededDirs = append(seededDirs, s.relDir)
			for _, file := range s.files {
				dest := filepath.Join(filepath.FromSlash(s.relDir), filepath.FromSlash(file.Path))
				if err := root.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
					return fmt.Errorf("create %s: %w", filepath.Dir(dest), err)
				}
				if err := root.WriteFile(dest, file.Content, 0o644); err != nil {
					return fmt.Errorf("write %s: %w", dest, err)
				}
				// Per FILE, matching what the install path reports. A caller diffing the
				// two runs should not have to know that one lists directories.
				out.Written = append(out.Written, path.Join(s.relDir, file.Path))
			}
			out.Skills = append(out.Skills, skillChangeRow{
				Name: s.name, State: skillChangePublished, Detail: s.relDir,
			})
		}

		if err := recordTeamPublish(ctx, tc.Path, seededDirs, names); err != nil {
			return err
		}
		committed = true
		return nil
	})
	if lockErr != nil {
		// Only when fn never ran: an error raised INSIDE it can carry a context
		// deadline of its own, and IsRepoLockBusy cannot tell the two apart. Told
		// "publish failed" for a clone that is merely busy, a human goes looking for
		// damage that is not there; told "it is syncing" when the commit actually
		// failed, they retry forever.
		if !acquired && gitutil.IsRepoLockBusy(lockErr) {
			return out, fmt.Errorf("your Team Context at %s is syncing right now, so ox did not publish into it — try again in a moment", tc.Path)
		}
		return out, lockErr
	}

	out.Guidance = skillsChangeGuidance(out)
	return out, nil
}

// rollbackTeamSeeds puts the Team Context back the way this publish found it:
// the seeded directories gone from disk, and their paths out of the index.
//
// Best effort, and deliberately unable to report failure. It only ever runs on a
// path that already has a real error to tell the human about, and an error about
// the handling of an error is how the cause gets lost. Whatever it cannot undo is
// logged, and nothing else.
//
// Removing each directory whole is both complete and safe, and it is the
// collision check in publishCatalogSkillsToTeam that makes it so: it refuses
// every name whose directory already exists, UNDER THE SAME LOCK, so everything
// on disk underneath a seeded directory was written by this invocation. Move
// that check back outside the lock and this turns into a delete of a peer's
// freshly committed files.
//
// The index is a separate question — a Team Context is a sparse checkout, where a
// path can be in the index and absent from disk — so it is restored from HEAD
// rather than assumed to have been empty.
//
// The caller holds the repository lock; this does not take it (WithRepoLock is
// not re-entrant).
func rollbackTeamSeeds(teamPath string, relDirs []string) {
	if len(relDirs) == 0 {
		return
	}
	for _, rel := range relDirs {
		absDir := filepath.Join(teamPath, filepath.FromSlash(rel))
		if err := os.RemoveAll(absDir); err != nil {
			slog.Warn("team publish rollback incomplete", "step", "remove", "path", absDir, "error", err)
		}
	}

	// A FRESH deadline, not the caller's. An expired context is one of the very
	// failures this rollback exists to clean up after, and reusing it would leave
	// the seed behind exactly when it matters most.
	ctx, cancel := context.WithTimeout(context.Background(), teamPublishTimeout)
	defer cancel()

	// `git reset` needs a commit to restore the index entries FROM. A Team Context
	// that exists but has never been committed into has no HEAD, and `reset HEAD`
	// there is a fatal on older git; every entry under these paths was staged by
	// this invocation anyway, so drop them outright instead.
	//
	// --sparse on that drop is mandatory and is NOT symmetric with the reset:
	// `git rm --cached --ignore-unmatch` without it exits 0 having done NOTHING to
	// a path outside the sparse cone — which is every path this command stages.
	// A rollback that silently succeeds at nothing is worse than no rollback.
	unstage := []string{"reset", "--quiet", "HEAD", "--"}
	if _, err := gitutil.RunGit(ctx, teamPath, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		unstage = []string{"rm", "--cached", "-r", "--quiet", "--ignore-unmatch", "--sparse", "--"}
	}
	if _, err := gitutil.RunGit(ctx, teamPath, append(unstage, relDirs...)...); err != nil {
		slog.Warn("team publish rollback incomplete", "step", "unstage", "team_context", teamPath, "error", err)
	}
}

// recordTeamPublish writes the new files into the Team Context's own history so
// a teammate's next sync sees them. The caller holds the repository lock and owns
// the deadline; this takes neither.
//
// Deliberately does NOT push. Nothing in cmd/ox pushes a team context; the
// daemon owns that leg, and a command that pushed here would fail on every
// machine whose credentials are not loaded — after having already written the
// files, leaving the human with a half-finished publish and a git error.
//
// NOT gitutil.CommitLedgerSnapshot, which looks like the same job: that helper
// carries Ledger blob validation and the ADR-024 sacred-deletion backstop, and
// neither governs a Team Context. Borrowing it would apply one repository kind's
// rules to another's history.
func recordTeamPublish(ctx context.Context, teamPath string, relPaths, names []string) error {
	for _, rel := range relPaths {
		// --sparse covers the Team Context whose cone does NOT reach these paths.
		// Usually it does: manifest.SparseSetFor floors agents/ into the sparse set
		// on every sync tick, and that floor is the whole reason team skills
		// materialize on disk at all (GH #862). An explicit deny of agents/
		// outranks the floor, and there git refuses to stage a path outside the
		// sparse definition — which is what this flag is for.
		//
		// Do NOT read this as "agents/skills is outside the cone." It is not, and
		// the check that refuses an already-published skill reads the worktree on
		// exactly that basis.
		if _, err := gitutil.RunGit(ctx, teamPath, "add", "--sparse", rel); err != nil {
			return fmt.Errorf("record %s in the Team Context: %w", rel, err)
		}
	}
	// Scoped to the paths this invocation staged. A Team Context is a checkout a
	// human also works in, so its index can already hold a change of theirs; a
	// bare `git commit` would carry that into the team's history under a message
	// about a skill, authored by them and pushed by the daemon. `--` keeps a path
	// that happens to look like a revision from being read as one.
	message := "add team skill: " + strings.Join(names, ", ")
	args := append([]string{"commit", "-m", message, "--"}, relPaths...)
	if _, err := gitutil.RunGit(ctx, teamPath, args...); err != nil {
		return fmt.Errorf("record the published skills in the Team Context: %w", err)
	}
	return nil
}

// refuseUnpublishableDescription rejects a skill whose description the Team
// Context parser cannot read.
//
// That parser (internal/teamdocs.parseRuleFrontmatter) has no block-scalar
// support for `description:`: given `description: >-` it stores the marker and
// drops the text on the indented lines below. The skill would then sit in every
// teammate's repository with ">-" as its activation surface — present, installed
// and invisible to every agent, which is worse than not publishing it, because
// nothing anywhere reports a failure.
func refuseUnpublishableDescription(name string, files []skills.File) error {
	for _, file := range files {
		if file.Path != skills.SkillFileName {
			continue
		}
		description, folded := manifestDescription(file.Content)
		switch {
		case folded:
			return fmt.Errorf("%q cannot be published as it stands: its description is a multi-line YAML block, and a Team Context reads `description:` as a single line only. Rewrite it as one line in extensions/skills/%s/%s first",
				name, name, skills.SkillFileName)
		case description == "":
			return fmt.Errorf("%q cannot be published as it stands: a Team Context reads `description:` from the first 30 lines of frontmatter and found none. Add one to extensions/skills/%s/%s first",
				name, name, skills.SkillFileName)
		}
		return nil
	}
	return fmt.Errorf("the catalog entry for %q has no %s", name, skills.SkillFileName)
}

// catalogSkillFiles reads one skill's complete directory out of the embedded
// source tree — the manifest plus references/, assets/ and scripts/.
func catalogSkillFiles(name string) ([]skills.File, error) {
	var files []skills.File
	err := fs.WalkDir(skills.FS, name, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		content, readErr := fs.ReadFile(skills.FS, p)
		if readErr != nil {
			return readErr
		}
		files = append(files, skills.File{Path: strings.TrimPrefix(p, name+"/"), Content: content})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read the catalog entry for %q: %w", name, err)
	}
	return files, nil
}

// validateCatalogNames refuses the WHOLE run on the first name ox does not ship,
// before anything is written, and returns the names deduplicated.
//
// All-or-nothing matches `ox skills approve`: a typo in the last name must not
// leave the earlier ones applied with no record of which. Deduplication matters
// for the same reason it does there — `install x x` reporting one row as
// installed and a second as already-installed reads as two skills, or as a race.
func validateCatalogNames(names []string) ([]string, error) {
	seen := make(map[string]bool, len(names))
	unique := make([]string, 0, len(names))
	for _, name := range names {
		switch {
		case skills.IsRetired(name):
			// Checked before IsKnown: a retired name was correct in a previous
			// release, and reporting it as merely unknown sends someone hunting for a
			// typo that is not there.
			return nil, fmt.Errorf("ox no longer ships %q — it was retired%s", name, nothingChangedSuffix(names))
		case !skills.IsKnown(name):
			return nil, fmt.Errorf("ox ships no skill named %q — run `ox skills catalog` to see what it does ship%s", name, nothingChangedSuffix(names))
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		unique = append(unique, name)
	}
	return unique, nil
}

// orEmptyStrings keeps a nil slice out of the wire contract. The reconcile plan
// returns nil for "no files", and json.Marshal renders that as null — which a
// reader cannot tell apart from a key this ox does not populate.
func orEmptyStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// nothingChangedSuffix says the run was all-or-nothing, but only when more than
// one name was given — that is the only case where a reader could reasonably
// wonder whether the earlier names took effect.
func nothingChangedSuffix(names []string) string {
	if len(names) < 2 {
		return ""
	}
	return "; nothing was changed"
}

// selectedCatalogNames expands this repository's committed selection into the
// skill names it resolves to, bundles included.
func selectedCatalogNames(desired skillmanager.DesiredSkills) (map[string]bool, error) {
	// Retired selections first: a lockfile can still carry the withdrawn `attest`
	// bundle, and BundleNames would reject the whole expansion over a selection
	// reconcile itself silently drops.
	desired, _ = skillmanager.RemoveRetiredSelections(desired)
	ids := make([]string, 0, len(desired.Bundles))
	for _, bundle := range desired.Bundles {
		ids = append(ids, bundle.ID)
	}
	names, err := skills.BundleNames(ids)
	if err != nil {
		return nil, err
	}
	selected := make(map[string]bool, len(names)+len(desired.Names))
	for _, name := range names {
		selected[name] = true
	}
	for _, name := range desired.Names {
		selected[name] = true
	}
	return selected, nil
}

// bundleSelecting names the selected bundle that carries a skill, or "" when the
// skill is not reached by any bundle this repository selected.
func bundleSelecting(desired skillmanager.DesiredSkills, name string) string {
	selected := make(map[string]bool, len(desired.Bundles))
	for _, bundle := range desired.Bundles {
		selected[bundle.ID] = true
	}
	for _, bundle := range skills.Catalog {
		if selected[bundle.ID] && slices.Contains(bundle.SkillIDs, name) {
			return bundle.ID
		}
	}
	return ""
}

// skillsChangeGuidance is the single next action, carried in the JSON so an AI
// coworker reading this gets the same answer a human reads off the terminal.
//
// Every branch has to be true of the run that produced it: a line that says a
// skill is installed, printed after a run that installed nothing, teaches a
// reader that the line means nothing.
func skillsChangeGuidance(out skillsChangeOutput) string {
	var installed, published, removed, unchanged []string
	for _, row := range out.Skills {
		switch row.State {
		case skillChangeInstalled:
			installed = append(installed, row.Name)
		case skillChangePublished:
			published = append(published, row.Name)
		case skillChangeUninstalled:
			removed = append(removed, row.Name)
		default:
			unchanged = append(unchanged, row.Name)
		}
	}
	switch {
	case len(published) > 0:
		// No mention of where it landed beyond the Team Context: the path is on the
		// row, and the actionable fact is that the team now owns the copy.
		return fmt.Sprintf("%s published to your Team Context — it is your team's to edit now, and reaches every repository on the team. Run `ox skills status` there to watch it arrive.",
			strings.Join(published, ", "))
	case len(installed) > 0:
		return fmt.Sprintf("%s installed — your AI coworkers can use %s now. The choice is recorded in %s, which travels with the repository, so your coworkers get the same skills. Run `ox skills list` to see everything installed here.",
			strings.Join(installed, ", "), itOrThem(len(installed)), lockfileDisplayPath())
	case len(removed) > 0:
		return fmt.Sprintf("%s removed from this repository. Run `ox skills catalog` to see what ox ships.", strings.Join(removed, ", "))
	case len(unchanged) > 0:
		return "Nothing changed. Run `ox skills list` to see what is already installed here."
	}
	return "Nothing to do. Run `ox skills catalog` to see what ox ships."
}

// lockfileDisplayPath is the repository-relative lockfile path, derived from the
// manager's own constant rather than retyped, so the text a human reads and the
// file ox actually writes cannot drift apart.
func lockfileDisplayPath() string {
	return filepath.ToSlash(skillmanager.LockPath(""))
}

func itOrThem(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// changeStateColumn is the width of the widest state word ("uninstalled"), so a
// run that both installs and removes still reads as two aligned columns.
const changeStateColumn = 11

func padChangeState(state string) string {
	if pad := changeStateColumn - len([]rune(state)); pad > 0 {
		return state + fmt.Sprintf("%*s", pad, "")
	}
	return state
}

func emitSkillsChange(w io.Writer, out skillsChangeOutput, asJSON bool) error {
	if asJSON {
		return encodeSkillsJSON(w, out)
	}
	p := func(format string, args ...any) { fmt.Fprintf(w, format+"\n", args...) }

	for _, row := range out.Skills {
		switch row.State {
		case skillChangeInstalled, skillChangePublished, skillChangeUninstalled:
			p("%s %s", cli.StyleSuccess.Render(padChangeState(row.State)), row.Name)
			if row.Detail != "" {
				p("  into        %s", row.Detail)
			}
		default:
			detail := row.Detail
			if detail == "" && row.Bundle != "" {
				detail = "selected by the " + row.Bundle + " bundle"
			}
			p("%-*s %s", changeStateColumn, "unchanged", row.Name)
			if detail != "" {
				p("  %s", detail)
			}
		}
	}

	if out.Guidance != "" {
		if len(out.Skills) > 0 {
			p("")
		}
		writeWrapped(w, "", "", out.Guidance)
	}
	return nil
}
