package livesession

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// SummaryBudget is the longest a stop waits for the session summary before it
// closes the stream without one. `session stop` runs inside the agent's Bash
// tool, whose default timeout is 2 minutes, and a stop killed mid-way leaves
// the stream unclosed (which also breaks resume-after-stop). Finalize, this
// wait and the CloseBudget (15 s) must therefore stay well under that, so the
// summary gets 60 s: enough for one model call over a typical transcript, and
// a slower one is not worth risking the close.
const SummaryBudget = 60 * time.Second

// ErrSummarySkipped is what a summarizer returns for a session too thin to
// summarize: the stream closes without a summary, and nothing went wrong.
var ErrSummarySkipped = errors.New("session too thin to summarize")

// SummaryRun is one bounded summary attempt. Time is injected so tests need no
// sleeps.
type SummaryRun struct {
	// Summarize produces the summary text. It must return when ctx ends.
	Summarize func(ctx context.Context) (string, error)
	// Budget is the longest to wait; zero means SummaryBudget.
	Budget time.Duration
	// After is time.After in production.
	After func(time.Duration) <-chan time.Time
	// Now is time.Now in production; it only measures the logged duration.
	Now func() time.Time
	Log *slog.Logger
}

// BoundedSummary runs the summarizer and returns its text, or "" when it was
// skipped, failed or ran past the budget. The caller closes the stream with
// whatever comes back, so a failure here never blocks or fails the close. The
// outcome is logged either way, so "no summary" is explainable from the log.
func BoundedSummary(ctx context.Context, r SummaryRun) string {
	budget := r.Budget
	if budget <= 0 {
		budget = SummaryBudget
	}
	after, now, log := r.After, r.Now, r.Log
	if after == nil {
		after = time.After
	}
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	start := now()
	done := func(outcome string, size int, err error) {
		args := []any{"outcome", outcome, "summary_bytes", size, "elapsed_ms", now().Sub(start).Milliseconds(), "budget_ms", budget.Milliseconds()}
		if err != nil {
			args = append(args, "error", err)
			log.WarnContext(ctx, "live_session_summary", args...)
			return
		}
		log.InfoContext(ctx, "live_session_summary", args...)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel() // on a timeout this also stops the summarizer's process
	type result struct {
		text string
		err  error
	}
	// buffered: a summarizer that finishes after the timeout must not block
	ch := make(chan result, 1)
	go func() {
		text, err := r.Summarize(runCtx)
		ch <- result{text, err}
	}()
	select {
	case res := <-ch:
		switch {
		case errors.Is(res.err, ErrSummarySkipped):
			done("skipped", 0, nil)
			return ""
		case res.err != nil:
			done("error", 0, res.err)
			return ""
		}
		done("sent", len(res.text), nil)
		return res.text
	case <-after(budget):
		done("timeout", 0, nil)
		return ""
	case <-ctx.Done():
		done("canceled", 0, ctx.Err())
		return ""
	}
}
