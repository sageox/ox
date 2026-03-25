package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
	"github.com/spf13/cobra"
)

// teamsOutput is the JSON output structure for ox teams.
type teamsOutput struct {
	PrimaryTeam string      `json:"primary_team"` // team ID of this repo's team
	Teams       []teamEntry `json:"teams"`
	Guidance    string      `json:"guidance"`
}

// teamEntry represents a single team in the output.
type teamEntry struct {
	TeamID   string `json:"team_id"`
	Name     string `json:"name"`
	Slug     string `json:"slug,omitempty"`
	Primary  bool   `json:"primary"`
	LastSync string `json:"last_sync,omitempty"`
	Path     string `json:"path"`
}

// teams command styles — follows status.go Tufte-inspired pattern
var (
	teamsHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(cli.ColorPrimary)

	teamsNameStyle = lipgloss.NewStyle().
			Bold(true)

	teamsPrimaryBadge = lipgloss.NewStyle().
				Foreground(cli.ColorSuccess)

	teamsLabelStyle = lipgloss.NewStyle().
			Foreground(cli.ColorDim)

	teamsValueStyle = lipgloss.NewStyle().
			Foreground(cli.ColorDim)

	teamsPathStyle = lipgloss.NewStyle().
			Foreground(cli.ColorAccent)

	teamsHintStyle = lipgloss.NewStyle().
			Foreground(cli.ColorDim)

	teamsCommandStyle = lipgloss.NewStyle().
				Foreground(cli.ColorSecondary)
)

var teamsCmd = &cobra.Command{
	Use:   "teams",
	Short: "List teams you belong to",
	Long:  `List all teams available to you, showing which is primary for this repo.`,
	RunE:  runTeams,
}

func init() {
	teamsCmd.Flags().Bool("json", false, "Output as JSON")
}

func runTeams(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	out := cmd.OutOrStdout()

	projectRoot, err := findProjectRoot()
	if err != nil {
		if jsonMode {
			return json.NewEncoder(out).Encode(teamsOutput{
				Teams:    []teamEntry{},
				Guidance: "Run 'ox init' to set up a project.",
			})
		}
		fmt.Fprintln(out, "Not in a SageOx project.")
		fmt.Fprintln(out, "Run 'ox init' to set up.")
		return nil
	}

	// identify primary team for this repo
	var primaryTeamID string
	projectCfg, err := config.LoadProjectConfig(projectRoot)
	if err == nil && projectCfg != nil {
		primaryTeamID = projectCfg.TeamID
	}

	entries := discoverTeams(projectRoot, primaryTeamID, projectCfg)
	if len(entries) == 0 {
		if jsonMode {
			return json.NewEncoder(out).Encode(teamsOutput{
				PrimaryTeam: primaryTeamID,
				Teams:       []teamEntry{},
				Guidance:    "No teams found. Run 'ox login' then 'ox init' to set up.",
			})
		}
		fmt.Fprintln(out, "No teams found.")
		fmt.Fprintln(out, "Run 'ox login' then 'ox init' to set up.")
		return nil
	}

	if jsonMode {
		output := teamsOutput{
			PrimaryTeam: primaryTeamID,
			Teams:       entries,
			Guidance:    "Use 'ox agent team-ctx <slug>' to read a team's context.",
		}
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(output)
	}

	// compute root dir — path is always root/team_id so no need to show per-team
	ep := endpoint.GetForProject(projectRoot)
	var rootDir string
	if ep != "" {
		rootDir = paths.TeamsDataDir(ep)
	}

	// compute column widths from data
	nameW, idW, slugW, syncW := len("Name"), len("ID"), len("Slug"), len("Sync")
	for _, e := range entries {
		name := e.Name
		if e.Primary {
			name += " (this repo)"
		}
		if len(name) > nameW {
			nameW = len(name)
		}
		if len(e.TeamID) > idW {
			idW = len(e.TeamID)
		}
		if len(e.Slug) > slugW {
			slugW = len(e.Slug)
		}
		if len(e.LastSync) > syncW {
			syncW = len(e.LastSync)
		}
	}

	// human output — aligned columns
	fmt.Fprintln(out, teamsHeaderStyle.Render("Teams"))
	fmt.Fprintln(out, teamsHeaderStyle.Render(strings.Repeat("─", 5)))
	if ep != "" && !endpoint.IsProduction(ep) {
		fmt.Fprintf(out, "  %s %s\n", teamsLabelStyle.Render("Endpoint:"), teamsValueStyle.Render(endpoint.NormalizeSlug(ep)))
	}

	// padRight pads raw text then applies style (ANSI codes break %-*s)
	padRight := func(s string, w int) string {
		if len(s) < w {
			return s + strings.Repeat(" ", w-len(s))
		}
		return s
	}

	// header row
	fmt.Fprintf(out, "  %s  %s  %s  %s  %s\n",
		" ",
		teamsLabelStyle.Render(padRight("Name", nameW)),
		teamsLabelStyle.Render(padRight("ID", idW)),
		teamsLabelStyle.Render(padRight("Slug", slugW)),
		teamsLabelStyle.Render("Sync"),
	)

	// data rows
	for _, e := range entries {
		marker := " "
		rawName := e.Name
		if e.Primary {
			marker = teamsPrimaryBadge.Render("★")
			rawName = e.Name + " (this repo)"
		}

		// style name: bold the team name, green the badge
		styledName := teamsNameStyle.Render(e.Name)
		if e.Primary {
			styledName = teamsNameStyle.Bold(true).Render(e.Name) + " " + teamsPrimaryBadge.Render("(this repo)")
		}
		// pad based on raw length, append spaces after styled text
		if pad := nameW - len(rawName); pad > 0 {
			styledName += strings.Repeat(" ", pad)
		}

		fmt.Fprintf(out, "  %s  %s  %s  %s  %s\n",
			marker,
			styledName,
			teamsValueStyle.Render(padRight(e.TeamID, idW)),
			teamsValueStyle.Render(padRight(e.Slug, slugW)),
			teamsValueStyle.Render(e.LastSync),
		)
	}

	if rootDir != "" {
		fmt.Fprintf(out, "\n  %s %s\n", teamsLabelStyle.Render("Path:"), teamsPathStyle.Render(rootDir+"/<team_id>"))
	}
	fmt.Fprintf(out, "  %s %s\n",
		teamsHintStyle.Render("Read:"),
		teamsCommandStyle.Render("ox agent team-ctx <slug>"),
	)

	return nil
}

