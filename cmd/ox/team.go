package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
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

// teamCmd is the single canonical home for everything about teams. `ox teams`
// (plural) is a back-compat alias; a bare `ox team`/`ox teams` lists the teams
// you belong to, and `ox team <verb>` inspects or acts on one team.
//
// The RunE is a strict dispatcher, not a catch-all: a bare invocation lists,
// but an unrecognized token errors instead of silently swallowing the argument.
// That closes the old trap where `ox teams members` printed the whole team list
// and ignored `members`.
var teamCmd = &cobra.Command{
	Use:     "team",
	Aliases: []string{"teams"},
	Short:   "Work with your teams and coworkers",
	Long: `Work with your teams and coworkers.

A bare 'ox team' lists the teams you belong to. Each team owns a Team Context —
its permanent conversation store of recordings, discussions, sessions, and
shared memory. That is not a knowledge bubble ('ox kb list' lists those; see ox
ADR-028 for the distinction).

Commands:
  list       List the teams you belong to
  members    List a team's coworkers (humans and AI coworkers)
  show       Show one team's details
  open       Open a team's dashboard in the browser
  invite     Invite people to a team by email`,
	Args: cobra.ArbitraryArgs,
	RunE: runTeamDispatch,
}

// runTeamDispatch makes a bare `ox team` list, while an unknown subcommand token
// fails loudly rather than falling through to the lister — a valid subcommand is
// routed by cobra before RunE is ever reached.
func runTeamDispatch(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return runTeams(cmd, args)
	}
	return fmt.Errorf("unknown subcommand %q for %q\nRun 'ox team --help' to see available commands", args[0], cmd.CommandPath())
}

var teamListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the teams you belong to",
	Long: `List the teams you belong to and their Team Contexts.

A Team Context is the team's permanent conversation store — recordings,
discussions, sessions, and shared memory. It is not a knowledge bubble
(` + "`ox kb list`" + ` lists those; see ox ADR-028 for the distinction).`,
	RunE: runTeams,
}

