package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/adapterstamp"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/teamdocs"
	"github.com/spf13/cobra"
)

// skills_publish.go — `ox skills publish`.
//
// This is the authoring bridge between a skill a coworker has proved in one
// repository and the team's shared source. It intentionally does not create a
// second kind of repository-scoped install: it copies into the Team Context,
// and normal `ox sync` convergence distributes that team-owned copy.
//
// Publishing seeds a copy of a skill into the Team Context and then STOPS
// owning it: the team edits it, the team's history carries it, and ox never
// writes over it again. Overwriting would discard a teammate's edits on every
// publish.
//
// The Team Context write below — the collision check, the lock, the rollback,
// and the scoped commit — is the one implementation of those rules for the
// checkout the daemon also mutates, so a second caller can never invent a
// second set.

var skillsPublishCmd = &cobra.Command{
	Use:   "publish <name>...",
	Short: "Publish repository skills to your Team Context",
	Long: `Publish hand-authored skills from this repository to your Team Context.

Each name is resolved from the skills directories selected by ` + "`ox init`" + `. If
the same name exists in more than one selected directory, every copy must be
identical. ox refuses skills it manages, symlinks, unsafe or mismatched names,
and any destination the team already owns.

Publishing copies the complete skill directory and records it in the Team
Context. It does not remove the repository copy and never overwrites a published
skill. The skill's existing ` + "`repos:`" + ` metadata controls which repositories
receive it; without ` + "`repos:`" + ` it applies to every repository on the team.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runSkillsPublish,
}

func init() {
	skillsPublishCmd.Flags().Bool("json", false, "Emit machine-readable JSON")
	skillsCmd.AddCommand(skillsPublishCmd)
}

func runSkillsPublish(cmd *cobra.Command, args []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return fmt.Errorf("not inside a git repository")
	}
	out, err := publishRepoSkillsToTeam(gitRoot, args)
	if err != nil {
		return err
	}
	return emitSkillsChange(cmd.OutOrStdout(), out, asJSON)
}

// publishRepoSkillsToTeam resolves every source before the shared Team Context
// transaction starts. That keeps a multi-name publish all-or-nothing even when
// the final name is missing, malformed, or differs between selected roots.
func publishRepoSkillsToTeam(repoRoot string, names []string) (skillsChangeOutput, error) {
	out := newSkillsChangeOutput()
	names, err := validatePublishNames(names)
	if err != nil {
		return out, err
	}
	roots, err := skillTargetRoots(repoRoot)
	if err != nil {
		return out, err
	}
	if len(roots) == 0 {
		return out, fmt.Errorf("this repository has not selected an AI coworker, so there is no skills directory to publish from — run `ox init`")
	}
	desired, _, err := skillmanager.LoadDesired(repoRoot)
	if err != nil {
		return out, err
	}
	selected, err := selectedCatalogNames(desired)
	if err != nil {
		return out, err
	}
	for _, name := range names {
		if selected[name] {
			return out, fmt.Errorf("%q is managed by ox in this repository and cannot be published as a hand-authored team skill%s",
				name, allOrNothingSuffix(names, "changed"))
		}
	}

	repo, err := os.OpenRoot(repoRoot)
	if err != nil {
		return out, fmt.Errorf("open repository: %w", err)
	}
	defer func() { _ = repo.Close() }()

	seeds := make([]teamSkillSeed, 0, len(names))
	for _, name := range names {
		files, foundRoots, readErr := findPublishSkill(repo, roots, name)
		if readErr != nil {
			return out, readErr
		}
		if len(foundRoots) == 0 {
			return out, fmt.Errorf("%q is not installed in this repository's selected skills directories (%s)%s",
				name, strings.Join(roots, ", "), allOrNothingSuffix(names, "changed"))
		}
		if err := validatePublishManifest(name, files); err != nil {
			return out, fmt.Errorf("cannot publish %q from %s: %w%s",
				name, strings.Join(foundRoots, ", "), err, allOrNothingSuffix(names, "changed"))
		}
		seeds = append(seeds, teamSkillSeed{
			name: name, relDir: path.Join(teamSkillsPublishRoot, name), files: files,
		})
	}
	published, err := publishTeamSkillSeeds(repoRoot, names, seeds)
	if err != nil {
		return published, err
	}
	published.Guidance = fmt.Sprintf("%s published to your Team Context without changing the repository copies. Their `repos:` metadata controls where they arrive; without `repos:` they reach every team repository. Distribution is automatic; run `ox sync` only if you need it immediately, verify with `ox skills status`, then remove any redundant repository copy.",
		strings.Join(names, ", "))
	return published, nil
}

func validatePublishNames(names []string) ([]string, error) {
	unique := dedupeNames(names)
	for _, name := range unique {
		if !teamdocs.ValidTeamSkillName(name) {
			return nil, fmt.Errorf("%q is not a safe team skill name; use lowercase letters, digits, dots, underscores, and hyphens, with no `..`%s",
				name, allOrNothingSuffix(names, "changed"))
		}
		// Two different refusals wear the same predicate, and saying so matters:
		// `ox-cli-plan` is ox's file, while `onboard-team` is the author's own
		// skill whose NAME is spoken for. Telling someone their skill "is managed
		// by ox" when it is not sends them looking for a conflict that isn't there.
		if strings.HasSuffix(name, skillmanager.TeamSuffix) {
			return nil, fmt.Errorf("%q already ends in %q, which is how ox names the copy it installs from your Team Context — rename it before publishing%s",
				name, skillmanager.TeamSuffix, allOrNothingSuffix(names, "changed"))
		}
		if skillmanager.IsReservedName(name) || name == skillmanager.CommittedOnRamp {
			return nil, fmt.Errorf("%q is managed by ox and cannot be published as a hand-authored team skill%s",
				name, allOrNothingSuffix(names, "changed"))
		}
	}
	return unique, nil
}

// findPublishSkill reads every selected copy of name. Multiple agent targets
// often share one skill semantically; accepting divergent bytes would make the
// selected root order decide what the whole team receives.
func findPublishSkill(repo *os.Root, roots []string, name string) ([]skills.File, []string, error) {
	var canonical []skills.File
	var found []string
	for _, root := range dedupeNames(roots) {
		relDir := path.Join(filepath.ToSlash(root), name)
		dir, err := openPublishSourceDir(repo, relDir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("inspect %s: %w", relDir, err)
		}
		files, err := readPublishSourceFiles(dir)
		_ = dir.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("read %s: %w", relDir, err)
		}
		if len(found) > 0 && !sameSkillFiles(canonical, files) {
			return nil, nil, fmt.Errorf("%q has different copies in %s and %s; make them identical before publishing",
				name, found[0], root)
		}
		canonical = files
		found = append(found, root)
	}
	return canonical, found, nil
}

// openPublishSourceDir pins each path component to a real directory. A plain
// os.OpenRoot(repo).OpenRoot(relative) prevents an escape outside the repository
// but still follows a link to some other directory *inside* it; publishing that
// would copy bytes outside the skill the coworker named.
func openPublishSourceDir(repo *os.Root, relative string) (*os.Root, error) {
	clean := filepath.Clean(filepath.FromSlash(relative))
	if filepath.IsAbs(clean) || filepath.VolumeName(clean) != "" || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("path escapes repository")
	}
	current, err := repo.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		info, err := current.Lstat(part)
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		if !info.IsDir() {
			_ = current.Close()
			return nil, fmt.Errorf("refusing symlink or non-directory path component %s", part)
		}
		child, err := current.OpenRoot(part)
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		actual, err := child.Stat(".")
		if err != nil || !os.SameFile(info, actual) {
			_ = child.Close()
			_ = current.Close()
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("repository directory changed while opening %s", part)
		}
		_ = current.Close()
		current = child
	}
	return current, nil
}

func readPublishSourceFiles(dir *os.Root) ([]skills.File, error) {
	var files []skills.File
	err := fs.WalkDir(dir.FS(), ".", func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if filePath == "." || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("refusing symlink or non-regular file %s", filepath.ToSlash(filePath))
		}
		info, err := dir.Lstat(filePath)
		if err != nil {
			return err
		}
		file, err := dir.Open(filePath)
		if err != nil {
			return err
		}
		actual, statErr := file.Stat()
		if statErr != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) {
			_ = file.Close()
			if statErr != nil {
				return statErr
			}
			return fmt.Errorf("refusing file that changed while reading %s", filepath.ToSlash(filePath))
		}
		content, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		files = append(files, skills.File{
			Path: strings.TrimPrefix(filepath.ToSlash(filePath), "./"), Content: content,
		})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, err
}

func sameSkillFiles(a, b []skills.File) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Path != b[i].Path || !bytes.Equal(a[i].Content, b[i].Content) {
			return false
		}
	}
	return true
}

func validatePublishManifest(name string, files []skills.File) error {
	var manifest []byte
	for _, file := range files {
		if file.Path == skills.SkillFileName {
			manifest = file.Content
			break
		}
	}
	if manifest == nil {
		return fmt.Errorf("the skill has no %s", skills.SkillFileName)
	}
	if adapterstamp.StampVerifies(manifest, "ox") {
		return fmt.Errorf("the skill is managed by ox, not hand-authored here")
	}
	gotName, _ := publishManifestField(manifest, "name")
	if gotName != name {
		return fmt.Errorf("frontmatter name is %q, but the skill directory is %q", gotName, name)
	}
	description, folded := manifestDescription(manifest)
	switch {
	case folded:
		return fmt.Errorf("description is a multi-line YAML block; rewrite it as one line so Team Context readers preserve it")
	case description == "":
		return fmt.Errorf("frontmatter has no single-line description in its first 30 lines")
	}
	if rawRepos, present := publishManifestField(manifest, "repos"); present {
		// Team Context intentionally accepts only an inline list or one bare slug.
		// A block list (`repos:` followed by `- owner/repo`) parses as absent and
		// therefore means ALL repositories. Refuse that silent scope widening at
		// the publishing boundary.
		if rawRepos == "" {
			return fmt.Errorf("repos: must use an inline list such as [\"owner/repo\"] (use [] explicitly for every team repository)")
		}
		if strings.HasPrefix(rawRepos, "[") != strings.HasSuffix(rawRepos, "]") {
			return fmt.Errorf("repos: has an incomplete inline list; use [\"owner/repo\"]")
		}
	}
	if audience, _ := publishManifestField(manifest, "audience"); audience == teamdocs.RuleAudienceHuman {
		return fmt.Errorf("audience: human prevents AI coworkers from receiving this skill")
	}
	if visibility, _ := publishManifestField(manifest, "visibility"); visibility == teamdocs.VisibilityHidden {
		return fmt.Errorf("visibility: hidden prevents this skill from being published")
	}
	if status, _ := publishManifestField(manifest, "status"); status == teamdocs.RuleStatusDraft ||
		strings.HasPrefix(status, teamdocs.RuleStatusSupersededPrefix) {
		return fmt.Errorf("status: %s prevents this skill from being published", status)
	}
	return nil
}

// publishManifestField mirrors the Team Context's deliberately-small
// frontmatter reader: unindented single-line fields in the first 30 lines.
func publishManifestField(content []byte, key string) (string, bool) {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	prefix := key + ":"
	for line := 0; scanner.Scan() && line < 30; line++ {
		text := strings.TrimRight(scanner.Text(), "\r")
		if line == 0 {
			if text != "---" {
				return "", false
			}
			continue
		}
		if text == "---" {
			return "", false
		}
		if value, ok := strings.CutPrefix(text, prefix); ok {
			value = strings.TrimSpace(value)
			if comment := strings.Index(value, " #"); comment >= 0 {
				value = value[:comment]
			}
			return strings.Trim(strings.TrimSpace(value), `"'`), true
		}
	}
	return "", false
}

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

