package agentwork

import (
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/pkg/sessionsummary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildPrompt(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	ledgerPath := createTestSession(t, "2026-01-06T14-32-testuser-OxABCD", nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", "2026-01-06T14-32-testuser-OxABCD")
	rawPath := filepath.Join(sessionDir, "raw.jsonl")

	item := &WorkItem{
		ID:   "test-item",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    rawPath,
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}

	req, err := handler.BuildPrompt(item)
	if err != nil {
		t.Fatalf("BuildPrompt failed: %v", err)
	}

	if req.Prompt == "" {
		t.Error("expected non-empty prompt")
	}
	if req.WorkDir != ledgerPath {
		t.Errorf("expected WorkDir=%q, got %q", ledgerPath, req.WorkDir)
	}
	// daemon embeds session content inline — prompt must contain the actual
	// session text, not a file-read instruction. The file-read approach
	// failed for 40+ sessions because the cold-start subprocess couldn't
	// reliably use tool calls to read files.
	if strings.Contains(req.Prompt, "Read the session recording at") {
		t.Error("daemon-side prompt must embed content inline, not instruct LLM to read a file")
	}
	// prompt must contain the actual session content from raw.jsonl
	if !strings.Contains(req.Prompt, "session-finalize loop") {
		t.Error("prompt should contain session content embedded inline")
	}
	// daemon owns persistence — prompt MUST NOT delegate to push-summary.
	if strings.Contains(req.Prompt, "push-summary") {
		t.Error("daemon-side prompt must not contain push-summary instruction; daemon writes meta directly")
	}
	// must instruct the LLM to output JSON only (no fences, no preamble)
	if !strings.Contains(req.Prompt, "Output ONLY the JSON") {
		t.Error("prompt must instruct LLM to output raw JSON without fences")
	}
}

func TestProcessResult(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipGit = true // no git repo in tests

	ledgerPath := createTestSession(t, "2026-01-06T14-32-testuser-OxPROC", nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", "2026-01-06T14-32-testuser-OxPROC")
	rawPath := filepath.Join(sessionDir, "raw.jsonl")

	// simulate LLM output with valid JSON. Summary body must be ≥20 chars
	// (sessionsummary.ValidateSummaryContent minimum); content validation
	// failures now replace summaryResp with a stub rather than passing the
	// original through, which would have been the visible artifact to
	// teammates despite failing validation.
	summaryJSON := map[string]any{
		"title":         "Test Session",
		"summary":       "A test session with enough substance to pass the validator minimum length.",
		"key_actions":   []string{"said hello"},
		"outcome":       "success",
		"topics_found":  []string{"testing"},
		"quality_score": 0.8,
		"score_reason":  "Substantive test session",
	}
	jsonBytes, _ := json.MarshalIndent(summaryJSON, "", "  ")
	llmOutput := string(jsonBytes)

	item := &WorkItem{
		ID:   "test-proc",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    rawPath,
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}

	result := &RunResult{
		Output:   llmOutput,
		Duration: 5 * time.Second,
		ExitCode: 0,
	}

	err := handler.ProcessResult(item, result)
	if err != nil {
		t.Fatalf("ProcessResult failed: %v", err)
	}

	// verify summary.md was written (structured markdown, not raw LLM output)
	summaryMDPath := filepath.Join(sessionDir, "summary.md")
	require.FileExists(t, summaryMDPath, "summary.md must be created")
	summaryContent, err := os.ReadFile(summaryMDPath)
	require.NoError(t, err)
	assert.Contains(t, string(summaryContent), "# Session Summary", "summary.md should contain structured markdown header")
	assert.Contains(t, string(summaryContent), "A test session with enough substance", "summary.md should contain the summary text from LLM output")

	// verify summary.json was written
	summaryJSONPath := filepath.Join(sessionDir, "summary.json")
	require.FileExists(t, summaryJSONPath, "summary.json must be created")

	// verify session.md was created
	mdPath := filepath.Join(sessionDir, "session.md")
	require.FileExists(t, mdPath, "session.md must be created")
}

// TestFinalize_ValidationFailure_LeavesUserVisibleFieldsEmpty is the
// ox-4ggw cross-layer property test. Per ox-qqka, when the LLM produces
// invalid output the daemon must:
//
//  1. Persist a deliberate failure marker (so the session isn't lost
//     and doctor knows to retry / surface it).
//  2. Leave user-visible fields (Title, Summary) EMPTY in BOTH
//     summary.json AND meta.json — the validator's diagnostic must
//     never surface as the session's title in the UI.
//  3. Populate ValidationError + SummaryStatus structurally so
//     consumers can distinguish "pending" from "failed" without
//     sniffing for sentinel strings.
//
// This test is the integration-level boundary check that the entire
// stack honors the contract — not the per-layer unit tests, which
// individually all passed even when the leak was live.
//
// Failure prevented: any of the producer-side fixes (the three
// SummarizeResponse construction sites in session_finalize.go) regress
// and copy the diagnostic into Summary again. Pre-this-test, that
// regression silently shipped to ledgers; post-this-test, CI catches
// it.
func TestFinalize_ValidationFailure_LeavesUserVisibleFieldsEmpty(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipGit = true
	handler.skipLFS = true

	ledgerPath := createTestSession(t, "2026-01-06T14-32-testuser-OxFAIL", nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", "2026-01-06T14-32-testuser-OxFAIL")

	// LLM returns valid JSON but the title is too short — content
	// validation will fail. This is the exact path that produced the
	// 14 leaked sessions on the SageOx Internal ledger.
	tooShort := map[string]any{
		"title":         "x", // < minimum (3)
		"summary":       "Some real-looking summary text that's long enough to pass length checks.",
		"key_actions":   []string{"a"},
		"outcome":       "success",
		"topics_found":  []string{"x"},
		"quality_score": 0.8,
	}
	jsonBytes, _ := json.MarshalIndent(tooShort, "", "  ")

	item := &WorkItem{
		ID: "test-fail", Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    filepath.Join(sessionDir, "raw.jsonl"),
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}
	require.NoError(t, handler.ProcessResult(item, &RunResult{
		Output: string(jsonBytes), Duration: time.Second, ExitCode: 0,
	}))

	// --- summary.json: the structured signal must be set, the user-
	//     visible string fields must be empty.
	summaryJSONBytes, err := os.ReadFile(filepath.Join(sessionDir, "summary.json"))
	require.NoError(t, err, "summary.json must be persisted as a deliberate failure marker, not silently dropped")
	var sj map[string]any
	require.NoError(t, json.Unmarshal(summaryJSONBytes, &sj))

	if title, _ := sj["title"].(string); title != "" {
		t.Errorf("summary.json title must be empty on validation failure (was the ox-qqka leak); got %q", title)
	}
	if summary, _ := sj["summary"].(string); summary != "" {
		t.Errorf("summary.json summary must be empty on validation failure; got %q", summary)
	}
	if status, _ := sj["summary_status"].(string); status != "failed_validation" {
		t.Errorf("summary.json summary_status must be 'failed_validation'; got %q", status)
	}
	if ve, _ := sj["validation_error"].(string); ve == "" {
		t.Error("summary.json validation_error must be populated so ops can diagnose")
	}

	// --- meta.json: same invariants. THIS is what the api-go list
	//     handler reads and the web UI renders. Pre-fix the diagnostic
	//     showed up here as the row title.
	meta, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Empty(t, meta.Title, "meta.title must be empty on validation failure (UI row title leak)")
	assert.Empty(t, meta.Summary, "meta.summary must be empty on validation failure (the ox-qqka leak)")
	assert.Equal(t, "failed_validation", meta.SummaryStatus, "meta must carry structured failure status")
	assert.NotEmpty(t, meta.ValidationError, "meta must carry the ops-facing diagnostic separately from user-visible fields")
}

// TestProcessResult_FailureStubsCapAtMaxAttempts verifies the
// MaxSummaryAttempts retry cap. Pre-fix, a session that consistently
// failed daemon-side LLM summarization (corrupt raw.jsonl, oversized
// prompt, model having a bad day) would re-enqueue on every anti-
// entropy cycle and burn unbounded tokens producing the same failure
// stub. Post-fix, after MaxSummaryAttempts the daemon flips
// SummaryStatus to "unrecoverable" and workNoLongerNeeded treats that
// as terminal, breaking the retry loop.
//
// Failure prevented: unbounded LLM spend on structurally-broken
// sessions; "Summary unavailable" rows flapping between failed_validation
// stubs forever instead of settling into a terminal unrecoverable state
// that ops/doctor can surface clearly.
func TestProcessResult_FailureStubsCapAtMaxAttempts(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipGit = true
	handler.skipLFS = true

	ledgerPath := createTestSession(t, "2026-05-01T20-04-testuser-OxRTRY", nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", "2026-05-01T20-04-testuser-OxRTRY")

	// LLM output that consistently fails content validation. Title=" " is
	// non-empty raw (so ParseSummaryJSON's TrimSpace gate would ALSO reject
	// it now) — we use a 1-char title instead so parse passes and the
	// failure flows through ValidateSummaryContent's "title too short"
	// branch, which is the actual production failure shape we observed.
	failingOutput := `{"title":"x","summary":"Some real-looking summary text that is long enough to pass length checks.","key_actions":["a"],"outcome":"success","topics_found":["x"],"quality_score":0.8}`
	item := &WorkItem{
		ID: "rtry", Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    filepath.Join(sessionDir, "raw.jsonl"),
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}

	// Run MaxSummaryAttempts-1 times — should keep status as failed_validation
	// with attempts incrementing each round.
	for i := 1; i < lfs.MaxSummaryAttempts; i++ {
		require.NoError(t, handler.ProcessResult(item, &RunResult{
			Output: failingOutput, Duration: time.Second, ExitCode: 0,
		}))
		meta, err := lfs.ReadSessionMeta(sessionDir)
		require.NoError(t, err)
		assert.Equal(t, sessionsummary.SummaryStatusFailedValidation, meta.SummaryStatus,
			"attempt %d/%d: status should still be failed_validation before cap", i, lfs.MaxSummaryAttempts)
		assert.Equal(t, i, meta.SummaryAttempts,
			"attempt %d: SummaryAttempts should mirror the attempt count", i)
	}

	// Final attempt — should flip to unrecoverable.
	require.NoError(t, handler.ProcessResult(item, &RunResult{
		Output: failingOutput, Duration: time.Second, ExitCode: 0,
	}))
	meta, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, sessionsummary.SummaryStatusUnrecoverable, meta.SummaryStatus,
		"after MaxSummaryAttempts, status must flip to unrecoverable to stop the retry loop")
	assert.Equal(t, lfs.MaxSummaryAttempts, meta.SummaryAttempts,
		"SummaryAttempts must record the final attempt count")
}

// TestProcessResult_SuccessResetsAttemptCounter verifies that a
// successful summary after one or more failures clears the
// SummaryAttempts counter. Without this, a session that recovered
// after two failed attempts would inherit attempts=2 forever, and a
// future regression that produced a single failure stub would
// immediately flip it to unrecoverable.
func TestProcessResult_SuccessResetsAttemptCounter(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipGit = true
	handler.skipLFS = true

	ledgerPath := createTestSession(t, "2026-05-01T20-04-testuser-OxRSET", nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", "2026-05-01T20-04-testuser-OxRSET")

	failingOutput := `{"title":"x","summary":"Some real-looking summary text that is long enough to pass length checks.","key_actions":["a"],"outcome":"success","topics_found":["x"],"quality_score":0.8}`
	successOutput := `{"title":"Real Title","summary":"This is a successful summary of the session that passes all the validators in place.","key_actions":["did the work","wrote the tests","shipped it"],"outcome":"success","topics_found":["x"],"quality_score":0.8}`
	item := &WorkItem{
		ID: "rset", Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    filepath.Join(sessionDir, "raw.jsonl"),
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}

	// One failed attempt — counter goes to 1.
	require.NoError(t, handler.ProcessResult(item, &RunResult{
		Output: failingOutput, Duration: time.Second, ExitCode: 0,
	}))
	meta, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	require.Equal(t, 1, meta.SummaryAttempts, "precondition: failure should bump counter")

	// Successful attempt — counter resets to 0, status flips to ok.
	require.NoError(t, handler.ProcessResult(item, &RunResult{
		Output: successOutput, Duration: time.Second, ExitCode: 0,
	}))
	meta, err = lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, sessionsummary.SummaryStatusOK, meta.SummaryStatus, "successful run must stamp status=ok")
	assert.Equal(t, 0, meta.SummaryAttempts, "successful run must reset SummaryAttempts so a future single failure doesn't immediately flip to unrecoverable")
	assert.Equal(t, "Real Title", meta.Title, "successful run must populate the title")
}

func TestProcessResult_UnparsableJSON(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipGit = true

	ledgerPath := createTestSession(t, "2026-01-06T14-32-testuser-OxBADJ", nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", "2026-01-06T14-32-testuser-OxBADJ")
	rawPath := filepath.Join(sessionDir, "raw.jsonl")

	item := &WorkItem{
		ID:   "test-bad-json",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    rawPath,
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}

	// LLM returns free-text, not valid JSON
	result := &RunResult{
		Output:   "This session was about testing things. It went well.",
		Duration: 3 * time.Second,
		ExitCode: 0,
	}

	err := handler.ProcessResult(item, result)
	if err != nil {
		t.Fatalf("ProcessResult should not fail with unparsable JSON: %v", err)
	}

	// all 3 artifacts should be written (unified code path always writes all)
	for _, artifact := range []string{"summary.md", "summary.json", "session.md"} {
		if _, statErr := os.Stat(filepath.Join(sessionDir, artifact)); statErr != nil {
			t.Errorf("%s should be created even when JSON parsing fails: %v", artifact, statErr)
		}
	}

	// summary.json should record the failure structurally, not as user-
	// visible prose, and must NOT echo the raw LLM free-text. After
	// ox-qqka the failure stub leaves "title" and "summary" empty and
	// records the diagnostic in "validation_error" + "summary_status";
	// older versions wrote "Summary generation failed: ..." into the
	// "summary" field, which then leaked into meta.json and the UI.
	//
	// We assert intent, not exact wording: (a) some structured failure
	// signal is present, (b) the LLM free-text is not in the file at
	// all, (c) the user-visible "summary" key is empty so consumers
	// that read it directly get a clean empty string rather than a
	// diagnostic disguised as a session description.
	data, _ := os.ReadFile(filepath.Join(sessionDir, "summary.json"))

	if strings.Contains(string(data), "This session was about testing") {
		t.Error("summary.json must not contain the raw LLM free-text output")
	}

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(data, &parsed))

	// User-visible "summary" must NOT carry a diagnostic — that's the
	// exact leak ox-qqka fixed.
	if s, _ := parsed["summary"].(string); s != "" {
		t.Errorf("summary.json[\"summary\"] must be empty on failure (was the ox-qqka leak); got %q", s)
	}
	if s, _ := parsed["title"].(string); s != "" {
		t.Errorf("summary.json[\"title\"] must be empty on failure; got %q", s)
	}

	// At least one structured failure signal must be present so consumers
	// can distinguish "still pending" from "tried and failed".
	hasFailureSignal := false
	if status, _ := parsed["summary_status"].(string); status == "failed_validation" {
		hasFailureSignal = true
	}
	if ve, _ := parsed["validation_error"].(string); ve != "" {
		hasFailureSignal = true
	}
	if sr, _ := parsed["score_reason"].(string); strings.Contains(sr, "failed") {
		hasFailureSignal = true // legacy diagnostic field kept for ops
	}
	if !hasFailureSignal {
		t.Errorf("summary.json must carry a structured failure signal (summary_status / validation_error / score_reason); got: %s", string(data))
	}
}

func TestProcessResult_WithRealGitRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}

	sessionName := "2026-01-06T14-32-testuser-OxGIT"
	ledgerPath, sessionDir := createTestSessionInGitRepo(t, sessionName)
	rawPath := filepath.Join(sessionDir, "raw.jsonl")

	handler := NewSessionFinalizeHandler(slog.Default())
	// skipGit=false — exercises the real git commit path

	llmOutput := `{"title":"Git Test","summary":"Testing git commit path.","key_actions":["tested git"],"outcome":"success","topics_found":["git"],"quality_score":0.9}`

	item := &WorkItem{
		ID:   "test-git",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    rawPath,
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}

	result := &RunResult{
		Output:   llmOutput,
		Duration: 2 * time.Second,
		ExitCode: 0,
	}

	err := handler.ProcessResult(item, result)
	require.NoError(t, err, "ProcessResult with real git should succeed")

	// verify artifacts exist
	require.FileExists(t, filepath.Join(sessionDir, "summary.md"))
	require.FileExists(t, filepath.Join(sessionDir, "summary.json"))
	require.FileExists(t, filepath.Join(sessionDir, "session.md"))

	// verify git commit was made — should have >1 commit now
	out, gitErr := exec.Command("git", "-C", ledgerPath, "log", "--oneline").CombinedOutput()
	require.NoError(t, gitErr)
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	assert.GreaterOrEqual(t, len(lines), 2, "should have at least 2 commits (init + finalize)")
}

func TestParseSummaryJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantOK  bool
		wantErr bool
	}{
		{
			name:   "raw JSON",
			input:  `{"title":"Test","summary":"A test","key_actions":["did stuff"],"outcome":"success","topics_found":["go"]}`,
			wantOK: true,
		},
		{
			name: "fenced JSON",
			input: "Here is the summary:\n```json\n" +
				`{"title":"Test","summary":"A test","key_actions":["did the thing"],"outcome":"success","topics_found":[]}` +
				"\n```\n",
			wantOK: true,
		},
		{
			name:    "plain text",
			input:   "This is just a text summary with no JSON.",
			wantErr: true,
		},
		{
			name:    "empty JSON object",
			input:   `{}`,
			wantErr: true, // title is empty
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := sessionsummary.ParseSummaryJSON(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if tt.wantOK {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
					return
				}
				if resp.Title != "Test" {
					t.Errorf("expected title 'Test', got %q", resp.Title)
				}
			}
		})
	}
}

