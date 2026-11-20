package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon/agentwork"
	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/livesession/livesessiontest"
	"github.com/sageox/ox/internal/sessionid"
	"github.com/sageox/ox/pkg/sessionsummary"
	"github.com/stretchr/testify/require"
)

type fakeSummaryRunner struct {
	available bool
	run       func(ctx context.Context, req agentwork.RunRequest) (*agentwork.RunResult, error)
	calls     int
}

func (f *fakeSummaryRunner) Available() bool { return f.available }

func (f *fakeSummaryRunner) Run(ctx context.Context, req agentwork.RunRequest) (*agentwork.RunResult, error) {
	f.calls++
	return f.run(ctx, req)
}

func liveSummaryEntries() []sessionsummary.Entry {
	return []sessionsummary.Entry{
		{Type: sessionsummary.EntryTypeUser, Content: "Please add a retry to the upload client and explain how the backoff interacts with the existing timeout."},
		{Type: sessionsummary.EntryTypeAssistant, Content: "I added exponential backoff with jitter to the upload client."},
		{Type: sessionsummary.EntryTypeUser, Content: "Thanks, ship it."},
	}
}

const liveSummaryJSON = `{"title":"Retry for the upload client","summary":"Added exponential backoff with jitter to the upload client so transient failures no longer fail a session upload, and kept the existing overall timeout as the outer bound.","key_actions":["Added backoff with jitter","Kept the overall timeout","Covered the retry path with tests"],"outcome":"success"}`

func TestLiveCloseSummary(t *testing.T) {
	tests := []struct {
		name      string
		entries   []sessionsummary.Entry
		runner    *fakeSummaryRunner
		fireClock bool
		want      string // substring of the summary; "" means an empty summary
		wantCalls int
	}{
		{
			name:    "summary is generated and rendered",
			entries: liveSummaryEntries(),
			runner: &fakeSummaryRunner{available: true, run: func(context.Context, agentwork.RunRequest) (*agentwork.RunResult, error) {
				return &agentwork.RunResult{Output: liveSummaryJSON}, nil
			}},
			want: "# Retry for the upload client", wantCalls: 1,
		},
		{
			name:    "trivial session is skipped before any model call",
			entries: liveSummaryEntries()[:1],
			runner:  &fakeSummaryRunner{available: true, run: nil},
			want:    "", wantCalls: 0,
		},
		{
			name:    "runner error gives an empty summary",
			entries: liveSummaryEntries(),
			runner: &fakeSummaryRunner{available: true, run: func(context.Context, agentwork.RunRequest) (*agentwork.RunResult, error) {
				return nil, errors.New("not logged in")
			}},
			want: "", wantCalls: 1,
		},
		{
			name:    "unusable output gives an empty summary",
			entries: liveSummaryEntries(),
			runner: &fakeSummaryRunner{available: true, run: func(context.Context, agentwork.RunRequest) (*agentwork.RunResult, error) {
				return &agentwork.RunResult{Output: "I cannot summarize this."}, nil
			}},
			want: "", wantCalls: 1,
		},
		{
			name:    "missing CLI gives an empty summary",
			entries: liveSummaryEntries(),
			runner:  &fakeSummaryRunner{available: false},
			want:    "", wantCalls: 0,
		},
		{
			name:    "a hung summarizer is abandoned at the bound",
			entries: liveSummaryEntries(),
			runner: &fakeSummaryRunner{available: true, run: func(ctx context.Context, _ agentwork.RunRequest) (*agentwork.RunResult, error) {
				<-ctx.Done() // only the bound's cancel ends it
				return nil, ctx.Err()
			}},
			fireClock: true, want: "", wantCalls: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			clock := make(chan time.Time, 1)
			if tt.fireClock {
				clock <- time.Time{}
			}
			got := livesession.BoundedSummary(t.Context(), livesession.SummaryRun{
				After: func(time.Duration) <-chan time.Time { return clock },
				Summarize: func(ctx context.Context) (string, error) {
					return summarizeForLive(ctx, tt.runner, "claude-code", tt.entries)
				},
			})
			if tt.want == "" && got != "" || !strings.Contains(got, tt.want) {
				t.Fatalf("summary = %q, want containing %q", got, tt.want)
			}
		})
	}
}

