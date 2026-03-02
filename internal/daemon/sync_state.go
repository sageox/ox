package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const (
	// DefaultStalenessThreshold is the duration after which sync data is considered stale.
	DefaultStalenessThreshold = 24 * time.Hour

	syncStateFilename = "sync-state.json"
	syncStateCacheDir = "cache"
	sageoxDirName     = ".sageox"
)

// SyncState tracks the sync health of a workspace (team context or ledger).
// Stored in .sageox/cache/sync-state.json within the workspace directory.
type SyncState struct {
	LastSync            time.Time `json:"last_sync"`
	LastSyncCommit      string    `json:"last_sync_commit"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
}

// IsStale returns true if the last successful sync exceeds the given threshold.
// A zero LastSync (never synced) is always stale.
func (s *SyncState) IsStale(threshold time.Duration) bool {
	if s.LastSync.IsZero() {
		return true
	}
	return time.Since(s.LastSync) > threshold
}

// StaleDuration returns how long since the last successful sync.
// Returns 0 if never synced.
func (s *SyncState) StaleDuration() time.Duration {
	if s.LastSync.IsZero() {
		return 0
	}
	return time.Since(s.LastSync)
}

// RecordSuccess updates state after a successful sync.
func (s *SyncState) RecordSuccess(commitSHA string) {
	s.LastSync = time.Now().UTC()
	s.LastSyncCommit = commitSHA
	s.ConsecutiveFailures = 0
}

// RecordFailure increments the consecutive failure count.
func (s *SyncState) RecordFailure() {
	s.ConsecutiveFailures++
}

// LoadSyncState reads sync state from .sageox/cache/sync-state.json within workspacePath.
// Returns empty SyncState (not error) if the file is missing or corrupt.
func LoadSyncState(workspacePath string) *SyncState {
	if workspacePath == "" {
		return &SyncState{}
	}

	statePath := filepath.Join(workspacePath, sageoxDirName, syncStateCacheDir, syncStateFilename)
	data, err := os.ReadFile(statePath)
	if err != nil {
		return &SyncState{}
	}

	var state SyncState
	if err := json.Unmarshal(data, &state); err != nil {
		return &SyncState{}
	}

	return &state
}

// SaveSyncState writes sync state to .sageox/cache/sync-state.json within workspacePath.
func SaveSyncState(workspacePath string, state *SyncState) error {
	if workspacePath == "" || state == nil {
		return nil
	}

	cacheDir := filepath.Join(workspacePath, sageoxDirName, syncStateCacheDir)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(cacheDir, syncStateFilename), data, 0600)
}
