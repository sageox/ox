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
	"github.com/sageox/ox/internal/theme"
	"github.com/spf13/cobra"
)

// kb_list.go — `ox kb list` subcommand. Fetches the caller's bubbles from
// the KB API for the project's ambient scopes (the only source of bubble
// rows under ox ADR-028 — team contexts and ledgers are conversation
// stores, never presented as bubbles) and renders them.
//
// Design notes:
//   - The command wires up the production client only inside runKBList; the
//     core rendering logic in renderKBListResult takes a pre-built
//     kb.ListResult so it stays trivial to unit-test.

// kbCmd is the parent command for knowledge bubble (`ox kb …`) operations.
// Moved here from kb_path.go when `ox kb path` was removed (ox ADR-028 /
// epic ox-nsf7) — this file owns the parent now; sibling subcommand files
// attach to kbCmd in their own init() blocks.
//
// `bubble` and `bubbles` are aliases: `kb` is the canonical noun in docs,
// the longer form is for discoverability. User-facing copy says "knowledge
// bubble" — never just "bubble" — but the CLI noun stays terse.
var kbCmd = &cobra.Command{
	Use:     "kb",
	Aliases: []string{"bubble", "bubbles"},
	Short:   "Work with knowledge bubbles",
	Long: `Work with knowledge bubbles — curated syntheses of your team's knowledge.

A knowledge bubble is a Curator-maintained, read-only synthesis of your
team's distilled conversations for one area of work (see ox ADR-028). Team
contexts and ledgers are separate, permanent conversation stores — they are
not bubbles.

Use ` + "`ox kb list`" + ` to see the bubbles you can access and
` + "`ox kb show <slug>`" + ` for details on one.`,
}

var kbListCmd = &cobra.Command{
	Use:   "list",
	Short: "List knowledge bubbles you can access",
	Long: `List the knowledge bubbles available in this project's context.

Bubbles are Curator-maintained syntheses of your team's knowledge (ox
ADR-028), listed per scope from the KB API. Inside an ox-initialized repo,
the ambient scope is the repo's team. Personal-scope listing is not yet
available — it remains deferred until personal-team provisioning is fully
rolled out. Team Contexts and Ledgers
are separate, permanent conversation stores — list them with ` + "`ox teams`" + `
and ` + "`ox status`" + `, not here.

Examples:
  ox kb list                  # bubbles in this project's scopes
  ox kb list --type=team      # only team-type bubbles
  ox kb list --json           # scriptable JSON output`,
	Args: cobra.NoArgs,
	RunE: runKBList,
}

func init() {
	rootCmd.AddCommand(kbCmd)
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
// JSON tags are stable even if the internal struct gains internal-only fields.
type kbListJSONBubble struct {
	KBID        string   `json:"kb_id,omitempty"`
	Type        string   `json:"type"`
	Slug        string   `json:"slug,omitempty"`
	Name        string   `json:"name,omitempty"`
	ScopeType   string   `json:"scope_type,omitempty"`
	ScopeID     string   `json:"scope_id,omitempty"`
	Description string   `json:"description,omitempty"`
	Topics      []string `json:"topics,omitempty"`
	ViewerRole  string   `json:"viewer_role,omitempty"`
	LocalPath   string   `json:"local_path,omitempty"`
	RepoURL     string   `json:"repo_url,omitempty"`
	Endpoint    string   `json:"endpoint,omitempty"`
}

type kbListJSONWarning struct {
	Error string `json:"error"`
}

func runKBList(cmd *cobra.Command, args []string) error {
	typeFilter, _ := cmd.Flags().GetString("type")
	jsonOutput, _ := cmd.Root().PersistentFlags().GetBool("json")

	projectRoot, _ := findProjectRoot()
	source, ep := newDefaultKBListSource(projectRoot)

	ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
	defer cancel()

	res := kb.FetchBubbles(ctx, source, ep, ambientKBScopes(projectRoot))
	return renderKBListResult(cmd.OutOrStdout(), res, typeFilter, jsonOutput, projectRoot)
}

// ambientKBScopes resolves the scopes this project context implies (ox
// ADR-028 §4): the repo's team. Outside a project, or in a project with no
// team binding, there is no scope to list and the result is empty.
func ambientKBScopes(projectRoot string) []api.KBScope {
	if projectRoot == "" {
		return nil
	}
	tc := config.FindRepoTeamContext(projectRoot)
	if tc == nil {
		return nil
	}
	return kb.AmbientScopes(tc.TeamID)
}

// newDefaultKBListSource builds the authenticated production KB client (and
// resolves the endpoint it targets). Pulled out so tests can swap the source
// via a fake without touching auth/credential plumbing.
func newDefaultKBListSource(projectRoot string) (kb.KBSource, string) {
	ep := endpoint.Get()
	if projectRoot != "" {
		ep = endpoint.GetForProject(projectRoot)
	}

	// authenticated when a token is available; the kb client translates
	// 403/404 into ErrKBAPIUnavailable which FetchBubbles treats as "no
	// bubbles for this caller", not a warning.
	kbClient := api.NewKBClientWithEndpoint(ep)
	if token, err := auth.GetTokenForEndpoint(ep); err == nil && token != nil && token.AccessToken != "" {
		kbClient = kbClient.WithAuthToken(token.AccessToken)
	}
	return kbClient, ep
}

// renderKBListResult is the unit-testable core: it takes a pre-built
// kb.ListResult, applies the type filter, and emits either JSON or a
// human-readable table. Splitting this out lets tests skip the entire
// auth + endpoint dance and just feed bubbles in.
func renderKBListResult(w io.Writer, res kb.ListResult, typeFilter string, jsonOutput bool, projectRoot string) error {
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

// filterAndSortBubbles applies the --type filter (if any). Sort order is
// stable on (type-priority, slug).
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
	case api.KBType("channel"):
		// channel slots between custom and unknown until KBTypeChannel is
		// promoted into internal/api. Kept in sync with internal/prime/kb.go.
		return 5
	default:
		// "" and KBTypeUnknown
		return 6
	}
}