func TestLiveSummaryRunsIsolatedWithinBudget(t *testing.T) {
	var req agentwork.RunRequest
	runner := &fakeSummaryRunner{available: true, run: func(_ context.Context, r agentwork.RunRequest) (*agentwork.RunResult, error) {
		req = r
		return &agentwork.RunResult{Output: liveSummaryJSON}, nil
	}}
	if _, err := summarizeForLive(t.Context(), runner, "claude-code", liveSummaryEntries()); err != nil {
		t.Fatal(err)
	}
	// the transcript is untrusted input: no tools, hooks or MCP for the model
	if !req.Isolated || req.TimeoutOverride != livesession.SummaryBudget {
		t.Fatalf("request = isolated %v timeout %v, want isolated and the %v bound", req.Isolated, req.TimeoutOverride, livesession.SummaryBudget)
	}
}

func TestLiveSummaryErrorNeverCarriesSummaryContent(t *testing.T) {
	const leaked = "SECRET-TRANSCRIPT-EXCERPT"
	bad := strings.Replace(liveSummaryJSON, `"outcome":"success"`, `"outcome":"`+leaked+`"`, 1)
	runner := &fakeSummaryRunner{available: true, run: func(context.Context, agentwork.RunRequest) (*agentwork.RunResult, error) {
		return &agentwork.RunResult{Output: bad}, nil
	}}
	_, err := summarizeForLive(t.Context(), runner, "claude-code", liveSummaryEntries())
	if err == nil {
		t.Fatal("a summary with an invalid outcome was accepted")
	}
	if strings.Contains(err.Error(), leaked) {
		t.Fatalf("error %q quotes rejected summary content, which BoundedSummary logs", err)
	}
}

func TestLiveConversationEntriesCoverTheRestatedSession(t *testing.T) {
	srv := livesessiontest.New(t)
	cfg := srv.Config(t)
	sessions := t.TempDir()
	first := filepath.Join(sessions, "first")
	require.NoError(t, os.Mkdir(first, 0o755))
	firstID := sessionid.GenerateSessionID()
	_, err := livesession.Prepare(t.Context(), cfg, first, livesession.OpenParams{SessionID: firstID, SessionName: "first", RepoID: "repo_test", Agent: "claude-code", StartedAt: time.Now()})
	require.NoError(t, err)
	earlier := `{"type":"header","metadata":{"session_id":"first"}}` + "\n" +
		`{"type":"user","content":"implement authentication for the upload service"}` + "\n" +
		`{"type":"assistant","content":"authentication implemented"}` + "\n"
	_, err = livesession.Spool(t.Context(), first, []byte(earlier), livesession.SpoolOptions{})
	require.NoError(t, err)
	res, err := livesession.Close(t.Context(), cfg, first, "", 5*time.Second)
	require.NoError(t, err)
	require.True(t, res.Done)

	src, err := livesession.FindReopenSource(t.Context(), cfg, sessions, firstID)
	require.NoError(t, err)
	require.NotNil(t, src)
	second := filepath.Join(sessions, "second")
	require.NoError(t, os.Mkdir(second, 0o755))
	_, err = livesession.PrepareReopen(t.Context(), cfg, second, livesession.OpenParams{SessionID: sessionid.GenerateSessionID()}, src)
	require.NoError(t, err)
	_, err = livesession.Spool(t.Context(), second, []byte(`{"type":"user","content":"fix a typo"}`+"\n"), livesession.SpoolOptions{})
	require.NoError(t, err)

	ownLines := []map[string]any{{"type": "user", "content": "fix a typo"}}
	got := liveConversationEntries(second, ownLines)
	var text strings.Builder
	for _, e := range got {
		text.WriteString(fmt.Sprint(e["content"]))
	}
	require.Contains(t, text.String(), "implement authentication", "the summary must cover the earlier recording")
	require.Contains(t, text.String(), "fix a typo")

	// a stream that was never reopened summarizes its own lines, as before
	require.Equal(t, ownLines, liveConversationEntries(first, ownLines))
}
