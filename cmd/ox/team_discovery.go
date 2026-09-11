package main

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/paths"
)

// enrichedTeam is a fully-resolved team with all fields populated.
// Built by merging daemon, local config, filesystem, and project config sources.
type enrichedTeam struct {
	TeamID   string
	Name     string
	Slug     string
	Path     string
	LastSync time.Time // zero if unknown
	Primary  bool      // true if this is the repo's team
}

// discoverAllTeams returns all teams the user belongs to, merging data from:
//  1. Daemon status (best: has names, slugs, accurate sync times)
//  2. LocalConfig team_contexts (daemon-populated toml, good when daemon is down)
//  3. Filesystem scan of teams directory (last resort, IDs only)
//  4. ProjectConfig enrichment (fills primary team name when missing)
//
// Primary team is always first. Returns nil if no teams found.
func discoverAllTeams(projectRoot string) []enrichedTeam {
	projectCfg, _ := config.LoadProjectConfig(projectRoot)
	var primaryTeamID string
	if projectCfg != nil {
		primaryTeamID = projectCfg.TeamID
	}

	// try sources in priority order
	var teams []enrichedTeam
	if teams = teamsFromDaemonStatus(); len(teams) == 0 {
		teams = teamsFromConfig(projectRoot)
	}

	if len(teams) == 0 {
		return nil
	}

	// load credentials for name/slug enrichment
	var creds *gitserver.GitCredentials
	if projectCfg != nil {
		ep := endpoint.GetForProject(projectRoot)
		if ep != "" {
			creds, _ = gitserver.LoadCredentialsForEndpoint(ep)
		}
	}

	// enrich: fill missing names/slugs, mark primary
	for i := range teams {
		t := &teams[i]
		t.Primary = t.TeamID == primaryTeamID

		// enrich primary team name from project config
		if t.Name == "" && t.Primary && projectCfg != nil {
			t.Name = projectCfg.TeamName
		}
		// enrich from credentials (has server-provided names/slugs)
		if (t.Name == "" || t.Slug == "") && creds != nil {
			repo, ok := creds.Repos[t.TeamID]
			if !ok {
				// fallback: scan values for legacy name-keyed credentials
				for _, r := range creds.Repos {
					if r.TeamID == t.TeamID {
						repo = r
						ok = true
						break
					}
				}
			}
			if ok {
				if t.Name == "" {
					t.Name = repo.Name
				}
				if t.Slug == "" && repo.Slug != "" {
					t.Slug = repo.Slug
				}
			}
		}
		if t.Name == "" {
			t.Name = t.TeamID
		}
		if t.Slug == "" {
			t.Slug = api.DeriveSlug(t.Name)
		}
		if t.Slug == "" {
			t.Slug = t.TeamID
		}
	}

	// sort: primary first
	var primary, others []enrichedTeam
	for _, t := range teams {
		if t.Primary {
			primary = append(primary, t)
		} else {
			others = append(others, t)
		}
	}
	return append(primary, others...)
}

// discoverTeamsGlobal returns teams from all authenticated endpoints.
// Used when not inside a SageOx project (no project root available).
// Discovers teams from git credentials across all endpoints.
func discoverTeamsGlobal() []enrichedTeam {
	endpoints, err := auth.ListEndpoints()
	if err != nil || len(endpoints) == 0 {
		return nil
	}

	var teams []enrichedTeam
	seen := make(map[string]bool)

	for _, ep := range endpoints {
		creds, err := gitserver.LoadCredentialsForEndpoint(ep)
		if err != nil || creds == nil {
			continue
		}
		for key, repo := range creds.Repos {
			if repo.Type != "team-context" {
				continue
			}
			teamID := repo.StableID()
			if teamID == "" {
				// legacy name-keyed credentials may have TeamID in the key
				if strings.HasPrefix(key, "team_") {
					teamID = key
				} else {
					continue
				}
			}
			if seen[teamID] {
				continue
			}
			seen[teamID] = true

			name := repo.Name
			if name == "" {
				name = teamID
			}
			slug := repo.Slug
			if slug == "" {
				slug = api.DeriveSlug(name)
			}
			if slug == "" {
				slug = teamID
			}

			teamPath := paths.TeamContextDir(teamID, ep)
			teams = append(teams, enrichedTeam{
				TeamID: teamID,
				Name:   name,
				Slug:   slug,
				Path:   teamPath,
			})
		}
	}
	return teams
}

