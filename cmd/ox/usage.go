package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/glance"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/recap"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/usage"
	"github.com/spf13/cobra"
)

var usageCmd = &cobra.Command{
	Use:   "brief",
	Short: "Show what SageOx gave this session",
	Long: `Show what SageOx gave this session: the ox calls your AI coworker made, how
much context ox put in front of the model (estimated tokens), what it read by
title (with sageox.ai links where one exists), and which items have evidence
they were used. Murmurs are listed only when they were useful.

When called by an AI coworker it emits JSON with a pre-rendered display and
guidance; in a bare terminal it prints the same report. --me rolls up your
recent sessions; --team shows a team-wide aggregate only — never who did what.

In the session view, calls.recent in the JSON lists the 10 newest ox calls;
--recent N widens it (0 = every call). The display text is unchanged.`,
	Example: `  ox brief                     # this session
  ox brief --session <name>    # a recorded session in the Ledger
  ox brief --me --since 14d    # your recent sessions, one row each
  ox brief --team              # team-wide aggregate (no per-person data)
  ox brief --json              # structured report for AI coworkers
  ox brief --json --recent 0   # every ox call in calls.recent`,
	Args: cobra.NoArgs,
	RunE: runUsage,
}

// usageDeprecatedCmd keeps `ox usage` working for one release after the
// rename to `ox brief`. Hidden, and without the pane subcommand, so help and
// generated docs show only `ox brief`.
var usageDeprecatedCmd = &cobra.Command{
	Use:        "usage",
	Short:      "Show what SageOx gave this session (use 'ox brief' instead)",
	Hidden:     true,
	Deprecated: "use 'ox brief' instead",
	Args:       cobra.NoArgs,
	RunE:       runUsage,
}

func init() {
	for _, c := range []*cobra.Command{usageCmd, usageDeprecatedCmd} {
		c.Flags().String("session", "", "report a recorded session by name instead of the current one")
		c.Flags().Bool("me", false, "roll up your recent sessions, one row each")
		c.Flags().Bool("team", false, "team-wide aggregate across all coworkers' sessions (no per-person data)")
		c.Flags().String("since", "7d", "window for --me and --team (e.g. 7d, 24h, or an ISO date)")
		c.Flags().Int("limit", 10, "maximum sessions for --me")
		c.Flags().Int("recent", 10, "session view: ox calls listed in the JSON calls.recent (0 = all)")
		c.Flags().Bool("json", false, "structured JSON report for AI coworkers")
		c.MarkFlagsMutuallyExclusive("session", "me", "team")
	}
}

func runUsage(cmd *cobra.Command, _ []string) error {
	jsonOut, _ := cmd.Flags().GetBool("json")
	if !cmd.Flags().Changed("json") && agentx.IsAgentContext() {
		jsonOut = true
	}
	width := terminalWidth()

	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a SageOx project: %w", err)
	}

	me, _ := cmd.Flags().GetBool("me")
	team, _ := cmd.Flags().GetBool("team")
	if me || team {
		return runUsageRollup(cmd, projectRoot, team, jsonOut, width)
	}

	name, _ := cmd.Flags().GetString("session")
	var in usage.Input
	if name != "" {
		in, err = usageInputForLedgerSession(name)
		if err != nil {
			return err
		}
	} else {
		in = usageInputForCurrentSession(projectRoot, resolveImplicitAgentID())
	}
	in.Endpoint = endpoint.GetForProject(projectRoot)
	in.Now = time.Now()
	in.Width = width
	if cmd.Flags().Changed("recent") {
		recent, _ := cmd.Flags().GetInt("recent")
		switch {
		case recent < 0:
			return fmt.Errorf("--recent must be 0 (all) or positive, got %d", recent)
		case recent == 0:
			in.RecentLimit = usage.RecentAll
		default:
			in.RecentLimit = recent
		}
	}
	r := usage.Build(in)

	return writeUsageReport(cmd.OutOrStdout(), jsonOut, r, func() string { return usage.Human(r, width) })
}

