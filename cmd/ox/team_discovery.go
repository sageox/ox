package main

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
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

// maxTeamCandidates caps how many teams an error message will name. A mistyped
// --team in CI should point at the fix, not print the whole org chart.
const maxTeamCandidates = 10

// usableTeams returns the memberships that ox init can actually register against.
//
// A membership with an empty ID cannot be one. RepoInfo.TeamID is
// `json:"team_id,omitempty"`, so the derived path in TeamMembershipsFromRepos can
// produce a team whose ID is "" — and init drops an empty team ID from the
// registration request while still writing the team NAME into .sageox/config.json
// and printing it on the success line. Matching such a team would name a team the
// repo was never registered to, in two places that ox status and ox doctor later
// read back as fact.
func usableTeams(teams []api.TeamMembership) []api.TeamMembership {
	usable := make([]api.TeamMembership, 0, len(teams))
	for _, t := range teams {
		if t.ID == "" {
			slog.Debug("dropping team membership with no ID", "name", t.Name, "slug", t.Slug)
			continue
		}
		usable = append(usable, t)
	}
	return usable
}

// resolveTeamMembership finds teams in an API-supplied membership list by slug,
// team ID, or name, using the same resolution order as resolveTeamByQuery above.
// It returns every match from the FIRST pass that hits anything, so a caller can
// tell "no match" from "the query does not identify one team".
//
// Returning all matches rather than the first is deliberate. The membership list
// is not guaranteed unique on any of these fields: one person can belong to two
// orgs that each named a team "Platform". Returning the first would resolve that
// silently, and — before TeamMembershipsFromRepos sorted its derived path —
// differently on different runs.
//
// The two resolvers sit next to each other on purpose. resolveTeamByQuery answers
// from locally cloned team contexts, which is right for `ox team show` but wrong for
// `ox init`: at init time the repo may have no local team data at all. This one
// answers from the authoritative membership list the API returns, and keeping the
// pass order identical means the same string resolves to the same team on either path.
func resolveTeamMembership(teams []api.TeamMembership, query string) []api.TeamMembership {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return nil
	}
	candidates := usableTeams(teams)
	if len(candidates) == 0 {
		return nil
	}

	// Every pass compares with EqualFold. Slugs are server-derived lowercase ASCII
	// and IDs are opaque generated tokens, so an exact comparison would be defensible
	// for both — but an asymmetry between passes is not: a case-variant of a real ID
	// would fall THROUGH the ID pass and be picked up by the case-insensitive name
	// pass below, matching a different team than the one the user typed.
	matchers := []func(api.TeamMembership) bool{
		// pass 1: slug. Skip empty slugs, or an empty-slug team would swallow
		// queries that should have fallen through to ID or name.
		func(t api.TeamMembership) bool { return t.Slug != "" && strings.EqualFold(t.Slug, trimmed) },
		// pass 2: team ID
		func(t api.TeamMembership) bool { return strings.EqualFold(t.ID, trimmed) },
		// pass 3: name
		func(t api.TeamMembership) bool { return t.Name != "" && strings.EqualFold(t.Name, trimmed) },
	}

	for _, matches := range matchers {
		var hits []api.TeamMembership
		for _, t := range candidates {
			if matches(t) {
				hits = append(hits, t)
			}
		}
		if len(hits) > 0 {
			return hits
		}
	}

	return nil
}

// fetchTeamMemberships returns the teams the API reports for the current user.
//
// An unusable token is not fatal here: the request goes out unauthenticated, the
// server rejects it, and resolveTeamFlag falls back to passing --team through. It is
// not silent either. The auth gate in runInit has already passed by this point, so a
// token that cannot be used here is a real degradation, and the picker path ten lines
// away reports the same condition rather than swallowing it.
func fetchTeamMemberships() ([]api.TeamMembership, error) {
	teamClient := api.NewRepoClient()

	token, tokenErr := auth.EnsureValidToken(300)
	if token != nil && token.AccessToken != "" {
		teamClient.WithAuthToken(token.AccessToken)
	} else {
		slog.Warn("no usable token for team fetch despite passing auth gate",
			"endpoint", endpoint.Get(), "token_err", tokenErr)
		cli.PrintWarning("Could not authenticate to check --team locally; the server will decide.")
	}

	reposResp, err := teamClient.GetRepos()
	if err != nil {
		return nil, err
	}
	return reposResp.TeamMembershipsFromRepos(), nil
}

