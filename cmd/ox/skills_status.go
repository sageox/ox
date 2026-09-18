package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/repotools"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/teamdocs"
	"github.com/spf13/cobra"
)

// skills_status.go — `ox skills status`.
//
// It answers one question in one command: "why isn't my team's skill on my
// machine?" That question was previously unanswerable without reading the
// daemon's logs, because every way it can go wrong produces the SAME
// observation from the repository — nothing there. A checkout that has not
// cloned, a sparse set that never materialized agents/, a repo whose slug does
// not match a skill's repos: filter, a skill withheld pending approval, and a
// team that simply published nothing all look identical from the outside.
//
// So the command's job is not to render state; it is to make those five cases
// distinguishable, and to end each failing line with the command that fixes it.

// skillsCmd is the parent for `ox skills …`.
var skillsCmd = &cobra.Command{
	Use:   "skills",
	Short: "Inspect the skills your AI coworkers have",
	Long: `Inspect the skills installed for this repository.

Skills come from two places: the ones ox itself ships, and the ones your team
publishes to its Team Context. This command shows both, and — when a team skill
is missing — which of the several possible reasons is the actual one.`,
}

var skillsStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show which team skills reached this repository, and why the rest did not",
	RunE:  runSkillsStatus,
}

func init() {
	// Without a GroupID cobra files this under "Additional Commands", away from
	// the Knowledge family it belongs to.
	skillsCmd.GroupID = "knowledge"
	skillsStatusCmd.Flags().Bool("json", false, "Emit machine-readable JSON")
	skillsCmd.AddCommand(skillsStatusCmd)
	rootCmd.AddCommand(skillsCmd)
}

// skillsStatusOutput is the JSON shape. Guidance travels in the payload rather
// than only in the human rendering, so Codex and Droid get the next action too —
// the CLI owns behavior, skills are thin relays over it.
type skillsStatusOutput struct {
	TeamContext *teamContextStatus `json:"team_context"`
	Repo        repoSkillStatus    `json:"repo"`
	TeamSkills  []teamSkillStatus  `json:"team_skills"`
	Problems    []string           `json:"problems,omitempty"`
	Guidance    string             `json:"guidance,omitempty"`
}

type teamContextStatus struct {
	Name string `json:"name,omitempty"`
	Path string `json:"path,omitempty"`
	// Present is the checkout directory; SkillsMaterialized is whether any root
	// discovery walks is a real directory inside it. Named for the state it
	// represents rather than for agents/ specifically: the legacy coworkers/ root
	// satisfies it, and reporting "agents/ materialized" for a legacy team would
	// be a lie in the reassuring direction.
	Present            bool   `json:"present"`
	SkillsMaterialized bool   `json:"skills_materialized"`
	LastSync           string `json:"last_sync,omitempty"`
	Stale              bool   `json:"stale"`
}

type repoSkillStatus struct {
	Slug           string   `json:"slug"`
	SlugFromRemote bool     `json:"slug_from_remote"`
	Targets        []string `json:"targets"`
	Selected       bool     `json:"selected"`
}

// The states a published skill can be in from this repository's point of view.
// Not a boolean: "the team published this" and "this is on my disk" are
// different facts, and collapsing them is how a diagnostic reports success at
// the exact moment the thing it diagnoses has not happened.
const (
	skillInstalled     = "installed"      // on disk now
	skillPending       = "pending"        // reconcile will create it; not there yet
	skillOutdated      = "outdated"       // present, but differs from the team's copy
	skillWithheld      = "withheld"       // executable, awaiting approval
	skillUnavailable   = "unavailable"    // discovered but ox could not use it
	skillNotApplicable = "not applicable" // published, but its repos: excludes this repo
)

type teamSkillStatus struct {
	Name        string `json:"name"`
	AppliesHere bool   `json:"applies_here"`
	State       string `json:"state"`
	Detail      string `json:"detail,omitempty"`
}

func runSkillsStatus(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")

	gitRoot := findGitRoot()
	if gitRoot == "" {
		return fmt.Errorf("not inside a git repository")
	}

	out := collectSkillsStatus(gitRoot)

	if asJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	renderSkillsStatus(cmd.OutOrStdout(), out)
	return nil
}

