package main

// kb_query.go — `ox kb query`: search curated files across knowledge bubbles
// via POST /api/v1/kb/search.
//
// Argument grammar: the LAST positional arg is the query text; every arg
// before it is a bubble identifier (#slug or kb_id) resolved exactly as
// `ox kb describe` resolves its one identifier. At least one bubble is
// required — there is deliberately no "no bubbles → search everything"
// default, so the caller always states where it is searching and the CLI
// never makes a hidden listing call or silently truncates at the server's
// bubble cap.
//
// Output honesty: the four per-bubble statuses (ok, empty, not_indexed,
// error) render distinctly and are never collapsed into "no results", and a
// requested bubble that the server omitted from the response (inaccessible,
// nonexistent, or a type outside the indexed allowlist — deliberately
// indistinguishable server-side) is called out rather than silently dropped.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/kb"
	"github.com/spf13/cobra"
)

// Client-side mirrors of the server caps, used only to fail fast with a
// better message than the server's 400. The server remains authoritative —
// its response `caps` echo the live values.
const (
	kbQueryMaxKBs        = 20
	kbQueryMaxQueryBytes = 1024
)

// kbQuerySearchTimeout bounds the search call — it embeds the query and fans
// out per bubble server-side, so it gets more headroom than the fast reads.
// Used for BOTH the search context and the client's transport timeout: the
// transport cap is the effective limit when it is lower than the context.
const kbQuerySearchTimeout = 30 * time.Second

var kbQueryCmd = &cobra.Command{
	Use:   "query <#slug|kb_id> [#slug|kb_id ...] \"<question>\"",
	Short: "Search files across knowledge bubbles",
	Long: `Search the curated files of one or more knowledge bubbles.

The LAST argument is the search text — quote it. Every argument before it
names a bubble: a kb_id (starts with 'kb_') or a slug (with or without the
display '#' prefix), resolved within one scope like 'ox kb describe'.

Results are ranked FILE hits, grouped per bubble in the order you named
them — search finds the file, then you read it from the bubble's local
mount (see 'ox kb describe' for the path). A bubble you cannot read, one
that does not exist, and one whose type is not indexed for search are all
reported the same way, without revealing which it is.`,
	Example: `  ox kb query '#engineering' "how do we batch relay spans"
  ox kb query '#engineering' '#platform' "relay span batching" -k 5
  ox kb query kb_01HXYZ... "retry policy" --path docs/ --json`,
	Args: cobra.MinimumNArgs(2),
	RunE: runKBQuery,
}

func init() {
	kbCmd.AddCommand(kbQueryCmd)
	kbQueryCmd.Flags().String("scope", "team", "scope to resolve slugs in: team|personal (personal is not yet available)")
	kbQueryCmd.Flags().String("mode", api.KBSearchModeHybrid, "retrieval mode: hybrid|bm25|vector (bm25 skips the embedding step)")
	kbQueryCmd.Flags().IntP("k", "k", 0, "files per bubble (0 = server default; over-cap values are clamped)")
	kbQueryCmd.Flags().String("path", "", "only return files under this repo-relative path prefix")
}

// kbQueryTarget pairs a resolved kb_id with the identifier the user typed,
// so rendering can speak the user's language and a group missing from the
// response can be attributed to the right argument.
type kbQueryTarget struct {
	Input string `json:"input"` // as typed (minus the display '#')
	KBID  string `json:"kb_id"`
}