var teamMembersCmd = &cobra.Command{
	Use:     "members",
	Aliases: []string{"coworkers"},
	Short:   "List the team's coworkers (humans and AI coworkers)",
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

var teamShowCmd = &cobra.Command{
	Use:   "show [team]",
	Short: "Show one team's details",
	Long: `Show a single team: its identity, where its Team Context lives on disk, how
recently it synced, and how many coworkers it has.

Name the team as a positional argument or with --team; with neither, shows this
repo's team.

Example:
  ox team show
  ox team show acme
  ox team show --team acme
  ox team show --json`,
	Args: cobra.MaximumNArgs(1),
	RunE: runTeamShow,
}

var teamOpenCmd = &cobra.Command{
	Use:   "open [team]",
	Short: "Open a team's dashboard in the browser",
	Long: `Open the SageOx web dashboard for a team.

Name the team as a positional argument or with --team; with neither, opens this
repo's team.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runTeamOpen,
}

func init() {
	teamListCmd.Flags().Bool("json", false, "Output as JSON")

	teamMembersCmd.Flags().String("team", "", "Team ID or slug to use (defaults to this repo's team)")
	teamMembersCmd.Flags().Bool("json", false, "Output as JSON")

	teamShowCmd.Flags().String("team", "", "Team ID or slug to show (defaults to this repo's team)")
	teamShowCmd.Flags().Bool("json", false, "Output as JSON")

	teamOpenCmd.Flags().String("team", "", "Team ID or slug to open (defaults to this repo's team)")
	teamOpenCmd.Flags().String("endpoint", "", "SageOx endpoint URL (for multi-endpoint repos)")

	// A bare `ox team`/`ox teams` lists, so the parent needs the list flag too.
	teamCmd.Flags().Bool("json", false, "Output as JSON")

	teamCmd.AddCommand(teamListCmd)
	teamCmd.AddCommand(teamMembersCmd)
	teamCmd.AddCommand(teamShowCmd)
	teamCmd.AddCommand(teamOpenCmd)
	teamCmd.AddCommand(inviteCmd) // re-homed: canonical `ox team invite` (invite.go)

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

// teamCard is the resolved set of fields `ox team show` renders for one team.
type teamCard struct {
	teamID   string
	name     string
	slug     string
	primary  bool
	path     string
	lastSync string
}

// teamRefFromArgs picks the team reference `ox team show`/`open` acts on. A
// positional argument wins over --team so `ox team show acme` (advertised by the
// list hints) is honored rather than silently ignored; with neither, the repo's
// primary team is used.
func teamRefFromArgs(cmd *cobra.Command, args []string) string {
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		return strings.TrimSpace(args[0])
	}
	ref, _ := cmd.Flags().GetString("team")
	return strings.TrimSpace(ref)
}

// validTeamRef rejects references that survive URL escaping and still change
// which /team/<ref> route the request reaches (dot segments, path separators,
// control bytes). Mirrors internal/api validateTeamRef and ListTeamRoster's
// guard so a stray value can neither retarget an endpoint nor corrupt terminal
// output. A real team id/slug never contains any of these.
func validTeamRef(ref string) bool {
	switch strings.TrimSpace(ref) {
	case "", ".", "..":
		return false
	}
	if strings.ContainsAny(ref, "/\\") {
		return false
	}
	for _, r := range ref {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// resolveTeamCard picks the team `ox team show` describes: the given ref when
// present (resolved from local state when synced, else a thin card the caller
// validates against the server), otherwise this repo's primary team. The bool
// reports whether the card came from local state — an unresolved ref must be
// server-confirmed before it is rendered as a real team.
func resolveTeamCard(projectRoot, teamRef string) (teamCard, bool, error) {
	if teamRef != "" {
		if t := resolveTeamByQuery(projectRoot, teamRef); t != nil {
			return teamCardFromEnriched(*t), true, nil
		}
		// Not synced locally — the server is the authority on whether this team
		// exists and who belongs to it. runTeamShow confirms it via the roster
		// before rendering, so a typo cannot print a plausible-looking card.
		return teamCard{teamID: teamRef, name: teamRef, slug: teamRef}, false, nil
	}
	for _, t := range discoverAllTeams(projectRoot) {
		if t.Primary {
			return teamCardFromEnriched(t), true, nil
		}
	}
	if tc := config.FindRepoTeamContext(projectRoot); tc != nil {
		return teamCard{teamID: tc.TeamID, name: tc.TeamName, slug: tc.Slug, path: tc.Path, primary: true}, true, nil
	}
	return teamCard{}, false, fmt.Errorf("no team configured; run 'ox init' or pass a team")
}

func teamCardFromEnriched(t enrichedTeam) teamCard {
	sync := "never"
	if !t.LastSync.IsZero() {
		sync = formatAge(time.Since(t.LastSync))
	}
	return teamCard{
		teamID:   t.TeamID,
		name:     t.Name,
		slug:     t.Slug,
		primary:  t.Primary,
		path:     t.Path,
		lastSync: sync,
	}
}

func runTeamShow(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")

	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}
	ep := endpoint.GetForProject(projectRoot)

	teamRef := teamRefFromArgs(cmd, args)
	if teamRef != "" && !validTeamRef(teamRef) {
		return fmt.Errorf("invalid team reference %q", teamRef)
	}

	card, local, err := resolveTeamCard(projectRoot, teamRef)
	if err != nil {
		return err
	}

	// One roster read serves two ends: the coworker count and, for a ref we
	// could not resolve locally, server confirmation that the team is real and
	// that the caller belongs to it. The typed error tells "not a member"
	// (reject) apart from "can't tell" (feature off / offline / unauthenticated).
	resp, rosterErr := teamRoster(projectRoot, ep, card.teamID)
	if teamRef != "" && !local {
		if errors.Is(rosterErr, api.ErrForbidden) {
			return fmt.Errorf("you are not a member of team %q (or it does not exist); run 'ox teams' to list your teams", teamRef)
		}
		if resp != nil && resp.TeamID != "" {
			card.teamID = resp.TeamID // canonical id from the server
		}
	}
	count, countKnown := rosterCount(resp)

	dashboard := ""
	if ep != "" && card.teamID != "" {
		dashboard = fmt.Sprintf("%s/team/%s", strings.TrimRight(ep, "/"), url.PathEscape(card.teamID))
	}

	if jsonMode {
		return writeTeamShowJSON(cmd.OutOrStdout(), card, count, countKnown, dashboard)
	}
	renderTeamShow(cmd.OutOrStdout(), card, count, countKnown, dashboard)
	return nil
}

// teamRoster fetches a team's roster, returning the response alongside the typed
// error so callers can distinguish "not a member" (api.ErrForbidden) from the
// benign "can't tell" states — no auth, feature off, server unreachable — which
// all return a nil error and nil response so the caller degrades gracefully.
func teamRoster(projectRoot, ep, teamRef string) (*api.TeamRosterResponse, error) {
	if teamRef == "" {
		return nil, nil
	}
	token, err := auth.EnsureValidTokenForEndpoint(ep, 300)
	if err != nil || token == nil || token.AccessToken == "" {
		return nil, nil
	}
	client := api.NewRepoClientForProject(projectRoot).WithAuthToken(token.AccessToken)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	return client.ListTeamRoster(ctx, teamRef)
}

// rosterCount derives the coworker count from a roster response, reporting
// (0, false) when the roster is absent so an unknown count never renders as a
// misleading "0".
func rosterCount(resp *api.TeamRosterResponse) (int, bool) {
	if resp == nil {
		return 0, false
	}
	if resp.Total > 0 {
		return resp.Total, true
	}
	return len(resp.Members), true
}

func writeTeamShowJSON(w io.Writer, c teamCard, count int, countKnown bool, dashboard string) error {
	env := struct {
		TeamID             string `json:"team_id"`
		Name               string `json:"name"`
		Slug               string `json:"slug,omitempty"`
		Primary            bool   `json:"primary"`
		CoworkerCount      int    `json:"coworker_count"`
		CoworkersAvailable bool   `json:"coworkers_available"`
		ContextPath        string `json:"context_path,omitempty"`
		LastSync           string `json:"last_sync,omitempty"`
		DashboardURL       string `json:"dashboard_url,omitempty"`
	}{
		TeamID:             c.teamID,
		Name:               c.name,
		Slug:               c.slug,
		Primary:            c.primary,
		CoworkerCount:      count,
		CoworkersAvailable: countKnown,
		ContextPath:        c.path,
		LastSync:           c.lastSync,
		DashboardURL:       dashboard,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}

func renderTeamShow(w io.Writer, c teamCard, count int, countKnown bool, dashboard string) {
	name := cli.SanitizeTerminalText(c.name)
	if name == "" {
		name = cli.SanitizeTerminalText(c.teamID)
	}

	fmt.Fprintln(w, teamsHeaderStyle.Render(name))
	fmt.Fprintln(w, teamsHeaderStyle.Render(strings.Repeat("─", len(name))))

	kv := func(label, val string) {
		if val == "" {
			return
		}
		fmt.Fprintf(w, "  %s  %s\n", teamsLabelStyle.Render(fmt.Sprintf("%-10s", label)), val)
	}

	teamValue := teamsNameStyle.Render(name)
	if c.primary {
		teamValue += " " + teamsPrimaryBadge.Render("(this repo)")
	}
	kv("Team", teamValue)
	kv("ID", teamsValueStyle.Render(cli.SanitizeTerminalText(c.teamID)))
	kv("Slug", teamsValueStyle.Render(cli.SanitizeTerminalText(c.slug)))

	coworkers := "unavailable"
	if countKnown {
		coworkers = fmt.Sprintf("%d", count)
	}
	kv("Coworkers", teamsValueStyle.Render(coworkers))

	sync := c.lastSync
	if sync == "" {
		sync = "never"
	}
	kv("Sync", teamsValueStyle.Render(sync))
	kv("Path", teamsPathStyle.Render(cli.SanitizeTerminalText(c.path)))
	kv("Dashboard", teamsPathStyle.Render(cli.SanitizeTerminalText(dashboard)))

	fmt.Fprintf(w, "\n  %s %s\n", teamsHintStyle.Render("Coworkers:"), teamsCommandStyle.Render("ox team members"))
}

func runTeamOpen(cmd *cobra.Command, args []string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a SageOx project: %w", err)
	}
	cfg, err := config.LoadProjectConfig(projectRoot)
	if err != nil {
		return fmt.Errorf("failed to load project config: %w", err)
	}

	teamRef := teamRefFromArgs(cmd, args)
	if teamRef != "" && !validTeamRef(teamRef) {
		return fmt.Errorf("invalid team reference %q", teamRef)
	}
	teamID := cfg.TeamID
	if teamRef != "" {
		if t := resolveTeamByQuery(projectRoot, teamRef); t != nil {
			teamID = t.TeamID
		} else {
			teamID = teamRef
		}
	}
	if teamID == "" {
		fmt.Fprintln(cmd.OutOrStdout(), "No team found. Run 'ox init' first to register this repository.")
		return nil
	}

	flagEndpoint, _ := cmd.Flags().GetString("endpoint")
	endpointURL, err := resolveEndpoint(projectRoot, cfg.GetEndpoint(), endpoint.NormalizeEndpoint(flagEndpoint))
	if err != nil {
		return err
	}

	dashURL := fmt.Sprintf("%s/team/%s", strings.TrimRight(endpointURL, "/"), url.PathEscape(teamID))
	fmt.Fprintf(cmd.OutOrStdout(), "Opening %s\n", cli.SanitizeTerminalText(dashURL))
	if err := cli.OpenInBrowser(dashURL); err != nil {
		if errors.Is(err, cli.ErrHeadless) {
			fmt.Fprintf(cmd.OutOrStdout(), "Visit: %s\n", cli.SanitizeTerminalText(dashURL))
			return nil
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s Could not open browser. Visit: %s\n", cli.StyleWarning.Render("!"), cli.SanitizeTerminalText(dashURL))
	}
	return nil
}
