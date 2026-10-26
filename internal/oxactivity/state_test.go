package oxactivity

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testAgentID = "OxA1b2"

// Failure prevented: the hook that classifies an action and the status-line
// process that renders it disagree on where or how state is stored.
func TestState_RoundTrip(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	want := State{
		LastAction:         "posted murmur",
		LastActionAt:       now,
		LastWhisperFrom:    "160305533-ryan",
		LastWhisperTopic:   "wip",
		LastWhisperContent: "Refactoring auth",
		LastWhisperAt:      now.Add(-time.Minute),
	}
	require.NoError(t, SaveState(root, testAgentID, want))

	path := filepath.Join(root, ".sageox", "cache", "statusline", testAgentID+".json")
	assert.Equal(t, path, StatePath(root, testAgentID))
	assert.FileExists(t, path)

	got := LoadState(root, testAgentID)
	assert.True(t, want.LastActionAt.Equal(got.LastActionAt))
	assert.True(t, want.LastWhisperAt.Equal(got.LastWhisperAt))
	got.LastActionAt, got.LastWhisperAt = want.LastActionAt, want.LastWhisperAt
	assert.Equal(t, want, got)

	// A second save replaces, not merges.
	require.NoError(t, SaveState(root, testAgentID, State{LastAction: "saved plan"}))
	assert.Equal(t, State{LastAction: "saved plan"}, LoadState(root, testAgentID))

	// Agents are isolated from each other.
	assert.Equal(t, State{}, LoadState(root, "OxZz99"))
}

// Failure prevented: a half-written, corrupt, or unreadable state file breaks
// the status line or a hook instead of degrading to "nothing to show".
func TestLoadState_DegradesToZero(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{"missing", func(t *testing.T, path string) {}},
		{"corrupt", func(t *testing.T, path string) {
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.WriteFile(path, []byte(`{"last_action": "posted`), 0o600))
		}},
		{"wrong shape", func(t *testing.T, path string) {
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.WriteFile(path, []byte(`{"last_action": 42}`), 0o600))
		}},
		{"directory in place of file", func(t *testing.T, path string) {
			require.NoError(t, os.MkdirAll(path, 0o755))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			tt.setup(t, StatePath(root, testAgentID))
			assert.Equal(t, State{}, LoadState(root, testAgentID))
		})
	}
}

// Failure prevented: an agent ID from hook input names a path outside the
// state directory ("../../etc/x").
func TestState_InvalidAgentID(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	for _, id := range []string{"", "../../x", "Ox/../a", "OxA1b2.", "OxA1b2.a/b", "OxA1b2..x", "abc", "OxA1b2c"} {
		assert.Empty(t, StatePath(root, id), id)
		err := SaveState(root, id, State{LastAction: "x"})
		require.ErrorIs(t, err, ErrInvalidAgentID, id)
		assert.Equal(t, State{}, LoadState(root, id), id)
	}
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Empty(t, entries, "invalid IDs must not create anything")
}

// TestStateKey scopes state to a harness session and keeps every key a safe
// file name. Failure prevented: one window's activity shown in another, or a
// session ID escaping the cache directory.
func TestStateKey(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "OxA1b2", StateKey("OxA1b2", ""))
	assert.Equal(t, "OxA1b2", StateKey("OxA1b2", "  "))
	assert.Equal(t, "OxA1b2.6f1c2e3d-9a8b-4c5d-8e7f-000000000001", StateKey("OxA1b2", "6f1c2e3d-9a8b-4c5d-8e7f-000000000001"))

	hashed := StateKey("OxA1b2", "../escape/../../x")
	assert.Regexp(t, `^OxA1b2\.[0-9a-f]{16}$`, hashed)
	assert.Equal(t, hashed, StateKey("OxA1b2", "../escape/../../x"), "deterministic")

	root := t.TempDir()
	for _, key := range []string{StateKey("OxA1b2", "s1"), hashed} {
		p := StatePath(root, key)
		require.NotEmpty(t, p)
		assert.Equal(t, filepath.Join(root, ".sageox", "cache", "statusline"), filepath.Dir(p))
	}
	require.NoError(t, SaveState(root, StateKey("OxA1b2", "s1"), State{LastAction: "a"}))
	require.NoError(t, SaveState(root, StateKey("OxA1b2", "s2"), State{LastAction: "b"}))
	assert.Equal(t, "a", LoadState(root, StateKey("OxA1b2", "s1")).LastAction)
	assert.Equal(t, "b", LoadState(root, StateKey("OxA1b2", "s2")).LastAction)
	assert.Equal(t, State{}, LoadState(root, "OxA1b2"), "agent-wide key does not see session state")
}

