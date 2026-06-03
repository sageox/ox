package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/session"
)

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		{"zero", 0, "<1m"},
		{"30 seconds rounds up", 30 * time.Second, "1m"},
		{"sub-30 seconds", 20 * time.Second, "<1m"},
		{"one minute", 1 * time.Minute, "1m"},
		{"minutes", 23 * time.Minute, "23m"},
		{"one hour", 60 * time.Minute, "1h"},
		{"hours and minutes", 1*time.Hour + 23*time.Minute, "1h 23m"},
		{"multiple hours", 3*time.Hour + 45*time.Minute, "3h 45m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, formatDuration(tt.duration))
		})
	}
}

// TestRecordingDestination asserts the destination phrase names team + repo and
// degrades independently — never a dangling possessive or a bare "your team".
// Failure prevented: regressing to ambiguous "your team" copy, or emitting a
// broken phrase like "Acme Eng's  ledger" / "'s web-app ledger" when one part
// is unset.
func TestRecordingDestination(t *testing.T) {
	tests := []struct {
		name, team, repo, want string
	}{
		{"team and repo", "Acme Eng", "web-app", "Acme Eng's web-app ledger"},
		{"team only", "Acme Eng", "", "Acme Eng's ledger"},
		{"repo only", "", "web-app", "the web-app ledger"},
		{"neither", "", "", "your team's ledger"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, recordingDestination(tt.team, tt.repo))
		})
	}
}

func TestRepoNameFromPath(t *testing.T) {
	assert.Equal(t, "web-app", repoNameFromPath("/Users/dev/code/web-app"))
	assert.Equal(t, "", repoNameFromPath("/"))
	assert.Equal(t, "", repoNameFromPath("."))
	assert.Equal(t, "", repoNameFromPath(""))
}

// TestFormatReminderContent asserts the first/periodic variants by intent, not
// byte-for-byte: the destination is unmissable, the learn-more link and stop
// hint appear ONLY on the first fire, and periodic fires carry the live counts.
// Failure prevented: leaking the hourly-noise link/stop-hint onto every
// periodic fire, or dropping the destination from either fire.
func TestFormatReminderContent(t *testing.T) {
	state := &session.RecordingState{
		StartedAt:  time.Now().Add(-1*time.Hour - 23*time.Minute),
		EntryCount: 45,
	}
	const dest = "Acme Eng's web-app ledger"

	t.Run("first fire", func(t *testing.T) {
		c := formatReminderContent(state, dest, true)
		assert.Contains(t, c, "Tell the user")
		assert.Contains(t, c, dest, "destination (team + repo) must be surfaced")
		assert.Contains(t, c, "/ox-session-stop", "first fire offers a stop hint")
		assert.Contains(t, c, "sageox.ai/rec", "first fire offers learn-more link")
	})

	t.Run("periodic fire", func(t *testing.T) {
		c := formatReminderContent(state, dest, false)
		assert.Contains(t, c, dest, "destination reasserted on every fire")
		assert.Contains(t, c, "45 turns")
		assert.Contains(t, c, "1h 23m")
		assert.NotContains(t, c, "sageox.ai/rec", "periodic fire must not repeat the link")
		assert.NotContains(t, c, "/ox-session-stop", "periodic fire must not repeat the stop hint")
	})
}

func TestRecordingReminderSource_Name(t *testing.T) {
	s := &RecordingReminderSource{}
	assert.Equal(t, "recording-reminder", s.Name())
}

func TestRecordingReminderSource_Interval(t *testing.T) {
	s := NewRecordingReminderSource(nil, nil, time.Hour, "")
	assert.Equal(t, 1*time.Minute, s.Interval(), "default tick is 1 minute")

	s.SetTick(5 * time.Second)
	assert.Equal(t, 5*time.Second, s.Interval(), "tick is configurable")
}

func TestRecordingReminderSource_Produce_NilDeps(t *testing.T) {
	s := &RecordingReminderSource{}
	assert.Nil(t, s.Produce(context.Background()))
}

// --- Integration tests: full Produce pipeline with real store + heartbeat ---

// initRecordingReminderProject creates a project root with .sageox/config.json
// and XDG cache pointing to a temp dir so LoadRecordingStateForAgent can find
// recording state written under the sessions/ cache path.
func initRecordingReminderProject(t *testing.T) (projectRoot string, sessionsBase string) {
	t.Helper()
	cacheDir := t.TempDir()
	projectRoot = t.TempDir()
	repoID := "test-recording-reminder"

	// create .sageox/config.json with repo_id
	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0o755))
	cfgData, _ := json.Marshal(map[string]string{
		"config_version":     "2",
		"repo_id":            repoID,
		"recording_reminder": "on",
		"team_name":          "Acme Eng",
		"project":            "web-app",
	})
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), cfgData, 0o644))

	// point XDG cache to temp dir
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)

	// compute sessions base (same path sessionsSearchPaths resolves)
	sessionsBase = filepath.Join(session.GetContextPath(repoID), "sessions")
	return projectRoot, sessionsBase
}

