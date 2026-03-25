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

func TestIsPIDAlive(t *testing.T) {
	// current process is alive
	assert.True(t, isPIDAlive(os.Getpid()))

	// zero/negative PID
	assert.False(t, isPIDAlive(0))
	assert.False(t, isPIDAlive(-1))

	// very large PID — almost certainly dead
	assert.False(t, isPIDAlive(99999999))
}
