package agentwork

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/pkg/summaryeval"
)

// TestNewRunnerCompleter_ForwardsPromptAndExtractsOutput verifies the
// Runner→Completer adapter calls the runner with the prompt and pulls
// text/token counts back into the Completer shape, INCLUDING the
// ModelUsed attribution so judge verdicts can record which model
// produced them.
func TestNewRunnerCompleter_ForwardsPromptAndExtractsOutput(t *testing.T) {
	var lastPrompt string
	mock := NewMockRunner(true)
	mock.RunFunc = func(ctx context.Context, req RunRequest) (*RunResult, error) {
		lastPrompt = req.Prompt
		return &RunResult{
			Output:    "ok",
			TokensIn:  500,
			TokensOut: 200,
			ModelUsed: "haiku-4-5-test",
		}, nil
	}
	c := NewRunnerCompleter(mock)
	res, err := c(context.Background(), "please judge me")
	if err != nil {
		t.Fatalf("completer: %v", err)
	}
	if res.Text != "ok" {
		t.Errorf("Text: got %q", res.Text)
	}
	if res.PromptTokens != 500 || res.CompletionTokens != 200 {
		t.Errorf("tokens: in=%d out=%d", res.PromptTokens, res.CompletionTokens)
	}
	if res.ModelUsed != "haiku-4-5-test" {
		t.Errorf("ModelUsed not forwarded: got %q", res.ModelUsed)
	}
	if lastPrompt != "please judge me" {
		t.Errorf("prompt not forwarded: %q", lastPrompt)
	}
}

// TestNewRunnerCompleter_NonZeroExitCodeIsError prevents a silent
// failure mode where a runner exits with a non-zero code but err==nil
// (e.g., the agent CLI wrote a usage banner to stdout). Without this
// check, the judge would parse the banner as JSON, fail, and either
// noise-log or in the worst case produce a malformed verdict that
// gets cached to disk.
func TestNewRunnerCompleter_NonZeroExitCodeIsError(t *testing.T) {
	mock := NewMockRunner(true)
	mock.RunFunc = func(ctx context.Context, req RunRequest) (*RunResult, error) {
		return &RunResult{
			Output:   "Usage: claude [options]...",
			ExitCode: 2,
		}, nil
	}
	c := NewRunnerCompleter(mock)
	_, err := c(context.Background(), "judge this")
	if err == nil {
		t.Fatal("expected error on non-zero ExitCode, got nil")
	}
	if !strings.Contains(err.Error(), "exit") {
		t.Errorf("error should mention exit code, got: %v", err)
	}
}

// TestMaybeRunJudge_LogDoesNotLeakRationaleOrSuggestions covers the
// privacy-sensitive requirement that only scalar fields leave the
// ledger-local cache path. rationale/suggestions stay in the cache
// JSON — never in logs that might flow to remote observability.
func TestMaybeRunJudge_LogDoesNotLeakRationaleOrSuggestions(t *testing.T) {
	t.Setenv("OX_SUMMARY_JUDGE", "on")

	// Capture slog output into a buffer so we can scan it.
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	h := NewSessionFinalizeHandlerForTest(logger)
	h.SetJudgeCompleter(func(ctx context.Context, prompt string) (summaryeval.CompletionResult, error) {
		return summaryeval.CompletionResult{
			Text: `{
				"dimensions":[{"dimension":"title","score":0.9}],
				"overall":0.85,
				"rationale":"SENSITIVE_RATIONALE_SHOULD_NOT_LEAK_TO_LOGS",
				"suggestions":["SENSITIVE_SUGGESTION_SHOULD_NOT_LEAK_TO_LOGS"]
			}`,
			ModelUsed: "mock-model",
		}, nil
	})

	h.maybeRunJudge("sess", t.TempDir(), &session.SummarizeResponse{Title: "x"})

	logged := logBuf.String()
	for _, forbidden := range []string{
		"SENSITIVE_RATIONALE_SHOULD_NOT_LEAK_TO_LOGS",
		"SENSITIVE_SUGGESTION_SHOULD_NOT_LEAK_TO_LOGS",
	} {
		if strings.Contains(logged, forbidden) {
			t.Errorf("log leaked session-derived content %q:\n%s", forbidden, logged)
		}
	}
	// Positive: scalar fields should be present.
	for _, must := range []string{"overall", "mock-model", "suggestion_count"} {
		if !strings.Contains(logged, must) {
			t.Errorf("log missing expected scalar field %q:\n%s", must, logged)
		}
	}
}

// TestMaybeRunJudge_DaemonContextCancellationIsRespected verifies the
// judge aborts promptly when the daemon's root context is canceled —
// enabling daemon shutdown to complete without waiting up to the
// 3-minute judge deadline.
func TestMaybeRunJudge_DaemonContextCancellationIsRespected(t *testing.T) {
	t.Setenv("OX_SUMMARY_JUDGE", "on")

	daemonCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Completer blocks until ctx is canceled.
	completerReady := make(chan struct{})
	h := NewSessionFinalizeHandlerForTest(nil)
	h.SetDaemonContext(daemonCtx)
	h.SetJudgeCompleter(func(ctx context.Context, prompt string) (summaryeval.CompletionResult, error) {
		close(completerReady)
		<-ctx.Done()
		return summaryeval.CompletionResult{}, ctx.Err()
	})

	done := make(chan struct{})
	go func() {
		h.maybeRunJudge("s", t.TempDir(), &session.SummarizeResponse{Title: "x"})
		close(done)
	}()

	<-completerReady
	// Simulate daemon shutdown.
	cancel()

	select {
	case <-done:
		// Judge returned promptly after daemon ctx canceled. Good.
	case <-time.After(5 * time.Second):
		t.Fatal("judge did not abort on daemon context cancellation within 5s")
	}
}