// runUsageRollup renders the --me rollup or the aggregate --team view.
func runUsageRollup(cmd *cobra.Command, projectRoot string, team, jsonOut bool, width int) error {
	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return err
	}
	window, _ := cmd.Flags().GetString("since")
	since, err := glance.ParseTimeFlag(window)
	if err != nil {
		return fmt.Errorf("invalid --since %q: %w", window, err)
	}
	limit, _ := cmd.Flags().GetInt("limit")
	if limit <= 0 {
		return fmt.Errorf("--limit must be positive, got %d", limit)
	}
	in := usage.RollupInput{
		LedgerPath: ledgerPath, Since: since, Until: time.Now(),
		Window: window, Limit: limit, Width: width,
	}
	if team {
		r := usage.BuildTeam(in)
		return writeUsageReport(cmd.OutOrStdout(), jsonOut, r, func() string { return usage.HumanTeam(r, width) })
	}
	in.Identity = currentRecapIdentity(projectRoot)
	r := usage.BuildMe(in)
	return writeUsageReport(cmd.OutOrStdout(), jsonOut, r, func() string { return usage.HumanMe(r, width) })
}

// writeUsageReport prints the JSON payload or the human text and charges the
// bytes to the AI coworker's context budget.
func writeUsageReport(w io.Writer, jsonOut bool, payload any, human func() string) error {
	var n int
	if jsonOut {
		b, err := cli.MarshalJSONIndent(payload)
		if err != nil {
			return err
		}
		n = len(b)
		if err := cli.WriteJSONBytes(w, b); err != nil {
			return err
		}
	} else {
		text := human()
		n = len(text)
		if _, err := io.WriteString(w, text); err != nil {
			return err
		}
	}
	trackContextBytes(int64(n))
	return nil
}

// usageInputForCurrentSession resolves the calling AI coworker's usage trace:
// its live recording's context-trace.jsonl, else its not-recording cache file.
func usageInputForCurrentSession(projectRoot, agentID string) usage.Input {
	in := usage.Input{AgentID: agentID, TraceSource: usage.TraceSourceNone}
	if agent := agentx.CurrentAgent(); agent != nil {
		in.Harness = string(agent.Type())
	}
	if agentID == "" {
		in.TraceReason = "no AI coworker identity found — run this from a primed AI coworker session (ox agent prime)"
		return in
	}
	if sf, err := session.ReadSageoxScore(agentID); err == nil && sf != nil {
		in.ScoreCategory, in.ScoreReason = string(sf.Category), sf.Reason
	}

	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	if err == nil && state != nil && state.SessionPath != "" {
		in.Recording = true
		in.SessionName = filepath.Base(state.SessionPath)
		in.Turns = state.TurnCount
		in.ProducedCommits = len(state.ProducedCommits)
		if h := firstNonEmpty(state.AgentType, state.AdapterName); h != "" {
			in.Harness = h
		}
		in.Events, _ = contexttrace.ReadEvents(filepath.Join(state.SessionPath, contexttrace.FileName))
		in.TraceSource = usage.TraceSourceSession
		return in
	}

	cachePath := filepath.Join(agentTraceCacheDir(projectRoot), agentID+".jsonl")
	if _, statErr := os.Stat(cachePath); statErr == nil {
		in.Events, _ = contexttrace.ReadEvents(cachePath)
		in.TraceSource = usage.TraceSourceAgentCache
		return in
	}
	in.TraceReason = "not recording, and no ox calls by " + agentID + " in the last 7 days"
	return in
}

// usageInputForLedgerSession resolves a recorded session's trace from the
// Ledger. It never hydrates: a trace that is only an LFS stub is reported as
// unavailable with the command that reads it.
func usageInputForLedgerSession(name string) (usage.Input, error) {
	if name != filepath.Base(name) || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return usage.Input{}, fmt.Errorf("invalid session name %q", name)
	}
	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return usage.Input{}, err
	}
	in := usage.Input{SessionName: name, TraceSource: usage.TraceSourceNone}
	meta, err := lfs.ReadSessionMeta(filepath.Join(ledgerPath, "sessions", name))
	if err != nil || meta == nil {
		return usage.Input{}, fmt.Errorf("session %q not found in the Ledger", name)
	}
	if meta.IsDraft() {
		in.TraceReason = "session is still recording; run `ox brief` from that session"
		return in, nil
	}
	in.AgentID = meta.AgentID
	in.Harness = meta.AgentType
	in.ProducedCommits = len(meta.ProducedCommits)
	in.ScoreCategory, in.ScoreReason = meta.SageoxScoreCategory, meta.SageoxScoreReason

	events, dehydrated := recap.ReadTrace(ledgerPath, name)
	switch {
	case dehydrated:
		in.TraceReason = "trace not hydrated locally; run `ox session view " + name + " --context`"
	case len(events) == 0:
		in.TraceReason = "this session has no context trace"
	default:
		in.Events = events
		in.TraceSource = usage.TraceSourceSession
	}
	return in, nil
}
