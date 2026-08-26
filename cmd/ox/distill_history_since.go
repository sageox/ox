package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/sageox/ox/internal/distill/history/read"
	"github.com/spf13/cobra"
)

// distillHistorySinceFlags captures the user-controllable inputs to `ox
// distill history since`. The positional <dur> argument comes in via cobra's
// args slice (not a flag) so it is NOT represented here. The flag
// surface differs from list in three ways:
//
//   - No --since / --until / --tz: since is relative-duration-only.
//     The positional <dur> is always anchored to time.Now() and the
//     window is [now-dur, now]. --tz is meaningless for a relative
//     window and is explicitly not accepted.
//   - --format accepts only content|json. content is the default
//     (spec §3.6: content is the format factory/distill/summary.ts
//     pipes straight into Claude); json is supported for programmatic
//     callers. text is intentionally NOT accepted — there is no spec'd
//     text rendering for since, and falling through to list's text
//     renderer would emit the wrong shape (metadata rows instead of
//     assembled bodies). A dedicated text renderer can be added later
//     if a caller needs it; the gate is additive.
//   - --limit defaults to 100 per spec §3.6 (list's default is 0 =
//     unlimited, since list is an enumeration; since materializes
//     bodies so a default cap is prudent).
type distillHistorySinceFlags struct {
	Layer    string
	Team     string
	AllTeams bool
	Format   string
	Limit    int
}

var distillHistorySinceFlagSet distillHistorySinceFlags

func init() {
	distillHistorySinceCmd.RunE = runDistillHistorySince
	registerDistillHistorySinceFlags(distillHistorySinceCmd, &distillHistorySinceFlagSet)
}

// registerDistillHistorySinceFlags binds every user-controllable flag on
// the provided cobra command, using the pointer-to-flag-set pattern that
// the init() function uses for production and the in-process tests use
// for drift-safe construction. Keeping the flag set in one place means a
// flag that gets added/removed/renamed in production is automatically
// picked up by the test helper, so tests can't go stale while the real
// command surface drifts out from under them.
func registerDistillHistorySinceFlags(cmd *cobra.Command, flags *distillHistorySinceFlags) {
	f := cmd.Flags()
	f.StringVar(&flags.Layer, "layer", "auto", "layer to read: daily|weekly|monthly|auto")
	f.StringVar(&flags.Team, "team", "", "team slug, id, or name (defaults to the repo's active team)")
	f.BoolVar(&flags.AllTeams, "all-teams", false, "merge entries across every registered team context")
	f.StringVar(&flags.Format, "format", "content", "output format: content|json")
	f.IntVar(&flags.Limit, "limit", 100, "cap the number of entries returned")
}