func runKBQuery(cmd *cobra.Command, args []string) error {
	jsonOutput, _ := cmd.Root().PersistentFlags().GetBool("json")
	scopeFlag, _ := cmd.Flags().GetString("scope")
	modeFlag, _ := cmd.Flags().GetString("mode")
	kFlag, _ := cmd.Flags().GetInt("k")
	pathFlag, _ := cmd.Flags().GetString("path")

	identifiers, query := args[:len(args)-1], strings.TrimSpace(args[len(args)-1])
	if err := validateKBQueryInput(identifiers, query, modeFlag, kFlag); err != nil {
		return err
	}

	projectRoot, _ := findProjectRoot()
	ep := resolveKBEndpoint(projectRoot)

	client := api.NewKBClientWithEndpoint(ep).WithHTTPTimeout(kbQuerySearchTimeout)
	if token, err := auth.GetTokenForEndpoint(ep); err == nil && token != nil && token.AccessToken != "" {
		client = client.WithAuthToken(token.AccessToken)
	}

	// Every network call carries its OWN budget — per-resolution and then a
	// fresh one for the search. A shared deadline in either position lets a
	// slow-but-valid early call starve a later one into a bare
	// context-deadline failure (search starved by resolutions, or a later
	// slug starved by an earlier slow resolution).
	targets := make([]kbQueryTarget, 0, len(identifiers))
	seenKBIDs := make(map[string]bool, len(identifiers))
	for _, raw := range identifiers {
		input := strings.TrimSpace(kb.NormalizeSlugArg(raw))
		if input == "" {
			return fmt.Errorf("empty bubble identifier\nUsage: ox kb query <#slug|kb_id> [#slug|kb_id ...] \"<question>\"")
		}
		kbID, err := resolveOneKBIdentifier(cmd.Context(), client, input, scopeFlag, projectRoot)
		if err != nil {
			if !jsonOutput && looksLikeProse(input) {
				cli.PrintHint(fmt.Sprintf("If %q was part of your question, quote the whole question — the last argument is the query.", input))
			}
			return handleKBDescribeError(cmd.OutOrStdout(), err, input, jsonOutput)
		}
		// Dedupe on the RESOLVED id, not the typed form: the same bubble can
		// be named twice ("#eng" and its kb_id, or a slug and its rename
		// alias), and a duplicate would both waste a server bubble slot and
		// collapse the per-bubble group rendering, which keys on kb_id.
		if seenKBIDs[kbID] {
			continue
		}
		seenKBIDs[kbID] = true
		targets = append(targets, kbQueryTarget{Input: input, KBID: kbID})
	}

	// Never log the query text (it can quote anything); lengths and counts only.
	slog.Info("kb query", "bubbles", len(targets), "mode", modeFlag, "k", kFlag, "query_len", len(query))

	searchCtx, cancelSearch := context.WithTimeout(cmd.Context(), kbQuerySearchTimeout)
	defer cancelSearch()
	resp, err := client.SearchFiles(searchCtx, api.KBSearchRequest{
		Query:      query,
		KBs:        kbQueryTargetIDs(targets),
		Mode:       modeFlag,
		K:          kFlag,
		PathPrefix: pathFlag,
	})
	if err != nil {
		return handleKBSearchError(cmd.OutOrStdout(), err, jsonOutput)
	}

	return renderKBQueryResult(cmd.OutOrStdout(), resp, targets, jsonOutput)
}

// validateKBQueryInput fails fast on shapes the server would 400 anyway,
// with friendlier copy. Server caps are authoritative; these mirror them.
func validateKBQueryInput(identifiers []string, query, mode string, k int) error {
	if query == "" {
		return fmt.Errorf("query text required (the last argument is the query)\nUsage: ox kb query <#slug|kb_id> [#slug|kb_id ...] \"<question>\"")
	}
	// The server cap is BYTES of UTF-8 (it bounds the embedding call), which
	// is exactly what len() measures on a Go string.
	if len(query) > kbQueryMaxQueryBytes {
		return fmt.Errorf("query is %d bytes; the limit is %d bytes of UTF-8", len(query), kbQueryMaxQueryBytes)
	}
	if len(identifiers) > kbQueryMaxKBs {
		return fmt.Errorf("%d bubbles requested; the limit is %d per query", len(identifiers), kbQueryMaxKBs)
	}
	switch mode {
	case api.KBSearchModeHybrid, api.KBSearchModeBM25, api.KBSearchModeVector:
	default:
		return fmt.Errorf("invalid --mode %q (want hybrid, bm25, or vector)", mode)
	}
	if k < 0 {
		return fmt.Errorf("invalid -k %d (want 0 for the server default, or a positive count)", k)
	}
	return nil
}