// skillsChangeOutput is the shape `ox skills publish` emits.
//
// Every array is present and `[]` when empty — a key that appears only
// sometimes forces the reader to guess whether its absence means "none" or
// "this ox does not report that."
type skillsChangeOutput struct {
	Skills  []skillChangeRow `json:"skills"`
	Written []string         `json:"written"`
	// TeamContext is the checkout this publish wrote into, and empty otherwise.
	// Always present, so a reader can tell "published nowhere" from "this ox
	// does not report where."
	TeamContext string `json:"team_context"`
	Guidance    string `json:"guidance"`
}

// teamSkillSeed is one complete skill ready to copy into the canonical Team
// Context root.
type teamSkillSeed struct {
	name   string
	relDir string
	files  []skills.File
}

func newSkillsChangeOutput() skillsChangeOutput {
	return skillsChangeOutput{Skills: []skillChangeRow{}, Written: []string{}}
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
					s.name, s.relDir, allOrNothingSuffix(names, "changed"))
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
					s.name, s.relDir, allOrNothingSuffix(names, "changed"))
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
				// Per FILE: a caller comparing two publish runs needs each written
				// path, not just the directories that changed.
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
// collision check in publishTeamSkillSeeds that makes it so: it refuses every
// name whose directory already exists, UNDER THE SAME LOCK, so everything on
// disk underneath a seeded directory was written by this invocation. Move
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

func emitSkillsChange(w io.Writer, out skillsChangeOutput, asJSON bool) error {
	if asJSON {
		return encodeSkillsJSON(w, out)
	}
	p := skillsPrintf(w)

	for _, row := range out.Skills {
		p("%s %s", cli.StyleSuccess.Render(row.State), row.Name)
		if row.Detail != "" {
			p("  into        %s", row.Detail)
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
