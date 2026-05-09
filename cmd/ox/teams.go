package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
	"github.com/spf13/cobra"
)

// teamsDeprecationNotice is printed once on stderr before the command runs.
// Centralized so tests can match against the same string the user sees.
//
// `ox teams` is retained for one release as an alias for `ox kb list --type=team`
// so existing scripts and muscle memory keep working through the Knowledge
// Bubbles rollout. The hint points users at the canonical command.
const teamsDeprecationNotice = "Note: 'ox teams' is deprecated. Use 'ox kb list --type=team' instead. This alias will be removed in a future release."

// teamsDeprecationJSON is the value placed in the JSON envelope's `deprecated`
// field. Tooling parsing `ox teams --json` already exists; the field is purely
// additive and signals the deprecation without breaking the legacy shape.
const teamsDeprecationJSON = "use 'ox kb list --type=team' — this alias will be removed in a future release"

// teamsOutput is the JSON output structure for ox teams.
type teamsOutput struct {
	PrimaryTeam string      `json:"primary_team"` // team ID of this repo's team
	Teams       []teamEntry `json:"teams"`
	Guidance    string      `json:"guidance"`
	// Deprecated is populated unconditionally on the deprecation alias so any
	// tooling can detect the legacy command at parse time.
	Deprecated string `json:"deprecated,omitempty"`
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
	Short: "[deprecated] alias for 'ox kb list --type=team'",
	Long: `List teams you belong to.

DEPRECATED: this command is retained as a one-release alias for
` + "`ox kb list --type=team`" + ` and will be removed in a future release. The
Knowledge Bubbles surface (` + "`ox kb`" + `) is the canonical way to enumerate
team contexts alongside personal bubbles, profiles, and ledgers.

Existing scripts that parse ` + "`ox teams --json`" + ` will continue to work — the
JSON envelope keeps its legacy shape and gains a top-level ` + "`deprecated`" + `
field signaling the move.`,
	// We intentionally do NOT set Deprecated on the cobra command: cobra's
	// docs generator hides commands with that field, and we need `ox teams`
	// to remain visible in `--help` and `docs/reference/teams.mdx` for the
	// one-release deprecation window. Runtime emits a stderr notice from
	// runTeams instead.
	RunE: runTeams,
}

func init() {
	teamsCmd.Flags().Bool("json", false, "Output as JSON")
}

func runTeams(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	out := cmd.OutOrStdout()

	// Emit the deprecation hint to stderr exactly once. Stderr keeps it
	// separate from the human/JSON listing on stdout so existing pipelines
	// and `--json` consumers aren't disturbed; the JSON envelope carries
	// its own `deprecated` field for programmatic detection.
	fmt.Fprintln(cmd.ErrOrStderr(), teamsDeprecationNotice)

	projectRoot, _ := findProjectRoot()

	var teams []enrichedTeam
	if projectRoot != "" {
		teams = discoverAllTeams(projectRoot)
	} else {
		// not in a project — discover from credentials across all endpoints
		teams = discoverTeamsGlobal()
	}

	var primaryTeamID string
	for _, t := range teams {
		if t.Primary {
			primaryTeamID = t.TeamID
			break
		}
	}

	if len(teams) == 0 {
		if jsonMode {
			return json.NewEncoder(out).Encode(teamsOutput{
				Teams:      []teamEntry{},
				Guidance:   "No teams found. Run 'ox login' then 'ox init' to set up.",
				Deprecated: teamsDeprecationJSON,
			})
		}
		fmt.Fprintln(out, "No teams found.")
		fmt.Fprintln(out, "Run 'ox login' then 'ox init' to set up.")
		return nil
	}

	entries := enrichedTeamsToEntries(teams)

	if jsonMode {
		output := teamsOutput{
			PrimaryTeam: primaryTeamID,
			Teams:       entries,
			Guidance:    "Use 'ox agent team-ctx <slug>' to read a team's context.",
			Deprecated:  teamsDeprecationJSON,
		}
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(output)
	}

	// compute root dir — path is always root/team_id so no need to show per-team
	var ep string
	if projectRoot != "" {
		ep = endpoint.GetForProject(projectRoot)
	}
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

// enrichedTeamsToEntries converts enrichedTeam slice to teamEntry slice for JSON/display.
func enrichedTeamsToEntries(teams []enrichedTeam) []teamEntry {
	entries := make([]teamEntry, 0, len(teams))
	for _, t := range teams {
		sync := "never"
		if !t.LastSync.IsZero() {
			sync = formatAge(time.Since(t.LastSync))
		}
		entries = append(entries, teamEntry{
			TeamID:   t.TeamID,
			Name:     t.Name,
			Slug:     t.Slug,
			Primary:  t.Primary,
			LastSync: sync,
			Path:     t.Path,
		})
	}
	return entries
}
