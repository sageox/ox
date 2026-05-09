package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/kb"
	"github.com/spf13/cobra"
)

// kb_list.go — `ox kb list` subcommand. Fans out via the F3 three-source
// merger (internal/kb.Merger) and renders a unified table of bubbles to the
// user. The merger owns kb-API + legacy team-context + legacy ledger fan-out
// and dedup; this file is purely the CLI shell + presentation.
//
// Design notes:
//   - The command wires up production sources only inside runKBList; the
//     core rendering logic in renderKBListResult takes a pre-built
//     MergeResult so it stays trivial to unit-test.
//   - The merger interface (kbListMerger) is intentionally narrow — one
//     method, the same shape Merger.Merge has — so tests can inject a fake
//     without standing up httptest servers.

// kbListMerger is the seam between runKBList and the F3 three-source merger.
// Production wires *kb.Merger; tests provide a fake. Mirrors the surface of
// kb.Merger.Merge so swapping in the real one is a one-line construction.
type kbListMerger interface {
	Merge(ctx context.Context) (kb.MergeResult, error)
}

var kbListCmd = &cobra.Command{
	Use:   "list",
	Short: "List knowledge bubbles you can access",
	Long: `List the knowledge bubbles available to you across all sources.

Merges three sources concurrently:
  - The kb API (/api/v1/kb) — the new typed knowledge-bubble surface
  - Legacy team contexts (/api/v1/cli/repos)
  - Local ledger registry

Legacy team contexts and ledgers surface here too — they're synthesized from
the per-source data and shown alongside new kb-API rows so the list is
complete during the migration window.

Examples:
  ox kb list                  # all bubbles
  ox kb list --type=team      # only team bubbles (legacy + new)
  ox kb list --json           # scriptable JSON output`,
	Args: cobra.NoArgs,
	RunE: runKBList,
}

func init() {
	kbCmd.AddCommand(kbListCmd)
	kbListCmd.Flags().String("type", "", "filter by kb type: personal|profile|team|repo|custom")
}

// kbListJSONOutput is the JSON envelope. Bubbles is the merged + filtered
// list; Warnings is one entry per source that errored non-fatally.
type kbListJSONOutput struct {
	Bubbles  []kbListJSONBubble  `json:"bubbles"`
	Warnings []kbListJSONWarning `json:"warnings"`
}

// kbListJSONBubble mirrors kb.Bubble for JSON. Defined separately so the
// JSON tags are stable even if the merger struct gains internal-only fields.
type kbListJSONBubble struct {
	KBID       string `json:"kb_id,omitempty"`
	Type       string `json:"type"`
	Slug       string `json:"slug,omitempty"`
	Name       string `json:"name,omitempty"`
	ViewerRole string `json:"viewer_role,omitempty"`
	LocalPath  string `json:"local_path,omitempty"`
	RepoURL    string `json:"repo_url,omitempty"`
	RepoID     string `json:"repo_id,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`
	Source     string `json:"source"`
	Legacy     bool   `json:"legacy"`
}

type kbListJSONWarning struct {
	Source string `json:"source"`
	Error  string `json:"error"`
}

func runKBList(cmd *cobra.Command, args []string) error {
	typeFilter, _ := cmd.Flags().GetString("type")
	jsonOutput, _ := cmd.Root().PersistentFlags().GetBool("json")

	projectRoot, _ := findProjectRoot()
	merger := newDefaultKBListMerger(projectRoot)

	ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
	defer cancel()

	res, err := merger.Merge(ctx)
	if err != nil {
		return fmt.Errorf("kb list: merge failed: %w", err)
	}

	return renderKBListResult(cmd.OutOrStdout(), res, typeFilter, jsonOutput, projectRoot)
}

// newDefaultKBListMerger wires up the three production sources for the merger.
// Pulled out so tests can swap the whole merger via a fake without touching
// auth/credential plumbing.
func newDefaultKBListMerger(projectRoot string) kbListMerger {
	ep := endpoint.Get()
	if projectRoot != "" {
		ep = endpoint.GetForProject(projectRoot)
	}

	// kb-API source — authenticated when a token is available; the kb client
	// translates 403/404 into ErrKBAPIUnavailable which the merger treats as
	// "0 rows from this source", not a warning.
	kbClient := api.NewKBClientWithEndpoint(ep)
	if token, err := auth.GetTokenForEndpoint(ep); err == nil && token != nil && token.AccessToken != "" {
		kbClient = kbClient.WithAuthToken(token.AccessToken)
	}

	// legacy sources — defined inline to keep the production wiring close to
	// the construction site. Each adapter is small enough that hoisting it
	// to a top-level type would obscure rather than clarify.
	teams := &kbListTeamSource{projectRoot: projectRoot, endpoint: ep}
	ledger := &kbListLedgerSource{projectRoot: projectRoot}

	return kb.NewMerger(kbClient, teams, ledger)
}

