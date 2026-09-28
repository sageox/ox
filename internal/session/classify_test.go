package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifySession(t *testing.T) {
	tests := []struct {
		name       string
		info       SessionInfo
		isUploaded bool
		want       SessionStatus
	}{
		{
			name:       "not recording, not uploaded → local",
			info:       SessionInfo{Recording: false},
			isUploaded: false,
			want:       StatusLocal,
		},
		{
			name:       "not recording, uploaded → uploaded",
			info:       SessionInfo{Recording: false},
			isUploaded: true,
			want:       StatusUploaded,
		},
		{
			name: "recording, live process → recording",
			info: SessionInfo{
				Recording: true,
				ParentPID: os.Getpid(), // current process is alive
			},
			want: StatusRecording,
		},
		{
			name: "recording, dead process, no data → ghost",
			info: SessionInfo{
				Recording:  true,
				ParentPID:  99999999, // guaranteed dead
				EntryCount: 0,
				HasRawData: false,
			},
			want: StatusGhost,
		},
		{
			name: "recording, dead process, has entry count → orphan",
			info: SessionInfo{
				Recording:  true,
				ParentPID:  99999999,
				EntryCount: 5,
				HasRawData: false,
			},
			want: StatusOrphan,
		},
		{
			name: "recording, dead process, has raw data → orphan",
			info: SessionInfo{
				Recording:  true,
				ParentPID:  99999999,
				EntryCount: 0,
				HasRawData: true,
			},
			want: StatusOrphan,
		},
		{
			name: "recording, no PID, recent → recording (heuristic)",
			info: SessionInfo{
				Recording:  true,
				ParentPID:  0,
				EntryCount: 0,
				CreatedAt:  time.Now(), // just created
			},
			want: StatusRecording,
		},
		{
			name: "recording, no PID, old, no data → ghost (heuristic)",
			info: SessionInfo{
				Recording:  true,
				ParentPID:  0,
				EntryCount: 0,
				CreatedAt:  time.Now().Add(-10 * time.Minute),
			},
			want: StatusGhost,
		},
		{
			name: "recording, no PID, old, has data → orphan (heuristic)",
			info: SessionInfo{
				Recording:  true,
				ParentPID:  0,
				EntryCount: 3,
				CreatedAt:  time.Now().Add(-10 * time.Minute),
			},
			want: StatusOrphan,
		},
		{
			name: "stopped by user, not uploaded → paused",
			info: SessionInfo{
				Recording:  false,
				StopReason: StopReasonStopped,
			},
			isUploaded: false,
			want:       StatusPaused,
		},
		{
			name: "stopped by user, uploaded → uploaded (takes precedence)",
			info: SessionInfo{
				Recording:  false,
				StopReason: StopReasonStopped,
			},
			isUploaded: true,
			want:       StatusUploaded,
		},
		{
			name: "canceled → canceled (even if uploaded somehow)",
			info: SessionInfo{
				Recording:  false,
				StopReason: StopReasonCanceled,
			},
			isUploaded: true,
			want:       StatusCanceled,
		},
		{
			name: "canceled, not uploaded → canceled",
			info: SessionInfo{
				Recording:  false,
				StopReason: StopReasonCanceled,
			},
			isUploaded: false,
			want:       StatusCanceled,
		},
		{
			name: "recovered → local (no special display)",
			info: SessionInfo{
				Recording:  false,
				StopReason: StopReasonRecovered,
			},
			isUploaded: false,
			want:       StatusLocal,
		},
		{
			name: "recovered and uploaded → uploaded",
			info: SessionInfo{
				Recording:  false,
				StopReason: StopReasonRecovered,
			},
			isUploaded: true,
			want:       StatusUploaded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifySession(tt.info, tt.isUploaded)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestHasSubstantiveEntries(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"empty file", "", false},
		{"header only", `{"metadata":{"agent_id":"Ox1"}}` + "\n", false},
		{"header plus entry", `{"metadata":{}}` + "\n" + `{"type":"user"}` + "\n", true},
		{"multi-turn", `{"metadata":{}}` + "\n" + `{"type":"user"}` + "\n" + `{"type":"assistant"}` + "\n", true},
		{"nonexistent", "", false}, // special case
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "nonexistent" {
				assert.False(t, HasSubstantiveEntries("/nonexistent/raw.jsonl"))
				return
			}
			path := filepath.Join(t.TempDir(), "raw.jsonl")
			require.NoError(t, os.WriteFile(path, []byte(tt.content), 0644))
			assert.Equal(t, tt.want, HasSubstantiveEntries(path))
		})
	}
}