// writeTestRecordingState creates a .recording.json for a fake agent session.
func writeTestRecordingState(t *testing.T, projectRoot, sessionsBase, agentID string, entryCount int, startedAt time.Time) {
	t.Helper()
	sessionName := "2026-01-01T00-00-test-" + agentID
	sessionPath := filepath.Join(sessionsBase, sessionName)
	require.NoError(t, os.MkdirAll(sessionPath, 0o755))

	state := &session.RecordingState{
		AgentID:     agentID,
		StartedAt:   startedAt,
		EntryCount:  entryCount,
		SessionPath: sessionPath,
	}
	require.NoError(t, session.SaveRecordingState(projectRoot, state))
}

func TestRecordingReminderSource_Produce_FirstReminder(t *testing.T) {
	// Verifies: an agent with active recording and enough heartbeats
	// receives a recording reminder on the first Produce tick.
	projectRoot, sessionsBase := initRecordingReminderProject(t)
	store := openTestWhisperStore(t)
	hb := NewHeartbeatHandler(slog.Default())

	agentID := "OxRem1"

	// write recording state: 12 turns, started 5 minutes ago
	writeTestRecordingState(t, projectRoot, sessionsBase, agentID, 12, time.Now().Add(-5*time.Minute))

	// send heartbeats to exceed Claude Code threshold (>= 2)
	sendHeartbeat(t, hb, HeartbeatPayload{AgentID: agentID, AgentType: "claude-code", Timestamp: time.Now()})
	sendHeartbeat(t, hb, HeartbeatPayload{AgentID: agentID, AgentType: "claude-code", Timestamp: time.Now()})

	src := NewRecordingReminderSource(store, hb, 1*time.Hour, projectRoot)
	entries := src.Produce(context.Background())

	require.Len(t, entries, 1, "first tick should produce exactly one reminder")
	assert.Equal(t, "recording-reminder", entries[0].Source)
	assert.Equal(t, "recording-status", entries[0].Topic)
	assert.Equal(t, agentID, entries[0].AgentID)
	// first fire: names team + repo destination, offers stop + learn-more;
	// counts are omitted (near-zero info at session start)
	assert.Contains(t, entries[0].Content, "Acme Eng's web-app ledger")
	assert.Contains(t, entries[0].Content, "/ox-session-stop")
	assert.Contains(t, entries[0].Content, "sageox.ai/rec")
}

func TestRecordingReminderSource_Produce_SuppressedUntilHeartbeatThreshold(t *testing.T) {
	// Verifies: Claude Code agents don't get reminders until heartbeat count >= 2.
	projectRoot, sessionsBase := initRecordingReminderProject(t)
	store := openTestWhisperStore(t)
	hb := NewHeartbeatHandler(slog.Default())

	agentID := "OxRem2"
	writeTestRecordingState(t, projectRoot, sessionsBase, agentID, 5, time.Now().Add(-2*time.Minute))

	// only 1 heartbeat — below threshold
	sendHeartbeat(t, hb, HeartbeatPayload{AgentID: agentID, AgentType: "claude-code", Timestamp: time.Now()})

	src := NewRecordingReminderSource(store, hb, 1*time.Hour, projectRoot)
	entries := src.Produce(context.Background())

	assert.Empty(t, entries, "should not produce reminder with only 1 heartbeat for claude-code")

	// second heartbeat crosses threshold
	sendHeartbeat(t, hb, HeartbeatPayload{AgentID: agentID, AgentType: "claude-code", Timestamp: time.Now()})
	entries = src.Produce(context.Background())

	require.Len(t, entries, 1, "should produce reminder after 2 heartbeats")
}

func TestRecordingReminderSource_Produce_NonClaudeAgent_ImmediateReminder(t *testing.T) {
	// Verifies: non-Claude agents get reminders immediately (no heartbeat gate).
	projectRoot, sessionsBase := initRecordingReminderProject(t)
	store := openTestWhisperStore(t)
	hb := NewHeartbeatHandler(slog.Default())

	agentID := "OxRem3"
	writeTestRecordingState(t, projectRoot, sessionsBase, agentID, 3, time.Now().Add(-1*time.Minute))

	// single heartbeat — should be enough for non-Claude agents
	sendHeartbeat(t, hb, HeartbeatPayload{AgentID: agentID, AgentType: "gemini", Timestamp: time.Now()})

	src := NewRecordingReminderSource(store, hb, 1*time.Hour, projectRoot)
	entries := src.Produce(context.Background())

	require.Len(t, entries, 1, "non-claude agent should get immediate reminder")
	// first fire names the destination rather than leaking startup counts
	assert.Contains(t, entries[0].Content, "Acme Eng's web-app ledger")
}