// kbListTeamSource adapts the existing team-discovery pipeline to the
// merger's LegacyTeamSource interface.
type kbListTeamSource struct {
	projectRoot string
	endpoint    string
}

func (s *kbListTeamSource) ListTeamContexts(_ context.Context) ([]kb.LegacyTeamRow, string, error) {
	var teams []enrichedTeam
	if s.projectRoot != "" {
		teams = discoverAllTeams(s.projectRoot)
	}
	if len(teams) == 0 {
		teams = discoverTeamsGlobal()
	}
	rows := make([]kb.LegacyTeamRow, 0, len(teams))
	for _, t := range teams {
		rows = append(rows, kb.LegacyTeamRow{
			TeamID:   t.TeamID,
			Name:     t.Name,
			Slug:     t.Slug,
			LocalDir: t.Path,
		})
	}
	return rows, s.endpoint, nil
}

// kbListLedgerSource adapts the local ledger registry to the merger's
// LedgerSource interface. Today the implementation is intentionally minimal:
// it surfaces the project's own ledger when discoverable, and leaves
// cross-project ledger enumeration to a future iteration. The merger will
// dedup against any kb-API row that claims the same repo_id, so partial
// coverage here doesn't cause double-listing.
type kbListLedgerSource struct {
	projectRoot string
}

func (s *kbListLedgerSource) ListLedgers(_ context.Context) ([]kb.LegacyLedgerRow, error) {
	if s.projectRoot == "" {
		return nil, nil
	}
	cfg, err := config.LoadProjectConfig(s.projectRoot)
	if err != nil || cfg == nil || cfg.RepoID == "" {
		return nil, nil
	}
	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return nil, nil
	}
	// ProjectConfig doesn't carry the repo's display name/slug — those live
	// only on the server today. Surface the project label as a best-effort
	// name so the row isn't blank; slug is left empty (the merger uses
	// (slug + endpoint) only as a tertiary dedup key).
	name := cfg.Project
	if name == "" {
		name = cfg.RepoID
	}
	return []kb.LegacyLedgerRow{{
		RepoID:   cfg.RepoID,
		Name:     name,
		LocalDir: ledgerPath,
		Endpoint: endpoint.GetForProject(s.projectRoot),
	}}, nil
}

// renderKBListResult is the unit-testable core: it takes a pre-built
// MergeResult, applies the (post-merge) type filter, and emits either JSON
// or a human-readable table. Splitting this out lets tests skip the entire
// auth + endpoint dance and just feed bubbles in.
func renderKBListResult(w io.Writer, res kb.MergeResult, typeFilter string, jsonOutput bool, projectRoot string) error {
	bubbles := filterAndSortBubbles(res.Bubbles, typeFilter)

	if jsonOutput {
		return emitKBListJSON(w, bubbles, res.Warnings)
	}

	if len(bubbles) == 0 {
		emitKBListEmpty(w, res.Warnings, projectRoot)
		return nil
	}

	emitKBListTable(w, bubbles)

	if len(res.Warnings) > 0 {
		emitKBListWarningFooter(w, res.Warnings)
	}
	return nil
}

// filterAndSortBubbles applies the --type filter (if any) AFTER the merge
// has happened — that way a bubble appearing under both kb-API and legacy
// sources still dedups before the filter sees it. Sort order is stable on
// (type-priority, slug); legacy entries sort within their type group, after
// non-legacy.
func filterAndSortBubbles(in []kb.Bubble, typeFilter string) []kb.Bubble {
	out := make([]kb.Bubble, 0, len(in))
	for _, b := range in {
		if typeFilter != "" && string(b.Type) != typeFilter {
			continue
		}
		out = append(out, b)
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := kbTypePriority(out[i].Type), kbTypePriority(out[j].Type)
		if pi != pj {
			return pi < pj
		}
		// non-legacy before legacy within the same type bucket
		if out[i].Legacy != out[j].Legacy {
			return !out[i].Legacy
		}
		return out[i].Slug < out[j].Slug
	})
	return out
}