// runDistillHistorySince is the RunE for `ox distill history since`. It validates
// flags + the positional duration, computes the window anchored at
// time.Now() in UTC, calls read.Since (list → load composition), and
// emits the caller-requested format. Content is the default because
// the primary consumer (factory/distill/summary.ts) pipes this output
// straight into Claude; json is supported for programmatic callers.
// text is intentionally not accepted — see the comment at the format
// validation branch below for the rationale.
func runDistillHistorySince(cmd *cobra.Command, args []string) error {
	flags := distillHistorySinceFlags{}
	flags.Layer, _ = cmd.Flags().GetString("layer")
	flags.Team, _ = cmd.Flags().GetString("team")
	flags.AllTeams, _ = cmd.Flags().GetBool("all-teams")
	flags.Format, _ = cmd.Flags().GetString("format")
	flags.Limit, _ = cmd.Flags().GetInt("limit")
	start := time.Now()
	elapsed := func() int64 { return time.Since(start).Milliseconds() }

	if len(args) == 0 {
		return emitDistillHistorySinceUsageError(cmd.OutOrStdout(), flags.Format,
			newJournalUsageError("<dur> positional argument is required"), elapsed())
	}
	if len(args) > 1 {
		return emitDistillHistorySinceUsageError(cmd.OutOrStdout(), flags.Format,
			newJournalUsageError(fmt.Sprintf("unexpected positional args after <dur>: %v", args[1:])), elapsed())
	}
	// `since` intentionally does NOT support --format=text. The writeJournalEnvelope
	// fallback for text would route to renderDistillHistoryListText, which emits
	// the list view (one metadata row per entry) rather than the assembled bodies
	// `since` produces — that would be a silent wrong-output bug. Until a dedicated
	// renderDistillHistorySinceText is spec'd and implemented, reject `text`
	// explicitly with a usage_error so callers see the deliberate gap instead of
	// getting misrouted output.
	if flags.Format != "content" && flags.Format != "json" {
		return emitDistillHistorySinceUsageError(cmd.OutOrStdout(), flags.Format,
			newJournalUsageError(fmt.Sprintf("--format must be content|json, got %q (text is not currently supported on since)", flags.Format)), elapsed())
	}
	if flags.AllTeams && flags.Team != "" {
		return emitDistillHistorySinceUsageError(cmd.OutOrStdout(), flags.Format,
			newJournalUsageError("cannot combine --team and --all-teams"), elapsed())
	}

	dur, err := time.ParseDuration(args[0])
	if err != nil {
		return emitDistillHistorySinceUsageError(cmd.OutOrStdout(), flags.Format,
			newJournalUsageError(fmt.Sprintf("invalid duration %q: %v", args[0], err)), elapsed())
	}
	if dur < 0 {
		return emitDistillHistorySinceUsageError(cmd.OutOrStdout(), flags.Format,
			newJournalUsageError(fmt.Sprintf("duration must be non-negative, got %v", dur)), elapsed())
	}

	now := time.Now().UTC()
	since := now.Add(-dur)
	until := now

	layer, layerErr := parseJournalLayerFlag(flags.Layer)
	if layerErr != nil {
		return emitDistillHistorySinceUsageError(cmd.OutOrStdout(), flags.Format, layerErr, elapsed())
	}

	projectRoot, err := findProjectRoot()
	if err != nil {
		return writeJournalRuntimeError(cmd.OutOrStdout(),
			distillHistorySinceErrorFormat(flags.Format), "project_not_found", err.Error(), elapsed())
	}
	teams, err := resolveDistillHistoryTeams(projectRoot, flags.Team, flags.AllTeams)
	if err != nil {
		return writeJournalRuntimeError(cmd.OutOrStdout(),
			distillHistorySinceErrorFormat(flags.Format), "team_not_found", err.Error(), elapsed())
	}

	q := read.ReadQuery{
		Since:    since,
		Until:    until,
		Layer:    layer,
		Teams:    teams,
		Limit:    flags.Limit,
		WantBody: true,
	}
	entries, bodies, meta, err := read.Since(context.Background(), q)
	if err != nil {
		return writeJournalRuntimeError(cmd.OutOrStdout(),
			distillHistorySinceErrorFormat(flags.Format), "read_failed", err.Error(), elapsed())
	}

	if flags.Format == "content" {
		renderDistillHistorySinceContent(cmd.OutOrStdout(), entries, bodies)
		emitDistillHistoryWarnings(cmd.ErrOrStderr(), meta.Warnings)
		return nil
	}

	env := buildDistillHistorySinceEnvelope(entries, bodies, meta, elapsed())
	writeJournalEnvelope(cmd.OutOrStdout(), flags.Format, env)
	emitDistillHistoryWarnings(cmd.ErrOrStderr(), meta.Warnings)
	return nil
}

// emitDistillHistorySinceUsageError wraps a usage error into the typed exit
// error, writes the envelope to stdout in the caller's requested
// format (content falls back to json), and returns exit-2 for main.go
// to unwrap. Mirrors the distill_history_list and distill_history_show pattern.
func emitDistillHistorySinceUsageError(w io.Writer, format string, u *distillHistoryUsageError, elapsedMS int64) error {
	exit := newDistillHistoryUsageExit(u, elapsedMS)
	writeJournalEnvelope(w, distillHistorySinceErrorFormat(format), &exit.Envelope)
	return exit
}