func TestRecordingReminderSource_Produce_IntervalGating(t *testing.T) {
	// Verifies: after the first reminder, subsequent reminders are gated by interval.
	projectRoot, sessionsBase := initRecordingReminderProject(t)
	store := openTestWhisperStore(t)
	hb := NewHeartbeatHandler(slog.Default())

	agentID := "OxRem4"
	writeTestRecordingState(t, projectRoot, sessionsBase, agentID, 20, time.Now().Add(-30*time.Minute))

	sendHeartbeat(t, hb, HeartbeatPayload{AgentID: agentID, AgentType: "claude-code", Timestamp: time.Now()})
	sendHeartbeat(t, hb, HeartbeatPayload{AgentID: agentID, AgentType: "claude-code", Timestamp: time.Now()})

	src := NewRecordingReminderSource(store, hb, 1*time.Hour, projectRoot)

	// first produce — should create reminder
	entries := src.Produce(context.Background())
	require.Len(t, entries, 1)

	// persist the whisper to the store (simulates what WhisperScheduler does)
	require.NoError(t, store.Add(entries...))

	// second produce immediately — should be suppressed (interval not elapsed)
	entries = src.Produce(context.Background())
	assert.Empty(t, entries, "should not produce another reminder before interval elapses")
}

func TestRecordingReminderSource_Produce_StoppedRecording(t *testing.T) {
	// Verifies: agents with stopped recordings don't get reminders.
	projectRoot, sessionsBase := initRecordingReminderProject(t)
	store := openTestWhisperStore(t)
	hb := NewHeartbeatHandler(slog.Default())

	agentID := "OxRem5"
	sessionName := "2026-01-01T00-00-test-" + agentID
	sessionPath := filepath.Join(sessionsBase, sessionName)
	require.NoError(t, os.MkdirAll(sessionPath, 0o755))

	stoppedAt := time.Now()
	state := &session.RecordingState{
		AgentID:     agentID,
		StartedAt:   time.Now().Add(-1 * time.Hour),
		EntryCount:  50,
		SessionPath: sessionPath,
		StoppedAt:   &stoppedAt,
	}
	require.NoError(t, session.SaveRecordingState(projectRoot, state))

	sendHeartbeat(t, hb, HeartbeatPayload{AgentID: agentID, AgentType: "claude-code", Timestamp: time.Now()})
	sendHeartbeat(t, hb, HeartbeatPayload{AgentID: agentID, AgentType: "claude-code", Timestamp: time.Now()})

	src := NewRecordingReminderSource(store, hb, 1*time.Hour, projectRoot)
	entries := src.Produce(context.Background())

	assert.Empty(t, entries, "stopped recording should not produce reminders")
}

func TestRecordingReminderSource_Produce_NoActiveAgents(t *testing.T) {
	// Verifies: no whispers produced when no agents are active.
	projectRoot, _ := initRecordingReminderProject(t)
	store := openTestWhisperStore(t)
	hb := NewHeartbeatHandler(slog.Default())

	src := NewRecordingReminderSource(store, hb, 1*time.Hour, projectRoot)
	entries := src.Produce(context.Background())

	assert.Empty(t, entries)
}

func TestRecordingReminderSource_Produce_AgentWithoutRecording(t *testing.T) {
	// Verifies: agents that are active but not recording get no reminders.
	projectRoot, _ := initRecordingReminderProject(t)
	store := openTestWhisperStore(t)
	hb := NewHeartbeatHandler(slog.Default())

	sendHeartbeat(t, hb, HeartbeatPayload{AgentID: "OxNoRec", AgentType: "claude-code", Timestamp: time.Now()})
	sendHeartbeat(t, hb, HeartbeatPayload{AgentID: "OxNoRec", AgentType: "claude-code", Timestamp: time.Now()})

	src := NewRecordingReminderSource(store, hb, 1*time.Hour, projectRoot)
	entries := src.Produce(context.Background())

	assert.Empty(t, entries, "agent without recording state should not get reminders")
}

func TestRecordingReminderSource_Produce_DisabledConfig(t *testing.T) {
	// Verifies: reminders are suppressed when recording_reminder=off.
	projectRoot, sessionsBase := initRecordingReminderProject(t)

	// override config to disable reminders
	cfgData, _ := json.Marshal(map[string]string{
		"config_version":     "2",
		"repo_id":            "test-recording-reminder",
		"recording_reminder": "off",
	})
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".sageox", "config.json"), cfgData, 0o644))

	store := openTestWhisperStore(t)
	hb := NewHeartbeatHandler(slog.Default())

	agentID := "OxOff1"
	writeTestRecordingState(t, projectRoot, sessionsBase, agentID, 10, time.Now().Add(-5*time.Minute))
	sendHeartbeat(t, hb, HeartbeatPayload{AgentID: agentID, AgentType: "claude-code", Timestamp: time.Now()})
	sendHeartbeat(t, hb, HeartbeatPayload{AgentID: agentID, AgentType: "claude-code", Timestamp: time.Now()})

	src := NewRecordingReminderSource(store, hb, 1*time.Hour, projectRoot)
	entries := src.Produce(context.Background())

	assert.Empty(t, entries, "should not produce reminders when config is off")
}