// kbTypePriority encodes the documented sort order. Unknown trails everything
// so forward-compat rows sink to the bottom rather than disrupting the table.
func kbTypePriority(t api.KBType) int {
	switch t {
	case api.KBTypePersonal:
		return 0
	case api.KBTypeProfile:
		return 1
	case api.KBTypeTeam:
		return 2
	case api.KBTypeRepo:
		return 3
	case api.KBTypeCustom:
		return 4
	default:
		// "" and KBTypeUnknown
		return 5
	}
}

// formatKBType renders the TYPE column. Empty/unknown types render as
// "unknown" so a forward-compat row never produces a blank column.
//
// Legacy origin (team-context list / ledger registry vs the new kb API) is
// tracked on the Bubble struct (Legacy bool) and used for sort-stability,
// but the rendered TYPE column intentionally omits it for now — keeps the
// column narrow during the migration window. Re-enable the suffix later
// once we want users to see migration progress at a glance.
func formatKBType(t api.KBType, legacy bool) string {
	_ = legacy
	base := string(t)
	if base == "" || t == api.KBTypeUnknown {
		base = "unknown"
	}
	return base
}

// kb-list lipgloss styles — kept local to this file rather than exported
// from cli.Styles, since the murmur list does the same and we want each
// command's table styling close to its renderer.
var (
	kbListHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(cli.ColorPrimary)

	kbListTypeStyle = lipgloss.NewStyle().
			Foreground(cli.ColorAccent)

	kbListSlugStyle = lipgloss.NewStyle().
			Foreground(cli.ColorPrimary)

	kbListNameStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#CCCCCC"))

	kbListWarnStyle = lipgloss.NewStyle().
			Foreground(cli.ColorWarning)
)

// kb-list column widths. SLUG at 24 chars covers the kebab-case slugs we
// generate; longer slugs truncate with an ellipsis to keep the table
// aligned — losing tail characters is preferable to breaking the column.
// TYPE is sized for the longest known KBType ("personal") plus a buffer
// for unknown forward-compat values; NAME absorbs the freed space so
// real-world bubble names rarely truncate.
const (
	kbListColTypeWidth = 10
	kbListColSlugWidth = 24
)

func emitKBListTable(w io.Writer, bubbles []kb.Bubble) {
	fmt.Fprintln(w)
	printKBListHeader(w)

	for _, b := range bubbles {
		printKBListRow(w, b)
	}

	fmt.Fprintln(w)
	summary := fmt.Sprintf("%d bubble(s)", len(bubbles))
	fmt.Fprintf(w, "%s %s\n", cli.StyleDim.Render("Total:"), cli.StyleDim.Render(summary))
}

// kbListColNameWidth — last column, gets the space we freed by dropping
// the ROLE column. Long names still truncate with an ellipsis so the
// rule line stays clean; slug remains the machine-readable identifier.
const kbListColNameWidth = 48

func printKBListHeader(w io.Writer) {
	typeCol := fmt.Sprintf("%-*s", kbListColTypeWidth, "TYPE")
	slugCol := fmt.Sprintf("%-*s", kbListColSlugWidth, "SLUG")
	nameCol := fmt.Sprintf("%-*s", kbListColNameWidth, "NAME")
	header := kbListHeaderStyle.Render(typeCol + slugCol + nameCol)
	fmt.Fprintln(w, "  "+header)
	fmt.Fprintln(w, "  "+cli.StyleDim.Render(strings.Repeat("-", kbListColTypeWidth+kbListColSlugWidth+kbListColNameWidth)))
}

func printKBListRow(w io.Writer, b kb.Bubble) {
	typeStr := formatKBType(b.Type, b.Legacy)
	typeCol := fmt.Sprintf("%-*s", kbListColTypeWidth, truncateForColumn(typeStr, kbListColTypeWidth))

	slugCol := fmt.Sprintf("%-*s", kbListColSlugWidth, truncateForColumn(b.Slug, kbListColSlugWidth))

	name := b.Name
	if name == "" {
		name = "-"
	}
	nameCol := fmt.Sprintf("%-*s", kbListColNameWidth, truncateForColumn(name, kbListColNameWidth))

	row := kbListTypeStyle.Render(typeCol) +
		kbListSlugStyle.Render(slugCol) +
		kbListNameStyle.Render(nameCol)
	fmt.Fprintln(w, "  "+row)
}

