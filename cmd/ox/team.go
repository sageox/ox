package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/spf13/cobra"
)

// memberLister is the seam `ox team members` fetches through, so the
// fetch-and-render core is unit-testable against a fake without a network.
// *api.RepoClient satisfies it.
type memberLister interface {
	ListTeamRoster(ctx context.Context, teamRef string) (*api.TeamRosterResponse, error)
}

// teamCmd is the singular `ox team` group. `ox teams` (plural) already lists the
// teams you belong to; `ox team <subcommand>` inspects one team.
var teamCmd = &cobra.Command{
	Use:   "team",
	Short: "Inspect your team",
	Long: `Inspect your team.

Commands:
  members   List the team's coworkers (humans and AI coworkers)`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

var teamMembersCmd = &cobra.Command{
	Use:   "members",
	Short: "List the team's coworkers (humans and AI coworkers)",
	Long: `List the coworkers on your team — humans and AI coworkers alike.

Shows each coworker's display name, type, role, and the handles they're known
by (e.g. a GitHub login). The roster answers "who is this?" and renders the
identity fields the server exposes.

The roster is feature-flag gated on the server. When the flag is off (or the
server is older), the command reports that the capability is unavailable rather
than failing.

Example:
  ox team members
  ox team members --team acme
  ox team members --json`,
	RunE: runTeamMembers,
}

func init() {
	teamMembersCmd.Flags().String("team", "", "Team ID or slug to use (defaults to this repo's team)")
	teamMembersCmd.Flags().Bool("json", false, "Output as JSON")

	teamCmd.AddCommand(teamMembersCmd)
	teamCmd.GroupID = "teams"
	rootCmd.AddCommand(teamCmd)
}

// resolveTeamRef returns the team ref to query and a human display label.
// --team wins (resolved locally, else passed through for the server to resolve
// as a slug); otherwise the repo's configured team is used.
func resolveTeamRef(cmd *cobra.Command, projectRoot string) (ref, label string, err error) {
	teamFlag, _ := cmd.Flags().GetString("team")
	if teamFlag != "" {
		if t := resolveTeamByQuery(projectRoot, teamFlag); t != nil {
			tc := t.toConfigTeamContext()
			return tc.TeamID, teamLabelFor(tc), nil
		}
		// not found locally — let the server resolve the slug/id
		return teamFlag, teamFlag, nil
	}
	tc := config.FindRepoTeamContext(projectRoot)
	if tc == nil {
		return "", "", fmt.Errorf("no team configured; run 'ox init' or pass --team")
	}
	return tc.TeamID, teamLabelFor(tc), nil
}

func teamLabelFor(tc *config.TeamContext) string {
	switch {
	case tc.TeamName != "":
		return tc.TeamName
	case tc.Slug != "":
		return tc.Slug
	default:
		return tc.TeamID
	}
}

func runTeamMembers(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")

	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}
	ep := endpoint.GetForProject(projectRoot)

	teamRef, label, err := resolveTeamRef(cmd, projectRoot)
	if err != nil {
		return err
	}

	// The roster is a team-scoped read: require a valid token up front and fail
	// fast with a friendly hint, matching `ox query` / `ox invite`.
	// EnsureValidTokenForEndpoint also proactively refreshes a near-expired token
	// (GetTokenForEndpoint would have sent it stale and eaten a 401).
	token, err := auth.EnsureValidTokenForEndpoint(ep, 300)
	if err != nil || token == nil || token.AccessToken == "" {
		return fmt.Errorf("not authenticated — run 'ox login' first")
	}
	client := api.NewRepoClientForProject(projectRoot).WithAuthToken(token.AccessToken)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return fetchAndRenderRoster(ctx, cmd.OutOrStdout(), client, teamRef, label, jsonMode)
}