func TestCountSubstantiveEntries(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
	}{
		{"empty file", "", 0},
		{"header only", `{"metadata":{}}` + "\n", 0},
		{"header plus one", `{"metadata":{}}` + "\n" + `{"type":"user"}` + "\n", 1},
		{"header plus three", `{"metadata":{}}` + "\n" + `{"type":"user"}` + "\n" + `{"type":"assistant"}` + "\n" + `{"entry_count":2}` + "\n", 3},
		{"header plus entry plus carrier footer", `{"metadata":{}}` + "\n" + `{"type":"user"}` + "\n" + `{"type":"footer","stopped_at":"2026-09-28T04:15:30Z"}` + "\n", 1},
		{"nonexistent", "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "nonexistent" {
				assert.Equal(t, 0, CountSubstantiveEntries("/nonexistent/raw.jsonl"))
				return
			}
			path := filepath.Join(t.TempDir(), "raw.jsonl")
			require.NoError(t, os.WriteFile(path, []byte(tt.content), 0644))
			assert.Equal(t, tt.want, CountSubstantiveEntries(path))
		})
	}
}

func TestRawJSONLHasData(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "test-session")
	require.NoError(t, os.MkdirAll(sessionPath, 0755))

	// no raw.jsonl → false
	assert.False(t, RawJSONLHasData(sessionPath))

	// empty raw.jsonl → false
	require.NoError(t, os.WriteFile(filepath.Join(sessionPath, "raw.jsonl"), []byte{}, 0644))
	assert.False(t, RawJSONLHasData(sessionPath))

	// raw.jsonl with content → true
	require.NoError(t, os.WriteFile(filepath.Join(sessionPath, "raw.jsonl"), []byte(`{"metadata":{}}`+"\n"), 0644))
	assert.True(t, RawJSONLHasData(sessionPath))
}

