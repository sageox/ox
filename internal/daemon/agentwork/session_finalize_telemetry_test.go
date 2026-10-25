package agentwork

import (
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/pkg/sessionsummary"
	"github.com/sageox/ox/pkg/summaryeval"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTelemetry captures Record() calls for assertions.
//
// Concurrent-safe because the agentwork TelemetryRecorder contract requires
// it (the daemon's real implementation is goroutine-safe). Tests typically
// drive a single ProcessResult call so the lock is uncontended in practice;
// keeping it correct still costs nothing and prevents flakes if a future
// change adds parallel emit paths.
type fakeTelemetry struct {
	mu     sync.Mutex
	events []recordedEvent
}

type recordedEvent struct {
	name  string
	props map[string]any
}

func (f *fakeTelemetry) Record(event string, props map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// copy props so test assertions are stable if the caller mutates the map after.
	clone := make(map[string]any, len(props))
	for k, v := range props {
		clone[k] = v
	}
	f.events = append(f.events, recordedEvent{name: event, props: clone})
}

func (f *fakeTelemetry) lastByName(name string) *recordedEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.events) - 1; i >= 0; i-- {
		if f.events[i].name == name {
			cp := f.events[i]
			return &cp
		}
	}
	return nil
}

// TestEmitSummarizationTelemetry_BasicFields verifies the canonical event
// shape: mode, model, input_tokens, output_tokens, duration_ms, exit_code,
// quality_score, summary_status, scored. Failure prevented: a future refactor
// drops one of the fields and the cost-vs-quality dashboards go silent.
func TestEmitSummarizationTelemetry_BasicFields(t *testing.T) {
	tel := &fakeTelemetry{}
	h := &SessionFinalizeHandler{telemetry: tel}

	result := &RunResult{
		Output:    "{}",
		Duration:  742 * time.Millisecond,
		ExitCode:  0,
		TokensIn:  18234,
		TokensOut: 1450,
		ModelUsed: "claude-haiku-4-5-20251001",
	}
	resp := &session.SummarizeResponse{
		QualityScore:  0.78,
		SummaryStatus: sessionsummary.SummaryStatusOK,
	}

	h.emitSummarizationTelemetry("2026-04-01T10-00-user-OxTest", "delegated", result, resp, true)

	got := tel.lastByName("summarization")
	require.NotNil(t, got, "summarization event must be emitted")
	assert.Equal(t, "delegated", got.props["mode"])
	assert.Equal(t, "claude-haiku-4-5-20251001", got.props["model"])
	assert.Equal(t, 18234, got.props["input_tokens"])
	assert.Equal(t, 1450, got.props["output_tokens"])
	assert.Equal(t, int64(742), got.props["duration_ms"])
	assert.Equal(t, 0, got.props["exit_code"])
	assert.Equal(t, true, got.props["scored"])
	assert.Equal(t, 0.78, got.props["quality_score"])
	assert.Equal(t, "ok", got.props["summary_status"])
}

// TestEmitSummarizationTelemetry_NilSink_NoOp verifies that handlers without
// telemetry wired (tests, telemetry opt-out) silently no-op rather than
// panic. Failure prevented: forgetting to wire the sink in the test harness
// crashes the entire finalize path.
func TestEmitSummarizationTelemetry_NilSink_NoOp(t *testing.T) {
	h := &SessionFinalizeHandler{telemetry: nil}
	// just verify it doesn't panic
	h.emitSummarizationTelemetry("session", "delegated", &RunResult{}, &session.SummarizeResponse{}, false)
}

// TestEmitSummarizationTelemetry_NilSummaryResp verifies the event still
// emits when summaryResp is nil (defensive — the early failure paths in
// ProcessResult build a stub before reaching emit, but a future regression
// might call this with nil).
func TestEmitSummarizationTelemetry_NilSummaryResp(t *testing.T) {
	tel := &fakeTelemetry{}
	h := &SessionFinalizeHandler{telemetry: tel}

	h.emitSummarizationTelemetry("s", "delegated", &RunResult{TokensIn: 10}, nil, false)

	got := tel.lastByName("summarization")
	require.NotNil(t, got)
	assert.NotContains(t, got.props, "quality_score",
		"quality_score must be omitted when summaryResp is nil")
	assert.NotContains(t, got.props, "summary_status",
		"summary_status must be omitted when summaryResp is nil")
}

