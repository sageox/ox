package skillmanager

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	skills "github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/repotools"
	"github.com/sageox/ox/internal/teamdocs"
	"github.com/sageox/ox/internal/teamskills"
)

// TeamSkillDecision records what happened to one discovered team skill, so a
// caller can tell a human WHY a skill they authored is not on disk.
//
// Silence is the failure mode this exists to prevent: a skill held for approval
// and a skill that was never discovered look identical from the repository.
type TeamSkillDecision struct {
	Name         string
	InstalledAs  string
	NeedsApprove bool
	Reason       string
}

// teamCatalog unions the binary's built-in catalog with the team skills this
// project is allowed to materialize.
//
// Team skills enter through the SAME seam as built-in ones, so they inherit the
// whole ownership model already proven for the CLI inventory: digest-tracked
// files, unconditional overwrite inside the reserved prefix, removal when they
// leave desired state. The alternative — a second installer beside the first —
// is exactly the two-mechanism split that let five orphaned command files
// survive across releases.
//
// They are COPIES, never links. The team checkout is mutable under the daemon (a
// sparse refresh deletes tracked files), and a background `git pull` must never
// silently change what a coding agent executes.
type teamCatalog struct {
	base      catalogSource
	teamFiles []skills.Skill
	teamPath  string
	// incomplete is non-empty when this source could NOT see the team's skills,
	// as opposed to seeing that the team publishes none. The planner reads it to
	// decide whether an absent skill means "retire it" or "we are blind right now".
	incomplete string
}

// TeamSkillSource builds a catalog source that adds approved team skills.
//
// It reverses the pointer-rule decision recorded in the Claude adapter — "Team
// rules stay where the team writes them. No sync. No cleanup." — and does so
// deliberately: that ruling predates the reserved-prefix inventory, which is what
// makes cleanup safe. Saying so openly because the comment it contradicts is
// still in the tree.
//
// teamPath is the team-context checkout. It is READ ONLY here: no git handle is
// opened, so this cannot race the daemon's pull. Callers that need the checkout
// to be current must take ADR-030's per-clone lease around their own refresh
// before calling.
func TeamSkillSource(base catalogSource, teamPath, repoSlug, projectRoot string) (catalogSource, []TeamSkillDecision, error) {
	if base == nil {
		base = builtInCatalog{}
	}
	if reason := unseeableTeamSkills(teamPath, repoSlug); reason != "" {
		// Deliberately still a teamCatalog, not the bare base. Returning base here
		// was the original shape, and it is the bug: base reports an authoritative
		// empty team half, so the planner retires every sageox-team-* file already
		// on disk. A project whose daemon has not finished its first clone would
		// have its team playbooks deleted for the crime of being early.
		return &teamCatalog{base: base, teamPath: teamPath, incomplete: reason}, nil, nil
	}

	discovered, rejected, err := teamdocs.DiscoverSkillsWithRejections(teamPath, repoSlug)
	if err != nil {
		return nil, nil, fmt.Errorf("discover team skills: %w", err)
	}

	// Refusals are carried BEFORE the empty check and never gated on what else was
	// found. A team whose only skill has an unusable name would otherwise get the
	// silent empty result this decision list exists to prevent — the author would
	// see their skill simply not appear, with nothing anywhere saying it was read.
	// InstalledAs stays empty and NeedsApprove stays false: `ox skills approve`
	// cannot help here, and sending the human there is worse than saying nothing.
	var decisions []TeamSkillDecision
	for _, r := range rejected {
		decisions = append(decisions, TeamSkillDecision{Name: r.Name, Reason: r.NameError})
	}
	if len(discovered) == 0 {
		return base, decisions, nil
	}

	approvals, err := teamskills.LoadApprovals(projectRoot)
	if err != nil {
		// A store ox cannot read is NOT an empty store. Materializing executable
		// content because the approval file was unparseable is the fail-open shape
		// this codebase keeps finding; refuse and let the human see it.
		return nil, nil, fmt.Errorf("team skill approvals unreadable, refusing to materialize: %w", err)
	}

	var allowed []skills.Skill
	for _, ts := range discovered {
		loaded, loadErr := loadTeamSkill(ts)
		if loadErr != nil {
			decisions = append(decisions, TeamSkillDecision{
				Name: ts.Name, Reason: "unreadable: " + loadErr.Error(),
			})
			continue
		}
		verdict := teamskills.Classify(loaded)
		manifestNeedsApproval := approvals.Decide(ts.Name, verdict) == teamskills.DecisionNeedsApproval
		scriptsNeedApproval := verdictHasCapability(verdict, teamskills.CapBundledScript) &&
			!approvals.ScriptsExecutable(ts.Name, verdict)

		// The boundary is the FILE, not the skill. An unapproved script is dropped
		// before it reaches disk and the prose installs anyway — an agent invited to
		// `sh` a file that is not there does nothing. Withholding the whole skill
		// gated the wrong thing: a script is the AUDITABLE form of risk, while prose
		// saying "run curl … | sh" materializes with no gate at all, and so do team
		// rules. Blocking the readable form and admitting the illegible one kept
		// roughly a third of real skills off every machine for no safety gained.
		//
		// The one case that still withholds is a manifest that is itself the
		// runnable thing — an allowed-tools: grant or an inline command lives IN
		// SKILL.md and cannot be dropped without rewriting the team's file.
		if manifestNeedsApproval && manifestIsRunnable(loaded, verdict) {
			decisions = append(decisions, TeamSkillDecision{
				Name: ts.Name, NeedsApprove: true,
				Reason: "withheld, the manifest itself needs approval: " + verdict.Describe(),
			})
			continue
		}

		installed := TeamPrefix + ts.Name
		allowed = append(allowed, skills.Skill{
			Name:    installed,
			Content: manifestContent(loaded),
			Files:   toCatalogFiles(loaded, approvals.ScriptsExecutable(ts.Name, verdict)),
		})
		decision := TeamSkillDecision{Name: ts.Name, InstalledAs: installed}
		if scriptsNeedApproval {
			// Installed, minus its scripts. Still surfaced: the author expects the
			// scripts to be there, and silence would read as "it all arrived."
			decision.NeedsApprove = true
			decision.Reason = "installed without its scripts pending approval: " + verdict.Describe()
		}
		decisions = append(decisions, decision)
	}

	sort.Slice(allowed, func(i, j int) bool { return allowed[i].Name < allowed[j].Name })
	return &teamCatalog{base: base, teamFiles: allowed, teamPath: teamPath}, decisions, nil
}

