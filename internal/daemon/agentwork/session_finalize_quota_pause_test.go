package agentwork

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyAgentFailure(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{"weekly limit", "You've hit your weekly limit · resets Oct 12 at 7am (America/Los_Angeles)", failureReasonQuotaExhausted},
		{"plain limit", "You've hit your limit", failureReasonQuotaExhausted},
		{"usage limit", "Claude AI usage limit reached|1760000000", failureReasonQuotaExhausted},
		{"rate limit mixed case", "API Error: Rate Limit exceeded", failureReasonQuotaExhausted},
		{"unrelated failure", "permission denied reading transcript", failureReasonUnknown},
		{"empty", "", failureReasonUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, classifyAgentFailure(tt.output))
		})
	}
}

func TestParseQuotaReset(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	require.NoError(t, err)
	now := time.Date(2026, 10, 8, 14, 30, 0, 0, la)

	tests := []struct {
		name   string
		output string
		want   time.Time
		ok     bool
	}{
		{"date and hour", "You've hit your weekly limit · resets Oct 12 at 7am (America/Los_Angeles)",
			time.Date(2026, 10, 12, 7, 0, 0, 0, la), true},
		{"hour only later today", "You've hit your weekly limit · resets 5pm (America/Los_Angeles)",
			time.Date(2026, 10, 8, 17, 0, 0, 0, la), true},
		{"hour only already past rolls to tomorrow", "You've hit your weekly limit · resets 5am (America/Los_Angeles)",
			time.Date(2026, 10, 9, 5, 0, 0, 0, la), true},
		{"minutes", "resets 7:30pm (America/Los_Angeles)",
			time.Date(2026, 10, 8, 19, 30, 0, 0, la), true},
		{"date already past rolls to next year", "resets Jan 2 at 12am (America/Los_Angeles)",
			time.Date(2027, 1, 2, 0, 0, 0, 0, la), true},
		{"dated reset that just passed is unknown", "resets Oct 8 at 12pm (America/Los_Angeles)", time.Time{}, false},
		{"epoch suffix", "Claude AI usage limit reached|1791500000",
			time.Unix(1791500000, 0), true},
		{"twelve pm is noon", "resets Oct 9 at 12pm (America/Los_Angeles)",
			time.Date(2026, 10, 9, 12, 0, 0, 0, la), true},
		{"unknown zone", "resets 5am (Mars/Olympus)", time.Time{}, false},
		{"no reset text", "You've hit your limit", time.Time{}, false},
		{"hour out of range", "resets 13pm (America/Los_Angeles)", time.Time{}, false},
		{"bad day", "resets Feb 31 at 5am (America/Los_Angeles)", time.Time{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseQuotaReset(tt.output, now)
			require.Equal(t, tt.ok, ok)
			if tt.ok {
				assert.True(t, tt.want.Equal(got), "want %s got %s", tt.want, got)
			}
		})
	}
}

func TestQuotaPauseUntil(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		output string
		want   time.Time
	}{
		{"parsed reset", "resets 11pm (UTC)", time.Date(2026, 10, 8, 23, 0, 0, 0, time.UTC)},
		{"unparsable falls back to one hour", "You've hit your limit", now.Add(time.Hour)},
		{"just-passed dated reset falls back to one hour", "resets Oct 8 at 11am (UTC)", now.Add(time.Hour)},
		{"far reset capped at seven days", "resets Oct 30 at 7am (UTC)", now.Add(7 * 24 * time.Hour)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.True(t, tt.want.Equal(quotaPauseUntil(tt.output, now)))
		})
	}
}