// distillHistorySinceErrorFormat maps since's three output formats to the
// format writeJournalEnvelope understands for error rendering. JSON
// and text pass through; content falls back to JSON so a failed
// since never leaves stdout silent — inheriting the Unit 3
// envelope-on-every-exit-path lesson.
func distillHistorySinceErrorFormat(format string) string {
	if format == "content" {
		return "json"
	}
	return format
}

// buildDistillHistorySinceEnvelope maps a (entries, bodies, meta) triple
// into the distill_history_since envelope. Unlike list, since populates
// Data.Bodies. Unlike show, since populates Data.Window (meta's
// day-rounded effective window). Entry counts (FactCount,
// CitationCount) and CreatedAt all come from the reader — the
// command layer never invents them.
func buildDistillHistorySinceEnvelope(entries []read.Entry, bodies []string, meta read.ListMeta, elapsedMS int64) *distillHistoryEnvelope {
	out := make([]distillHistoryEnvelopeEntry, 0, len(entries))
	for _, e := range entries {
		row := distillHistoryEnvelopeEntry{
			ID:            e.ID,
			Layer:         string(e.Layer),
			Date:          e.Date,
			Team:          e.Team,
			Path:          e.RelPath,
			FactCount:     e.FactCount,
			CitationCount: e.CitationCount,
			SourceFiles:   nonNilStringSlice(e.SourceFiles),
		}
		if !e.CreatedAt.IsZero() {
			row.CreatedAt = rfc3339Z(e.CreatedAt)
		}
		out = append(out, row)
	}
	win := &distillHistoryEnvelopeWindow{
		Since:         rfc3339Z(meta.EffectiveSince),
		Until:         rfc3339Z(meta.EffectiveUntil),
		LayerResolved: string(meta.LayerResolved),
	}
	// bodies is guaranteed len-matched by read.Since; if a caller
	// passes nil (empty window), we still hand the envelope a non-nil
	// *[]string pointing at an empty slice so the JSON encoder emits
	// `"bodies": []` rather than dropping the field (spec §3.6).
	bodiesOut := nonNilStringSlice(bodies)
	return &distillHistoryEnvelope{
		Success:   true,
		Type:      "distill_history_since",
		ElapsedMS: elapsedMS,
		Data: &distillHistoryEnvelopeData{
			Entries:   out,
			Bodies:    &bodiesOut,
			Window:    win,
			Truncated: meta.Truncated,
		},
	}
}

// renderDistillHistorySinceContent emits the --format=content output per
// spec §3.6:684-687: bodies concatenated in chronological order,
// separated by "\n---\n", each preceded by a rich per-entry marker
//
//	<!-- entry: <id> | layer: <layer> | date: <date> -->
//
// Note that since's marker is the rich form (id + layer + date),
// differing from show's minimal `<!-- entry: <id> -->`. This is
// deliberate: factory/distill/summary.ts uses the layer/date
// segments to route summarization and to recover context when the
// stream is chunked.
//
// Non-ok rows (Status="read_error" from mid-flight materialization
// failures) and rows with empty bodies are skipped so the content
// stream stays clean. Callers who need failure detail must use
// --format=json.
func renderDistillHistorySinceContent(w io.Writer, entries []read.Entry, bodies []string) {
	first := true
	for i, e := range entries {
		if e.Status != "" && e.Status != "ok" {
			continue
		}
		body := bodies[i]
		if body == "" {
			continue
		}
		marker := fmt.Sprintf("<!-- entry: %s | layer: %s | date: %s -->",
			e.ID, string(e.Layer), e.Date)
		if first {
			fmt.Fprintln(w, marker)
			first = false
		} else {
			fmt.Fprint(w, "\n---\n")
			fmt.Fprintln(w, marker)
		}
		_, _ = w.Write([]byte(body)) // best-effort terminal output
		if body[len(body)-1] != '\n' {
			_, _ = w.Write([]byte{'\n'}) // best-effort terminal output
		}
	}
}