func verdictHasCapability(v teamskills.Verdict, want teamskills.Capability) bool {
	for _, capability := range v.Capabilities {
		if capability == want {
			return true
		}
	}
	return false
}

func (c *teamCatalog) Digest() (string, error) {
	base, err := c.base.Digest()
	if err != nil {
		return "", err
	}
	// Keyed on the checkout's commit, NOT on a hash of the skills it contains.
	// Prime compares this against the recorded revision to decide whether to
	// re-plan at all, and it must be able to compute the same value WITHOUT
	// walking the team tree — otherwise the cheap path costs exactly what it was
	// added to avoid, on every session start, in every repo with a team context.
	//
	// The trade is that an UNCOMMITTED edit inside the checkout does not move this
	// value. That is the right trade: the checkout is daemon-managed, the daemon
	// reconciles off the pull that lands a commit, and the 30-minute drift check
	// is the floor under anything else.
	return base + "+team:" + teamRevision(c.teamPath), nil
}

func (c *teamCatalog) Select(version string, desired DesiredSkills) ([]skills.Skill, error) {
	base, err := c.base.Select(version, desired)
	if err != nil {
		return nil, err
	}
	out := make([]skills.Skill, 0, len(base)+len(c.teamFiles))
	out = append(out, base...)
	for _, s := range c.teamFiles {
		s.Version = version
		out = append(out, s)
	}
	return out, nil
}

