package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncState_LoadSave(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".sageox", "cache"), 0755))

	state := &SyncState{
		LastSync:            time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		LastSyncCommit:      "abc123def456",
		ConsecutiveFailures: 0,
	}

	require.NoError(t, SaveSyncState(dir, state))

	loaded := LoadSyncState(dir)
	assert.Equal(t, state.LastSync.UTC(), loaded.LastSync.UTC())
	assert.Equal(t, state.LastSyncCommit, loaded.LastSyncCommit)
	assert.Equal(t, 0, loaded.ConsecutiveFailures)
}

func TestSyncState_MissingFile(t *testing.T) {
	dir := t.TempDir()

	state := LoadSyncState(dir)
	assert.True(t, state.LastSync.IsZero())
	assert.Empty(t, state.LastSyncCommit)
	assert.Equal(t, 0, state.ConsecutiveFailures)
}

func TestSyncState_CorruptFile(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, ".sageox", "cache")
	require.NoError(t, os.MkdirAll(cacheDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "sync-state.json"), []byte("{invalid json"), 0600))

	state := LoadSyncState(dir)
	assert.True(t, state.LastSync.IsZero(), "corrupt file should return zero-value state")
}

func TestSyncState_EmptyPath(t *testing.T) {
	state := LoadSyncState("")
	assert.True(t, state.LastSync.IsZero())

	assert.NoError(t, SaveSyncState("", &SyncState{}))
}

func TestSyncState_IsStale(t *testing.T) {
	tests := []struct {
		name      string
		lastSync  time.Time
		threshold time.Duration
		wantStale bool
	}{
		{
			name:      "zero time is always stale",
			lastSync:  time.Time{},
			threshold: 24 * time.Hour,
			wantStale: true,
		},
		{
			name:      "recent sync is not stale",
			lastSync:  time.Now().Add(-1 * time.Hour),
			threshold: 24 * time.Hour,
			wantStale: false,
		},
		{
			name:      "old sync is stale",
			lastSync:  time.Now().Add(-48 * time.Hour),
			threshold: 24 * time.Hour,
			wantStale: true,
		},
		{
			name:      "exactly at threshold boundary",
			lastSync:  time.Now().Add(-24*time.Hour - time.Second),
			threshold: 24 * time.Hour,
			wantStale: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &SyncState{LastSync: tt.lastSync}
			assert.Equal(t, tt.wantStale, state.IsStale(tt.threshold))
		})
	}
}

func TestSyncState_RecordSuccess(t *testing.T) {
	state := &SyncState{ConsecutiveFailures: 5}

	before := time.Now().UTC()
	state.RecordSuccess("deadbeef1234")

	assert.False(t, state.LastSync.Before(before), "LastSync should be at or after test start")
	assert.Equal(t, "deadbeef1234", state.LastSyncCommit)
	assert.Equal(t, 0, state.ConsecutiveFailures, "failures should reset on success")
}

func TestSyncState_RecordFailure(t *testing.T) {
	state := &SyncState{
		LastSync:            time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		LastSyncCommit:      "abc123",
		ConsecutiveFailures: 2,
	}

	state.RecordFailure()

	assert.Equal(t, 3, state.ConsecutiveFailures)
	assert.Equal(t, "abc123", state.LastSyncCommit, "commit should be preserved on failure")
	assert.False(t, state.LastSync.IsZero(), "last sync should be preserved on failure")
}

func TestSyncState_CreatesCacheDir(t *testing.T) {
	dir := t.TempDir()
	// .sageox/cache does NOT exist yet

	state := &SyncState{LastSync: time.Now().UTC(), LastSyncCommit: "sha1"}
	require.NoError(t, SaveSyncState(dir, state))

	// verify the cache dir and file were created
	statePath := filepath.Join(dir, ".sageox", "cache", "sync-state.json")
	data, err := os.ReadFile(statePath)
	require.NoError(t, err)

	var loaded SyncState
	require.NoError(t, json.Unmarshal(data, &loaded))
	assert.Equal(t, "sha1", loaded.LastSyncCommit)
}

func TestSyncState_StaleDuration(t *testing.T) {
	t.Run("zero time returns zero", func(t *testing.T) {
		state := &SyncState{}
		assert.Equal(t, time.Duration(0), state.StaleDuration())
	})

	t.Run("returns elapsed time", func(t *testing.T) {
		state := &SyncState{LastSync: time.Now().Add(-2 * time.Hour)}
		d := state.StaleDuration()
		assert.InDelta(t, 2*time.Hour, d, float64(5*time.Second))
	})
}