// fetchAndRenderRoster is the testable core: it fetches the roster and renders
// it, degrading gracefully (exit 0) when the server reports the feature is
// unavailable. Auth / forbidden / version errors are surfaced as real errors.
func fetchAndRenderRoster(ctx context.Context, w io.Writer, lister memberLister, teamRef, label string, jsonMode bool) error {
	resp, err := lister.ListTeamRoster(ctx, teamRef)
	if err != nil {
		// Degrade gracefully (exit 0) when the roster can't be served: either the
		// feature/route doesn't exist (404) or the server is down/unreachable
		// (transport failure or 5xx). The roster is informational — a missing
		// feature or a reachability blip shouldn't be a hard failure. Auth,
		// forbidden, and version errors still surface: those need user action.
		if errors.Is(err, api.ErrTeamRosterUnsupported) || errors.Is(err, api.ErrTeamRosterUnavailable) {
			if jsonMode {
				return writeRosterJSON(w, nil, false)
			}
			msg := "Team roster is unavailable (feature not enabled on this server, or team not found)."
			if errors.Is(err, api.ErrTeamRosterUnavailable) {
				msg = "Team roster is unavailable right now — couldn't reach the server."
			}
			fmt.Fprintf(w, "%s %s\n", cli.Styles.Info.Render("ℹ"), msg)
			return nil
		}
		// ErrUnauthorized / ErrForbidden / ErrVersionUnsupported / other → real error
		return err
	}

	if jsonMode {
		return writeRosterJSON(w, resp, true)
	}

	renderRosterTable(w, resp, label)
	return nil
}

// writeRosterJSON emits ONE stable envelope for both success and graceful
// degrade so a JSON consumer (often an AI coworker) can rely on the shape:
// `members` is always an array (never null) and `available` is always present.
func writeRosterJSON(w io.Writer, resp *api.TeamRosterResponse, available bool) error {
	env := struct {
		TeamID    string           `json:"team_id"`
		Members   []api.TeamMember `json:"members"`
		Total     int              `json:"total"`
		Available bool             `json:"available"`
	}{Members: []api.TeamMember{}, Available: available}
	if resp != nil {
		env.TeamID = resp.TeamID
		env.Total = resp.Total
		if resp.Members != nil {
			env.Members = resp.Members
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}

func renderRosterTable(w io.Writer, resp *api.TeamRosterResponse, label string) {
	label = cli.SanitizeTerminalText(label)
	if resp == nil || len(resp.Members) == 0 {
		fmt.Fprintf(w, "%s No coworkers found in %s.\n", cli.Styles.Info.Render("ℹ"), label)
		return
	}

	header := fmt.Sprintf("Team Coworkers (%s)", label)
	fmt.Fprintln(w, cli.StyleGroupHeader.Render(header))
	fmt.Fprintln(w, cli.StyleDim.Render(strings.Repeat("─", len(header))))

	// aligned columns: name, type, role. Aliases (variable length) trail the row.
	rows := make([][]string, 0, len(resp.Members))
	aliases := make([]string, 0, len(resp.Members))
	for _, m := range resp.Members {
		// Every field is untrusted server text: sanitize each before render so
		// no column (m.Type was the gap) can carry ANSI/control bytes to the TTY.
		name := cli.SanitizeTerminalText(m.Name)
		if name == "" {
			name = cli.SanitizeTerminalText(m.PrincipalID)
		}
		typ := cli.SanitizeTerminalText(m.Type)
		if typ == "" {
			typ = "human"
		}
		rows = append(rows, []string{name, typ, cli.SanitizeTerminalText(m.Role)})
		aliases = append(aliases, cli.SanitizeTerminalText(strings.Join(m.Aliases, ", ")))
	}
	widths := cli.ColumnWidths(rows, []int{16, 5, 6}, []int{32, 6, 10})

	for i, row := range rows {
		name := fmt.Sprintf("%-*s", widths[0], row[0])
		typ := fmt.Sprintf("%-*s", widths[1], row[1])
		role := fmt.Sprintf("%-*s", widths[2], row[2])
		line := fmt.Sprintf("  %s  %s  %s",
			cli.StyleCalloutBold.Render(name),
			cli.StyleDim.Render(typ),
			cli.StyleDim.Render(role))
		if aliases[i] != "" {
			line += "  " + cli.StyleDim.Render(aliases[i])
		}
		fmt.Fprintln(w, line)
	}
}
