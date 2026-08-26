package main

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/sageox/ox/internal/distill/history/read"
	"github.com/spf13/cobra"
)

// inferShowLayer detects which memory layer an ID belongs to based on
// its filename-stem shape. Returns the inferred layer, or LayerDaily as
// the fallback for bare UUID7 short forms and unrecognized shapes (the
// reader's matchID still handles ambiguous prefixes within the chosen
// layer).
//
// Stem formats are unambiguous because weekly uses a literal `W` on the
// second segment and monthly has only two date segments where daily has
// three:
//
//   - "YYYY-MM-DD"          (bare date)       → daily
//   - "YYYY-MM-DD-<uuid7>"  (full daily stem) → daily
//   - "YYYY-Www"            (bare week)       → weekly
//   - "YYYY-Www-<uuid7>"    (full weekly)     → weekly
//   - "YYYY-MM"             (bare month)      → monthly
//   - "YYYY-MM-<uuid7>"     (full monthly)    → monthly
//   - "<uuid7>"             (short prefix)    → daily fallback
//
// The daily regex is checked before monthly so `YYYY-MM-DD-<uuid7>`
// stems (which also match the monthly prefix pattern) resolve to daily.
func inferShowLayer(id string) read.Layer {
	switch {
	case showWeeklyIDRe.MatchString(id):
		return read.LayerWeekly
	case showDailyIDRe.MatchString(id):
		return read.LayerDaily
	case showMonthlyIDRe.MatchString(id):
		return read.LayerMonthly
	default:
		return read.LayerDaily
	}
}