func TestProcessResult_QualityScoreDiscard(t *testing.T) {
	// Discard-by-deletion only applies to sessions in a recording cache —
	// git-tracked ledger sessions are finalized in place instead (see
	// TestProcessResult_SkipOnLedger_FinalizesInPlace). Stage the session
	// in the ledger's gitignored cache dir.
	ledgerPath := createTestSession(t, "test-discard", nil)
	trackedDir := filepath.Join(ledgerPath, "sessions", "test-discard")
	sessionDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", "test-discard")
	if err := os.MkdirAll(filepath.Dir(sessionDir), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(trackedDir, sessionDir); err != nil {
		t.Fatal(err)
	}

	handler := NewSessionFinalizeHandlerForTest(slog.Default())
	handler.SetQualityThresholds(0.3, 0.1)

	item := &WorkItem{
		ID:       "test-1",
		Type:     sessionFinalizeType,
		DedupKey: "session-finalize:test-discard",
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    filepath.Join(sessionDir, "raw.jsonl"),
			Missing:    []string{"summary.md"},
			LedgerPath: ledgerPath,
		},
	}

	// score below discard threshold — session dir should be removed.
	// key_actions must be non-empty so ParseSummaryJSON accepts the
	// payload as a real summary; otherwise the parse-failure path
	// kicks in and the discard gate is never reached.
	result := &RunResult{
		Output: `{"title":"Routine rebasing","summary":"Just a simple rebase with no meaningful changes to report","key_actions":["rebased branch"],"outcome":"success","topics_found":[],"quality_score":0.05,"score_reason":"Trivial maintenance"}`,
	}

	err := handler.ProcessResult(item, result)
	if err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	// session dir should be removed
	if _, statErr := os.Stat(sessionDir); !os.IsNotExist(statErr) {
		t.Error("expected session directory to be removed for quality below discard threshold")
	}
}

