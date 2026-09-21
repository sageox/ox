package lfs

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionMeta_NativeSessionsAndStoppedAtRoundTrip: the two additive
// meta.json fields survive write → read unchanged, are absent (not null /
// zero) when unset so older readers see no change, and the builder ignores a
// zero stop time rather than writing 0001-01-01.
func TestSessionMeta_NativeSessionsAndStoppedAtRoundTrip(t *testing.T) {
	stoppedAt := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	createdAt := stoppedAt.Add(-2 * time.Hour)

	t.Run("populated", func(t *testing.T) {
		dir := t.TempDir()
		meta := NewSessionMeta("2026-09-21T10-00-tester-Ox1", "tester", "Ox1", "claude-code", createdAt).
			NativeSessions([]NativeSession{
				{ID: "sess-a", Source: "startup", FirstSeen: createdAt, LastSeen: createdAt},
				{ID: "sess-b", Source: "clear", FirstSeen: createdAt.Add(time.Hour), LastSeen: createdAt.Add(90 * time.Minute)},
			}).
			StoppedAt(stoppedAt).
			Build()
		require.NoError(t, WriteSessionMetaOnly(dir, meta))

		got, err := ReadSessionMeta(dir)
		require.NoError(t, err)
		require.NotNil(t, got.StoppedAt)
		assert.True(t, got.StoppedAt.Equal(stoppedAt))
		assert.True(t, got.StoppedAt.After(got.CreatedAt), "stopped_at must be later than created_at")
		require.Len(t, got.NativeSessions, 2)
		assert.Equal(t, "sess-a", got.NativeSessions[0].ID)
		assert.Equal(t, "startup", got.NativeSessions[0].Source)
		assert.Equal(t, "clear", got.NativeSessions[1].Source)
		assert.True(t, got.NativeSessions[1].LastSeen.Equal(createdAt.Add(90*time.Minute)))
	})

	t.Run("absent when unset", func(t *testing.T) {
		meta := NewSessionMeta("n", "u", "a", "t", createdAt).StoppedAt(time.Time{}).Build()
		assert.Nil(t, meta.StoppedAt, "a zero stop time must be ignored, not written")
		data, err := json.Marshal(meta)
		require.NoError(t, err)
		assert.NotContains(t, string(data), "native_sessions")
		assert.NotContains(t, string(data), "stopped_at")
	})

	t.Run("legacy meta.json without the fields still reads", func(t *testing.T) {
		var meta SessionMeta
		require.NoError(t, json.Unmarshal([]byte(`{"version":"1.0","session_name":"x","agent_id":"a","created_at":"2026-01-01T00:00:00Z"}`), &meta))
		assert.Nil(t, meta.StoppedAt)
		assert.Empty(t, meta.NativeSessions)
	})
}

// TestRecordNativeSession is the dedup rule both the live recording state and
// the finalize doors rely on.
func TestRecordNativeSession(t *testing.T) {
	t0 := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	var list []NativeSession
	list = RecordNativeSession(list, "", "startup", t0)
	assert.Empty(t, list, "empty id is a no-op")
	list = RecordNativeSession(list, "a", "", t0)
	list = RecordNativeSession(list, "a", "startup", t0.Add(time.Minute))
	list = RecordNativeSession(list, "a", "compact", t0.Add(-time.Minute)) // out-of-order sighting
	list = RecordNativeSession(list, "b", "clear", t0.Add(time.Hour))
	require.Len(t, list, 2)
	assert.Equal(t, "startup", list[0].Source, "first non-empty source sticks")
	assert.True(t, list[0].FirstSeen.Equal(t0))
	assert.True(t, list[0].LastSeen.Equal(t0.Add(time.Minute)), "last_seen never moves backwards")
	assert.Equal(t, "b", list[1].ID)
}
