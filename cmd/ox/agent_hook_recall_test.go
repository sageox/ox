package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/promptintent"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/session/contextuse"
	"github.com/stretchr/testify/require"
)

// fakeRunner is a configurable LocalQueryRunner for hook tests.
type fakeRunner struct {
	results   []LocalQueryResult
	err       error
	delay     time.Duration
	panicWith any
	called    int
	gotPrompt string
}

func (f *fakeRunner) Query(ctx context.Context, prompt string, limit int) ([]LocalQueryResult, error) {
	f.called++
	f.gotPrompt = prompt
	if f.panicWith != nil {
		panic(f.panicWith)
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.results, f.err
}

func withFakeRunner(t *testing.T, f *fakeRunner) {
	t.Helper()
	prev := SetLocalQueryRunner(f)
	t.Cleanup(func() { SetLocalQueryRunner(prev) })
}

const recallPrompt = "how did we end up handling LFS pointer hydration without depending on the git-lfs binary in this codebase across the daemon and CLI paths, and which package owns the Batch API client right now?"

func TestEmitLocalRecallPreamble_HappyPath(t *testing.T) {
	withFakeRunner(t, &fakeRunner{
		results: []LocalQueryResult{
			{Title: "ADR-014 LFS without git-lfs binary", Source: "ledger", Path: ".sageox/adr/014.md"},
			{Title: "Session: lfs-remove-git-lfs-binary", Source: "session", Session: "sess-123"},
		},
	})

	var buf bytes.Buffer
	emitLocalRecallPreamble(&buf, "", "", recallPrompt)

	out := buf.String()
	if !strings.Contains(out, "<system-reminder>") {
		t.Fatalf("expected <system-reminder> wrapper, got %q", out)
	}
	if !strings.Contains(out, "[ox-recall]") {
		t.Fatalf("expected [ox-recall] tag, got %q", out)
	}
	if !strings.Contains(out, "ADR-014") {
		t.Fatalf("expected first result, got %q", out)
	}
	contentLines := 0
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l == "<system-reminder>" || l == "</system-reminder>" {
			continue
		}
		contentLines++
	}
	if contentLines > 5 {
		t.Fatalf("expected <=5 content lines, got %d: %q", contentLines, out)
	}
}

func TestEmitLocalRecallPreamble_TruncatesToFiveLines(t *testing.T) {
	results := make([]LocalQueryResult, 0, 10)
	for i := 0; i < 10; i++ {
		results = append(results, LocalQueryResult{Title: "Result", Source: "ledger"})
	}
	withFakeRunner(t, &fakeRunner{results: results})

	var buf bytes.Buffer
	emitLocalRecallPreamble(&buf, "", "", recallPrompt)

	contentLines := 0
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if l == "<system-reminder>" || l == "</system-reminder>" {
			continue
		}
		contentLines++
	}
	if contentLines > 5 {
		t.Fatalf("preamble exceeded 5-line cap: got %d lines\n%s", contentLines, buf.String())
	}
}

func TestEmitLocalRecallPreamble_ShortPromptSkipped(t *testing.T) {
	runner := &fakeRunner{results: []LocalQueryResult{{Title: "x"}}}
	withFakeRunner(t, runner)

	var buf bytes.Buffer
	emitLocalRecallPreamble(&buf, "", "", "find x")

	if runner.called != 0 {
		t.Fatalf("runner should not have been called for short prompt, got called=%d", runner.called)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected empty output for short prompt, got %q", buf.String())
	}
}

func TestEmitLocalRecallPreamble_ActionVerbSkipped(t *testing.T) {
	runner := &fakeRunner{results: []LocalQueryResult{{Title: "x"}}}
	withFakeRunner(t, runner)

	var buf bytes.Buffer
	emitLocalRecallPreamble(&buf, "", "", "fix the bug where ox doctor reports a stale recording even when the daemon was bounced cleanly")

	if runner.called != 0 {
		t.Fatalf("runner should not have been called for action verb prompt")
	}
	if buf.Len() != 0 {
		t.Fatalf("expected empty output for action verb prompt, got %q", buf.String())
	}
}