// truncateForColumn ellipsizes strings that exceed the column width. Drops
// the last 3 characters and replaces them with "..." so column alignment
// is preserved. Width must be >= 4 — smaller would not leave room for the
// ellipsis.
func truncateForColumn(s string, width int) string {
	if width < 4 {
		return s
	}
	// runes for safety with multi-byte names (display width is approximate
	// — fine for an ops table, not a typesetting engine).
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	return string(runes[:width-3]) + "..."
}

// emitKBListJSON writes the JSON envelope to w. Used directly when --json is
// set; outputJSON's hardcoded os.Stdout would defeat the writer abstraction
// the rest of this file uses for testability.
func emitKBListJSON(w io.Writer, bubbles []kb.Bubble, warnings []kb.SourceWarning) error {
	out := kbListJSONOutput{
		Bubbles:  make([]kbListJSONBubble, 0, len(bubbles)),
		Warnings: make([]kbListJSONWarning, 0, len(warnings)),
	}
	for _, b := range bubbles {
		out.Bubbles = append(out.Bubbles, kbListJSONBubble{
			KBID:       b.KBID,
			Type:       jsonTypeForBubble(b.Type),
			Slug:       b.Slug,
			Name:       b.Name,
			ViewerRole: b.ViewerRole,
			LocalPath:  b.LocalPath,
			RepoURL:    b.RepoURL,
			RepoID:     b.RepoID,
			Endpoint:   b.Endpoint,
			Source:     string(b.Source),
			Legacy:     b.Legacy,
		})
	}
	for _, sw := range warnings {
		out.Warnings = append(out.Warnings, kbListJSONWarning{
			Source: string(sw.Source),
			Error:  sw.Err,
		})
	}
	return writeJSONIndent(w, out)
}

// jsonTypeForBubble maps an empty or unknown KBType to the literal "unknown"
// string in JSON output. Empty strings would let consumers think the field
// is missing rather than explicitly normalized.
func jsonTypeForBubble(t api.KBType) string {
	if t == "" || t == api.KBTypeUnknown {
		return "unknown"
	}
	return string(t)
}

// emitKBListEmpty handles the no-bubbles case. When at least one source
// warned, surface the warnings + an `ox doctor` hint so the user can act.
// Otherwise default to a clean "no bubbles + how to bootstrap" message.
func emitKBListEmpty(w io.Writer, warnings []kb.SourceWarning, projectRoot string) {
	fmt.Fprintln(w)
	if len(warnings) > 0 {
		fmt.Fprintln(w, "  "+kbListWarnStyle.Render("No knowledge bubbles available; some sources errored:"))
		for _, sw := range warnings {
			fmt.Fprintf(w, "  %s %s: %s\n", kbListWarnStyle.Render("⚠"), kbListWarnStyle.Render(string(sw.Source)), sw.Err)
		}
		fmt.Fprintln(w)
		cli.PrintHint("Run 'ox doctor' to diagnose source errors.")
		return
	}

	fmt.Fprintln(w, "  "+cli.StyleDim.Render("No knowledge bubbles available."))
	fmt.Fprintln(w)

	// hint depends on context: if we're not in a project, suggest init;
	// otherwise suggest login (the most common cause of an empty list is
	// a missing token for the kb-API source).
	if projectRoot == "" {
		cli.PrintHint("Run 'ox init' inside a repo to register a knowledge bubble for it.")
	} else {
		cli.PrintHint("Run 'ox login' if you haven't authenticated this endpoint yet.")
	}
}

// emitKBListWarningFooter prints a one-line dim footer when results were
// non-empty but at least one source still errored. Keeps the table the
// primary signal; the warning is informational, not blocking.
func emitKBListWarningFooter(w io.Writer, warnings []kb.SourceWarning) {
	srcs := make([]string, 0, len(warnings))
	for _, sw := range warnings {
		srcs = append(srcs, string(sw.Source))
	}
	fmt.Fprintf(w, "%s %s\n",
		kbListWarnStyle.Render("!"),
		kbListWarnStyle.Render(fmt.Sprintf("%d source had errors (%s) — run 'ox doctor' to investigate", len(warnings), strings.Join(srcs, ","))),
	)
}

// writeJSONIndent emits indented JSON to w. We don't use outputJSON because
// it's hardcoded to os.Stdout, which makes the renderer untestable via a
// captured buffer.
func writeJSONIndent(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