// collectSkillsStatus gathers the state without printing, so the rendering and
// the JSON shape are both testable without a terminal.
func collectSkillsStatus(gitRoot string) skillsStatusOutput {
	out := skillsStatusOutput{TeamSkills: []teamSkillStatus{}}

	slug := repotools.RepoSlug(gitRoot)
	fromRemote := slug != filepath.Base(gitRoot)
	out.Repo = repoSkillStatus{Slug: slug, SlugFromRemote: fromRemote}

	_, _, selected := skillmanager.InstalledSource(gitRoot)
	out.Repo.Selected = selected
	targets, desiredErr := skillTargetRoots(gitRoot)
	out.Repo.Targets = targets
	if desiredErr != nil {
		// A missing lockfile is a valid empty state; anything else means ox cannot
		// read its own record of what it installed. Suppressing that made the
		// command recommend `ox init` for a repo that is already initialized and
		// merely has a corrupt lockfile.
		out.Problems = append(out.Problems,
			fmt.Sprintf("ox could not read this repository's skill lockfile, so what is installed here is unknown: %v", desiredErr))
	}

	// Only when the lockfile READ succeeded. InstalledSource also reports
	// selected=false for an unreadable or malformed lockfile, so firing this
	// unconditionally told someone with a corrupt lockfile to run `ox init` on a
	// repository that is already initialized — the one action that cannot help.
	if !selected && desiredErr == nil {
		out.Problems = append(out.Problems, "this repository has not selected an AI coworker, so no skills are installed — run `ox init`")
	}
	// A slug that fell back to the directory name matches no `repos:` filter, so
	// every targeted team skill silently vanishes. Saying so is the whole point:
	// the symptom is identical to the team having published nothing.
	if !fromRemote {
		out.Problems = append(out.Problems,
			fmt.Sprintf("this repository's slug is %q, derived from the directory name because no recognized remote was found — team skills with a `repos:` filter cannot match here", slug))
	}

	tc := config.FindRepoTeamContext(gitRoot)
	if tc == nil || tc.Path == "" {
		out.Problems = append(out.Problems, "no Team Context is configured for this project, so there are no team skills to install")
		out.Guidance = skillsStatusGuidance(out)
		return out
	}

	status := &teamContextStatus{Name: tc.TeamName, Path: tc.Path}
	if _, statErr := os.Stat(tc.Path); statErr == nil {
		status.Present = true
	} else {
		out.Problems = append(out.Problems, "the Team Context has not finished cloning yet — nothing installs until it does; watch it with `ox sync --all-teams`")
	}
	if status.Present {
		// Both roots count. A team that predates the agents/ migration keeps its
		// content under coworkers/, and discovery walks both — reporting a problem
		// because the canonical root is absent would send every such team to
		// `ox doctor` for a condition that is not wrong.
		for _, root := range teamdocs.SkillRoots {
			parent := filepath.Dir(filepath.FromSlash(root))
			// IsDir, not merely "exists": a regular file named agents/ makes the
			// ReadDir inside discovery fail, so the checkout is not usable either.
			if info, statErr := os.Stat(filepath.Join(tc.Path, parent)); statErr == nil && info.IsDir() {
				status.SkillsMaterialized = true
				break
			}
		}
		if !status.SkillsMaterialized {
			out.Problems = append(out.Problems,
				"the Team Context is on disk but no skills directory was materialized, so team rules AND team skills are invisible here — run `ox doctor`")
		}
	}
	if sync := daemon.LoadSyncState(tc.Path); sync != nil && !sync.LastSync.IsZero() {
		status.LastSync = sync.LastSync.Format(time.RFC3339)
		if sync.IsStale(daemon.DefaultStalenessThreshold) {
			status.Stale = true
			out.Problems = append(out.Problems,
				fmt.Sprintf("the Team Context has not synced in %s — recent edits have not arrived; run `ox sync --all-teams`", roundedAge(sync.LastSync)))
		}
	}
	out.TeamContext = status

	// Three different questions, deliberately asked separately:
	//   what does the team publish        -> PublishedSkills
	//   which of those are for this repo  -> SkillAppliesToRepo
	//   which of those are actually here  -> the plan, and the file on disk
	//
	// Collapsing any two of them is how a diagnostic reports success at the exact
	// moment the thing it diagnoses has not happened.
	published, discoverErr := teamdocs.PublishedSkills(tc.Path)
	if discoverErr != nil {
		// A missing root is already an empty result; an error here means the
		// checkout is present but malformed — e.g. agents/skills is a regular
		// file. Reporting "none found" would tell the reader nothing exists when
		// the truth is that the checkout needs repair.
		out.Problems = append(out.Problems,
			fmt.Sprintf("ox could not read the team's skills: %v", discoverErr))
	}

	decisions := map[string]skillmanager.TeamSkillDecision{}
	var planned plannedPaths
	plan, planErr := planCommittedSkills(gitRoot)
	switch {
	case planErr != nil:
		out.Problems = append(out.Problems,
			fmt.Sprintf("ox could not compute what should be installed here, so no skill below can be confirmed: %v", planErr))
	case plan != nil:
		for _, d := range plan.TeamSkills {
			decisions[d.Name] = d
		}
		for _, action := range plan.Creates {
			planned.created = append(planned.created, action.Path)
		}
		for _, action := range plan.Updates {
			planned.updated = append(planned.updated, action.Path)
		}
		if reason := plan.RetainedTeamReason(); reason != "" {
			out.Problems = append(out.Problems,
				fmt.Sprintf("team skills already installed here are being RETAINED rather than refreshed: %s", reason))
		}
	}

	for _, sk := range published {
		row := teamSkillStatus{Name: sk.Name, AppliesHere: teamdocs.SkillAppliesToRepo(sk, slug)}
		switch {
		case !row.AppliesHere:
			// Without this row the skill is invisible, and "the team published
			// nothing" looks identical to "the team published it for other repos."
			// Those need opposite actions: author one, versus widen a repos: list.
			row.State = skillNotApplicable
			row.Detail = "its repos: list targets " + strings.Join(sk.Repos, ", ")
		case planErr != nil:
			row.State = "unknown"
			row.Detail = "could not compute the plan"
		default:
			row.State, row.Detail = installedState(gitRoot, targets, decisions[sk.Name], planned)
		}
		out.TeamSkills = append(out.TeamSkills, row)
	}

	out.Guidance = skillsStatusGuidance(out)
	return out
}