// TestCanTransitionStopReason verifies the precedence lattice for
// StopReason transitions. Failure prevented: a stale replayed adapter
// rate-limit line overwriting a user-initiated "stopped" reason and
// flipping the session display to "rate limit".
func TestCanTransitionStopReason(t *testing.T) {
	tests := []struct {
		name    string
		current string
		next    string
		want    bool
	}{
		{name: "empty to rate_limited (0 to 50)", current: "", next: StopReasonRateLimited, want: true},
		{name: "rate_limited idempotent", current: StopReasonRateLimited, next: StopReasonRateLimited, want: true},
		{name: "stopped beats rate_limited", current: StopReasonStopped, next: StopReasonRateLimited, want: false},
		{name: "recovered to rate_limited", current: StopReasonRecovered, next: StopReasonRateLimited, want: true},
		{name: "rate_limited to stopped (user override)", current: StopReasonRateLimited, next: StopReasonStopped, want: true},
		{name: "unknown to unknown (both rank 0)", current: "weird", next: "alsoweird", want: true},
		{name: "canceled beats rate_limited", current: StopReasonCanceled, next: StopReasonRateLimited, want: false},
		{name: "quota beats terminal_error", current: StopReasonTerminalError, next: StopReasonQuotaExceeded, want: true},
		{name: "stopped beats canceled (equal rank, idempotent)", current: StopReasonStopped, next: StopReasonCanceled, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CanTransitionStopReason(tt.current, tt.next)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestFormatStopReason verifies the user-facing string produced for each
// terminal-stop reason and reset-time shape. Failure prevented: a UI
// regression that renders user-initiated stops as "rate limit", or omits
// the reset-time hint when the adapter parsed one out of the matched line.
func TestFormatStopReason(t *testing.T) {
	resetTime := time.Date(2026, 5, 28, 15, 0, 0, 0, time.Local)

	tests := []struct {
		name string
		info SessionInfo
		want string
	}{
		{
			name: "empty stop reason",
			info: SessionInfo{StopReason: ""},
			want: "",
		},
		{
			name: "rate limited, no resets",
			info: SessionInfo{StopReason: StopReasonRateLimited},
			want: "rate limit",
		},
		{
			name: "rate limited, raw resets only",
			info: SessionInfo{StopReason: StopReasonRateLimited, StopResetsAtRaw: "in 3h"},
			want: "rate limit (resets in 3h)",
		},
		{
			name: "rate limited, parsed resets time",
			info: SessionInfo{StopReason: StopReasonRateLimited, StopResetsAt: &resetTime},
			want: "rate limit (resets " + resetTime.Local().Format("15:04") + ")",
		},
		{
			name: "quota exceeded",
			info: SessionInfo{StopReason: StopReasonQuotaExceeded},
			want: "quota exceeded",
		},
		{
			name: "terminal error",
			info: SessionInfo{StopReason: StopReasonTerminalError},
			want: "agent error",
		},
		{
			name: "user-initiated stopped is suppressed",
			info: SessionInfo{StopReason: StopReasonStopped},
			want: "",
		},
		{
			name: "user-initiated canceled is suppressed",
			info: SessionInfo{StopReason: StopReasonCanceled},
			want: "",
		},
		{
			name: "recovered is suppressed",
			info: SessionInfo{StopReason: StopReasonRecovered},
			want: "",
		},
		{
			name: "parsed time wins over raw",
			info: SessionInfo{
				StopReason:      StopReasonRateLimited,
				StopResetsAt:    &resetTime,
				StopResetsAtRaw: "in 3h",
			},
			want: "rate limit (resets " + resetTime.Local().Format("15:04") + ")",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatStopReason(tt.info)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsPIDAlive(t *testing.T) {
	// current process is alive
	assert.True(t, isPIDAlive(os.Getpid()))

	// zero/negative PID
	assert.False(t, isPIDAlive(0))
	assert.False(t, isPIDAlive(-1))

	// very large PID — almost certainly dead
	assert.False(t, isPIDAlive(99999999))
}

// TestClassifyRawFile_FramingLinesAreNotContent pins the rule every finalize,
// anti-entropy and phantom-reaper gate leans on: only a conversation entry
// makes a raw.jsonl substantive. Since #1025 each finalize door appends a
// {"type":"footer"} carrier before the daemon looks at the file, so a
// recording that never captured a turn is header + footer, two lines.
// Counting lines classified that as content: the daemon summarized an empty
// transcript, uploaded it and committed a blank session to the Ledger, one per
// throwaway agent process.
func TestClassifyRawFile_FramingLinesAreNotContent(t *testing.T) {
	header := `{"metadata":{"agent_id":"Ox1"}}` + "\n"
	footer := `{"closed_at":"2026-09-28T04:15:30Z","native_sessions":[{"id":"5a0b7989-e539-4c99-979f-b1d4cb19d697","source":"startup","first_seen":"2026-09-28T04:15:29Z","last_seen":"2026-09-28T04:15:30Z"}],"stopped_at":"2026-09-28T04:15:30Z","type":"footer"}` + "\n"
	user := `{"type":"user","content":"hello"}` + "\n"

	tests := []struct {
		name    string
		content string
		want    RawKind
	}{
		{"header plus carrier footer", header + footer, RawHeaderOnly},
		{"header plus two footers", header + footer + footer, RawHeaderOnly},
		{"header plus blank line", header + "\n", RawHeaderOnly},
		{"header, footer, then a turn", header + footer + user, RawSubstantive},
		{"header, turn, then footer", header + user + footer, RawSubstantive},
		// fail safe: a line we cannot read is never a deletable phantom
		{"header plus a line that is not JSON", header + "not json\n", RawSubstantive},
		{"header plus an entry of unknown type", header + `{"type":"message"}` + "\n", RawSubstantive},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "raw.jsonl")
			require.NoError(t, os.WriteFile(path, []byte(tt.content), 0o600))
			assert.Equal(t, tt.want, ClassifyRawFile(path))
		})
	}
}

// TestClassifyRawFile_RealCarrierStampKeepsHeaderOnly drives the actual
// finalize-door writer so the classifier and StampRawCarrier cannot drift
// apart again: a header-only recording stays header-only after the stamp and
// becomes substantive only once a turn lands.
func TestClassifyRawFile_RealCarrierStampKeepsHeaderOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raw.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(`{"type":"header","metadata":{"agent_id":"Ox1"}}`+"\n"), 0o600))
	require.Equal(t, RawHeaderOnly, ClassifyRawFile(path), "precondition")

	require.NoError(t, StampRawCarrier(path, CarrierStamp{StoppedAt: time.Date(2026, 9, 28, 4, 15, 30, 0, time.UTC)}))
	assert.Equal(t, RawHeaderOnly, ClassifyRawFile(path), "a carrier footer is framing, not a turn")
	assert.False(t, HasSubstantiveEntries(path))

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(`{"type":"user","content":"hello"}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	assert.Equal(t, RawSubstantive, ClassifyRawFile(path))
}