// loadTeamSkill reads a discovered skill's bytes for classification.
//
// Reads go through an *os.Root anchored at the skill directory, NOT through
// os.ReadFile on a rejoined path. Discovery skips symlinks, but the team checkout
// is mutable under the daemon: a sparse refresh or another writer can replace an
// entry between the walk and the read. A path-based read would then follow the
// replacement and pull bytes from an arbitrary location on the machine into the
// catalog — content that is then materialized into the customer's repository.
// Root-relative reads resolve every component against the held directory, so a
// symlink swapped in afterwards cannot escape it.
func loadTeamSkill(ts teamdocs.TeamSkill) (teamskills.Skill, error) {
	root, err := os.OpenRoot(ts.AbsDir)
	if err != nil {
		return teamskills.Skill{}, fmt.Errorf("open team skill dir: %w", err)
	}
	defer func() { _ = root.Close() }()

	out := teamskills.Skill{Name: ts.Name}
	for _, rel := range ts.Files {
		// Re-check through the held root: a regular file at walk time may be a
		// symlink now, and Root.ReadFile would happily read a link that stays
		// inside the root.
		info, statErr := root.Lstat(rel)
		if statErr != nil {
			return teamskills.Skill{}, fmt.Errorf("inspect %s: %w", rel, statErr)
		}
		if !info.Mode().IsRegular() {
			return teamskills.Skill{}, fmt.Errorf("refusing non-regular team skill file %s", rel)
		}
		data, readErr := root.ReadFile(rel)
		if readErr != nil {
			return teamskills.Skill{}, readErr
		}
		out.Files = append(out.Files, teamskills.File{Path: rel, Content: data})
	}
	return out, nil
}

// manifestIsRunnable reports whether SKILL.md itself carries a capability —
// as opposed to the skill merely bundling script files beside it.
//
// Bundled scripts are droppable one file at a time, so the prose can install
// without them. A grant or command embedded in the manifest is not: the only
// way to remove it is to rewrite the team's file, which ox does not do.
func manifestIsRunnable(s teamskills.Skill, v teamskills.Verdict) bool {
	for _, c := range v.Capabilities {
		if c != teamskills.CapBundledScript {
			return true
		}
	}
	for _, f := range s.Files {
		if strings.EqualFold(f.Path, skills.SkillFileName) {
			runnable, _ := teamskills.IsExecutableFile(f.Path, f.Content)
			return runnable
		}
	}
	return false
}

func manifestContent(s teamskills.Skill) []byte {
	for _, f := range s.Files {
		if strings.EqualFold(f.Path, skills.SkillFileName) {
			return f.Content
		}
	}
	return nil
}

