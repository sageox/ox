package livesummary

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon/agentwork"
	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/sessionstream"
	"github.com/sageox/ox/pkg/sessionsummary"
)

type fakeSummaryRunner struct {
	available bool
	run       func(ctx context.Context, req agentwork.RunRequest) (*agentwork.RunResult, error)
	// atomic: a runner abandoned at the bound is still running when the
	// summary returns, so the test reads this while it may be written
	calls atomic.Int32
}

func (f *fakeSummaryRunner) Available() bool { return f.available }

func (f *fakeSummaryRunner) Run(ctx context.Context, req agentwork.RunRequest) (*agentwork.RunResult, error) {
	f.calls.Add(1)
	return f.run(ctx, req)
}

func summaryEntries() []sessionsummary.Entry {
	return []sessionsummary.Entry{
		{Type: sessionsummary.EntryTypeUser, Content: "Please add a retry to the upload client and explain how the backoff interacts with the existing timeout."},
		{Type: sessionsummary.EntryTypeAssistant, Content: "I added exponential backoff with jitter to the upload client."},
		{Type: sessionsummary.EntryTypeUser, Content: "Thanks, ship it."},
	}
}

const liveSummaryJSON = `{"title":"Retry for the upload client","summary":"Added exponential backoff with jitter to the upload client so transient failures no longer fail a session upload, and kept the existing overall timeout as the outer bound.","key_actions":["Added backoff with jitter","Kept the overall timeout","Covered the retry path with tests"],"outcome":"success"}`

const skipWithTextJSON = `{"title":"Quick question","summary":"Asked how the upload retry backs off; no code changed.","quality_category":"skip"}`

func TestSummarizeBounded(t *testing.T) {
	tests := []struct {
		name    string
		entries []sessionsummary.Entry
		runner  *fakeSummaryRunner
		// hang: the runner blocks until its context ends, and the bound fires
		// only once it is running
		hang      bool
		want      string // substring of the summary; "" means an empty summary
		wantCalls int32
	}{
		{
			name:    "summary is generated and rendered",
			entries: summaryEntries(),
			runner: &fakeSummaryRunner{available: true, run: func(context.Context, agentwork.RunRequest) (*agentwork.RunResult, error) {
				return &agentwork.RunResult{Output: liveSummaryJSON}, nil
			}},
			want: "# Retry for the upload client", wantCalls: 1,
		},
		{
			// every close carries a summary: too thin for a model call, it
			// gets the deterministic one the Ledger writes
			name:    "trivial session gets the deterministic summary without a model call",
			entries: summaryEntries()[:1],
			runner:  &fakeSummaryRunner{available: true, run: nil},
			want:    "# Brief session", wantCalls: 0,
		},
		{
			name:    "a session the model skips keeps the text the model wrote",
			entries: summaryEntries(),
			runner: &fakeSummaryRunner{available: true, run: func(context.Context, agentwork.RunRequest) (*agentwork.RunResult, error) {
				return &agentwork.RunResult{Output: skipWithTextJSON}, nil
			}},
			want: "# Quick question", wantCalls: 1,
		},
		{
			// a title alone would show no summary card: the body is what the
			// person asked, under the model's title
			name:    "a session the model skips with only a title keeps the title over its prompts",
			entries: summaryEntries(),
			runner: &fakeSummaryRunner{available: true, run: func(context.Context, agentwork.RunRequest) (*agentwork.RunResult, error) {
				return &agentwork.RunResult{Output: `{"title":"Upload retry question","quality_category":"skip"}`}, nil
			}},
			want: "# Upload retry question\n\nA routine session. Asked:\n- Please add a retry", wantCalls: 1,
		},
		{
			// the model may leave a skip with no title or summary; the close
			// still carries what the person asked
			name:    "a session the model skips without text is summarized by its prompts",
			entries: summaryEntries(),
			runner: &fakeSummaryRunner{available: true, run: func(context.Context, agentwork.RunRequest) (*agentwork.RunResult, error) {
				return &agentwork.RunResult{Output: `{"quality_category":"skip","score_reason":"a single routine question"}`}, nil
			}},
			want: "- Please add a retry to the upload client", wantCalls: 1,
		},
		{
			name:    "runner error gives an empty summary",
			entries: summaryEntries(),
			runner: &fakeSummaryRunner{available: true, run: func(context.Context, agentwork.RunRequest) (*agentwork.RunResult, error) {
				return nil, errors.New("not logged in")
			}},
			want: "", wantCalls: 1,
		},
		{
			name:    "unusable output gives an empty summary",
			entries: summaryEntries(),
			runner: &fakeSummaryRunner{available: true, run: func(context.Context, agentwork.RunRequest) (*agentwork.RunResult, error) {
				return &agentwork.RunResult{Output: "I cannot summarize this."}, nil
			}},
			want: "", wantCalls: 1,
		},
		{
			name:    "missing CLI gives an empty summary",
			entries: summaryEntries(),
			runner:  &fakeSummaryRunner{available: false},
			want:    "", wantCalls: 0,
		},
		{
			name:    "a hung summarizer is abandoned at the bound",
			entries: summaryEntries(),
			runner:  &fakeSummaryRunner{available: true},
			hang:    true, want: "", wantCalls: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			entered := make(chan struct{})
			observed := make(chan error, 1)
			if tt.hang {
				tt.runner.run = func(ctx context.Context, _ agentwork.RunRequest) (*agentwork.RunResult, error) {
					close(entered)
					<-ctx.Done() // only the bound's cancel ends it
					observed <- ctx.Err()
					return nil, ctx.Err()
				}
			}
			clock := make(chan time.Time, 1)
			got := make(chan string, 1)
			go func() {
				got <- livesession.BoundedSummary(t.Context(), livesession.SummaryRun{
					After: func(time.Duration) <-chan time.Time { return clock },
					Summarize: func(ctx context.Context) (string, error) {
						return Summarize(ctx, tt.runner, "claude-code", tt.entries)
					},
				})
			}()
			if tt.hang {
				// fire the bound only after the runner is running, so the case
				// cannot pass by timing out before the summarizer starts
				select {
				case <-entered:
				case <-time.After(10 * time.Second): // test watchdog only
					t.Fatal("the summarizer never started")
				}
				clock <- time.Time{}
			}
			var summary string
			select {
			case summary = <-got:
			case <-time.After(10 * time.Second): // test watchdog only
				t.Fatal("BoundedSummary did not return")
			}
			if tt.want == "" && summary != "" || !strings.Contains(summary, tt.want) {
				t.Fatalf("summary = %q, want containing %q", summary, tt.want)
			}
			if tt.hang {
				select {
				case err := <-observed:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("runner context ended with %v, want context.Canceled", err)
					}
				case <-time.After(10 * time.Second): // test watchdog only
					t.Fatal("the abandoned summarizer's context was never canceled")
				}
			}
			if calls := tt.runner.calls.Load(); calls != tt.wantCalls {
				t.Fatalf("runner calls = %d, want %d", calls, tt.wantCalls)
			}
		})
	}
}