// skillsStatusGuidance is the single next action, for an AI coworker reading the
// JSON. One action, not a menu: a list of things that might help is a list the
// caller has to triage, which is the work this command exists to have already done.
func skillsStatusGuidance(out skillsStatusOutput) string {
	if len(out.Problems) > 0 {
		return out.Problems[0]
	}
	for _, s := range out.TeamSkills {
		if s.State == skillWithheld {
			return fmt.Sprintf("team skill %q is withheld: %s. Read the file it bundles before deciding to approve it.", s.Name, s.Detail)
		}
	}
	for _, s := range out.TeamSkills {
		if s.State == skillPending || s.State == skillOutdated || s.State == skillUnavailable || s.State == "unknown" {
			return fmt.Sprintf("team skill %q is %s: %s", s.Name, s.State, s.Detail)
		}
	}
	// A successful, empty read is a real answer and needs its own next action.
	// Falling through to "current" told the person asking "why isn't my skill
	// here?" that everything was fine, which is true and useless — and made
	// "nobody published one" indistinguishable from "it was filtered out."
	if out.TeamContext != nil && out.TeamContext.SkillsMaterialized && len(out.TeamSkills) == 0 {
		return "Your team has not published any skills yet. Add one under agents/skills/<name>/SKILL.md in the Team Context."
	}
	if out.TeamContext == nil {
		return "This project has no Team Context, so there are no team skills to install."
	}
	return "Team skills are current. Nothing to do."
}

// skillTargetRoots reads the repo's selected skill roots, distinguishing "no
// lockfile yet" from "the lockfile cannot be read."
func skillTargetRoots(gitRoot string) ([]string, error) {
	_, targets, err := skillmanager.LoadDesired(gitRoot)
	if err != nil {
		return nil, err
	}
	roots := make([]string, 0, len(targets))
	for _, t := range targets {
		roots = append(roots, t.Root)
	}
	return roots, nil
}

