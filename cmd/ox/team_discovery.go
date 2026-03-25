package main

import (
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
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

	// enrich: fill missing names/slugs, mark primary
	for i := range teams {
		t := &teams[i]
		t.Primary = t.TeamID == primaryTeamID

		// enrich primary team name from project config
		if t.Name == "" && t.Primary && projectCfg != nil {
			t.Name = projectCfg.TeamName
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

// teamsFromDaemonStatus queries the running daemon for team context workspaces.
func teamsFromDaemonStatus() []enrichedTeam {
	client := daemon.NewClientWithTimeout(500 * time.Millisecond)
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
