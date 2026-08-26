package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/distill/history/read"
	"github.com/spf13/cobra"
)

// distillHistoryListFlags captures every user-controllable input to the list
// command. It is populated by the Cobra flag parser and then passed as
// a value — every call builds its own copy so two concurrent invocations
// in the same test process never interact.
//
// The fields map one-to-one onto the flag surface documented in
// distill-history-read-plan.md §3 Unit 3. Defaults are chosen to match agent-ux
// principles: --format defaults to json (machine-first) and --layer
// defaults to auto (let the reader pick based on the window width).
type distillHistoryListFlags struct {
	Since    string
	Until    string
	TZ       string
	Layer    string
	Team     string
	AllTeams bool
	Limit    int
	Format   string
}

var distillHistoryListFlagSet distillHistoryListFlags

func init() {
	distillHistoryListCmd.RunE = runDistillHistoryList
	registerDistillHistoryListFlags(distillHistoryListCmd, &distillHistoryListFlagSet)
}

// registerDistillHistoryListFlags binds every user-controllable flag on
// the provided cobra command. Keeping the binding in one place means the
// in-process test helper can reuse it, so tests cannot go stale while
// the production flag surface drifts out from under them.
func registerDistillHistoryListFlags(cmd *cobra.Command, flags *distillHistoryListFlags) {
	f := cmd.Flags()
	f.StringVar(&flags.Since, "since", "", "start of window (duration like 24h, or absolute RFC3339/YYYY-MM-DD[THH:MM[:SS]])")
	f.StringVar(&flags.Until, "until", "", "end of window (absolute timestamp; defaults to now)")
	f.StringVar(&flags.TZ, "tz", "", "IANA timezone for naked absolute timestamps (e.g. America/Los_Angeles)")
	f.StringVar(&flags.Layer, "layer", "auto", "layer to read: daily|weekly|monthly|auto")
	f.StringVar(&flags.Team, "team", "", "team slug, id, or name (defaults to the repo's active team)")
	f.BoolVar(&flags.AllTeams, "all-teams", false, "merge entries across every registered team context")
	f.IntVar(&flags.Limit, "limit", 0, "cap the number of entries returned (0 = unlimited)")
	f.StringVar(&flags.Format, "format", "json", "output format: json|text")
}

// runDistillHistoryList is the RunE for `ox distill history list`. It resolves the time
// window, picks the team set, calls the reader, and writes the envelope
// (json) or text (human) to stdout. Every error path here has to emit
// something readable on stdout before returning: json-format callers
// parse stdout as an error envelope and the exit code distinguishes
// runtime (1) from usage (2).
func runDistillHistoryList(cmd *cobra.Command, _ []string) error {
	flags := distillHistoryListFlagSet
	start := time.Now()
	elapsed := func() int64 { return time.Since(start).Milliseconds() }

	if flags.Format != "json" && flags.Format != "text" {
		// Malformed --format: write the envelope in json by default so
		// machine parsers still get structured output, then return the
		// typed exit error. main.go unwraps ExitCode=2.
		exit := newDistillHistoryUsageExit(newJournalUsageError(
			fmt.Sprintf("--format must be json or text, got %q", flags.Format)), elapsed())
		writeJournalEnvelope(cmd.OutOrStdout(), "json", &exit.Envelope)
		return exit
	}

	if flags.AllTeams && flags.Team != "" {
		exit := newDistillHistoryUsageExit(
			newJournalUsageError("cannot combine --team and --all-teams"), elapsed())
		writeJournalEnvelope(cmd.OutOrStdout(), flags.Format, &exit.Envelope)
		return exit
	}

	since, until, err := resolveDistillHistoryWindow(flags.Since, flags.Until, flags.TZ, time.Now())
	if err != nil {
		var uerr *distillHistoryUsageError
		if asJournalUsageError(err, &uerr) {
			exit := newDistillHistoryUsageExit(uerr, elapsed())
			writeJournalEnvelope(cmd.OutOrStdout(), flags.Format, &exit.Envelope)
			return exit
		}
		return err
	}

	layer, layerErr := parseJournalLayerFlag(flags.Layer)
	if layerErr != nil {
		exit := newDistillHistoryUsageExit(layerErr, elapsed())
		writeJournalEnvelope(cmd.OutOrStdout(), flags.Format, &exit.Envelope)
		return exit
	}

	projectRoot, err := findProjectRoot()
	if err != nil {
		return writeJournalRuntimeError(cmd.OutOrStdout(), flags.Format, "project_not_found", err.Error(), elapsed())
	}

	teams, err := resolveDistillHistoryTeams(projectRoot, flags.Team, flags.AllTeams)
	if err != nil {
		return writeJournalRuntimeError(cmd.OutOrStdout(), flags.Format, "team_not_found", err.Error(), elapsed())
	}

	q := read.ReadQuery{
		Since:    since,
		Until:    until,
		Layer:    layer,
		Teams:    teams,
		Limit:    flags.Limit,
		WantBody: false,
	}
	entries, meta, err := read.ListEntries(context.Background(), q)
	if err != nil {
		return writeJournalRuntimeError(cmd.OutOrStdout(), flags.Format, "read_failed", err.Error(), elapsed())
	}

	env := buildDistillHistoryListEnvelope(entries, meta, elapsed())
	writeJournalEnvelope(cmd.OutOrStdout(), flags.Format, env)
	emitDistillHistoryWarnings(cmd.ErrOrStderr(), meta.Warnings)
	return nil
}