// installedState answers "is this skill actually here?" from the reconcile
// decision plus the file on disk — never from the fact that it was discovered.
//
// Discovery only establishes that the team published a skill for this repo. A
// pending create means reconcile intends to write it and has not; a decision
// with no InstalledAs means ox could not use the skill at all (unreadable
// files), which WithheldTeamSkills does not report because it only returns
// approval holds.
func installedState(gitRoot string, targets []string, d skillmanager.TeamSkillDecision, planned plannedPaths) (state, detail string) {
	switch {
	case d.NeedsApprove:
		return skillWithheld, d.Reason
	case d.Name != "" && d.InstalledAs == "":
		return skillUnavailable, d.Reason
	}
	if d.InstalledAs == "" {
		// No decision at all: the plan did not see it, so it is not ours to claim.
		return skillUnavailable, "ox did not record a decision for this skill"
	}

	if len(targets) == 0 {
		// No selected root means there is nowhere for it to be. Falling through the
		// per-target loop returned "installed" on an empty list, which is the most
		// confident possible answer about a repository that has installed nothing.
		return skillPending, "this repository has no skills directory selected — run `ox init`"
	}

	// EVERY selected target must be complete, not the first one that looks it.
	// A repo with both .claude/skills and .agents/skills selected — Claude Code
	// beside Codex — would otherwise report installed while the second root was
	// missing the skill, or held a stale copy, or had lost a bundled file.
	var incomplete, outdated []string
	for _, root := range targets {
		dir := path.Join(root, d.InstalledAs) + "/"
		switch {
		case planned.creates(dir):
			incomplete = append(incomplete, root)
		case !manifestPresent(gitRoot, dir):
			incomplete = append(incomplete, root)
		case planned.updates(dir):
			// Present but not what the team published: a diagnostic that calls this
			// "installed" is answering a different question than the one asked.
			outdated = append(outdated, root)
		}
	}
	switch {
	case len(incomplete) > 0:
		return skillPending, "not complete in " + strings.Join(incomplete, ", ") + " — run `ox doctor --fix`"
	case len(outdated) > 0:
		return skillOutdated, "differs from the team's copy in " + strings.Join(outdated, ", ") + " — run `ox doctor --fix`"
	}
	return skillInstalled, ""
}

// manifestPresent reports whether the skill's SKILL.md exists under dir.
func manifestPresent(gitRoot, dir string) bool {
	_, err := os.Stat(filepath.Join(gitRoot, filepath.FromSlash(path.Join(dir, "SKILL.md"))))
	return err == nil
}

// plannedPaths indexes the reconcile plan's intended writes by path.
//
// Matched on the skill's DIRECTORY prefix rather than on SKILL.md alone, so a
// missing or stale bundled file — a reference doc, an asset — counts as not
// installed too. The manifest being present says nothing about the rest.
type plannedPaths struct {
	created []string
	updated []string
}

func (p plannedPaths) creates(dir string) bool { return hasPrefixIn(p.created, dir) }
func (p plannedPaths) updates(dir string) bool { return hasPrefixIn(p.updated, dir) }

func hasPrefixIn(paths []string, dir string) bool {
	for _, candidate := range paths {
		if strings.HasPrefix(candidate, dir) {
			return true
		}
	}
	return false
}

func roundedAge(t time.Time) string {
	d := time.Since(t)
	if d >= 24*time.Hour {
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}

func renderSkillsStatus(w interface{ Write([]byte) (int, error) }, out skillsStatusOutput) {
	p := func(format string, args ...any) { fmt.Fprintf(w, format+"\n", args...) }

	if out.TeamContext != nil {
		name := out.TeamContext.Name
		if name == "" {
			name = "(unnamed)"
		}
		p("%s  %s", cli.StyleAccent.Render("Team Context"), name)
		p("  path         %s", out.TeamContext.Path)
		p("  checkout     %s", presence(out.TeamContext.Present))
		p("  skill roots  %s", materialized(out.TeamContext.SkillsMaterialized))
		if out.TeamContext.LastSync != "" {
			p("  last sync    %s", out.TeamContext.LastSync)
		}
	} else {
		p("%s  none configured for this project", cli.StyleAccent.Render("Team Context"))
	}

	p("")
	p("%s  %s", cli.StyleAccent.Render("This repo"), out.Repo.Slug)
	if !out.Repo.SlugFromRemote {
		p("  slug         from the directory name — no recognized remote")
	}
	if len(out.Repo.Targets) > 0 {
		p("  targets      %s", strings.Join(out.Repo.Targets, ", "))
	} else {
		p("  targets      none — run `ox init`")
	}

	p("")
	if len(out.TeamSkills) == 0 {
		p("%s  none found", cli.StyleAccent.Render("Team skills"))
	} else {
		p("%s", cli.StyleAccent.Render("Team skills"))
		for _, s := range out.TeamSkills {
			detail := s.Detail
			if detail != "" {
				detail = " — " + detail
			}
			p("  %-24s %s%s", s.Name, s.State, detail)
		}
	}

	if len(out.Problems) > 0 {
		p("")
		p("%s", cli.StyleWarning.Render("Why something may be missing"))
		for _, problem := range out.Problems {
			p("  • %s", problem)
		}
	}
}

func presence(ok bool) string {
	if ok {
		return "on disk"
	}
	return "NOT CLONED YET"
}

func materialized(ok bool) string {
	if ok {
		return "materialized"
	}
	return "NOT MATERIALIZED — team rules and skills are invisible here"
}
