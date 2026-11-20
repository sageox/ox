// Package livesummary writes the summary a live coding session closes with: the
// summarizer, prompt and validators `ox session import` uses, run through the
// CLI of the agent that recorded the session (the person's own login and
// model). Both closers use it: `ox session stop`, and the daemon when it
// finishes the close a SessionEnd hook asked for.
package livesummary

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

// Bounded is the summary for a session of the given agent (any adapter name),
// or "" for a thin session, a failure or a timeout, so the close never waits on
// it longer than livesession.SummaryBudget and never fails because of it.
// entries are raw.jsonl-shaped lines, already redacted.
func Bounded(ctx context.Context, log *slog.Logger, adapterName string, entries []map[string]any) string {
	agent := adapters.CanonicalAdapterName(adapterName)
	runner := Runner(agent)
	return livesession.BoundedSummary(ctx, livesession.SummaryRun{
		Log: log.With("agent", agent),
		Summarize: func(ctx context.Context) (string, error) {
			return Summarize(ctx, runner, agent, sessionsummary.EntriesFromRaw(entries))
		},
	})
}

// ForStream is Bounded over a stream's own spool: exactly what the stream
// carries, so a conversation resumed after an agent exit is summarized whole.
func ForStream(ctx context.Context, log *slog.Logger, dir string, st livesession.State) string {
	stored, err := session.ReadSessionFromPath(filepath.Join(dir, "spool.jsonl"))
	if err != nil {
		log.WarnContext(ctx, "live_session_summary", "outcome", "error", "error", "read spool")
		return ""
	}
	return Bounded(ctx, log, st.Agent, stored.Entries)
}

// Runner is the summarizer CLI for an agent, or nil for one whose
// CLI cannot summarize a transcript headless: its sessions close without one.
func Runner(agent string) agentwork.Runner {
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

// Summarize generates the summary markdown for entries. A single
// attempt: the stop is waiting, and a rejected or failed answer is not worth a
// retry the budget may not cover.
func Summarize(ctx context.Context, runner agentwork.Runner, agent string, entries []sessionsummary.Entry) (string, error) {
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
	md := Markdown(resp)
	if md == "" {
		return "", errors.New("summary is empty")
	}
	return md, nil
}

// Markdown is the summary.md body the viewer shows: title, one
// paragraph, key actions. The close scans it for secrets before it is sent.
func Markdown(resp *sessionsummary.SummarizeResponse) string {
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