// formatTeamCandidates renders the user's teams for an error message, so a failed
// --team lookup can show what would have worked rather than only what did not.
//
// Every field here is server-supplied and lands on a terminal, so it goes through
// cli.SanitizeTerminalText — the same guard renderTeamShow and the invite path apply
// to these exact three fields. This message fires on a typo, which means untrusted
// text reaches the screen on the path a user is least expecting output from.
func formatTeamCandidates(teams []api.TeamMembership) string {
	shown := teams
	if len(shown) > maxTeamCandidates {
		shown = shown[:maxTeamCandidates]
	}

	parts := make([]string, 0, len(shown)+1)
	for _, t := range shown {
		name := cli.SanitizeTerminalText(t.Name)
		id := cli.SanitizeTerminalText(t.ID)
		if t.Slug != "" {
			parts = append(parts, fmt.Sprintf("%s (%s, %s)", name, cli.SanitizeTerminalText(t.Slug), id))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", name, id))
	}
	if len(teams) > len(shown) {
		parts = append(parts, fmt.Sprintf("and %d more — run 'ox team list'", len(teams)-len(shown)))
	}
	return strings.Join(parts, ", ")
}

// unknownTeamError reports a --team value that a known, non-empty membership list
// does not contain. Callers must not use it for an empty list: an empty list means
// "nothing visible", not "no teams". See resolveTeamFlag.
func unknownTeamError(query string, teams []api.TeamMembership) error {
	return fmt.Errorf("unknown team %q\n\nYour teams: %s", query, formatTeamCandidates(teams))
}

// ambiguousTeamError reports a --team value that identifies more than one team.
// The membership list is unique on none of slug, ID or name — two orgs can each
// have a team named "Platform" — and picking one of them silently would bind the
// repo to a tenant the user never chose.
func ambiguousTeamError(query string, matches []api.TeamMembership) error {
	return fmt.Errorf("ambiguous team %q matches %d teams: %s\n\nUse the team ID to pick one",
		query, len(matches), formatTeamCandidates(matches))
}

// resolveTeamFlag decides what --team resolves to, given the outcome of fetching the
// user's memberships. Only ONE of the three outcomes may reject the flag:
//
//	fetchErr != nil    the API could not be reached: nothing here is knowable.
//	no usable teams    no usable answer; see below.
//	usable teams > 0   authoritative: a value absent from this list is a typo, and a
//	                   value matching several of them is not a choice ox may make.
//
// "Usable" excludes memberships with an empty ID, which cannot be registered against
// at all; see usableTeams.
//
// An empty list does not reject, because ox treats "no teams" as a continuable state
// everywhere else: on the picker path promptNoTeams offers "Continue (a new team will
// be created)" and proceeds. Rejecting would make --team the only surface on which
// zero teams is fatal. Nor is an empty list proof of zero teams — the same shape
// covers an account whose team context is still provisioning. Passing through costs a
// less precise server-side error for an account that truly has none; rejecting costs
// a blocked init for one whose teams simply have not appeared yet.
func resolveTeamFlag(flag string, teams []api.TeamMembership, fetchErr error) (teamID, teamName string, err error) {
	trimmed := strings.TrimSpace(flag)

	if fetchErr != nil {
		slog.Debug("could not fetch teams to resolve --team; passing the value through", "error", fetchErr)
		return trimmed, "", nil
	}
	candidates := usableTeams(teams)
	if len(candidates) == 0 {
		slog.Debug("no usable team memberships reported; passing --team through unresolved", "team", trimmed)
		return trimmed, "", nil
	}

	matches := resolveTeamMembership(candidates, trimmed)
	switch len(matches) {
	case 0:
		return "", "", unknownTeamError(trimmed, candidates)
	case 1:
		return matches[0].ID, matches[0].Name, nil
	default:
		return "", "", ambiguousTeamError(trimmed, matches)
	}
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
