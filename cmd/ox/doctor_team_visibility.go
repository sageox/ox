package main

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
)

// CheckSlugTeamVisibility is the slug for the team-visibility check.
const CheckSlugTeamVisibility = "team-visibility"

const teamVisibilityCheckName = "Team visibility"

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugTeamVisibility,
		Name:        teamVisibilityCheckName,
		Category:    "Team Context",
		FixLevel:    FixLevelCheckOnly,
		Description: "Verifies the repo is bound to a team the signed-in account can actually see",
		Run: func(fix bool) checkResult {
			return checkTeamVisibility()
		},
	})
}

// checkTeamVisibility answers the question "will my sessions show up where I'm
// looking?".
//
// A repo binds to exactly one team_id at `ox init` time, but sessions flow to a
// ledger keyed by repo_id — nothing on disk ties a recorded session back to a
// team. So a repo can be bound to a team the signed-in account is not a member
// of (or simply never visits) and every other check still passes: recording
// works, the push succeeds, and the user sees an empty dashboard and concludes
// ox is broken. "Team registration" only asserts team_id is non-empty, which is
// true in exactly that failure. This check compares the bound team against the
// memberships the server reports for this account.
//
// Everything that cannot be established locally degrades to a skip, never a
// failure: no team bound, not signed in, and no network all mean "cannot
// verify", and a red mark there would train people to ignore doctor output.
func checkTeamVisibility() checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck(teamVisibilityCheckName, "not in git repo", "")
	}

	cfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil || cfg == nil {
		return SkippedCheck(teamVisibilityCheckName, "no config", "")
	}
	if cfg.TeamID == "" {
		// the Team registration check owns this state; don't double-report it
		return SkippedCheck(teamVisibilityCheckName, "repo not registered with a team", "")
	}

	projectEndpoint := endpoint.GetForProject(gitRoot)
	token, err := auth.GetTokenForEndpoint(projectEndpoint)
	if err != nil || token == nil || token.AccessToken == "" {
		return SkippedCheck(teamVisibilityCheckName, "not signed in", "")
	}

	client := api.NewRepoClientWithEndpoint(projectEndpoint).WithAuthToken(token.AccessToken)
	resp, err := client.GetRepos()
	if err != nil || resp == nil {
		return SkippedCheck(teamVisibilityCheckName, "could not reach SageOx", "")
	}

	// Only the server's own Teams array is authoritative. When it is absent,
	// TeamMembershipsFromRepos derives a list by scanning for team-context
	// repos — which is a SUBSET (a team the user belongs to but has no
	// team-context repo for simply won't appear) and is also what an older
	// server returns. Warning off a derived list would mark healthy repos
	// broken, and a check that cries wolf is a check people learn to skip.
	if len(resp.Teams) == 0 {
		return SkippedCheck(teamVisibilityCheckName,
			"server did not report team memberships", "")
	}
	teams := resp.Teams

	for _, t := range teams {
		if t.ID != cfg.TeamID {
			continue
		}
		name := t.Name
		if name == "" {
			name = cfg.TeamID
		}
		if t.Personal {
			// a personal team is structurally single-member, so teammates will
			// never see these sessions no matter how the sync behaves
			return WarningCheck(teamVisibilityCheckName,
				fmt.Sprintf("%s (personal team — teammates cannot see these sessions)", name),
				"Sessions recorded here go to your own private team.\n"+
					"       To share them with your team, re-run `ox init` and pick the team you share with coworkers.")
		}
		return PassedCheck(teamVisibilityCheckName, name)
	}

	return WarningCheck(teamVisibilityCheckName,
		"bound to a team this account cannot see",
		teamVisibilityDetail(cfg, teams, projectEndpoint))
}

// teamVisibilityDetail explains the mismatch in the terms the user experiences
// it: sessions record fine, but the dashboard they are looking at is not the one
// receiving them.
func teamVisibilityDetail(cfg *config.ProjectConfig, teams []api.TeamMembership, ep string) string {
	bound := cfg.TeamID
	if cfg.TeamName != "" {
		bound = fmt.Sprintf("%s (%s)", cfg.TeamName, cfg.TeamID)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "This repo records sessions to %s, which is not among your team memberships.\n", bound)
	b.WriteString("       Sessions are still being captured — they are landing somewhere you are not looking.\n")

	if dash := teamDashboardURL(ep, cfg.TeamID); dash != "" {
		fmt.Fprintf(&b, "       Bound team dashboard: %s\n", dash)
	}

	switch len(teams) {
	case 0:
		b.WriteString("       This account belongs to no teams. Ask a teammate to invite you, or re-run `ox init`.")
	default:
		names := make([]string, 0, len(teams))
		for _, t := range teams {
			label := t.Name
			if label == "" {
				label = t.ID
			}
			if t.Personal {
				label += " (personal)"
			}
			names = append(names, label)
		}
		sort.Strings(names)
		fmt.Fprintf(&b, "       Teams you belong to: %s\n", strings.Join(names, ", "))
		b.WriteString("       Re-run `ox init` to bind this repo to one of them.")
	}
	return b.String()
}

// teamDashboardURL builds the web dashboard link for a team, matching the URL
// shape `ox team show` already prints.
func teamDashboardURL(ep, teamID string) string {
	if ep == "" || teamID == "" {
		return ""
	}
	return fmt.Sprintf("%s/team/%s", strings.TrimRight(ep, "/"), url.PathEscape(teamID))
}