// TestProcessResult_SkipOnLedger_FinalizesInPlace is the regression test for
// the 2026-07 ledger anti-entropy incident. A session that already lives in
// the git-tracked ledger sessions/ tree gets a skip verdict from the LLM
// (fenced JSON, no key_actions — the exact shape the parser used to reject).
// The daemon must NOT delete the directory (an uncommitted deletion is
// resurrected by the next pull, so detection loops forever; a committed one
// erases shared history). Instead it finalizes in place: skip summary
// written, meta.json marked ok, .needs-summary marker cleared.
func TestProcessResult_SkipOnLedger_FinalizesInPlace(t *testing.T) {
	ledgerPath := createTestSession(t, "2026-05-12T05-59-ajit-OxSKIP", nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", "2026-05-12T05-59-ajit-OxSKIP")

	// git-tracked marker, as committed by doctor auto-commit in the incident
	markerPath := filepath.Join(sessionDir, ".needs-summary")
	require.NoError(t, os.WriteFile(markerPath, []byte(`{"cache_dir":"`+sessionDir+`"}`), 0644))

	handler := NewSessionFinalizeHandlerForTest(slog.Default())
	handler.SetQualityThresholds(0.3, 0.1)

	item := &WorkItem{
		ID:       "test-skip-ledger",
		Type:     sessionFinalizeType,
		DedupKey: "session-finalize:2026-05-12T05-59-ajit-OxSKIP",
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    filepath.Join(sessionDir, "raw.jsonl"),
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}

	// verbatim incident shape: fenced skip JSON, no key_actions
	result := &RunResult{
		Output: "```json\n" +
			`{"quality_category":"skip","score_reason":"Routine maintenance task with no broader insight or decision-making.","title":"Remove worktree and local branch"}` +
			"\n```",
	}

	require.NoError(t, handler.ProcessResult(item, result))

	// the session dir must survive — it is shared team history
	require.DirExists(t, sessionDir, "git-tracked ledger session must not be deleted on skip")

	// skip summary written with the LLM's title
	summaryData, err := os.ReadFile(filepath.Join(sessionDir, "summary.json"))
	require.NoError(t, err, "summary.json must be written for in-place skip finalize")
	var summary map[string]any
	require.NoError(t, json.Unmarshal(summaryData, &summary))
	assert.Equal(t, "Remove worktree and local branch", summary["title"])
	assert.Equal(t, "skip", summary["quality_category"])

	// meta.json must be terminal-clean so detection and the daily
	// inline-summary-retry autofix stop re-arming this session
	metaData, err := os.ReadFile(filepath.Join(sessionDir, "meta.json"))
	require.NoError(t, err, "meta.json must be written for in-place skip finalize")
	var meta map[string]any
	require.NoError(t, json.Unmarshal(metaData, &meta))
	assert.Equal(t, "ok", meta["summary_status"], "meta must be marked ok, not failed_validation")
	if ve, ok := meta["validation_error"].(string); ok {
		assert.NotContains(t, ve, "title too short")
	}

	// marker cleared → the uncapped HasNeedsSummaryMarker branch goes quiet
	_, statErr := os.Stat(markerPath)
	assert.True(t, os.IsNotExist(statErr), ".needs-summary marker must be cleared")
}

// TestProcessResult_SkipTitleDefault verifies a title-less skip verdict on a
// ledger-resident session gets the prefilter's "Brief session" default —
// an empty title in summary.json would re-trigger shouldRetryEmptySummary
// detection and restart the loop the in-place finalize exists to end.
func TestProcessResult_SkipTitleDefault(t *testing.T) {
	ledgerPath := createTestSession(t, "2026-05-12T06-00-ajit-OxSKP2", nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", "2026-05-12T06-00-ajit-OxSKP2")

	handler := NewSessionFinalizeHandlerForTest(slog.Default())
	handler.SetQualityThresholds(0.3, 0.1)

	item := &WorkItem{
		ID:       "test-skip-title",
		Type:     sessionFinalizeType,
		DedupKey: "session-finalize:2026-05-12T06-00-ajit-OxSKP2",
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    filepath.Join(sessionDir, "raw.jsonl"),
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}
	result := &RunResult{
		Output: `{"quality_category":"skip","score_reason":"No substantive work."}`,
	}

	require.NoError(t, handler.ProcessResult(item, result))
	require.DirExists(t, sessionDir)

	summaryData, err := os.ReadFile(filepath.Join(sessionDir, "summary.json"))
	require.NoError(t, err)
	var summary map[string]any
	require.NoError(t, json.Unmarshal(summaryData, &summary))
	assert.Equal(t, "Brief session", summary["title"], "title-less skip must default to the prefilter title")
}

// TestProcessResult_ValidationFailure_UploadsFallbackStub pins down the
// validation-failure branch: when a parsed LLM response fails content
// validation, the session must still be uploaded with the fallback stub —
// never discarded as though it had a real quality_score of 0. Guards against
// a future refactor sending validation-failures through the discard gate.
func TestProcessResult_ValidationFailure_UploadsFallbackStub(t *testing.T) {
	ledgerPath := createTestSession(t, "test-valfail", nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", "test-valfail")

	handler := NewSessionFinalizeHandlerForTest(slog.Default())
	handler.SetQualityThresholds(0.3, 0.1)

	item := &WorkItem{
		ID:       "test-valfail",
		Type:     sessionFinalizeType,
		DedupKey: "session-finalize:test-valfail",
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    filepath.Join(sessionDir, "raw.jsonl"),
			Missing:    []string{"summary.md", "summary.json", "session.md"},
			LedgerPath: ledgerPath,
		},
	}

	// valid JSON but summary is too short — trips ValidateSummaryContent.
	result := &RunResult{
		Output: `{"title":"x","summary":"too short","key_actions":[],"outcome":"success","topics_found":[],"quality_score":0.9}`,
	}

	if err := handler.ProcessResult(item, result); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	// validation failed → fallback stub → upload path → artifacts written, dir retained
	if _, statErr := os.Stat(sessionDir); os.IsNotExist(statErr) {
		t.Fatal("validation failure must not discard the session")
	}
	if _, statErr := os.Stat(filepath.Join(sessionDir, "summary.json")); os.IsNotExist(statErr) {
		t.Error("summary.json should be written for validation-failure fallback")
	}
}

// TestProcessResult_EmptySessionLLMScoreZero_Discarded is the #525 regression:
// when the LLM correctly scores an empty session as 0 (not missing, explicit 0),
// the session must flow through the discard gate and be removed — not uploaded.
// Before the fix, EvaluateQuality's `score <= 0` short-circuit classified these
// as QualityUpload, causing empty "No Activity Recorded" sessions to reach the
// team ledger.
func TestProcessResult_EmptySessionLLMScoreZero_Discarded(t *testing.T) {
	// The leak this guards (#525) happens on the cache→ledger upload path —
	// discard-by-deletion applies to recording-cache sessions. Git-tracked
	// ledger sessions are finalized in place instead (see
	// TestProcessResult_SkipOnLedger_FinalizesInPlace).
	ledgerPath := createTestSession(t, "test-empty-zero", nil)
	trackedDir := filepath.Join(ledgerPath, "sessions", "test-empty-zero")
	sessionDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", "test-empty-zero")
	if err := os.MkdirAll(filepath.Dir(sessionDir), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(trackedDir, sessionDir); err != nil {
		t.Fatal(err)
	}

	handler := NewSessionFinalizeHandlerForTest(slog.Default())
	handler.SetQualityThresholds(0.3, 0.1)

	item := &WorkItem{
		ID:       "test-525",
		Type:     sessionFinalizeType,
		DedupKey: "session-finalize:test-empty-zero",
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    filepath.Join(sessionDir, "raw.jsonl"),
			Missing:    []string{"summary.md"},
			LedgerPath: ledgerPath,
		},
	}

	// shape of LLM output for an empty session, verbatim from the on-disk
	// summary.json of one of the 42 leaked sessions observed in the wild —
	// modulo a non-empty key_actions entry so ParseSummaryJSON's tightened
	// acceptance gate treats this as a real summary rather than a parse
	// failure. The point of the regression is the discard path, not the
	// parse path; the LLM in the field would still have surfaced something
	// in key_actions for an honest empty session ("noted no activity", etc.).
	result := &RunResult{
		Output: `{"title":"Empty Session - No Activity Recorded","summary":"This session contained no dialog or activity. Only a session header was recorded, indicating the session was opened but no work was performed.","key_actions":["observed no recorded activity"],"outcome":"failed","topics_found":[],"quality_score":0,"score_reason":"Session contained only a header with no dialog or work performed."}`,
	}

	if err := handler.ProcessResult(item, result); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	if _, statErr := os.Stat(sessionDir); !os.IsNotExist(statErr) {
		t.Error("empty session scored 0 by LLM must be discarded, not uploaded")
	}
}

func TestProcessResult_QualityScoreBelowUpload(t *testing.T) {
	ledgerPath := createTestSession(t, "test-local", nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", "test-local")

	handler := NewSessionFinalizeHandlerForTest(slog.Default())
	handler.SetQualityThresholds(0.3, 0.1)

	item := &WorkItem{
		ID:       "test-2",
		Type:     sessionFinalizeType,
		DedupKey: "session-finalize:test-local",
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    filepath.Join(sessionDir, "raw.jsonl"),
			Missing:    []string{"summary.md", "summary.json", "session.md"},
			LedgerPath: ledgerPath,
		},
	}

	// score above discard but below upload — artifacts generated, no push
	result := &RunResult{
		Output: `{"title":"Quick fix","summary":"Minor config change","key_actions":["updated config"],"outcome":"success","topics_found":["config"],"quality_score":0.2,"score_reason":"Routine config update"}`,
	}

	err := handler.ProcessResult(item, result)
	if err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	// session dir should still exist (not discarded)
	if _, statErr := os.Stat(sessionDir); os.IsNotExist(statErr) {
		t.Error("expected session directory to still exist for quality between discard and upload thresholds")
	}

	// summary.json should be written (artifacts generated locally)
	if _, statErr := os.Stat(filepath.Join(sessionDir, "summary.json")); os.IsNotExist(statErr) {
		t.Error("expected summary.json to be generated even below upload threshold")
	}
}

func TestProcessResult_QualityScoreAboveUpload(t *testing.T) {
	ledgerPath := createTestSession(t, "test-upload", nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", "test-upload")

	handler := NewSessionFinalizeHandlerForTest(slog.Default())
	handler.SetQualityThresholds(0.3, 0.1)

	item := &WorkItem{
		ID:       "test-3",
		Type:     sessionFinalizeType,
		DedupKey: "session-finalize:test-upload",
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    filepath.Join(sessionDir, "raw.jsonl"),
			Missing:    []string{"summary.md", "summary.json", "session.md"},
			LedgerPath: ledgerPath,
		},
	}

	// score above upload threshold — should proceed to git push (skipped in test mode)
	result := &RunResult{
		Output: `{"title":"Feature implementation","summary":"Implemented quality scoring","key_actions":["added scoring","updated config"],"outcome":"success","topics_found":["architecture"],"quality_score":0.8,"score_reason":"New feature with design decisions"}`,
	}

	err := handler.ProcessResult(item, result)
	if err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	// session dir should still exist
	if _, statErr := os.Stat(sessionDir); os.IsNotExist(statErr) {
		t.Error("expected session directory to still exist for high quality session")
	}

	// artifacts should be generated
	for _, artifact := range []string{"summary.json", "summary.md"} {
		if _, statErr := os.Stat(filepath.Join(sessionDir, artifact)); os.IsNotExist(statErr) {
			t.Errorf("expected %s to be generated for high quality session", artifact)
		}
	}
}

func TestProcessResult_WritesMetaJSON(t *testing.T) {
	handler := NewSessionFinalizeHandlerForTest(slog.Default())

	sessionName := "2026-01-10T09-30-testuser-OxMETA"
	ledgerPath := createTestSession(t, sessionName, nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	rawPath := filepath.Join(sessionDir, "raw.jsonl")

	item := &WorkItem{
		ID:   "test-meta",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    rawPath,
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}

	summaryJSON := map[string]any{
		"title":         "Meta Test Session",
		"summary":       "Testing meta.json generation",
		"key_actions":   []string{"tested meta"},
		"outcome":       "success",
		"topics_found":  []string{"testing"},
		"quality_score": 0.8,
		"score_reason":  "Test session",
	}
	jsonBytes, _ := json.MarshalIndent(summaryJSON, "", "  ")

	result := &RunResult{
		Output:   string(jsonBytes),
		Duration: 5 * time.Second,
	}

	if err := handler.ProcessResult(item, result); err != nil {
		t.Fatalf("ProcessResult failed: %v", err)
	}

	// verify meta.json was written
	meta, err := lfs.ReadSessionMeta(sessionDir)
	if err != nil {
		t.Fatalf("meta.json not found after ProcessResult: %v", err)
	}

	if meta.SessionName != sessionName {
		t.Errorf("session_name mismatch: got %q, want %q", meta.SessionName, sessionName)
	}
	if meta.StopReason != session.StopReasonRecovered {
		t.Errorf("stop_reason mismatch: got %q, want %q", meta.StopReason, session.StopReasonRecovered)
	}
	if meta.Title != "Meta Test Session" {
		t.Errorf("title mismatch: got %q, want %q", meta.Title, "Meta Test Session")
	}
	if meta.Summary != "Testing meta.json generation" {
		t.Errorf("summary mismatch: got %q", meta.Summary)
	}
	if meta.EntryCount != 3 { // 3 entries in createTestSession raw.jsonl (user + 2 assistant)
		t.Errorf("entry_count mismatch: got %d, want 3", meta.EntryCount)
	}
}

func TestProcessResult_MetaJSON_NoLFS_EmptyFiles(t *testing.T) {
	handler := NewSessionFinalizeHandlerForTest(slog.Default())

	sessionName := "2026-01-10T10-00-testuser-OxNOLF"
	ledgerPath := createTestSession(t, sessionName, nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	rawPath := filepath.Join(sessionDir, "raw.jsonl")

	item := &WorkItem{
		ID:   "test-nolfs",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    rawPath,
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}

	result := &RunResult{
		Output: `{"title":"No LFS","summary":"Testing without LFS","key_actions":["test"],"outcome":"success","topics_found":[],"quality_score":0.8,"score_reason":"Test"}`,
	}

	if err := handler.ProcessResult(item, result); err != nil {
		t.Fatalf("ProcessResult failed: %v", err)
	}

	meta, err := lfs.ReadSessionMeta(sessionDir)
	if err != nil {
		t.Fatalf("meta.json not found: %v", err)
	}

	// with skipLFS=true, files map should be empty
	if len(meta.Files) != 0 {
		t.Errorf("expected empty files map when LFS is skipped, got %d entries", len(meta.Files))
	}
}

func TestProcessResult_ExtractsMetadataFromHeader(t *testing.T) {
	handler := NewSessionFinalizeHandlerForTest(slog.Default())

	sessionName := "2026-01-10T10-30-alice-OxHDR1"
	ledgerPath := t.TempDir()
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	// raw.jsonl with metadata header containing agent info
	rawContent := `{"metadata":{"agent_id":"OxHDR1","agent_type":"claude-code","created_at":"2026-01-10T10:30:00Z","username":"alice@example.com"},"type":"header"}
{"type":"user","content":"test","seq":1}
{"type":"assistant","content":"done","seq":2}
`
	rawPath := filepath.Join(sessionDir, "raw.jsonl")
	if err := os.WriteFile(rawPath, []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}

	item := &WorkItem{
		ID:   "test-header",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    rawPath,
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}

	result := &RunResult{
		Output: `{"title":"Header Test","summary":"Testing header extraction","key_actions":["test"],"outcome":"success","topics_found":[],"quality_score":0.8,"score_reason":"Test"}`,
	}

	if err := handler.ProcessResult(item, result); err != nil {
		t.Fatalf("ProcessResult failed: %v", err)
	}

	meta, err := lfs.ReadSessionMeta(sessionDir)
	if err != nil {
		t.Fatalf("meta.json not found: %v", err)
	}

	if meta.AgentID != "OxHDR1" {
		t.Errorf("agent_id mismatch: got %q, want %q", meta.AgentID, "OxHDR1")
	}
	if meta.AgentType != "claude-code" {
		t.Errorf("agent_type mismatch: got %q, want %q", meta.AgentType, "claude-code")
	}
	if meta.Username != "alice@example.com" {
		t.Errorf("username mismatch: got %q, want %q", meta.Username, "alice@example.com")
	}
}

// TestProcessResult_ContentFilesNotPointers_AfterFinalization is a regression test for
// bug #291: WriteSessionMeta (with fileRefs) was called before git push, replacing
// content files with LFS pointer stubs. If push then failed, the content was lost.
//
// This test verifies the end-to-end contract: after ProcessResult with LFS disabled
// (skipLFS=true), content files are never replaced with LFS pointer stubs.
// The test would fail if the pipeline called WriteSessionMeta (with fileRefs)
// instead of WriteSessionMetaOnly followed by a post-push WritePointerFiles.
func TestProcessResult_ContentFilesNotPointers_AfterFinalization(t *testing.T) {
	handler := NewSessionFinalizeHandlerForTest(slog.Default()) // skipGit=true, skipLFS=true

	sessionName := "2026-01-15T12-00-testuser-OxPTR1"
	ledgerPath := createTestSession(t, sessionName, nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	rawPath := filepath.Join(sessionDir, "raw.jsonl")

	// record the original content so we can compare after ProcessResult
	originalContent, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("read raw.jsonl: %v", err)
	}

	item := &WorkItem{
		ID:   "test-ptr-regression",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    rawPath,
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}

	result := &RunResult{
		Output: `{"title":"Pointer Regression Test","summary":"Verifying no pointer replacement","key_actions":["verified"],"outcome":"success","topics_found":[],"quality_score":0.8,"score_reason":"Test"}`,
	}

	if err := handler.ProcessResult(item, result); err != nil {
		t.Fatalf("ProcessResult failed: %v", err)
	}

	// CRITICAL: raw.jsonl must not be replaced with an LFS pointer stub.
	// Before the fix, WriteSessionMeta(meta_with_files) was called before push,
	// which would have replaced raw.jsonl with a tiny pointer file.
	if lfs.IsPointerFile(rawPath) {
		t.Error("raw.jsonl was replaced with an LFS pointer stub (bug #291 regression)")
	}

	// content must be unchanged — not a truncated pointer
	afterContent, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("read raw.jsonl after ProcessResult: %v", err)
	}
	if string(afterContent) != string(originalContent) {
		t.Errorf("raw.jsonl content changed unexpectedly after ProcessResult\nbefore: %q\nafter:  %q",
			originalContent, afterContent)
	}

	// meta.json must be written with empty Files (no LFS refs with skipLFS=true)
	meta, err := lfs.ReadSessionMeta(sessionDir)
	if err != nil {
		t.Fatalf("meta.json not found after ProcessResult: %v", err)
	}
	if len(meta.Files) != 0 {
		t.Errorf("meta.Files must be empty when LFS is skipped, got %d entries", len(meta.Files))
	}
}

// createTestSession creates a session directory with raw.jsonl and optional artifacts.
// Returns the ledger path.
func createTestSession(t *testing.T, sessionName string, includeArtifacts []string) string {
	t.Helper()
	ledgerPath := t.TempDir()
	sessionsDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(sessionsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// raw.jsonl with substantive content. Two important properties:
	//
	//   1. User content is long enough (> 80 chars) that
	//      sessionsummary.MaybeBuildSkipSummary does NOT trigger — these
	//      tests exercise the LLM-output path, not the prefilter path.
	//      Tests targeting the prefilter use createThinTestSession below.
	//
	//   2. At least 3 entries (header + user + assistant) so the
	//      entry-count floor in the prefilter is also cleared.
	rawContent := `{"_meta":{"schema_version":"1","agent_type":"claude-code"}}
{"type":"user","content":"Walk me through how the daemon's session-finalize loop interacts with the manager's queue and ProcessResult callback","seq":1}
{"type":"assistant","content":"Sure — the daemon polls for orphaned sessions, BuildPrompt constructs a RunRequest, the manager runs it through claude, and ProcessResult parses the output and writes artifacts.","seq":2}
{"type":"assistant","content":"Want me to walk through the queue invariants too, or just the happy path?","seq":3}
`
	if err := os.WriteFile(filepath.Join(sessionsDir, "raw.jsonl"), []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}

	for _, name := range includeArtifacts {
		if err := os.WriteFile(filepath.Join(sessionsDir, name), []byte("placeholder"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	return ledgerPath
}

// createTestSessionInGitRepo creates a session inside a real git repo.
// Returns (ledgerPath, sessionDir).
func createTestSessionInGitRepo(t *testing.T, sessionName string) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	ledgerPath := t.TempDir()

	// init git repo with isolated config to avoid host git settings (gpgsign, hooksPath, etc.)
	require.NoError(t, exec.Command("git", "init", "--initial-branch=main", ledgerPath).Run())
	require.NoError(t, exec.Command("git", "-C", ledgerPath, "config", "user.email", "test@test.com").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerPath, "config", "user.name", "Test").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerPath, "config", "commit.gpgsign", "false").Run())

	// create sessions dir and raw.jsonl
	sessionsDir := filepath.Join(ledgerPath, "sessions", sessionName)
	require.NoError(t, os.MkdirAll(sessionsDir, 0755))

	// Same substantive content as createTestSession — keeps the LLM
	// path active rather than triggering the pre-LLM prefilter.
	rawContent := `{"_meta":{"schema_version":"1","agent_type":"claude-code"}}
{"type":"user","content":"Walk me through how the daemon's session-finalize loop interacts with the manager's queue and ProcessResult callback","seq":1}
{"type":"assistant","content":"Sure — the daemon polls for orphaned sessions, BuildPrompt constructs a RunRequest, the manager runs it through claude, and ProcessResult parses the output and writes artifacts.","seq":2}
{"type":"assistant","content":"Want me to walk through the queue invariants too, or just the happy path?","seq":3}
`
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "raw.jsonl"), []byte(rawContent), 0644))

	// initial commit
	require.NoError(t, exec.Command("git", "-C", ledgerPath, "add", "-A").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerPath, "commit", "-m", "init").Run())

	return ledgerPath, sessionsDir
}