// formatKBType renders the TYPE column. Empty/unknown types render as
// "unknown" so a forward-compat row never produces a blank column.
func formatKBType(t api.KBType) string {
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
			Foreground(theme.Color("#CCCCCC"))

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
	typeStr := formatKBType(b.Type)
	typeCol := fmt.Sprintf("%-*s", kbListColTypeWidth, truncateForColumn(typeStr, kbListColTypeWidth))

	// Human-display form is `#<slug>`; storage and JSON keep the bare slug.
	slugCol := fmt.Sprintf("%-*s", kbListColSlugWidth, truncateForColumn(cli.FormatKBSlug(b.Slug), kbListColSlugWidth))

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
func emitKBListJSON(w io.Writer, bubbles []kb.Bubble, warnings []kb.Warning) error {
	out := kbListJSONOutput{
		Bubbles:  make([]kbListJSONBubble, 0, len(bubbles)),
		Warnings: make([]kbListJSONWarning, 0, len(warnings)),
	}
	for _, b := range bubbles {
		out.Bubbles = append(out.Bubbles, kbListJSONBubble{
			KBID:        b.KBID,
			Type:        jsonTypeForBubble(b.Type),
			Slug:        b.Slug,
			Name:        b.Name,
			ScopeType:   b.ScopeType,
			ScopeID:     b.ScopeID,
			Description: b.Description,
			Topics:      b.Topics,
			ViewerRole:  b.ViewerRole,
			LocalPath:   b.LocalPath,
			RepoURL:     b.RepoURL,
			Endpoint:    b.Endpoint,
		})
	}
	for _, sw := range warnings {
		out.Warnings = append(out.Warnings, kbListJSONWarning{Error: sw.Err})
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

// emitKBListEmpty handles the no-bubbles case. When the fetch warned,
// surface the warnings + an `ox doctor` hint so the user can act. Otherwise
// default to a clean "no bubbles + how to bootstrap" message.
func emitKBListEmpty(w io.Writer, warnings []kb.Warning, projectRoot string) {
	fmt.Fprintln(w)
	if len(warnings) > 0 {
		fmt.Fprintln(w, "  "+kbListWarnStyle.Render("No knowledge bubbles available; the KB API errored:"))
		for _, sw := range warnings {
			fmt.Fprintf(w, "  %s %s\n", kbListWarnStyle.Render("⚠"), sw.Err)
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
func emitKBListWarningFooter(w io.Writer, warnings []kb.Warning) {
	fmt.Fprintf(w, "%s %s\n",
		kbListWarnStyle.Render("!"),
		kbListWarnStyle.Render(fmt.Sprintf("%d KB API error(s) — run 'ox doctor' to investigate", len(warnings))),
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