// resolveTeamByQuery finds a team by slug, team ID, or name.
// Resolution order: exact slug -> exact team ID -> case-insensitive name.
func resolveTeamByQuery(projectRoot, query string) *enrichedTeam {
	teams := discoverAllTeams(projectRoot)
	if len(teams) == 0 {
		return nil
	}

	q := strings.ToLower(strings.TrimSpace(query))

	// pass 1: exact slug match
	for i, t := range teams {
		if strings.ToLower(t.Slug) == q {
			return &teams[i]
		}
	}

	// pass 2: exact team ID match
	for i, t := range teams {
		if t.TeamID == query {
			return &teams[i]
		}
	}

	// pass 3: case-insensitive name match
	for i, t := range teams {
		if strings.EqualFold(t.Name, query) {
			return &teams[i]
		}
	}

	return nil
}

// resolveTeamMembership finds a team in an API-supplied membership list by slug,
// team ID, or name, using the same resolution order as resolveTeamByQuery above.
//
// The two resolvers sit next to each other on purpose. resolveTeamByQuery answers
// from locally cloned team contexts, which is right for `ox team show` but wrong for
// `ox init`: at init time the repo may have no local team data at all. This one
// answers from the authoritative membership list the API returns, and keeping the
// pass order identical means the same string resolves to the same team on either path.
func resolveTeamMembership(teams []api.TeamMembership, query string) *api.TeamMembership {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" || len(teams) == 0 {
		return nil
	}
	q := strings.ToLower(trimmed)

	// pass 1: exact slug match. Skip empty slugs, or an empty-slug team would
	// swallow queries that should have fallen through to ID or name.
	for i, t := range teams {
		if t.Slug != "" && strings.ToLower(t.Slug) == q {
			return &teams[i]
		}
	}

	// pass 2: exact team ID match
	for i, t := range teams {
		if t.ID == trimmed {
			return &teams[i]
		}
	}

	// pass 3: case-insensitive name match
	for i, t := range teams {
		if t.Name != "" && strings.EqualFold(t.Name, trimmed) {
			return &teams[i]
		}
	}

	return nil
}

// fetchTeamMemberships returns the teams the authenticated user belongs to, as
// reported by the API. A missing or unusable token is not fatal here: the request
// is attempted unauthenticated and the caller decides what an empty result means.
// fetchTeamMemberships returns the teams the API reports for the current user.
//
// Note that it returns (nil, nil) when GetRepos yields no response body: reachable,
// but no answer. That is indistinguishable here from an account that genuinely has
// no team contexts, so callers must not read an empty result as a count of zero.
// resolveTeamFlag is where that rule is enforced.
func fetchTeamMemberships() ([]api.TeamMembership, error) {
	teamClient := api.NewRepoClient()
	if token, err := auth.EnsureValidToken(300); err == nil && token != nil && token.AccessToken != "" {
		teamClient.WithAuthToken(token.AccessToken)
	}
	reposResp, err := teamClient.GetRepos()
	if err != nil {
		return nil, err
	}
	if reposResp == nil {
		return nil, nil
	}
	return reposResp.TeamMembershipsFromRepos(), nil
}

