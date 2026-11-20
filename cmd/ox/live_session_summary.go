package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/daemon/agentwork"
	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/pkg/sessionsummary"
)

// liveCloseSummary is the summary a stop closes the live stream with: the same
// summarizer, prompt and validators `ox session import` uses, run through the
// CLI of the agent that recorded the session (the person's own login and
// model). It is bounded by livesession.SummaryBudget and returns "" on a thin
// session, a failure or a timeout, so the close never waits on it for long and
// never fails because of it.
func liveCloseSummary(state *session.RecordingState, raw []map[string]any) string {
	raw = liveConversationEntries(state.SessionPath, raw)
	agent := adapters.CanonicalAdapterName(state.AdapterName)
	runner := liveSummaryRunner(agent)
	log := slog.With("session_id", state.SessionID, "agent", agent)
	return livesession.BoundedSummary(context.Background(), livesession.SummaryRun{
		Log: log,
		Summarize: func(ctx context.Context) (string, error) {
			return summarizeForLive(ctx, runner, agent, sessionsummary.EntriesFromRaw(raw))
		},
	})
}

// liveSummaryRunner is the summarizer CLI for an agent, or nil for one whose
// CLI cannot summarize a transcript headless: its sessions close without one.
func liveSummaryRunner(agent string) agentwork.Runner {
	switch agent {
	case "claude-code", "codex", "gemini":
		name := agent
		if agent == "claude-code" {
			name = "claude"
		}
		return agentwork.NewRunner(name, slog.Default())
	}
	return nil
}

// summarizeForLive generates the summary markdown for entries. A single
// attempt: the stop is waiting, and a rejected or failed answer is not worth a
// retry the budget may not cover.
func summarizeForLive(ctx context.Context, runner agentwork.Runner, agent string, entries []sessionsummary.Entry) (string, error) {
	if _, thin := sessionsummary.MaybeBuildSkipSummary(entries); thin {
		return "", livesession.ErrSummarySkipped
	}
	if runner == nil || !runner.Available() {
		return "", fmt.Errorf("no summarizer CLI available for agent %q", agent)
	}
	workDir, err := os.MkdirTemp("", "ox-live-summary-*")
	if err != nil {
		return "", fmt.Errorf("summary workspace: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }() // scratch dir; nothing to flush

	model := ""
	if agent == "claude-code" {
		model = sessionsummary.DefaultSummaryModel()
	}
	prompt := sessionsummary.BuildInlineSummaryPrompt(sessionsummary.TrimEntriesForBudget(entries, sessionsummary.ImportPromptBudget))
	res, err := runner.Run(ctx, agentwork.RunRequest{
		Prompt: prompt, WorkDir: workDir, Model: model, Isolated: true, TimeoutOverride: livesession.SummaryBudget,
	})
	if err != nil {
		return "", fmt.Errorf("run summarizer: %w", err)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("summarizer exited with code %d", res.ExitCode)
	}
	resp, err := sessionsummary.ParseAndValidate(res.Output, len(entries))
	if err != nil {
		// the validator's error quotes the rejected title, summary or outcome,
		// which are transcript content and must not reach any log
		return "", errors.New("summary validation failed")
	}
	if sessionsummary.IsSkipCategory(resp) {
		return "", livesession.ErrSummarySkipped // the model judged it not worth sharing
	}
	md := liveSummaryMarkdown(resp)
	if md == "" {
		return "", errors.New("summary is empty")
	}
	return md, nil
}

// liveSummaryMarkdown is the summary.md body the viewer shows: title, one
// paragraph, key actions. The close scans it for secrets before it is sent.
func liveSummaryMarkdown(resp *sessionsummary.SummarizeResponse) string {
	var b strings.Builder
	if t := strings.TrimSpace(resp.Title); t != "" {
		b.WriteString("# " + t + "\n\n")
	}
	if s := strings.TrimSpace(resp.Summary); s != "" {
		b.WriteString(s + "\n\n")
	}
	var actions []string
	for _, a := range resp.KeyActions {
		if a = strings.TrimSpace(a); a != "" {
			actions = append(actions, "- "+a)
		}
	}
	if len(actions) > 0 {
		b.WriteString("## Key actions\n\n" + strings.Join(actions, "\n") + "\n")
	}
	return strings.TrimSpace(b.String())
}

// liveConversationEntries is what the live close summarizes. A stream resumed
// after a stop restates the whole conversation (earlier recordings first), so
// its summary must cover all of it, while raw holds only this recording's
// lines. The spool carries exactly what the stream carries: redacted, with
// paused content already left out. Any other stream, or a spool that cannot be
// read, summarizes raw as before.
func liveConversationEntries(dir string, raw []map[string]any) []map[string]any {
	st, err := livesession.ReadState(dir)
	if err != nil || st.SeedBytes == 0 {
		return raw
	}
	stored, err := session.ReadSessionFromPath(filepath.Join(dir, "spool.jsonl"))
	if err != nil || len(stored.Entries) < len(raw) {
		return raw
	}
	return stored.Entries
}
