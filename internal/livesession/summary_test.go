package livesession_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
)

func TestBoundedSummary(t *testing.T) {
	tests := []struct {
		name      string
		summarize func(ctx context.Context) (string, error)
		fire      bool // the injected clock reaches the budget
		want      string
	}{
		{"summary is sent", func(context.Context) (string, error) { return "# Title\n\nbody", nil }, false, "# Title\n\nbody"},
		{"summarizer error gives an empty summary", func(context.Context) (string, error) { return "partial", errors.New("model failed") }, false, ""},
		{"thin session closes without one", func(context.Context) (string, error) { return "", livesession.ErrSummarySkipped }, false, ""},
		{"a summarizer past the budget is abandoned", func(ctx context.Context) (string, error) {
			<-ctx.Done() // returns only when the bound cancels it
			return "late", ctx.Err()
		}, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			clock := make(chan time.Time, 1)
			var asked time.Duration
			run := livesession.SummaryRun{
				Summarize: tt.summarize,
				After: func(d time.Duration) <-chan time.Time {
					asked = d
					return clock
				},
			}
			if tt.fire {
				clock <- time.Time{}
			}
			got := make(chan string, 1)
			go func() { got <- livesession.BoundedSummary(t.Context(), run) }()
			select {
			case s := <-got:
				if s != tt.want {
					t.Fatalf("summary = %q, want %q", s, tt.want)
				}
			case <-time.After(10 * time.Second): // test watchdog only
				t.Fatal("BoundedSummary did not return")
			}
			if asked != livesession.SummaryBudget {
				t.Fatalf("bound = %v, want %v", asked, livesession.SummaryBudget)
			}
		})
	}
}

func TestCloseSendsTheSummary(t *testing.T) {
	e := newEnv(t)
	e.spool(withPrompt(lines(0, 2)))
	if res, err := livesession.Close(t.Context(), e.cfg, e.dir, "# Done\n\nit worked", testBudget); err != nil || !res.Done {
		t.Fatalf("close: %+v %v", res, err)
	}
	closed, ok := e.srv.ClosedWith(e.st.ConversationID)
	if !ok || closed.Summary != "# Done\n\nit worked" {
		t.Fatalf("server saw close %+v, want the summary", closed)
	}
}