// looksLikeProse reports whether a failed identifier reads like an ordinary
// English word — the signature of an unquoted query being consumed as bubble
// names. Slugs in practice carry '-', '_', or digits; a bare lowercase word
// is far more likely to be prose.
func looksLikeProse(input string) bool {
	if input == "" || strings.HasPrefix(input, kbIDPrefix) {
		return false
	}
	for _, r := range input {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// resolveOneKBIdentifier resolves a single identifier under its own bounded
// context (a helper rather than an inline WithTimeout so the loop doesn't
// stack deferred cancels). kb_id inputs pass through without any network
// call; slugs get the same per-read budget describe's flow uses.
func resolveOneKBIdentifier(parent context.Context, client *api.KBClient, input, scopeFlag, projectRoot string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	return resolveKBIdentifier(ctx, client, input, scopeFlag, projectRoot)
}

func kbQueryTargetIDs(targets []kbQueryTarget) []string {
	ids := make([]string, len(targets))
	for i, t := range targets {
		ids[i] = t.KBID
	}
	return ids
}

// handleKBSearchError translates a SearchFiles failure. The sentinel carries
// a different meaning here than in describe: on the search route a 404 means
// the server-side feature flag is off (the route is absent), not "slug not
// found" — slug resolution already happened in its own earlier call.
func handleKBSearchError(w io.Writer, err error, jsonOutput bool) error {
	if errors.Is(err, api.ErrKBAPIUnavailable) {
		const msg = "KB file search isn't enabled for your team yet."
		slog.Info("kb query: search route unavailable", "error", err)
		if jsonOutput {
			return outputJSON(w, map[string]any{
				"status":   "unavailable",
				"message":  msg,
				"guidance": "The server does not expose POST /api/v1/kb/search (feature flag off or older server). Bubbles are still readable: 'ox kb list' to find one, 'ox kb describe' for its local mount path.",
			})
		}
		fmt.Fprintln(os.Stderr, msg)
		cli.PrintHint("Bubbles are still readable — run 'ox kb list' and read from a bubble's local mount path.")
		return cli.ErrSilent
	}
	if errors.Is(err, api.ErrUnauthorized) {
		return fmt.Errorf("not authenticated — run 'ox login'")
	}
	// Wrap so an unclassified failure (e.g. a bare context deadline) names
	// which call failed; errors.Is/As still see the cause.
	return fmt.Errorf("kb file search: %w", err)
}

// kbQueryJSONOutput is the --json envelope: the wire response verbatim plus
// which requested bubbles the server omitted and agent-facing guidance (the
// thin-relay rule: behavioral guidance ships in CLI JSON, not in skills).
type kbQueryJSONOutput struct {
	Caps     api.KBSearchCaps    `json:"caps"`
	Groups   []api.KBSearchGroup `json:"groups"`
	Missing  []kbQueryTarget     `json:"missing,omitempty"`
	Guidance string              `json:"guidance"`
}

// renderKBQueryResult renders the search outcome — pure with respect to the
// network, so tests can drive it with hand-built responses.
func renderKBQueryResult(w io.Writer, resp *api.KBSearchResponse, targets []kbQueryTarget, jsonOutput bool) error {
	byID := make(map[string]*api.KBSearchGroup, len(resp.Groups))
	for i := range resp.Groups {
		byID[resp.Groups[i].KBID] = &resp.Groups[i]
	}
	var missing []kbQueryTarget
	for _, t := range targets {
		if _, ok := byID[t.KBID]; !ok {
			missing = append(missing, t)
		}
	}

	if jsonOutput {
		// Scope the output to the request: only groups whose kb_id was a
		// resolved target are serialized, in request order. A malformed or
		// hostile response must not be able to smuggle another bubble's
		// paths and snippets through this CLI. (The human path is scoped
		// the same way by construction — it iterates targets.)
		groups := make([]api.KBSearchGroup, 0, len(targets))
		for _, t := range targets {
			if group, ok := byID[t.KBID]; ok {
				groups = append(groups, *group)
			}
		}
		return outputJSON(w, kbQueryJSONOutput{
			Caps:     resp.Caps,
			Groups:   groups,
			Missing:  missing,
			Guidance: kbQueryGuidance(groups, missing),
		})
	}

	headStyle := lipgloss.NewStyle().Foreground(cli.ColorPrimary)
	dimStyle := lipgloss.NewStyle().Foreground(cli.ColorDim)
	warnStyle := lipgloss.NewStyle().Foreground(cli.ColorWarning)

	fmt.Fprintln(w)
	for _, t := range targets {
		// The identifier is user-typed and the kb_id server-derived; both are
		// sanitized like every other externally-sourced string below.
		display := cli.SanitizeTerminalText(t.Input)
		if !strings.HasPrefix(t.Input, kbIDPrefix) {
			display = cli.FormatKBSlug(display)
		}
		header := headStyle.Render(display)
		if t.KBID != t.Input {
			header += "  " + dimStyle.Render(cli.SanitizeTerminalText(t.KBID))
		}
		fmt.Fprintf(w, "  %s\n", header)

		group, ok := byID[t.KBID]
		if !ok {
			fmt.Fprintf(w, "    %s\n", warnStyle.Render("not searchable for you — it may not exist, you may lack access, or its type isn't indexed for search"))
			fmt.Fprintln(w)
			continue
		}
		renderKBQueryGroup(w, group, dimStyle, warnStyle)
		fmt.Fprintln(w)
	}
	// The read-the-file hint only makes sense when there is a file to read —
	// groups alone don't imply hits (empty/not_indexed/error groups have
	// none), and only requested groups count (same scoping as the output).
	for _, t := range targets {
		group, ok := byID[t.KBID]
		if ok && group.Status == api.KBSearchStatusOK && len(group.Hits) > 0 {
			cli.PrintHintTo(w, "Hits are files, not answers — read one from the bubble's local mount ('ox kb describe' shows the path).")
			break
		}
	}
	return nil
}

// renderKBQueryGroup renders one bubble's outcome. The four statuses stay
// visually distinct; "empty" and "not indexed" must never read the same.
func renderKBQueryGroup(w io.Writer, group *api.KBSearchGroup, dimStyle, warnStyle lipgloss.Style) {
	switch group.Status {
	case api.KBSearchStatusOK:
		for _, hit := range group.Hits {
			// Path, title, and snippet are indexed FILE CONTENT — the most
			// hostile text this command renders. Sanitize at the point of
			// render; snippet continuation lines stay indented so free text
			// can never pose as a rendered row of its own.
			line := fmt.Sprintf("%d. %s", hit.Rank, cli.SanitizeTerminalText(hit.Path))
			if hit.Title != "" {
				line += dimStyle.Render(" — " + cli.SanitizeTerminalText(hit.Title))
			}
			fmt.Fprintf(w, "    %s\n", line)
			for _, snippetLine := range cli.SanitizeTerminalLines(hit.Snippet) {
				fmt.Fprintf(w, "       %s\n", dimStyle.Render(snippetLine))
			}
			if hit.ChunkCount > 1 {
				fmt.Fprintf(w, "       %s\n", dimStyle.Render(fmt.Sprintf("matched %d of %d sections", len(hit.MatchedChunks), hit.ChunkCount)))
			}
		}
	case api.KBSearchStatusEmpty:
		fmt.Fprintf(w, "    %s\n", dimStyle.Render("no matches"))
	case api.KBSearchStatusNotIndexed:
		fmt.Fprintf(w, "    %s\n", warnStyle.Render("not indexed yet — this bubble has never been indexed for search"))
	case api.KBSearchStatusError:
		msg := "search failed for this bubble"
		if group.ErrorClass != "" {
			msg += " (" + cli.SanitizeTerminalText(group.ErrorClass) + ")"
		}
		fmt.Fprintf(w, "    %s\n", warnStyle.Render(msg))
	default:
		// A future server status renders honestly rather than disappearing.
		fmt.Fprintf(w, "    %s\n", warnStyle.Render("status: "+cli.SanitizeTerminalText(group.Status)))
	}
}

// kbQueryGuidance tells an AI coworker how to act on the result. Statuses
// are restated because they are load-bearing: collapsing empty/not_indexed
// into "no results" is exactly the misreport this command is designed to
// prevent.
func kbQueryGuidance(groups []api.KBSearchGroup, missing []kbQueryTarget) string {
	var b strings.Builder
	b.WriteString("Hits are ranked FILES, not answers: read a hit via its path from the bubble's local mount ('ox kb describe <kb_id>' shows local_path); compare content_rev if the exact indexed version matters. ")
	b.WriteString("Per-bubble status is load-bearing: 'empty' means the bubble is indexed and matched nothing; 'not_indexed' means it was never indexed — do not report either as \"the bubble knows nothing\" interchangeably; 'error' groups can be retried. ")
	if len(missing) > 0 {
		names := make([]string, len(missing))
		for i, m := range missing {
			names[i] = m.Input
		}
		fmt.Fprintf(&b, "Requested but absent from the response: %s — each may not exist, may not be readable by this account, or may be a bubble type that isn't indexed; the server deliberately does not say which. ", strings.Join(names, ", "))
	}
	if len(groups) == 0 {
		b.WriteString("No requested bubble was searchable. Run 'ox kb list' to see the bubbles this account can access. ")
	}
	b.WriteString("Results are grouped per bubble and ranks are not comparable across bubbles.")
	return b.String()
}
