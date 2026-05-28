package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSerializeAndParseClearNoticeEnv verifies round-trip of the env handoff
// between stopSessionForClear (hook process) and prime (subprocess).
func TestSerializeAndParseClearNoticeEnv(t *testing.T) {
	info := &ClearNoticeInfo{
		SessionName:  "2026-05-27T07-12-ryan-Oxa7b3",
		AgentID:      "Oxa7b3",
		Status:       "recording",
		DurationSecs: 1234,
		WasSuspended: false,
	}

	pair := serializeClearNoticeEnv(info)
	require.NotEmpty(t, pair)
	require.True(t, strings.HasPrefix(pair, envClearNotice+"="))

	// install into env to simulate subprocess inheriting it
	t.Setenv(envClearNotice, strings.TrimPrefix(pair, envClearNotice+"="))

	got := parseClearNoticeEnv()
	require.NotNil(t, got)
	assert.Equal(t, info.SessionName, got.SessionName)
	assert.Equal(t, info.AgentID, got.AgentID)
	assert.Equal(t, info.Status, got.Status)
	assert.Equal(t, info.DurationSecs, got.DurationSecs)
	assert.Equal(t, info.WasSuspended, got.WasSuspended)
}

// TestSerializeClearNoticeEnv_Nil verifies nil returns empty string (caller
// must not append an empty pair to cmd.Env).
func TestSerializeClearNoticeEnv_Nil(t *testing.T) {
	assert.Empty(t, serializeClearNoticeEnv(nil))
}

// TestParseClearNoticeEnv_MissingOrMalformed verifies graceful nil returns.
func TestParseClearNoticeEnv_MissingOrMalformed(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		// ensure env var is unset for this sub-test
		_ = os.Unsetenv(envClearNotice)
		assert.Nil(t, parseClearNoticeEnv())
	})

	t.Run("invalid json", func(t *testing.T) {
		t.Setenv(envClearNotice, "{not json")
		assert.Nil(t, parseClearNoticeEnv())
	})

	t.Run("missing session_name", func(t *testing.T) {
		t.Setenv(envClearNotice, `{"agent_id":"Oxa7b3"}`)
		assert.Nil(t, parseClearNoticeEnv(), "session_name is required")
	})
}

// TestRenderClearNotice covers the standard recording-then-cleared transition.
func TestRenderClearNotice_Recording(t *testing.T) {
	info := &ClearNoticeInfo{
		SessionName:  "2026-05-27T07-12-ryan-Oxa7b3",
		AgentID:      "Oxa7b3",
		Status:       "recording",
		DurationSecs: 754, // 12m 34s
	}
	got := renderClearNotice(info, "Oxb3x9", true)

	assert.Contains(t, got, "/clear")
	assert.Contains(t, got, "2026-05-27T07-12-ryan-Oxa7b3")
	assert.Contains(t, got, "recording")
	assert.Contains(t, got, "12m 34s")
	assert.Contains(t, got, "Oxb3x9")
	assert.NotContains(t, got, "OxOxb3x9",
		"newAgentID already carries the Ox prefix; renderer must not re-prefix")
	assert.Contains(t, got, "Recording: on")
}

// TestRenderClearNotice_Suspended verifies the suspended-prior-session variant
// includes the paused-range-excluded line (ADR-020 semantics).
func TestRenderClearNotice_Suspended(t *testing.T) {
	info := &ClearNoticeInfo{
		SessionName:  "2026-05-27T07-12-ryan-Oxa7b3",
		AgentID:      "Oxa7b3",
		Status:       "suspended",
		DurationSecs: 3600,
		WasSuspended: true,
	}
	got := renderClearNotice(info, "Oxb3x9", false)

	assert.Contains(t, got, "suspended")
	assert.Contains(t, got, "1h")
	assert.Contains(t, got, "Paused range excluded from upload")
	assert.Contains(t, got, "RECORDING SUSPENDED")
	assert.Contains(t, got, "carried from previous")
	assert.NotContains(t, got, "OxOxb3x9",
		"newAgentID already carries the Ox prefix; renderer must not re-prefix")
}

// TestRenderClearNotice_Nil verifies graceful empty return.
func TestRenderClearNotice_Nil(t *testing.T) {
	assert.Empty(t, renderClearNotice(nil, "Oxa7b3", true))
}

// TestFormatClearDuration exercises the duration formatter for the three
// magnitude buckets used in the notice.
func TestFormatClearDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{d: 47 * time.Second, want: "47s"},
		{d: 1 * time.Minute, want: "1m"},
		{d: 3*time.Minute + 24*time.Second, want: "3m 24s"},
		{d: 1 * time.Hour, want: "1h"},
		{d: 1*time.Hour + 12*time.Minute, want: "1h 12m"},
		{d: 2 * time.Hour, want: "2h"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.want, func(t *testing.T) {
			assert.Equal(t, c.want, formatClearDuration(c.d))
		})
	}
}

// TestBuildClearNoticeFromState verifies extraction from a RecordingState.
func TestBuildClearNoticeFromState(t *testing.T) {
	state := &session.RecordingState{
		AgentID:     "Oxa7b3",
		StartedAt:   time.Now().Add(-15 * time.Minute),
		SessionPath: "/tmp/.sageox/cache/sessions/2026-05-27T07-12-ryan-Oxa7b3",
	}
	got := buildClearNoticeFromState(state)
	require.NotNil(t, got)
	assert.Equal(t, "Oxa7b3", got.AgentID)
	assert.Equal(t, "2026-05-27T07-12-ryan-Oxa7b3", got.SessionName)
	assert.InDelta(t, int64(15*60), got.DurationSecs, 5, "duration ~15 min")
	assert.Equal(t, "recording", got.Status)
	assert.False(t, got.WasSuspended)
}

// TestBuildClearNoticeFromState_Nil verifies nil-safe behavior.
func TestBuildClearNoticeFromState_Nil(t *testing.T) {
	assert.Nil(t, buildClearNoticeFromState(nil))
}