var (
	// showDailyIDRe matches YYYY-MM-DD stems (bare or with a uuid7 suffix).
	// Anchored with `(-|$)` after the day so it does NOT swallow monthly
	// stems like `2026-04-<uuid7>` where the third segment is uuid7 chars.
	showDailyIDRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(-|$)`)
	// showWeeklyIDRe matches YYYY-Www stems (literal W on segment 2).
	showWeeklyIDRe = regexp.MustCompile(`^\d{4}-W\d{2}(-|$)`)
	// showMonthlyIDRe matches YYYY-MM stems when daily did NOT match first.
	// Relies on the check order in inferShowLayer — this regex alone
	// would also match daily stems since daily's prefix is a superset.
	showMonthlyIDRe = regexp.MustCompile(`^\d{4}-\d{2}(-|$)`)
)

// distillHistoryShowFlags captures the user-controllable inputs to
// `ox distill history show`. Positional IDs come in via the cobra args slice
// (not a flag), so they are NOT represented here. The field set is
// deliberately narrower than `list` — show does not take --since,
// --until, --tz, --layer, --all-teams, or --limit. Its scope is a set
// of explicit IDs against one team.
//
// There is no --latest flag. Cobra rejects unknown flags at parse
// time, so a user passing --latest gets a usage error from cobra
// itself before runDistillHistoryShow sees the command.
type distillHistoryShowFlags struct {
	Team   string
	Format string
}

var distillHistoryShowFlagSet distillHistoryShowFlags

func init() {
	distillHistoryShowCmd.RunE = runDistillHistoryShow
	registerDistillHistoryShowFlags(distillHistoryShowCmd, &distillHistoryShowFlagSet)
}

// registerDistillHistoryShowFlags binds every user-controllable flag on
// the provided cobra command. Shared by production init() and the
// in-process test helper so the test surface cannot drift out of sync
// with production flag definitions.
func registerDistillHistoryShowFlags(cmd *cobra.Command, flags *distillHistoryShowFlags) {
	f := cmd.Flags()
	f.StringVar(&flags.Team, "team", "", "team slug, id, or name (defaults to the repo's active team)")
	f.StringVar(&flags.Format, "format", "json", "output format: json|text|content")
}

// runDistillHistoryShow is the RunE for `ox distill history show`. It validates
// flags, resolves the active team, groups the requested IDs by layer
// (daily/weekly/monthly inferred from stem shape), walks each layer
// via read.LoadEntries, and emits the partial-success envelope per
// plan §3 Unit 4. All three layers are supported — a `list --layer=weekly`
// ID can be piped straight into `show`.
//
// Per-ID failures DO NOT abort the call: a mix of ok / not_found /
// ambiguous entries is a success envelope (exit 0) with per-entry
// Status and Error fields populated. Only when every requested ID
// failed does the command emit a success:false envelope and return
// exit 1 via distillHistoryExitError.
func runDistillHistoryShow(cmd *cobra.Command, args []string) error {
	flags := distillHistoryShowFlagSet
	start := time.Now()
	elapsed := func() int64 { return time.Since(start).Milliseconds() }

	if len(args) == 0 {
		return emitDistillHistoryShowUsageError(cmd.OutOrStdout(), flags.Format,
			newJournalUsageError("at least one <id> is required"), elapsed())
	}

	if flags.Format != "json" && flags.Format != "text" && flags.Format != "content" {
		return emitDistillHistoryShowUsageError(cmd.OutOrStdout(), flags.Format,
			newJournalUsageError(
				fmt.Sprintf("--format must be json|text|content, got %q", flags.Format)),
			elapsed())
	}

	projectRoot, err := findProjectRoot()
	if err != nil {
		return writeJournalRuntimeError(cmd.OutOrStdout(),
			distillHistoryShowErrorFormat(flags.Format), "project_not_found", err.Error(), elapsed())
	}

	teams, err := resolveDistillHistoryTeams(projectRoot, flags.Team, false)
	if err != nil {
		return writeJournalRuntimeError(cmd.OutOrStdout(),
			distillHistoryShowErrorFormat(flags.Format), "team_not_found", err.Error(), elapsed())
	}

	// Group the input IDs by inferred layer so a single `show` call can
	// resolve daily, weekly, and monthly stems in one shot. Full stems
	// like "2026-04-12-<uuid7>" are unambiguously daily, "2026-W15-<uuid7>"
	// is weekly, "2026-04-<uuid7>" is monthly; bare UUID7 short prefixes
	// fall back to daily (matchID inside the reader still handles the
	// ambiguous cases within a layer). See inferShowLayer for the regex
	// discriminators.
	//
	// show has no --since/--until; it reads across all time via
	// NoTimeFilter, which short-circuits the window filter in listEntries
	// so the index scan returns every file under Teams for the selected
	// layer. See ReadQuery.NoTimeFilter doc.
	idsByLayer := map[read.Layer][]string{}
	for _, id := range args {
		layer := inferShowLayer(id)
		idsByLayer[layer] = append(idsByLayer[layer], id)
	}

	var entries []read.Entry
	// Iterate layers in a stable order so multi-layer show calls emit
	// entries in a predictable sequence: daily, weekly, monthly. Within
	// a layer, LoadEntries preserves the input-ID order (with bare-date
	// expansion interleaved at the bare-date arg's position).
	for _, layer := range []read.Layer{read.LayerDaily, read.LayerWeekly, read.LayerMonthly} {
		layerIDs := idsByLayer[layer]
		if len(layerIDs) == 0 {
			continue
		}
		q := read.ReadQuery{
			NoTimeFilter: true,
			Layer:        layer,
			Teams:        teams,
			WantBody:     true,
		}
		layerEntries, err := read.LoadEntries(context.Background(), q, layerIDs)
		if err != nil {
			return writeJournalRuntimeError(cmd.OutOrStdout(),
				distillHistoryShowErrorFormat(flags.Format), "read_failed", err.Error(), elapsed())
		}
		entries = append(entries, layerEntries...)
	}

	allFailed := true
	var firstErr *read.EntryError
	for i := range entries {
		if entries[i].Status == "ok" {
			allFailed = false
		}
		if firstErr == nil && entries[i].Error != nil {
			e := *entries[i].Error
			firstErr = &e
		}
	}
	if allFailed && firstErr != nil {
		env := &distillHistoryEnvelope{
			Success:   false,
			ElapsedMS: elapsed(),
			Error: &distillHistoryEnvelopeError{
				Code:      firstErr.Code,
				Message:   firstErr.Message,
				Retryable: false,
			},
		}
		writeJournalEnvelope(cmd.OutOrStdout(), distillHistoryShowErrorFormat(flags.Format), env)
		return &distillHistoryExitError{ExitCode: 1, Envelope: *env}
	}

	if flags.Format == "content" {
		renderDistillHistoryShowContent(cmd.OutOrStdout(), entries)
		return nil
	}

	env := buildDistillHistoryShowEnvelope(entries, elapsed())
	if flags.Format == "text" {
		renderDistillHistoryShowText(cmd.OutOrStdout(), env)
		return nil
	}
	writeJournalEnvelope(cmd.OutOrStdout(), flags.Format, env)
	return nil
}

// emitDistillHistoryShowUsageError wraps a usage error into a typed exit
// value, writes the envelope to stdout in the caller's requested
// format (defaulting to JSON when the format itself is being
// rejected), and returns the exit-2 error for main.go to unwrap.
// Mirrors the distill_history_list pattern so behavior is consistent.
func emitDistillHistoryShowUsageError(w io.Writer, format string, u *distillHistoryUsageError, elapsedMS int64) error {
	exit := newDistillHistoryUsageExit(u, elapsedMS)
	writeJournalEnvelope(w, distillHistoryShowErrorFormat(format), &exit.Envelope)
	return exit
}

// distillHistoryShowErrorFormat maps show's three output formats to the
// format writeJournalEnvelope understands for error rendering. JSON
// and text pass through; content falls back to JSON so a caller in
// content mode still gets a machine-parseable failure instead of a
// silent stdout.
func distillHistoryShowErrorFormat(format string) string {
	if format == "content" {
		return "json"
	}
	return format
}

// buildDistillHistoryShowEnvelope maps a LoadEntries result into the shared
// distillHistoryEnvelope. Unlike list, show populates BodyMD + Citations on
// every ok row and leaves Window nil (show is not window-scoped).
// Per-entry Status/Error are stamped onto envelope rows via the
// extended fields on distillHistoryEnvelopeEntry — see distill_history_envelope.go.
func buildDistillHistoryShowEnvelope(entries []read.Entry, elapsedMS int64) *distillHistoryEnvelope {
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
			Citations:     e.Citations,
			BodyMD:        e.BodyMD,
			Status:        e.Status,
		}
		if !e.CreatedAt.IsZero() {
			row.CreatedAt = rfc3339Z(e.CreatedAt)
		}
		if e.Error != nil {
			row.Error = &distillHistoryEnvelopeError{
				Code:      e.Error.Code,
				Message:   e.Error.Message,
				Retryable: false,
			}
		}
		out = append(out, row)
	}
	return &distillHistoryEnvelope{
		Success:   true,
		Type:      "distill_history_show",
		ElapsedMS: elapsedMS,
		Data: &distillHistoryEnvelopeData{
			Entries: out,
		},
	}
}

// renderDistillHistoryShowContent writes ONLY the markdown bodies of every ok
// entry to stdout, with the per-entry marker and the \n---\n separator
// from spec §3.5. Failed entries are skipped — the partial-success
// contract keeps the content stream clean; callers who need failure
// detail must use --format=json.
//
// The marker appears BEFORE each body so the first entry's output
// looks like:
//
//	<!-- entry: 2026-04-12-019c8a3f-0001 -->
//	# Daily Memory — 2026-04-12
//	...
//
// and subsequent entries are preceded by "\n---\n<marker>\n".
func renderDistillHistoryShowContent(w io.Writer, entries []read.Entry) {
	first := true
	for _, e := range entries {
		if e.Status != "ok" {
			continue
		}
		if first {
			fmt.Fprintf(w, "<!-- entry: %s -->\n", e.ID)
			first = false
		} else {
			fmt.Fprintf(w, "\n---\n<!-- entry: %s -->\n", e.ID)
		}
		_, _ = w.Write([]byte(e.BodyMD)) // best-effort terminal output
		if len(e.BodyMD) > 0 && e.BodyMD[len(e.BodyMD)-1] != '\n' {
			_, _ = w.Write([]byte{'\n'}) // best-effort terminal output
		}
	}
}

// renderDistillHistoryShowText is the --format=text renderer. Shape is pinned
// by spec §3.5:587-596:
//
//	entry 2026-04-12-019c8a3f  daily  team=sageox
//	  path: memory/daily/2026-04-12-019c8a3f-....md
//	  facts: 14   citations: 9   source files: 11
//
//	  # Daily Memory — 2026-04-12
//	  ...
//
// Each ok entry emits the header + path + counts block, a blank line,
// and then the body markdown with every line prefixed by two spaces.
// Failed rows (not_found / ambiguous) fall back to a shorter header +
// error line so a human skim still sees the id and why it missed.
// Entries are separated by a blank line so consecutive bodies do not
// run together.
func renderDistillHistoryShowText(w io.Writer, env *distillHistoryEnvelope) {
	if env.Data == nil || len(env.Data.Entries) == 0 {
		fmt.Fprintln(w, "(no entries)")
		return
	}
	for i, e := range env.Data.Entries {
		if i > 0 {
			fmt.Fprintln(w)
		}
		team := e.Team
		if team == "" {
			team = "-"
		}
		status := e.Status
		if status == "" {
			status = "ok"
		}
		if status != "ok" {
			fmt.Fprintf(w, "entry %s  %s  team=%s  status=%s\n",
				e.ID, e.Layer, team, status)
			if e.Error != nil {
				fmt.Fprintf(w, "  error: %s: %s\n", e.Error.Code, e.Error.Message)
			}
			continue
		}
		fmt.Fprintf(w, "entry %s  %s  team=%s\n", e.ID, e.Layer, team)
		fmt.Fprintf(w, "  path: %s\n", e.Path)
		fmt.Fprintf(w, "  facts: %d   citations: %d   source files: %d\n",
			e.FactCount, e.CitationCount, len(e.SourceFiles))
		if e.BodyMD == "" {
			continue
		}
		fmt.Fprintln(w)
		body := strings.TrimRight(e.BodyMD, "\n")
		for _, line := range strings.Split(body, "\n") {
			if line == "" {
				fmt.Fprintln(w)
			} else {
				fmt.Fprintf(w, "  %s\n", line)
			}
		}
	}
}