// asJournalUsageError is a thin wrapper around errors.As that keeps the
// call site one-liner-friendly. Using errors.As (not a direct type
// assertion) handles any future wrapping of the usage error without
// breaking this branch.
func asJournalUsageError(err error, target **distillHistoryUsageError) bool {
	return errors.As(err, target)
}

// parseJournalLayerFlag validates the --layer flag and returns the
// reader-side Layer enum. Empty string and "auto" both map to LayerAuto.
func parseJournalLayerFlag(raw string) (read.Layer, *distillHistoryUsageError) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "auto":
		return read.LayerAuto, nil
	case "daily":
		return read.LayerDaily, nil
	case "weekly":
		return read.LayerWeekly, nil
	case "monthly":
		return read.LayerMonthly, nil
	default:
		return "", newJournalUsageError(
			fmt.Sprintf("--layer must be daily|weekly|monthly|auto, got %q", raw))
	}
}

// resolveDistillHistoryTeams picks the team set the reader should walk. The
// precedence matches the spec's read-command defaults:
//
//  1. --all-teams → every registered team context
//  2. --team=<query> → slug/id/name lookup (uses existing discovery)
//  3. otherwise → the repo's active team only
//
// Returns a usable team slice or an error whose message is suitable for
// the error envelope's message field.
func resolveDistillHistoryTeams(projectRoot, teamFlag string, allTeams bool) ([]read.TeamRef, error) {
	if allTeams {
		all := config.FindAllTeamContexts(projectRoot)
		if len(all) == 0 {
			return nil, fmt.Errorf("no team contexts found for --all-teams")
		}
		out := make([]read.TeamRef, 0, len(all))
		for _, tc := range all {
			if tc.Path == "" {
				continue
			}
			out = append(out, read.TeamRef{Slug: teamSlugOrID(tc), Path: tc.Path})
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("no usable team context paths for --all-teams")
		}
		return out, nil
	}

	if teamFlag != "" {
		t := resolveTeamByQuery(projectRoot, teamFlag)
		if t == nil || t.Path == "" {
			return nil, fmt.Errorf("team not found: %q", teamFlag)
		}
		slug := t.Slug
		if slug == "" {
			slug = t.TeamID
		}
		return []read.TeamRef{{Slug: slug, Path: t.Path}}, nil
	}

	tc := config.FindRepoTeamContext(projectRoot)
	if tc == nil || tc.Path == "" {
		return nil, fmt.Errorf("no team context registered for this repo")
	}
	return []read.TeamRef{{Slug: teamSlugOrID(*tc), Path: tc.Path}}, nil
}

// teamSlugOrID picks the best identifier to stamp on Entry.Team. The
// reader surfaces this verbatim in the envelope; falling back to
// team_id keeps the field populated even for older configs that
// predated the slug column.
func teamSlugOrID(tc config.TeamContext) string {
	if tc.Slug != "" {
		return tc.Slug
	}
	return tc.TeamID
}

// buildDistillHistoryListEnvelope maps a reader result into the JSON envelope
// every distill history read command shares. The Bodies field stays nil — list
// never materializes bodies; that's the show/since contract.
func buildDistillHistoryListEnvelope(entries []read.Entry, meta read.ListMeta, elapsedMS int64) *distillHistoryEnvelope {
	out := make([]distillHistoryEnvelopeEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, distillHistoryEnvelopeEntry{
			ID:            e.ID,
			Layer:         string(e.Layer),
			Date:          e.Date,
			Team:          e.Team,
			Path:          e.RelPath,
			FactCount:     e.FactCount,
			CitationCount: e.CitationCount,
			SourceFiles:   nonNilStringSlice(e.SourceFiles),
			CreatedAt:     rfc3339Z(e.CreatedAt),
		})
	}
	win := &distillHistoryEnvelopeWindow{
		Since:         rfc3339Z(meta.EffectiveSince),
		Until:         rfc3339Z(meta.EffectiveUntil),
		LayerResolved: string(meta.LayerResolved),
	}
	return &distillHistoryEnvelope{
		Success:   true,
		Type:      "distill_history_list",
		ElapsedMS: elapsedMS,
		Data: &distillHistoryEnvelopeData{
			Entries:   out,
			Window:    win,
			Truncated: meta.Truncated,
		},
	}
}