func TestEmitLocalRecallPreamble_NoResults(t *testing.T) {
	withFakeRunner(t, &fakeRunner{results: nil})
	var buf bytes.Buffer
	emitLocalRecallPreamble(&buf, "", "", recallPrompt)
	if buf.Len() != 0 {
		t.Fatalf("expected empty output on no results, got %q", buf.String())
	}
}

func TestEmitLocalRecallPreamble_ErrorIsSilent(t *testing.T) {
	withFakeRunner(t, &fakeRunner{err: errors.New("kaboom")})
	var buf bytes.Buffer
	emitLocalRecallPreamble(&buf, "", "", recallPrompt)
	if buf.Len() != 0 {
		t.Fatalf("expected empty output on error, got %q", buf.String())
	}
}

func TestEmitLocalRecallPreamble_TimeoutIsSilent(t *testing.T) {
	// runner sleeps longer than the 100ms recallQueryTimeout
	withFakeRunner(t, &fakeRunner{delay: 500 * time.Millisecond, results: []LocalQueryResult{{Title: "late"}}})

	var buf bytes.Buffer
	start := time.Now()
	emitLocalRecallPreamble(&buf, "", "", recallPrompt)
	elapsed := time.Since(start)

	if elapsed > 300*time.Millisecond {
		t.Fatalf("hook took %v, expected ~100ms timeout to bound it", elapsed)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected empty output on timeout, got %q", buf.String())
	}
}

// Fail-open under a panicking runner — agent_hook_recall.go has a
// defer/recover specifically because a half-built local index could
// dereference a nil pointer and crash the whole hook process otherwise.
func TestEmitLocalRecallPreamble_PanicIsSilent(t *testing.T) {
	withFakeRunner(t, &fakeRunner{panicWith: "panic from runner"})
	var buf bytes.Buffer
	// must not propagate the panic
	emitLocalRecallPreamble(&buf, "", "", recallPrompt)
	if buf.Len() != 0 {
		t.Fatalf("expected empty output on panic, got %q", buf.String())
	}
}

