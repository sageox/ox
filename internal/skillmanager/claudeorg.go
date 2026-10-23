package skillmanager

import (
	"log/slog"
	"strings"
	"time"

	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/teamdocs"
	"github.com/sageox/ox/internal/teamskills"
)

// ClaudeProjectTargetKey is the Claude Code adapter's project skill target
// (.claude/skills). It is the ONLY target a Claude organization plugin
// duplicates: Codex, Cursor and every other adapter read .agents/skills or
// their own roots, where no org plugin ever lands, so ox keeps copying team
// skills there unconditionally.
const ClaudeProjectTargetKey = "claude-project"

// ClaudeOrgSyncForRepo returns what ox believes the Claude organization of
// repoRoot's team delivers. A repository with no team context gets the zero
// value. The error is for diagnostics only; planning treats it as "no org
// sync", which copies every skill as ox always has.
func ClaudeOrgSyncForRepo(repoRoot string) (teamskills.ClaudeOrgSync, error) {
	if repoRoot == "" {
		return teamskills.ClaudeOrgSync{}, nil
	}
	tc := config.FindRepoTeamContext(repoRoot)
	if tc == nil || tc.TeamID == "" {
		return teamskills.ClaudeOrgSync{}, nil
	}
	return teamskills.LoadClaudeOrgSync(endpoint.GetForProject(repoRoot), tc.TeamID, time.Now())
}

// claudeOrgSyncFor is ClaudeOrgSyncForRepo for the planner, which already
// resolved the team context and must not fail a reconcile over a cache.
func claudeOrgSyncFor(repoRoot, teamID string) teamskills.ClaudeOrgSync {
	if teamID == "" {
		return teamskills.ClaudeOrgSync{}
	}
	orgSync, err := teamskills.LoadClaudeOrgSync(endpoint.GetForProject(repoRoot), teamID, time.Now())
	if err != nil {
		slog.Debug("skills: claude org sync record unreadable; copying every team skill", "error", err)
	}
	return orgSync
}

// claudeOrgCannotCarry explains why the org plugin cannot be the copy Claude
// Code uses for this skill, or "" when it can.
//
// The server lists only skills it classified portable, but its list can lag
// this checkout: a skill that gained a script, a repos: filter, or a shell
// requirement since Claude's last pull is still the OLD prose version in the
// org plugin. Deferring then would hand Claude Code a skill missing the very
// thing that changed. So ox re-checks the bytes it has, with the same rules,
// and keeps its own copy when they disagree. The cost of a disagreement is a
// duplicate for one export cycle, never a missing skill.
func claudeOrgCannotCarry(ts teamdocs.TeamSkill, loaded teamskills.Skill, verdict teamskills.Verdict) string {
	if verdict.Executable {
		return "it can run code here (" + verdict.Describe() + ")"
	}
	if len(ts.Repos) > 0 {
		return "its repos: list limits it to specific repositories"
	}
	if ts.RequiresDeclared {
		if len(ts.Requires) > 0 {
			return "it declares requires: " + strings.Join(ts.Requires, ", ")
		}
		return "" // `requires: []`: the author says it runs anywhere
	}
	for _, f := range loaded.Files {
		if !strings.EqualFold(f.Path, skills.SkillFileName) {
			continue
		}
		if signals := teamskills.BodyRuntimeSignals(teamdocs.ManifestBody(string(f.Content))); len(signals) > 0 {
			return "its instructions need a terminal (" + signals[0].Code + ": " + signals[0].Evidence + ")"
		}
	}
	return ""
}

// targetSkillFilter is implemented by a catalog that withholds some of the
// skills it selects from ONE target, while still projecting them everywhere
// else.
type targetSkillFilter interface {
	withheldFromTarget(targetKey string) map[string]struct{}
}

// skillsForTarget drops the skills source withholds from targetKey. Everything
// else in the plan, including retirement of copies already on disk, then
// treats those skills as simply not desired in that target.
func skillsForTarget(source catalogSource, targetKey string, selected []skills.Skill) []skills.Skill {
	filter, ok := source.(targetSkillFilter)
	if !ok {
		return selected
	}
	withheld := filter.withheldFromTarget(targetKey)
	if len(withheld) == 0 {
		return selected
	}
	out := make([]skills.Skill, 0, len(selected))
	for _, s := range selected {
		if _, skip := withheld[s.Name]; skip {
			continue
		}
		out = append(out, s)
	}
	return out
}