// nonNilStringSlice returns a non-nil []string so json.Marshal emits
// `[]` instead of `null` for an absent slice. The spec §4.3 contract
// says `source_files` is always present; pairing that with a
// no-omitempty JSON tag requires the Go value to be a real empty slice.
func nonNilStringSlice(s []string) []string {
	if len(s) == 0 {
		return []string{}
	}
	return s
}

// writeJournalEnvelope renders env to w in the requested format. JSON is
// marshaled without indent and terminated by exactly one newline (per
// spec §4.5). Text format delegates to renderDistillHistoryListText when the
// envelope is a list-shaped success, and falls back to a one-line error
// summary otherwise.
func writeJournalEnvelope(w io.Writer, format string, env *distillHistoryEnvelope) {
	if format == "text" {
		if !env.Success && env.Error != nil {
			fmt.Fprintf(w, "Error: %s: %s\n", env.Error.Code, env.Error.Message)
			return
		}
		if env.Data != nil {
			renderDistillHistoryListText(w, env)
			return
		}
	}
	b, err := json.Marshal(env)
	if err != nil {
		fmt.Fprintf(w, `{"success":false,"error":{"code":"envelope_marshal_failed","message":%q,"retryable":false}}`+"\n", err.Error())
		return
	}
	_, _ = w.Write(b)            // best-effort terminal output
	_, _ = w.Write([]byte{'\n'}) // best-effort terminal output
}

// writeJournalRuntimeError emits an error envelope (exit code 1) onto
// stdout in the requested format. Returns nil so runDistillHistoryList falls
// through to its natural end-of-function — runtime errors print their
// envelope but do NOT propagate a Go error back to cobra, because we
// don't want friction recovery or the default error printer to touch
// them (the envelope is already on stdout).
//
// Caller decides the code (team_not_found, read_failed, etc.).
func writeJournalRuntimeError(w io.Writer, format, code, msg string, elapsedMS int64) error {
	env := &distillHistoryEnvelope{
		Success:   false,
		ElapsedMS: elapsedMS,
		Error: &distillHistoryEnvelopeError{
			Code:      code,
			Message:   msg,
			Retryable: false,
		},
	}
	writeJournalEnvelope(w, format, env)
	return &distillHistoryExitError{ExitCode: 1, Envelope: *env}
}

// emitDistillHistoryWarnings writes one line per reader warning to stderr. The
// format is intentionally minimal — agent-ux says warnings belong on
// stderr, never in the stdout envelope, so callers grepping stdout JSON
// do not trip on progress noise.
func emitDistillHistoryWarnings(w io.Writer, warnings []read.Warning) {
	for _, wg := range warnings {
		fmt.Fprintf(w, "warning: %s: %s\n", wg.Code, wg.Message)
	}
}

// renderDistillHistoryListText is the --format=text renderer. It prints one
// line per entry with fixed columns so a human can eyeball a listing
// without parsing JSON. The columns match the planning doc's
// "text-format discipline": date, layer, id, team, fact/citation count.
// The trailing blank line separates the entry list from the window
// summary.
func renderDistillHistoryListText(w io.Writer, env *distillHistoryEnvelope) {
	if env.Data == nil {
		return
	}
	if len(env.Data.Entries) == 0 {
		fmt.Fprintln(w, "(no entries)")
	}
	for _, e := range env.Data.Entries {
		team := e.Team
		if team == "" {
			team = "-"
		}
		fmt.Fprintf(w, "%s  %-7s  %s  team=%s  facts=%d  cites=%d\n",
			e.Date, e.Layer, e.ID, team, e.FactCount, e.CitationCount)
	}
	if env.Data.Window != nil {
		fmt.Fprintf(w, "\nwindow: %s .. %s  layer=%s\n",
			env.Data.Window.Since, env.Data.Window.Until, env.Data.Window.LayerResolved)
	}
	if env.Data.Truncated {
		fmt.Fprintln(w, "(truncated by --limit)")
	}
}