// formatTeamCandidates renders the user's teams for an error message, so a failed
// --team lookup can show what would have worked rather than only what did not.
func formatTeamCandidates(teams []api.TeamMembership) string {
	parts := make([]string, 0, len(teams))
	for _, t := range teams {
		if t.Slug != "" {
			parts = append(parts, fmt.Sprintf("%s (%s, %s)", t.Name, t.Slug, t.ID))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", t.Name, t.ID))
	}
	return strings.Join(parts, ", ")
}

// unknownTeamError reports a --team value that a known, non-empty membership list
// does not contain. Callers must not use it for an empty list: memberships are
// derived rather than declared, so empty means "nothing visible", not "no teams".
// See resolveTeamFlag.
func unknownTeamError(query string, teams []api.TeamMembership) error {
	return fmt.Errorf("unknown team %q\n\nYour teams: %s", query, formatTeamCandidates(teams))
}

// resolveTeamFlag decides what --team resolves to, given the outcome of fetching the
// user's memberships. Only ONE of the three outcomes may reject the flag:
//
//	fetchErr != nil   the API could not be reached: nothing here is knowable.
//	len(teams) == 0   no usable answer. This covers both a nil response body and an
//	                  account the API reports no team contexts for; see below.
//	len(teams) > 0    authoritative: a value absent from this list is a typo.
//
// An empty list does not reject, for two independent reasons.
//
// It is ambiguous: fetchTeamMemberships returns (nil, nil) for a response with no
// body, so empty conflates "no answer" with "answered zero". Anyone adding a
// rejection here must split those apart first, or a missing body will once again
// hard-fail a valid --team.
//
// And even an unambiguous zero should not reject, because ox treats "no teams" as a
// continuable state everywhere else: on the picker path promptNoTeams offers
// "Continue (a new team will be created)" and proceeds. Rejecting would make --team
// the only surface on which zero teams is fatal. Passing through costs a less
// precise server-side error for an account that truly has no teams; rejecting costs
// a blocked init for one whose team context is merely still provisioning.
func resolveTeamFlag(flag string, teams []api.TeamMembership, fetchErr error) (teamID, teamName string, err error) {
	trimmed := strings.TrimSpace(flag)

	if fetchErr != nil {
		slog.Debug("could not fetch teams to resolve --team; passing the value through", "error", fetchErr)
		return trimmed, "", nil
	}
	if len(teams) == 0 {
		slog.Debug("no team memberships reported; passing --team through unresolved", "team", trimmed)
		return trimmed, "", nil
	}
	if match := resolveTeamMembership(teams, trimmed); match != nil {
		return match.ID, match.Name, nil
	}
	return "", "", unknownTeamError(trimmed, teams)
}

// teamsFromDaemonStatus queries the running daemon for team context workspaces.
func teamsFromDaemonStatus() []enrichedTeam {
	client := daemon.NewClientForCurrentRepoWithTimeout(500 * time.Millisecond)
	status, err := client.Status()
	if err != nil || status == nil {
		return nil
	}

	tcWorkspaces, ok := status.Workspaces["team-context"]
	if !ok || len(tcWorkspaces) == 0 {
		return nil
	}

	var teams []enrichedTeam
	for _, ws := range tcWorkspaces {
		teamID := ws.TeamID
		if teamID == "" {
			teamID = ws.ID
		}

		teams = append(teams, enrichedTeam{
			TeamID:   teamID,
			Name:     ws.TeamName,
			Slug:     ws.TeamSlug,
			Path:     ws.Path,
			LastSync: ws.LastSync,
		})
	}
	return teams
}

// teamsFromConfig uses FindAllTeamContexts (local config + filesystem fallback).
func teamsFromConfig(projectRoot string) []enrichedTeam {
	allTeams := config.FindAllTeamContexts(projectRoot)
	if len(allTeams) == 0 {
		return nil
	}

	var teams []enrichedTeam
	for _, tc := range allTeams {
		teams = append(teams, enrichedTeam{
			TeamID:   tc.TeamID,
			Name:     tc.TeamName,
			Slug:     tc.Slug,
			Path:     tc.Path,
			LastSync: tc.LastSync,
		})
	}
	return teams
}

// toConfigTeamContext converts an enrichedTeam back to config.TeamContext
// for callers that need the original type.
func (t *enrichedTeam) toConfigTeamContext() *config.TeamContext {
	return &config.TeamContext{
		TeamID:   t.TeamID,
		TeamName: t.Name,
		Slug:     t.Slug,
		Path:     t.Path,
		LastSync: t.LastSync,
	}
}