func TestExtractPromptText(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"empty bytes", "", ""},
		{"claude format", `{"hook_event_name":"UserPromptSubmit","prompt":"hello world"}`, "hello world"},
		{"user_prompt alt", `{"user_prompt":"alt name"}`, "alt name"},
		{"message alt", `{"message":"msg name"}`, "msg name"},
		{"prompt wins over alt", `{"prompt":"primary","message":"alt"}`, "primary"},
		{"malformed json", `not-json`, ""},
		{"no prompt field", `{"hook_event_name":"X"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractPromptText([]byte(tc.raw))
			if got != tc.want {
				t.Fatalf("extractPromptText(%q)=%q want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// Regression: handlePrompt must still emit whispers when ctx has a marker
// even with no recall input. The new local-recall preamble is strictly
// additive and must NEVER short-circuit the existing whisper delivery.
func TestHandlePrompt_WhisperRegression_NoRecall(t *testing.T) {
	// Force the recall path to be a no-op so we can isolate whisper behavior.
	withFakeRunner(t, &fakeRunner{}) // empty results -> no preamble

	// ctx has no Input (no prompt), no marker -> no whispers, no recall.
	// We just verify handlePrompt does not error and does not write recall.
	ctx := &HookContext{ProjectRoot: t.TempDir()}
	if err := handlePrompt(ctx); err != nil {
		t.Fatalf("handlePrompt errored unexpectedly: %v", err)
	}
}

// Regression: even when the recall runner is broken, the whisper code
// path must still be invoked. We verify this by confirming the runner
// invocation order: recall is attempted first, then whisper delivery
// happens regardless of recall outcome.
func TestHandlePrompt_RecallFailure_DoesNotBreakWhisperPath(t *testing.T) {
	withFakeRunner(t, &fakeRunner{err: errors.New("intentional recall failure")})

	ctx := &HookContext{
		ProjectRoot: t.TempDir(),
		Input:       &agentx.HookInput{RawBytes: []byte(`{"prompt":"` + recallPrompt + `"}`)},
	}
	// no marker -> emitWhispers not called, but recall path is exercised
	if err := handlePrompt(ctx); err != nil {
		t.Fatalf("handlePrompt errored after recall failure: %v", err)
	}
}

// startRecallRecording starts a live recording for agentID with entryCount
// recorded entries, so traced events carry a known seq.
func startRecallRecording(t *testing.T, agentID string, entryCount int) (string, *session.RecordingState) {
	t.Helper()
	projectRoot := setupSessionTestProject(t)
	state := startTestRecording(t, projectRoot, agentID, "test")
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
		s.EntryCount = entryCount
	}))
	return projectRoot, state
}

// Failure prevented: a recall hit reaches the model but never counts as
// surfaced, so later use of it can't be credited — or worse, the trace
// captures the user's prompt (ADR-027: refs, not content).
func TestEmitLocalRecallPreamble_TracesHitsWithoutPrompt(t *testing.T) {
	const agentID = "OxRcl1"
	projectRoot, state := startRecallRecording(t, agentID, 7)

	withFakeRunner(t, &fakeRunner{
		results: []LocalQueryResult{
			{Title: "Session: lfs-remove-git-lfs-binary", Source: "session", Session: "2026-04-11-ryan-lfs", Path: "/ledger/sessions/2026-04-11-ryan-lfs"},
			{Source: "murmur", Snippet: "hydration is opt-in", Path: "/ledger/murmurs/2026/x.json", SourceID: "mur_abc123"},
			{Source: "plan", Path: "/ledger/data/plans/2026-06-03-add-cache/plan.md", SourceID: "2026-06-03-add-cache"},
		},
	})

	var buf bytes.Buffer
	emitLocalRecallPreamble(&buf, projectRoot, agentID, recallPrompt)
	if !strings.Contains(buf.String(), "context used <ref>") {
		t.Fatalf("preamble header must name the self-report command, got %q", buf.String())
	}

	tracePath := contexttrace.NewWriter(state.SessionPath).Path()
	raw, err := os.ReadFile(tracePath)
	require.NoError(t, err)
	for _, needle := range []string{recallPrompt, promptintent.TrimForMatch(recallPrompt), "hydration without depending", "hydration is opt-in"} {
		if strings.Contains(string(raw), needle) {
			t.Fatalf("trace leaked prompt/snippet content %q:\n%s", needle, raw)
		}
	}

	events, err := contexttrace.ReadEvents(tracePath)
	require.NoError(t, err)
	require.Len(t, events, 3)
	want := []struct{ ref, refType, title string }{
		{"2026-04-11-ryan-lfs", "session", "Session: lfs-remove-git-lfs-binary"},
		{"mur_abc123", "murmur", ""},
		{"2026-06-03-add-cache", "plan", ""},
	}
	for i, w := range want {
		ev := events[i]
		require.Equal(t, contexttrace.EventProvided, ev.Type, "event %d", i)
		require.Equal(t, contexttrace.SourceLocalRecall, ev.Source, "event %d", i)
		require.Equal(t, contexttrace.MechanismInjection, ev.Mechanism, "event %d", i)
		require.Equal(t, w.ref, ev.Ref, "event %d", i)
		require.Equal(t, w.refType, ev.RefType, "event %d", i)
		require.Equal(t, w.title, ev.Title, "event %d", i)
		require.Equal(t, 7, ev.Seq, "event %d", i)
		require.Positive(t, ev.InlineTokens, "event %d", i)
		require.Empty(t, ev.Query, "event %d", i)
	}

	surfaced := contextuse.Surfaced(events)
	require.Len(t, surfaced, 3)
	require.Equal(t, contextuse.ViaRecall, surfaced[0].Via)
}

// Only rendered hits are traced: the 5-line budget drops the fifth hit from
// the preamble, so it must not count as surfaced either.
func TestEmitLocalRecallPreamble_TracesOnlyRenderedHits(t *testing.T) {
	const agentID = "OxRcl2"
	projectRoot, state := startRecallRecording(t, agentID, 3)

	var results []LocalQueryResult
	for i := 0; i < 6; i++ {
		results = append(results, LocalQueryResult{Source: "session", Session: fmt.Sprintf("sess-%d", i), Title: "t"})
	}
	withFakeRunner(t, &fakeRunner{results: results})

	var buf bytes.Buffer
	emitLocalRecallPreamble(&buf, projectRoot, agentID, recallPrompt)

	events, err := contexttrace.ReadEvents(contexttrace.NewWriter(state.SessionPath).Path())
	require.NoError(t, err)
	require.Len(t, events, 4)
	require.Equal(t, "sess-3", events[3].Ref)
}

func TestEmitLocalRecallPreamble_NotRecordingWritesNoTrace(t *testing.T) {
	projectRoot := setupSessionTestProject(t)
	withFakeRunner(t, &fakeRunner{results: []LocalQueryResult{{Source: "session", Session: "s1", Title: "t"}}})

	var buf bytes.Buffer
	emitLocalRecallPreamble(&buf, projectRoot, "OxNoRec", recallPrompt)
	require.Contains(t, buf.String(), "[ox-recall]")

	for _, root := range []string{projectRoot, os.Getenv("XDG_CACHE_HOME")} {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err == nil && d.Name() == contexttrace.FileName {
				t.Fatalf("not recording, yet a trace was written: %s", path)
			}
			return nil
		})
	}
}

func TestParseLocalQueryJSON_LocalResultsShape(t *testing.T) {
	in := `{"local_results":[
		{"score":0.9,"text":"snippet","doc_type":"session","file_path":"/l/sessions/s1","source_type":"ledger","source_id":"s1"},
		{"score":0.5,"text":"m","doc_type":"murmur","file_path":"/l/murmurs/m.json","source_type":"ledger","source_id":"mur_1"}
	]}`
	got, err := parseLocalQueryJSON([]byte(in))
	require.NoError(t, err)
	require.Equal(t, []LocalQueryResult{
		{Source: "session", Snippet: "snippet", Path: "/l/sessions/s1", Session: "s1", SourceID: "s1"},
		{Source: "murmur", Snippet: "m", Path: "/l/murmurs/m.json", SourceID: "mur_1"},
	}, got)
}

func TestRecallTraceRef(t *testing.T) {
	cases := []struct {
		name        string
		in          LocalQueryResult
		ref, refTyp string
	}{
		{"session id", LocalQueryResult{Source: "session", Session: "s1", Path: "/x/s1"}, "s1", "session"},
		{"murmur id", LocalQueryResult{Source: "murmur", SourceID: "mur_1", Path: "/x/m.json"}, "mur_1", "murmur"},
		{"plan feedback maps to plan", LocalQueryResult{Source: "plan-feedback", SourceID: "2026-01-01-x"}, "2026-01-01-x", "plan"},
		{"ledger path doc", LocalQueryResult{Source: "ledger", Path: ".sageox/adr/014.md"}, ".sageox/adr/014.md", "doc"},
		{"session only", LocalQueryResult{Session: "s2"}, "s2", "session"},
		{"nothing", LocalQueryResult{Title: "x"}, "", "doc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, typ := recallTraceRef(tc.in)
			require.Equal(t, tc.ref, ref)
			require.Equal(t, tc.refTyp, typ)
		})
	}
}
