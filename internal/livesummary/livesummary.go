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
		// every close carries a summary (owner ruling, 2026-11-20): a session
		// too thin to be worth a model call is summarized by what the person
		// asked. Not the Ledger's stub: it echoes whole prompts, and a long one
		// would push the summary past the wire limit, which drops it.
		return markdownOrSkipped(promptSummary(entries, "Brief session", "A brief session."))
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
		// the runner's error can quote up to 2 KiB of the CLI's stdout or
		// stderr, which is transcript-derived model text, and BoundedSummary
		// logs this error: keep only the cancel or deadline cause
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("run summarizer: %w", ctxErr)
		}
		return "", errors.New("run summarizer failed")
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
		// The model judged it not worth sharing, and may then leave out its
		// title and summary. A live close still carries one: the model's own
		// text when it wrote any, else what the person asked.
		if md := Markdown(resp); md != "" {
			return md, nil
		}
		return markdownOrSkipped(promptSummary(entries, "Routine session", "A routine session."))
	}
	md := Markdown(resp)
	if md == "" {
		return "", errors.New("summary is empty")
	}
	return md, nil
}

// markdownOrSkipped is resp's markdown, or ErrSummarySkipped when it renders
// to nothing.
func markdownOrSkipped(resp *sessionsummary.SummarizeResponse) (string, error) {
	if resp == nil {
		return "", livesession.ErrSummarySkipped
	}
	if md := Markdown(resp); md != "" {
		return md, nil
	}
	return "", livesession.ErrSummarySkipped
}

// promptsShown and promptRunes bound promptSummary: a session summarized by
// what the person asked shows the first few prompts, each cut to a sentence
// or two. That says what the session was about without restating the
// transcript the page already shows, and keeps the summary far below the
// close's wire limit (sessionstream.MaxSummaryBytes) however long a prompt is.
const (
	promptsShown = 3
	promptRunes  = 200
)

// promptSummary is the summary of a session no model summary covers: lead,
// then the person's first prompts, in order. Nil when there is no prompt.
// The entries are what the stream carried (see the callers), so a prompt the
// stream withheld as a secret is never copied here.
func promptSummary(entries []sessionsummary.Entry, title, lead string) *sessionsummary.SummarizeResponse {
	var prompts []string
	total := 0
	for _, e := range entries {
		c := strings.TrimSpace(e.Content)
		if e.Type != sessionsummary.EntryTypeUser || c == "" {
			continue
		}
		total++
		if len(prompts) < promptsShown {
			if r := []rune(c); len(r) > promptRunes {
				c = strings.TrimSpace(string(r[:promptRunes])) + "…"
			}
			prompts = append(prompts, c)
		}
	}
	if len(prompts) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(lead + " Asked:")
	for _, p := range prompts {
		b.WriteString("\n- " + strings.Join(strings.Fields(p), " "))
	}
	if more := total - len(prompts); more > 0 {
		fmt.Fprintf(&b, "\n\n(and %d more)", more)
	}
	return &sessionsummary.SummarizeResponse{Title: title, Summary: b.String()}
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