// TestMaybeRunJudge_GatedByEnvVar confirms the judge is a no-op when
// OX_SUMMARY_JUDGE is unset, even with a valid completer installed.
// This is the load-bearing safety: operators shouldn't pay LLM cost
// until they flip the switch.
func TestMaybeRunJudge_GatedByEnvVar(t *testing.T) {
	t.Setenv("OX_SUMMARY_JUDGE", "")

	h := NewSessionFinalizeHandlerForTest(nil)
	h.SetJudgeCompleter(func(ctx context.Context, prompt string) (summaryeval.CompletionResult, error) {
		t.Fatal("completer must not be called when OX_SUMMARY_JUDGE is unset")
		return summaryeval.CompletionResult{}, nil
	})

	ledgerDir := t.TempDir()
	h.maybeRunJudge("session-x", ledgerDir, &session.SummarizeResponse{Title: "x"})

	// No cache file should exist.
	cacheDir := filepath.Join(ledgerDir, ".sageox", "cache", "summary-judge")
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Errorf("cache dir should not be created when judge is disabled")
	}
}

// TestMaybeRunJudge_WritesVerdictToCacheWhenEnabled covers the happy
// path: env var on + completer returns a judge-shaped JSON.
func TestMaybeRunJudge_WritesVerdictToCacheWhenEnabled(t *testing.T) {
	t.Setenv("OX_SUMMARY_JUDGE", "on")

	completerCalled := false
	h := NewSessionFinalizeHandlerForTest(nil)
	h.SetJudgeCompleter(func(ctx context.Context, prompt string) (summaryeval.CompletionResult, error) {
		completerCalled = true
		if !strings.Contains(prompt, "Session Summary Quality Judge") {
			t.Errorf("prompt missing judge header")
		}
		return summaryeval.CompletionResult{
			Text: `{
				"dimensions":[{"dimension":"title","score":0.9}],
				"overall":0.85,
				"rationale":"solid",
				"suggestions":[]
			}`,
			ModelUsed:    "mock",
			PromptTokens: 1000,
		}, nil
	})

	ledgerDir := t.TempDir()
	h.maybeRunJudge("session-y", ledgerDir, &session.SummarizeResponse{
		Title:   "A valid title",
		Summary: "A body with substance.",
		Outcome: "success",
	})

	if !completerCalled {
		t.Fatal("completer was not called despite env var=on")
	}

	cachePath := filepath.Join(ledgerDir, ".sageox", "cache", "summary-judge", "session-y.json")
	data, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("cache file not written: %v", err)
	}
	var result summaryeval.JudgeResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("cache file not valid JSON: %v", err)
	}
	if result.Overall != 0.85 {
		t.Errorf("Overall: got %f", result.Overall)
	}
	if result.Name != "session-y" {
		t.Errorf("Name: got %q", result.Name)
	}
}

// TestMaybeRunJudge_FailuresAreSwallowed: a judge completer that errors
// or returns malformed JSON must NOT crash or block the caller. The
// whole purpose of the judge is diagnostic; it can't be a regression
// vector for session finalization.
func TestMaybeRunJudge_FailuresAreSwallowed(t *testing.T) {
	t.Setenv("OX_SUMMARY_JUDGE", "on")

	cases := map[string]summaryeval.Completer{
		"completer error": func(ctx context.Context, prompt string) (summaryeval.CompletionResult, error) {
			return summaryeval.CompletionResult{}, context.DeadlineExceeded
		},
		"malformed json": func(ctx context.Context, prompt string) (summaryeval.CompletionResult, error) {
			return summaryeval.CompletionResult{Text: "I refuse to judge"}, nil
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := NewSessionFinalizeHandlerForTest(nil)
			h.SetJudgeCompleter(c)
			ledgerDir := t.TempDir()
			// Must not panic.
			h.maybeRunJudge("s", ledgerDir, &session.SummarizeResponse{Title: "x"})
		})
	}
}

// TestMaybeRunJudge_NilSummaryIsNoOp prevents a nil-deref on an
// unexpected call path.
func TestMaybeRunJudge_NilSummaryIsNoOp(t *testing.T) {
	t.Setenv("OX_SUMMARY_JUDGE", "on")
	h := NewSessionFinalizeHandlerForTest(nil)
	h.SetJudgeCompleter(func(ctx context.Context, prompt string) (summaryeval.CompletionResult, error) {
		t.Fatal("should not call completer with nil summary")
		return summaryeval.CompletionResult{}, nil
	})
	h.maybeRunJudge("s", t.TempDir(), nil)
}

// TestMaybeRunJudge_NoCompleterIsNoOp confirms the ambient safety net:
// installing the judge completer is what enables judging, and its
// absence is the permanent off state.
func TestMaybeRunJudge_NoCompleterIsNoOp(t *testing.T) {
	t.Setenv("OX_SUMMARY_JUDGE", "on")
	h := NewSessionFinalizeHandlerForTest(nil)
	// Do NOT SetJudgeCompleter.
	ledgerDir := t.TempDir()
	h.maybeRunJudge("s", ledgerDir, &session.SummarizeResponse{Title: "x"})
	cacheDir := filepath.Join(ledgerDir, ".sageox", "cache", "summary-judge")
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Errorf("no completer → no cache dir; got one")
	}
}