// discoverTeams builds the team list, preferring daemon status (has names/slugs)
// and falling back to filesystem scan via FindAllTeamContexts.
func discoverTeams(projectRoot, primaryTeamID string, projectCfg *config.ProjectConfig) []teamEntry {
	// try daemon first — it has team names, slugs, and accurate sync times
	if entries := teamsFromDaemon(primaryTeamID, projectCfg); len(entries) > 0 {
		return entries
	}

	// fallback: filesystem scan (no names for most teams)
	return teamsFromFilesystem(projectRoot, primaryTeamID, projectCfg)
}

// teamsFromDaemon queries the running daemon for team context workspaces.
func teamsFromDaemon(primaryTeamID string, projectCfg *config.ProjectConfig) []teamEntry {
	client := daemon.NewClientWithTimeout(500 * time.Millisecond)
	status, err := client.Status()
	if err != nil || status == nil {
		return nil
	}

	tcWorkspaces, ok := status.Workspaces["team-context"]
	if !ok || len(tcWorkspaces) == 0 {
		return nil
	}

	var primary, others []teamEntry
	for _, ws := range tcWorkspaces {
		teamID := ws.TeamID
		if teamID == "" {
			teamID = ws.ID
		}

		name := ws.TeamName
		if name == "" && teamID == primaryTeamID && projectCfg != nil {
			name = projectCfg.TeamName
		}
		if name == "" {
			name = teamID
		}

		slug := ws.TeamSlug
		if slug == "" {
			slug = api.DeriveSlug(name)
		}
		if slug == "" {
			slug = teamID
		}

		sync := "unknown"
		if !ws.LastSync.IsZero() {
			sync = formatAge(time.Since(ws.LastSync))
		}

		entry := teamEntry{
			TeamID:   teamID,
			Name:     name,
			Slug:     slug,
			Primary:  teamID == primaryTeamID,
			LastSync: sync,
			Path:     ws.Path,
		}

		if entry.Primary {
			primary = append(primary, entry)
		} else {
			others = append(others, entry)
		}
	}

	return append(primary, others...)
}

// teamsFromFilesystem uses FindAllTeamContexts (filesystem scan fallback).
func teamsFromFilesystem(projectRoot, primaryTeamID string, projectCfg *config.ProjectConfig) []teamEntry {
	allTeams := config.FindAllTeamContexts(projectRoot)
	if len(allTeams) == 0 {
		return nil
	}

	var primary, others []teamEntry
	for _, tc := range allTeams {
		teamName := tc.TeamName
		if teamName == "" && tc.TeamID == primaryTeamID && projectCfg != nil {
			teamName = projectCfg.TeamName
		}

		slug := tc.Slug
		if slug == "" {
			slug = api.DeriveSlug(teamName)
		}
		if slug == "" {
			slug = tc.TeamID
		}

		name := teamName
		if name == "" {
			name = tc.TeamID
		}

		sync := teamContextAge(tc.Path)
		if sync == "" {
			if tc.HasLastSync() {
				sync = formatAge(time.Since(tc.LastSync))
			} else {
				sync = "unknown"
			}
		}

		entry := teamEntry{
			TeamID:   tc.TeamID,
			Name:     name,
			Slug:     slug,
			Primary:  tc.TeamID == primaryTeamID,
			LastSync: sync,
			Path:     tc.Path,
		}

		if entry.Primary {
			primary = append(primary, entry)
		} else {
			others = append(others, entry)
		}
	}

	return append(primary, others...)
}
