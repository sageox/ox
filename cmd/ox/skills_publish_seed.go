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
	"strings"
	"time"

	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/skillmanager"
)

// skills_publish_seed.go — the Team Context write path shared by `ox skills
// publish`.
//
// Publishing seeds a copy of a skill into the Team Context and then STOPS
// owning it: the team edits it, the team's history carries it, and ox never
// writes over it again. That is deliberately not how a managed install behaves,
// because overwriting would discard a teammate's edits on every publish.
//
// This file is the one implementation of the collision, locking, rollback, and
// commit rules for that checkout — the same checkout the daemon mutates — so a
// second caller cannot invent a second set.
//
// It previously also carried `ox skills install` / `ox skills uninstall`, the
// repository-scoped half of the catalog surface. Those are gone: catalog
// content is chosen once for a team, never per repository, and the replacement
// is `ox packs` writing into Team Context (ADR-032). Nothing here selects
// catalog content.

const skillChangePublished = "published"

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

// skillChangeRow is one name's outcome, in both renderings.
type skillChangeRow struct {
	Name   string `json:"name"`
	State  string `json:"state"`
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

// teamSkillSeed is one complete skill ready to copy into the canonical Team
// Context root. Catalog publishing and repository-skill publishing deliberately
// converge here: there must be one collision, locking, rollback, and commit
// implementation for the checkout the daemon also mutates.
type teamSkillSeed struct {
	name   string
	relDir string
	files  []skills.File
}

func newSkillsChangeOutput() skillsChangeOutput {
	return skillsChangeOutput{Skills: []skillChangeRow{}, Written: []string{}, Removed: []string{}}
}

// publishTeamSkillSeeds performs the Team Context transaction shared by every
// publishing surface. Callers must fully resolve and validate their sources
// first, so a bad final name cannot leave an earlier name partially published.
func publishTeamSkillSeeds(repoRoot string, names []string, seeds []teamSkillSeed) (skillsChangeOutput, error) {
	out := newSkillsChangeOutput()
	tc := config.FindRepoTeamContext(repoRoot)
	if tc == nil || tc.Path == "" {
		return out, fmt.Errorf("no Team Context is configured for this project, so there is nowhere to publish — run `ox skills status` to see why")
	}
	out.TeamContext = tc.Path

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

// skillsChangeGuidance is the single next action, carried in the JSON so an AI
// coworker reading this gets the same answer a human reads off the terminal.
//
// Every branch has to be true of the run that produced it: a line that says a
// skill is installed, printed after a run that installed nothing, teaches a
// reader that the line means nothing.
func skillsChangeGuidance(out skillsChangeOutput) string {
	var published, unchanged []string
	for _, row := range out.Skills {
		if row.State == skillChangePublished {
			published = append(published, row.Name)
			continue
		}
		unchanged = append(unchanged, row.Name)
	}
	switch {
	case len(published) > 0:
		// No mention of where it landed beyond the Team Context: the path is on the
		// row, and the actionable facts are that the team now owns the copy and
		// targeting remains explicit in the manifest.
		return fmt.Sprintf("%s published to your Team Context — it is your team's to edit now. Its `repos:` metadata controls where it arrives; without `repos:` it reaches every repository on the team. Distribution is automatic; run `ox sync` in this repository only if you need it immediately, then `ox skills status` to verify it.",
			strings.Join(published, ", "))
	case len(unchanged) > 0:
		return "Nothing changed. Run `ox skills list` to see what is already available here."
	}
	return "Nothing to do. Run `ox skills list` to see what is available here."
}

// changeStateColumn is the width of the widest state word ("published"), so a
// multi-name run still reads as two aligned columns.
const changeStateColumn = 9

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
		case skillChangePublished:
			p("%s %s", cli.StyleSuccess.Render(padChangeState(row.State)), row.Name)
			if row.Detail != "" {
				p("  into        %s", row.Detail)
			}
		default:
			detail := row.Detail
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