// TestBuildPrompt_PrefilterEmitsSkippedEvent verifies that when the
// prefilter triggers (low-value session), BuildPrompt emits a
// `summarization_skipped` telemetry event with the expected fields.
//
// Failure prevented: the prefilter quietly skips the LLM call but no
// event is recorded, so dashboards measuring "we saved tokens" can
// only infer skips from absent `summarization` events — which is
// indistinguishable from any other emission failure (telemetry off,
// daemon restart, network drop). An explicit marker keeps the
// skip-rate signal honest.
func TestBuildPrompt_PrefilterEmitsSkippedEvent(t *testing.T) {
	tel := &fakeTelemetry{}
	handler := NewSessionFinalizeHandler(slog.Default())
	handler.SetTelemetry(tel)

	// Build a thin session that's guaranteed to trip the prefilter:
	// short user content (< 80 chars) and only 2 entries (header + user)
	// — no assistant, no tool. Hits all three prefilter heuristics.
	ledgerPath := t.TempDir()
	sessionName := "2026-05-04T15-00-testuser-OxThIN"
	sessionsDir := filepath.Join(ledgerPath, "sessions", sessionName)
	require.NoError(t, os.MkdirAll(sessionsDir, 0755))
	rawContent := `{"_meta":{"schema_version":"1","agent_type":"claude-code"}}
{"type":"user","content":"hi","seq":1}
`
	rawPath := filepath.Join(sessionsDir, "raw.jsonl")
	require.NoError(t, os.WriteFile(rawPath, []byte(rawContent), 0644))

	item := &WorkItem{
		ID:   "test-prefilter",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionsDir,
			RawPath:    rawPath,
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}

	req, err := handler.BuildPrompt(item)
	require.NoError(t, err)
	require.True(t, req.SkipLLM, "prefilter must short-circuit via SkipLLM")

	got := tel.lastByName("summarization_skipped")
	require.NotNil(t, got, "summarization_skipped event must be emitted on prefilter trigger")

	// Fields the dashboard needs to compute prefilter-skip rate and segment
	// by reason. Pinning their presence here so a future refactor that
	// drops one is caught at test-time, not when the dashboard goes silent.
	assert.Equal(t, sessionsummary.SessionHash(sessionName), got.props["session_hash"],
		"session_hash must match the same hash emitted by EventTokenoptRun, so server-side joins work")
	assert.NotEmpty(t, got.props["reason"], "reason must carry the prefilter's ScoreReason")
	// entry_count is the count of post-EntriesFromRaw entries handed to
	// the prefilter — the _meta header is consumed by ReadSessionFromPath
	// into stored.Meta, not surfaced as a regular entry, so a session
	// with just a header + 1 user line produces 1 entry here.
	assert.Equal(t, 1, got.props["entry_count"])
	assert.Equal(t, 1, got.props["user_prompt_count"])
}

// TestBuildPrompt_NormalSession_NoSkippedEvent verifies the inverse:
// real sessions that pass the prefilter do NOT emit a
// summarization_skipped event. Failure prevented: prefilter starts
// over-firing on real sessions and we don't notice because the event
// fires for everything.
func TestBuildPrompt_NormalSession_NoSkippedEvent(t *testing.T) {
	tel := &fakeTelemetry{}
	handler := NewSessionFinalizeHandler(slog.Default())
	handler.SetTelemetry(tel)

	// createTestSession produces a session intentionally above all prefilter
	// thresholds (substantive user prompt + assistant response). See its
	// docstring for the invariant.
	ledgerPath := createTestSession(t, "2026-05-04T15-00-testuser-OxRealS", nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", "2026-05-04T15-00-testuser-OxRealS")
	rawPath := filepath.Join(sessionDir, "raw.jsonl")

	item := &WorkItem{
		ID:   "test-normal",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    rawPath,
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}

	req, err := handler.BuildPrompt(item)
	require.NoError(t, err)
	assert.False(t, req.SkipLLM, "normal session should not trigger SkipLLM")
	// The transcript can carry prompt injection; a bypassed-permission
	// summarizer would let it drive Bash in the Ledger clone.
	assert.True(t, req.IsolateIfSupported, "the daemon must summarize in an isolated run when the CLI can")
	assert.Nil(t, tel.lastByName("summarization_skipped"),
		"summarization_skipped event must NOT fire on a real session")
}

// TestEmitSummarizationTelemetry_JudgePiggyback verifies that a stashed
// judge result is folded into the summarization event and then cleared
// (so the next session's emit doesn't double-count this judge run).
// Failure prevented: judge tokens leak into the wrong session's event,
// or stay attached forever and pollute every subsequent emit.
func TestEmitSummarizationTelemetry_JudgePiggyback(t *testing.T) {
	tel := &fakeTelemetry{}
	h := &SessionFinalizeHandler{telemetry: tel}

	jr := summaryeval.JudgeResult{
		Overall:          0.71,
		ModelUsed:        "claude-haiku-4-5-20251001",
		DurationMs:       320,
		PromptTokens:     5400,
		CompletionTokens: 220,
	}
	h.lastJudgeResult = &jr

	h.emitSummarizationTelemetry("s1", "delegated", &RunResult{
		TokensIn: 1, TokensOut: 1, ModelUsed: "claude-haiku-4-5-20251001",
	}, &session.SummarizeResponse{}, true)

	first := tel.lastByName("summarization")
	require.NotNil(t, first)
	assert.Equal(t, 0.71, first.props["judge_overall"])
	assert.Equal(t, "claude-haiku-4-5-20251001", first.props["judge_model"])
	assert.Equal(t, int64(320), first.props["judge_duration_ms"])
	assert.Equal(t, 5400, first.props["judge_input_tokens"])
	assert.Equal(t, 220, first.props["judge_output_tokens"])

	// stash should be cleared — second emit must NOT carry judge fields
	assert.Nil(t, h.lastJudgeResult, "judge stash must be cleared after emit")

	h.emitSummarizationTelemetry("s2", "delegated", &RunResult{
		TokensIn: 1, TokensOut: 1, ModelUsed: "claude-haiku-4-5-20251001",
	}, &session.SummarizeResponse{}, true)

	second := tel.events[1]
	assert.NotContains(t, second.props, "judge_overall",
		"second emit must not carry the previous session's judge tokens")
	assert.NotContains(t, second.props, "judge_input_tokens")
}