// toCatalogFiles converts a team skill's files for materialization.
//
// Runnable files are dropped entirely unless the approver explicitly allowed
// them. Writing them non-executable would still put runnable content on disk that
// an agent is invited to `sh` — the file's PRESENCE is the boundary, not its mode.
//
// It uses teamskills.IsExecutableFile, the same predicate that classified the
// skill. Two definitions of "runnable" is how `bin/deploy.sh` came to be
// classified as prose while a root-level `scripts/` check filtered nothing.
func toCatalogFiles(s teamskills.Skill, allowScripts bool) []skills.File {
	var out []skills.File
	for _, f := range s.Files {
		clean := filepath.ToSlash(filepath.Clean(f.Path))
		// A runnable manifest reaches this point only after its digest-pinned
		// manifest approval. --allow-scripts governs additional executable files,
		// not whether that already-approved SKILL.md is silently dropped.
		if !allowScripts && !strings.EqualFold(clean, skills.SkillFileName) {
			if executable, _ := teamskills.IsExecutableFile(clean, f.Content); executable {
				continue
			}
		}
		out = append(out, skills.File{Path: clean, Content: f.Content})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// teamRevision is a cheap signal that changes when the team checkout's committed
// content could have changed: its resolved HEAD commit.
//
// Two file reads and no subprocess, because this runs on the prime hot path.
// An unreadable or absent checkout yields "", which simply means "no team
// component" — the same value a project with no team context produces, so the
// comparison stays well-defined rather than erroring on the fast path.
func teamRevision(teamPath string) string {
	if teamPath == "" {
		return ""
	}
	head, err := os.ReadFile(filepath.Join(teamPath, ".git", "HEAD"))
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(head))
	ref, isSymbolic := strings.CutPrefix(line, "ref: ")
	if !isSymbolic {
		return line // detached HEAD already holds the sha
	}
	sha, err := os.ReadFile(filepath.Join(teamPath, ".git", filepath.FromSlash(ref)))
	if err != nil {
		// Packed refs, or a ref this process cannot read. Returning "" re-plans
		// every time, which is slow but never wrong; guessing would be the reverse.
		return ""
	}
	return strings.TrimSpace(string(sha))
}

// anySkillRootOnDisk reports whether any directory discovery would walk exists.
// Keyed on teamdocs.SkillRoots, the same list DiscoverSkills uses, so a root can
// never be walked by one and ignored by the other.
func anySkillRootOnDisk(teamPath string) bool {
	for _, root := range teamdocs.SkillRoots {
		// The parent is what the sparse set includes ("agents/"), so a materialized
		// parent with no skills yet is present, not blind.
		parent := filepath.Dir(filepath.FromSlash(root))
		// IsDir, not merely "exists": a regular FILE named agents/ makes
		// os.ReadDir on agents/skills fail, so discovery is not authoritative
		// there either. Accepting it would report the checkout healthy while
		// every skill silently failed to load.
		if info, err := os.Stat(filepath.Join(teamPath, parent)); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// ExpectedRevision computes what Plan would record for this repo, without
// walking the catalog or the team checkout.
//
// It exists so the prime fast path and the planner cannot disagree. Prime used
// to compare against the BUILT-IN digest alone while the planner recorded the
// built-in digest plus a team component, so the two could never match once a
// team context existed — turning the cheap path into a full plan on every
// session start, silently, forever.
func ExpectedRevision(repoRoot string) (string, error) {
	base, err := skills.Digest()
	if err != nil {
		return "", err
	}
	var teamPath string
	if repoRoot != "" {
		if tc := config.FindRepoTeamContext(repoRoot); tc != nil {
			teamPath = tc.Path
		}
	}
	return base + "+team:" + teamRevision(teamPath), nil
}

// catalogForRepo resolves the catalog Plan should project into repoRoot: the
// built-in one, unioned with this repository's team skills when a team context
// is resolvable.
//
// Resolution deliberately reuses the two mechanisms that already exist rather
// than taking teamPath and repoSlug as new Plan parameters:
//
//   - config.FindRepoTeamContext is what `ox agent prime` and `ox doctor` use to
//     find the checkout. It matches THIS project's team_id and never guesses a
//     cross-team context, so a machine with several teams cannot leak one team's
//     skills into another team's repository.
//   - repotools.RepoSlug is what prime feeds to teamdocs.DiscoverRules. Skills
//     reuse the rules' `repos:` frontmatter contract verbatim, so they must be
//     filtered against the same slug; deriving it a second way is how the same
//     document comes to apply to rules but not to skills.
//
// Threading them through Plan instead would push the resolution onto every
// caller — the adapters, the daemon autofix tick, `ox init` — and each would get
// to be subtly wrong on its own.
//
// ABSENT IS FINE; UNREADABLE IS NOT. A repository with no team, or whose team
// context has not been cloned yet, silently gets the built-in catalog: that is
// the overwhelmingly common case and it must never fail a reconcile. A team
// context that EXISTS but cannot be read is an error, and the reason is stronger
// than tidiness — falling back to the built-in catalog would compute "no team
// skills are desired" and Apply would then DELETE the sageox-team-* files
// already on disk. Guessing "empty" on an unanswered question is the fail-open
// shape teamskills.LoadApprovals already refuses; the cost here is deletion of
// working content rather than materialization of unapproved content.
func catalogForRepo(repoRoot string) (catalogSource, []TeamSkillDecision, error) {
	base := catalogSource(builtInCatalog{})
	// Every early exit returns a teamCatalog carrying a REASON, never the bare
	// base. Returning base says "authoritatively, this team publishes no skills",
	// and the planner acts on that by retiring every sageox-team-* file on disk.
	// A repo with no team context configured, or whose daemon has not finished
	// its first clone, would have its team skills deleted for being early.
	if repoRoot == "" {
		return &teamCatalog{base: base, incomplete: "no repository root to resolve a team context from"}, nil, nil
	}

	tc := config.FindRepoTeamContext(repoRoot)
	if tc == nil || tc.Path == "" {
		return &teamCatalog{base: base, incomplete: "no team context is configured for this project"}, nil, nil
	}
	if _, err := os.Stat(tc.Path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// The daemon clones team contexts in the background; a repo that was
			// just initialized legitimately has nothing here yet.
			return &teamCatalog{base: base, teamPath: tc.Path, incomplete: "team context checkout is not on disk yet"}, nil, nil
		}
		return nil, nil, fmt.Errorf("stat team context %s: %w", tc.Path, err)
	}

	// The slug costs a `git remote get-url`, so it is resolved only once a team
	// context is known to exist — the population that can actually use it.
	return TeamSkillSource(base, tc.Path, repotools.RepoSlug(repoRoot), repoRoot)
}

// WithheldTeamSkills returns every team skill with an outstanding approval:
// either the whole skill is withheld because its manifest is runnable, or its
// readable files are installed while bundled scripts remain absent.
//
// Exported because a decision nobody renders is invisible: a fully withheld
// skill looks unauthored, and a partially installed one otherwise looks complete.
func (plan *ReconcilePlan) WithheldTeamSkills() []TeamSkillDecision {
	var out []TeamSkillDecision
	for _, d := range plan.TeamSkills {
		if d.NeedsApprove {
			out = append(out, d)
		}
	}
	return out
}

// UnusableTeamSkills returns the team skills ox discovered but CANNOT install —
// an unusable name, or files it could not read — as opposed to the ones waiting
// on an approval.
//
// Deliberately a second list rather than more entries in WithheldTeamSkills,
// because the two need opposite next actions. A withheld skill is resolved by a
// human reading it and running `ox skills approve`; an unusable one is resolved
// by fixing it in the Team Context. Routing a refusal to the approval command is
// worse than saying nothing: the human runs it, nothing changes, and the real
// cause stays hidden. This is the same split `ox skills status` already renders
// as `withheld` versus `unavailable`.
func (plan *ReconcilePlan) UnusableTeamSkills() []TeamSkillDecision {
	var out []TeamSkillDecision
	for _, d := range plan.TeamSkills {
		if !d.NeedsApprove && d.InstalledAs == "" {
			out = append(out, d)
		}
	}
	return out
}

// IncompleteReason reports why this source could not see the team's skills, or
// "" when its answer is authoritative.
//
// The planner removes any managed file that is absent from desired state. That
// is correct for a skill the team retired and catastrophic for a skill ox simply
// could not see, and the two produce the identical empty result — so the source
// that produced the emptiness is the only thing that can tell them apart.
func (c *teamCatalog) IncompleteReason() string { return c.incomplete }

// unseeableTeamSkills reports why an empty discovery result must NOT be believed,
// or "" when it can be.
//
// Three cheap checks, deliberately no git and no manifest parsing. The failure
// this guards is a mass delete, and every condition that could hide a team's
// skills resolves to "hold what we have" — so a conservative check that
// occasionally declines to prune is strictly better than an exact one that costs
// a subprocess on the prime hot path.
func unseeableTeamSkills(teamPath, repoSlug string) string {
	if teamPath == "" {
		return "no team context is configured for this project"
	}
	if _, err := os.Stat(teamPath); err != nil {
		return "team context checkout is not on disk yet"
	}
	// Discovery walks BOTH roots — agents/skills is canonical, coworkers/skills is
	// legacy — so blindness means neither is on disk. Checking only agents/ marked
	// every pre-migration team permanently blind, which silently suppressed
	// retirement for them forever: a skill the team deleted would never leave any
	// of their machines, and nothing would say why.
	//
	// Their absence still means the sparse checkout never materialized the
	// directory (GH #862) rather than that the team authored nothing. A team with
	// neither root has no skills either, so retaining nothing is harmless there.
	if !anySkillRootOnDisk(teamPath) {
		return "no skills directory is materialized in the team context"
	}
	// Without a slug ox cannot evaluate any skill's repos: filter, so every
	// targeted skill silently drops out — indistinguishable from the team
	// un-publishing them.
	if repoSlug == "" {
		return "this repository's slug could not be determined"
	}
	return ""
}