// TestSaveState_PrunesStaleSessions removes files idle past StateRetention
// and keeps the rest. Failure prevented: one file per Claude Code session
// accumulating forever in the cache.
func TestSaveState_PrunesStaleSessions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	old := StateKey("OxA1b2", "old")
	fresh := StateKey("OxA1b2", "fresh")
	require.NoError(t, SaveState(root, old, State{LastAction: "x"}))
	require.NoError(t, SaveState(root, fresh, State{LastAction: "y"}))
	require.NoError(t, SaveState(root, "OxA1b2", State{LastAction: "w"}))
	other := filepath.Join(filepath.Dir(StatePath(root, old)), "murmurs.json")
	require.NoError(t, os.WriteFile(other, []byte("{}"), 0o600))
	stale := time.Now().Add(-StateRetention - time.Hour)
	for _, p := range []string{StatePath(root, old), StatePath(root, "OxA1b2"), other} {
		require.NoError(t, os.Chtimes(p, stale, stale))
	}

	require.NoError(t, SaveState(root, StateKey("OxA1b2", "new"), State{LastAction: "z"}))
	assert.NoFileExists(t, StatePath(root, old))
	assert.FileExists(t, StatePath(root, "OxA1b2"), "agent-wide state is not a session file")
	assert.FileExists(t, other, "only session state files are pruned")
	assert.FileExists(t, StatePath(root, fresh))
	assert.FileExists(t, StatePath(root, StateKey("OxA1b2", "new")))
}

// Failure prevented: SaveState reports success when it could not write.
func TestSaveState_SurfacesWriteFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// A regular file where the .sageox directory must be: MkdirAll fails everywhere.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox"), []byte("x"), 0o600))
	assert.Error(t, SaveState(root, testAgentID, State{LastAction: "x"}))
}

func TestState_Segment(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		state State
		want  Segment
	}{
		{"zero", State{}, Segment{}},
		{"action only", State{LastAction: "saved plan", LastActionAt: now.Add(-5 * time.Minute)},
			Segment{Action: "saved plan", Age: 5 * time.Minute}},
		{"whisper age wins", State{
			LastAction: "posted murmur", LastActionAt: now.Add(-10 * time.Minute),
			LastWhisperFrom: "ryan", LastWhisperTopic: "wip", LastWhisperContent: "hi",
			LastWhisperAt: now.Add(-3 * time.Minute),
		}, Segment{Action: "posted murmur", WhisperFrom: "ryan", WhisperTopic: "wip", WhisperContent: "hi", Age: 3 * time.Minute}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.state.Segment(now))
		})
	}
}

// Failure prevented: SaveState reports success when the directory exists but
// the file itself cannot be written. A directory where the state file must go
// defeats the atomic rename on every platform, unlike permissions.
func TestSaveState_SurfacesFileWriteFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(StatePath(root, testAgentID), 0o755))
	assert.Error(t, SaveState(root, testAgentID, State{LastAction: "x"}))
}

// TestUpdateState_ConcurrentWritersKeepBothFields: a tool-call hook and a
// prompt hook updating different fields at the same time must not lose either.
// Failure prevented: the status line shows a whisper but forgets the action,
// or the reverse, because the later writer restored a stale snapshot.
func TestUpdateState_ConcurrentWritersKeepBothFields(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			_ = UpdateState(root, testAgentID, func(s *State) bool {
				s.LastAction = "posted murmur"
				s.LastActionAt = now.Add(time.Duration(i) * time.Second)
				return true
			})
		}(i)
		go func(i int) {
			defer wg.Done()
			_ = UpdateState(root, testAgentID, func(s *State) bool {
				s.LastWhisperFrom = "ryan"
				s.LastWhisperAt = now.Add(time.Duration(i) * time.Second)
				return true
			})
		}(i)
	}
	wg.Wait()

	got := LoadState(root, testAgentID)
	assert.Equal(t, "posted murmur", got.LastAction)
	assert.Equal(t, "ryan", got.LastWhisperFrom)
	assert.False(t, got.LastActionAt.IsZero())
	assert.False(t, got.LastWhisperAt.IsZero())
}

// TestUpdateState_NoChangeSkipsWrite: a stale whisper must not touch the file.
func TestUpdateState_NoChangeSkipsWrite(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, UpdateState(root, testAgentID, func(s *State) bool { return false }))
	assert.NoFileExists(t, StatePath(root, testAgentID))
	assert.Error(t, UpdateState(root, "bad id", func(s *State) bool { return true }))
}
