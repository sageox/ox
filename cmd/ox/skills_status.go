package main

import (
	"encoding/json"
	"fmt"
	"os"
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
	Name               string `json:"name,omitempty"`
	Path               string `json:"path,omitempty"`
	Present            bool   `json:"present"`
	AgentsMaterialized bool   `json:"agents_materialized"`
	LastSync           string `json:"last_sync,omitempty"`
	Stale              bool   `json:"stale"`
}

type repoSkillStatus struct {
	Slug           string   `json:"slug"`
	SlugFromRemote bool     `json:"slug_from_remote"`
	Targets        []string `json:"targets"`
	Selected       bool     `json:"selected"`
}

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
	if _, targets, err := skillmanager.LoadDesired(gitRoot); err == nil {
		for _, t := range targets {
			out.Repo.Targets = append(out.Repo.Targets, t.Root)
		}
	}
	if !selected {
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
			if _, statErr := os.Stat(filepath.Join(tc.Path, parent)); statErr == nil {
				status.AgentsMaterialized = true
				break
			}
		}
		if !status.AgentsMaterialized {
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

	// Discovery tells us what the team publishes; the plan tells us what reached
	// disk and what was held. Neither alone answers the question.
	discovered, _ := teamdocs.DiscoverSkills(tc.Path, slug)
	withheld := map[string]skillmanager.TeamSkillDecision{}
	if plan, planErr := planCommittedSkills(gitRoot); planErr == nil && plan != nil {
		for _, d := range plan.WithheldTeamSkills() {
			withheld[d.Name] = d
		}
		if reason := plan.RetainedTeamReason(); reason != "" {
			out.Problems = append(out.Problems,
				fmt.Sprintf("team skills already installed here are being RETAINED rather than refreshed: %s", reason))
		}
	}

	for _, s := range discovered {
		row := teamSkillStatus{Name: s.Name, AppliesHere: true, State: "installed"}
		if d, held := withheld[s.Name]; held {
			row.State = "withheld"
			row.Detail = d.Reason
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
		if s.State == "withheld" {
			return fmt.Sprintf("team skill %q is withheld: %s. Read the file it bundles before deciding to approve it.", s.Name, s.Detail)
		}
	}
	return "Team skills are current. Nothing to do."
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
		p("  agents/      %s", materialized(out.TeamContext.AgentsMaterialized))
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