// A quota exit is global: once one session hits it, every other session must
// skip the claude spawn (SkipLLM) until the pause ends, with one WARN total.
func TestQuotaExhaustionPausesSummarizationForAllSessions(t *testing.T) {
	var logs bytes.Buffer
	handler := NewSessionFinalizeHandlerForTest(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	clock := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	handler.now = func() time.Time { return clock }

	itemFor := func(name string) *WorkItem {
		ledgerPath := createTestSession(t, name, nil)
		sessionDir := filepath.Join(ledgerPath, "sessions", name)
		return &WorkItem{ID: name, Type: sessionFinalizeType, Payload: &SessionFinalizePayload{
			SessionDir: sessionDir, RawPath: filepath.Join(sessionDir, "raw.jsonl"),
			Missing: requiredArtifacts, LedgerPath: ledgerPath,
		}}
	}

	first := itemFor("2026-05-04T15-00-testuser-OxQuotaA")
	req, err := handler.BuildPrompt(first)
	require.NoError(t, err)
	require.False(t, req.SkipLLM, "no pause yet: the agent must run")

	quotaExit := &RunResult{ExitCode: 1, Output: "You've hit your weekly limit · resets 11pm (UTC)"}
	require.NoError(t, handler.ProcessResult(first, quotaExit))

	var skippedItem *WorkItem
	for _, name := range []string{"2026-05-04T15-00-testuser-OxQuotaB", "2026-05-04T15-00-testuser-OxQuotaC"} {
		second := itemFor(name)
		skippedItem = second
		req, err = handler.BuildPrompt(second)
		require.NoError(t, err)
		require.True(t, req.SkipLLM, "paused: the agent must not be spawned for %s", name)
		require.NoError(t, handler.ProcessResult(second, &RunResult{}))
		entries, err := os.ReadDir(second.Payload.(*SessionFinalizePayload).SessionDir)
		require.NoError(t, err)
		for _, e := range entries {
			assert.NotContains(t, e.Name(), "summary", "paused item must not write a summary or failure stub")
		}
	}

	// a repeat quota exit from an in-flight item must not announce a second pause
	require.NoError(t, handler.ProcessResult(itemFor("2026-05-04T15-00-testuser-OxQuotaD"), quotaExit))

	var warns, pauseWarns, skipDebug, resumed int
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var rec struct {
			Level  string `json:"level"`
			Msg    string `json:"msg"`
			Reason string `json:"reason"`
			Until  string `json:"until"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &rec))
		switch {
		case rec.Msg == "summarization paused: claude quota exhausted":
			pauseWarns++
			assert.Equal(t, "WARN", rec.Level)
			assert.Contains(t, rec.Until, "2026-10-08T23:00:00")
		case rec.Msg == "summarization agent exited with error, discarding output":
			warns++
			assert.Equal(t, failureReasonQuotaExhausted, rec.Reason)
		case strings.HasPrefix(rec.Msg, "summarization paused, skipping session"):
			skipDebug++
			assert.NotEqual(t, "WARN", rec.Level)
		case rec.Msg == "summarization resumed: quota pause ended":
			resumed++
		}
	}
	assert.Equal(t, 1, pauseWarns, "pause start is announced exactly once")
	assert.Equal(t, 2, warns, "each failing exit keeps its own diagnostic WARN with a reason")
	assert.Equal(t, 2, skipDebug, "one non-WARN line per skipped session")
	assert.Zero(t, resumed)

	// pause expires: agent runs again and the end is logged once
	clock = time.Date(2026, 10, 8, 23, 0, 1, 0, time.UTC)
	for range 2 {
		req, err = handler.BuildPrompt(itemFor("2026-05-04T15-00-testuser-OxQuotaE"))
		require.NoError(t, err)
		require.False(t, req.SkipLLM, "pause over: the agent must run again")
	}
	// a session skipped during the pause is picked up again on the next cycle
	req, err = handler.BuildPrompt(skippedItem)
	require.NoError(t, err)
	require.False(t, req.SkipLLM, "a session skipped during the pause must be summarized once it ends")
	assert.Equal(t, 1, strings.Count(logs.String(), "summarization resumed: quota pause ended"))
}