func TestSummaryRunsIsolatedWithinBudget(t *testing.T) {
	var req agentwork.RunRequest
	runner := &fakeSummaryRunner{available: true, run: func(_ context.Context, r agentwork.RunRequest) (*agentwork.RunResult, error) {
		req = r
		return &agentwork.RunResult{Output: liveSummaryJSON}, nil
	}}
	if _, err := Summarize(t.Context(), runner, "claude-code", summaryEntries()); err != nil {
		t.Fatal(err)
	}
	// the transcript is untrusted input: no tools, hooks or MCP for the model
	if !req.Isolated || req.TimeoutOverride != livesession.SummaryBudget {
		t.Fatalf("request = isolated %v timeout %v, want isolated and the %v bound", req.Isolated, req.TimeoutOverride, livesession.SummaryBudget)
	}
}

func TestSummaryErrorNeverCarriesSummaryContent(t *testing.T) {
	const leaked = "SECRET-TRANSCRIPT-EXCERPT"
	bad := strings.Replace(liveSummaryJSON, `"outcome":"success"`, `"outcome":"`+leaked+`"`, 1)
	runner := &fakeSummaryRunner{available: true, run: func(context.Context, agentwork.RunRequest) (*agentwork.RunResult, error) {
		return &agentwork.RunResult{Output: bad}, nil
	}}
	_, err := Summarize(t.Context(), runner, "claude-code", summaryEntries())
	if err == nil {
		t.Fatal("a summary with an invalid outcome was accepted")
	}
	if strings.Contains(err.Error(), leaked) {
		t.Fatalf("error %q quotes rejected summary content, which BoundedSummary logs", err)
	}
}

func TestRunnerErrorNeverCarriesCLIOutput(t *testing.T) {
	t.Parallel()
	// ClaudeRunner quotes up to 2 KiB of the CLI's stdout or stderr in its
	// error, and that output can be transcript-derived model text
	const leaked = "SECRET-TRANSCRIPT-EXCERPT"
	runner := &fakeSummaryRunner{available: true, run: func(context.Context, agentwork.RunRequest) (*agentwork.RunResult, error) {
		return nil, errors.New("claude exited with code 1: " + leaked)
	}}
	_, err := Summarize(t.Context(), runner, "claude-code", summaryEntries())
	if err == nil {
		t.Fatal("a failed summarizer run was accepted")
	}
	if strings.Contains(err.Error(), leaked) {
		t.Fatalf("error %q quotes the CLI's output, which BoundedSummary logs", err)
	}
	if calls := runner.calls.Load(); calls != 1 {
		t.Fatalf("runner calls = %d, want 1", calls)
	}
}

// Failure prevented (ox #2009 review): a thin session's summary echoed whole
// prompts, so one long unanswered prompt pushed it past the close's wire
// limit, and the close dropped it. The prompt is cut, and the summary stays
// far below the limit.
func TestABriefSessionsSummaryStaysUnderTheWireLimit(t *testing.T) {
	huge := strings.Repeat("log line from a pasted diagnostic dump ", 2000) // ~78 KiB
	entries := []sessionsummary.Entry{{Type: sessionsummary.EntryTypeUser, Content: huge}}
	got, err := Summarize(t.Context(), &fakeSummaryRunner{available: true}, "claude-code", entries)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "# Brief session") || !strings.Contains(got, "…") {
		t.Fatalf("summary = %.120q, want the brief heading and a cut prompt", got)
	}
	if len(got) > sessionstream.MaxSummaryBytes/16 {
		t.Fatalf("summary is %d bytes, want far below the %d-byte wire limit", len(got), sessionstream.MaxSummaryBytes)
	}
}

// Failure prevented (ox #2009 review): a close with no prompt to quote (an
// assistant-only transcript, or every prompt withheld as a secret) carried no
// summary at all. It carries the fixed brief line, quoting nothing.
func TestAPromptlessCloseStillCarriesASummary(t *testing.T) {
	entries := []sessionsummary.Entry{{Type: sessionsummary.EntryTypeAssistant, Content: "Hello."}}
	got, err := Summarize(t.Context(), &fakeSummaryRunner{available: true}, "claude-code", entries)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "# Brief session") || !strings.Contains(got, "No prompt is available to quote.") {
		t.Fatalf("summary = %q, want the fixed brief summary", got)
	}
}
