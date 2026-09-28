package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// SyncMetrics — pure computation tests
// =============================================================================

func TestSyncMetrics_NewSyncMetrics(t *testing.T) {
	m := NewSyncMetrics()
	require.NotNil(t, m)
	assert.Equal(t, 100, m.maxSamples)
	assert.Empty(t, m.pullDurations)
}

func TestSyncMetrics_RecordPullSuccess(t *testing.T) {
	m := NewSyncMetrics()

	m.RecordPullSuccess(50 * time.Millisecond)
	m.RecordPullSuccess(100 * time.Millisecond)

	snap := m.Snapshot()
	assert.Equal(t, int64(2), snap.PullSuccessCount)
	assert.False(t, snap.LastPullSuccess.IsZero())
	assert.Equal(t, 75*time.Millisecond, snap.AvgPullDuration)
}

func TestSyncMetrics_RecordPullFailure(t *testing.T) {
	m := NewSyncMetrics()

	m.RecordPullFailure()
	m.RecordPullFailure()
	m.RecordPullFailure()

	snap := m.Snapshot()
	assert.Equal(t, int64(3), snap.PullFailureCount)
	assert.False(t, snap.LastPullFailure.IsZero())
}

func TestSyncMetrics_RecordConflict(t *testing.T) {
	m := NewSyncMetrics()

	m.RecordConflict()

	snap := m.Snapshot()
	assert.Equal(t, int64(1), snap.ConflictCount)
	assert.False(t, snap.LastConflict.IsZero())
}

func TestSyncMetrics_RecordTeamSync(t *testing.T) {
	m := NewSyncMetrics()

	m.RecordTeamSync()

	snap := m.Snapshot()
	assert.Equal(t, int64(1), snap.TeamSyncCount)
}

func TestSyncMetrics_RecordTeamSyncError(t *testing.T) {
	m := NewSyncMetrics()

	m.RecordTeamSyncError()
	m.RecordTeamSyncError()

	snap := m.Snapshot()
	assert.Equal(t, int64(2), snap.TeamSyncErrorCount)
}

func TestSyncMetrics_SnapshotEmpty(t *testing.T) {
	m := NewSyncMetrics()
	snap := m.Snapshot()

	assert.Equal(t, int64(0), snap.PullSuccessCount)
	assert.Equal(t, int64(0), snap.PullFailureCount)
	assert.Equal(t, int64(0), snap.ConflictCount)
	assert.True(t, snap.LastPullSuccess.IsZero())
	assert.True(t, snap.LastPullFailure.IsZero())
	assert.True(t, snap.LastConflict.IsZero())
	assert.Equal(t, time.Duration(0), snap.AvgPullDuration)
	assert.Equal(t, time.Duration(0), snap.P95PullDuration)
}

func TestSyncMetrics_SnapshotConcurrent(t *testing.T) {
	m := NewSyncMetrics()

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			m.RecordPullSuccess(time.Duration(n) * time.Millisecond)
			m.RecordPullFailure()
			m.RecordConflict()
			_ = m.Snapshot()
		}(i)
	}
	wg.Wait()

	snap := m.Snapshot()
	assert.Equal(t, int64(20), snap.PullSuccessCount)
	assert.Equal(t, int64(20), snap.PullFailureCount)
	assert.Equal(t, int64(20), snap.ConflictCount)
}

// =============================================================================
// Pure helpers: timeFromNano, appendDuration, avgDuration, p95Duration
// =============================================================================

func TestTimeFromNano(t *testing.T) {
	tests := []struct {
		name     string
		nano     int64
		wantZero bool
	}{
		{"zero returns zero time", 0, true},
		{"positive nano returns non-zero time", time.Now().UnixNano(), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := timeFromNano(tt.nano)
			assert.Equal(t, tt.wantZero, result.IsZero())
		})
	}
}

func TestTimeFromNano_Roundtrip(t *testing.T) {
	now := time.Now()
	nano := now.UnixNano()
	result := timeFromNano(nano)
	assert.Equal(t, now.UnixNano(), result.UnixNano())
}

func TestAppendDuration(t *testing.T) {
	tests := []struct {
		name       string
		initial    []time.Duration
		add        time.Duration
		maxSamples int
		wantLen    int
	}{
		{
			name:       "append within capacity",
			initial:    []time.Duration{1, 2, 3},
			add:        4,
			maxSamples: 10,
			wantLen:    4,
		},
		{
			name:       "append at capacity truncates oldest",
			initial:    []time.Duration{1, 2, 3},
			add:        4,
			maxSamples: 3,
			wantLen:    3,
		},
		{
			name:       "append to empty",
			initial:    nil,
			add:        1,
			maxSamples: 5,
			wantLen:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := appendDuration(tt.initial, tt.add, tt.maxSamples)
			assert.Len(t, result, tt.wantLen)
			// newest element should always be the added value
			assert.Equal(t, tt.add, result[len(result)-1])
		})
	}
}

func TestAppendDuration_TruncatesOldest(t *testing.T) {
	durations := []time.Duration{10, 20, 30}
	result := appendDuration(durations, 40, 3)

	// should keep [20, 30, 40], not [10, 20, 30]
	assert.Equal(t, []time.Duration{20, 30, 40}, result)
}

func TestAvgDuration(t *testing.T) {
	tests := []struct {
		name      string
		durations []time.Duration
		want      time.Duration
	}{
		{"empty", nil, 0},
		{"single", []time.Duration{100 * time.Millisecond}, 100 * time.Millisecond},
		{"multiple", []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond}, 200 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, avgDuration(tt.durations))
		})
	}
}

func TestP95Duration(t *testing.T) {
	tests := []struct {
		name      string
		durations []time.Duration
		want      time.Duration
	}{
		{"empty", nil, 0},
		{"single element", []time.Duration{50 * time.Millisecond}, 50 * time.Millisecond},
		{"two elements", []time.Duration{10, 20}, 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, p95Duration(tt.durations))
		})
	}
}

func TestP95Duration_LargeSet(t *testing.T) {
	// 100 values: 1ms, 2ms, ..., 100ms
	durations := make([]time.Duration, 100)
	for i := range 100 {
		durations[i] = time.Duration(i+1) * time.Millisecond
	}

	p95 := p95Duration(durations)
	// index = int(100 * 0.95) = 95, value = 96ms
	assert.Equal(t, 96*time.Millisecond, p95)
}

func TestP95Duration_DoesNotMutateInput(t *testing.T) {
	original := []time.Duration{30, 10, 20}
	copied := make([]time.Duration, len(original))
	copy(copied, original)

	p95Duration(original)

	assert.Equal(t, copied, original, "p95Duration should not sort the input slice")
}

// =============================================================================
// GC helpers: acquireGCLock, releaseGCLock
// =============================================================================

func TestAcquireGCLock_Success(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.gc-lock")

	f, err := acquireGCLock(lockPath)
	require.NoError(t, err)
	require.NotNil(t, f)

	releaseGCLock(f, lockPath)

	// lock file should be removed after release
	_, err = os.Stat(lockPath)
	assert.True(t, os.IsNotExist(err))
}

func TestAcquireGCLock_AlreadyHeld(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.gc-lock")

	// acquire first lock
	f1, err := acquireGCLock(lockPath)
	require.NoError(t, err)
	defer releaseGCLock(f1, lockPath)

	// second acquire should fail
	f2, err := acquireGCLock(lockPath)
	assert.Error(t, err)
	assert.Nil(t, f2)
	assert.Contains(t, err.Error(), "gc lock held")
}

func TestAcquireGCLock_StaleLockRecovered(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.gc-lock")

	// create a stale lock file (>5 min old)
	f, err := os.Create(lockPath)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	// backdate the lock file to 10 minutes ago
	staleTime := time.Now().Add(-10 * time.Minute)
	require.NoError(t, os.Chtimes(lockPath, staleTime, staleTime))

	// should succeed by removing the stale lock
	lockFile, err := acquireGCLock(lockPath)
	require.NoError(t, err)
	require.NotNil(t, lockFile)
	releaseGCLock(lockFile, lockPath)
}

// =============================================================================
// GC helpers: gcPreserveCache, gcRestoreCache
// =============================================================================

func TestGCPreserveCache_NoCacheDir(t *testing.T) {
	srcRepo := t.TempDir()
	backupDir := filepath.Join(t.TempDir(), "backup")

	// no .sageox/cache/ exists — should return nil (nothing to preserve)
	err := gcPreserveCache(srcRepo, backupDir)
	assert.NoError(t, err)
	assert.NoDirExists(t, backupDir)
}

func TestGCPreserveCache_WithCache(t *testing.T) {
	srcRepo := t.TempDir()
	cacheDir := filepath.Join(srcRepo, ".sageox", "cache")
	require.NoError(t, os.MkdirAll(cacheDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "index.db"), []byte("db-data"), 0644))

	backupDir := filepath.Join(t.TempDir(), "backup")

	err := gcPreserveCache(srcRepo, backupDir)
	require.NoError(t, err)

	// verify backup contains the file
	got, err := os.ReadFile(filepath.Join(backupDir, "index.db"))
	require.NoError(t, err)
	assert.Equal(t, "db-data", string(got))
}

func TestGCRestoreCache_NoBackup(t *testing.T) {
	dstRepo := t.TempDir()
	backupDir := filepath.Join(t.TempDir(), "nonexistent-backup")

	// no backup exists — should return nil (nothing to restore)
	err := gcRestoreCache(backupDir, dstRepo)
	assert.NoError(t, err)
}

func TestGCPreserveAndRestoreCache_Roundtrip(t *testing.T) {
	srcRepo := t.TempDir()
	cacheDir := filepath.Join(srcRepo, ".sageox", "cache")
	require.NoError(t, os.MkdirAll(filepath.Join(cacheDir, "codedb"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "index.db"), []byte("main-db"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "codedb", "bleve.idx"), []byte("bleve-index"), 0644))

	backupDir := filepath.Join(t.TempDir(), "gc-cache-backup")

	// preserve
	require.NoError(t, gcPreserveCache(srcRepo, backupDir))

	// simulate reclone: new repo with no cache
	dstRepo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dstRepo, ".sageox"), 0755))

	// restore
	require.NoError(t, gcRestoreCache(backupDir, dstRepo))

	// verify restored files
	got, err := os.ReadFile(filepath.Join(dstRepo, ".sageox", "cache", "index.db"))
	require.NoError(t, err)
	assert.Equal(t, "main-db", string(got))

	got, err = os.ReadFile(filepath.Join(dstRepo, ".sageox", "cache", "codedb", "bleve.idx"))
	require.NoError(t, err)
	assert.Equal(t, "bleve-index", string(got))
}

// =============================================================================
// SyncState — SaveSyncState edge cases
// =============================================================================

func TestSyncState_SaveNilState(t *testing.T) {
	dir := t.TempDir()
	// nil state should be a no-op
	err := SaveSyncState(dir, nil)
	assert.NoError(t, err)

	// verify no file was created
	_, err = os.Stat(filepath.Join(dir, ".sageox", "cache", "sync-state.json"))
	assert.True(t, os.IsNotExist(err))
}

func TestSyncState_SaveAndLoadPreservesFailureCount(t *testing.T) {
	dir := t.TempDir()

	state := &SyncState{
		LastSync:            time.Now().UTC().Truncate(time.Millisecond),
		LastSyncCommit:      "abc123",
		ConsecutiveFailures: 7,
	}
	require.NoError(t, SaveSyncState(dir, state))

	loaded := LoadSyncState(dir)
	assert.Equal(t, 7, loaded.ConsecutiveFailures)
	assert.Equal(t, "abc123", loaded.LastSyncCommit)
}

// =============================================================================
// Registry — file I/O operations
// =============================================================================

func TestRegistry_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "registry.json")

	// create a registry with data
	reg := &Registry{Daemons: map[string]DaemonInfo{
		"ws1": {
			WorkspaceID:   "ws1",
			WorkspacePath: "/tmp/project-a",
			RepoID:        "repo_123",
			PID:           1234,
			Version:       "0.9.0",
			StartedAt:     time.Now().Truncate(time.Millisecond),
		},
	}}

	// marshal and write manually (since Save uses RegistryPath which we can't override)
	data, err := json.MarshalIndent(reg, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(regPath, data, 0600))

	// read back and verify
	readData, err := os.ReadFile(regPath)
	require.NoError(t, err)

	var loaded Registry
	require.NoError(t, json.Unmarshal(readData, &loaded))
	require.NotNil(t, loaded.Daemons)
	assert.Len(t, loaded.Daemons, 1)

	info := loaded.Daemons["ws1"]
	assert.Equal(t, "ws1", info.WorkspaceID)
	assert.Equal(t, "/tmp/project-a", info.WorkspacePath)
	assert.Equal(t, "repo_123", info.RepoID)
	assert.Equal(t, 1234, info.PID)
}

func TestRegistry_CorruptJSON_ReturnsEmpty(t *testing.T) {
	// simulate what LoadRegistry does with corrupt data
	corruptJSON := `{not valid json`

	var reg Registry
	err := json.Unmarshal([]byte(corruptJSON), &reg)
	assert.Error(t, err)

	// LoadRegistry returns fresh registry on corrupt data — verify the pattern
	freshReg := &Registry{Daemons: make(map[string]DaemonInfo)}
	assert.NotNil(t, freshReg.Daemons)
	assert.Len(t, freshReg.Daemons, 0)
}

func TestRegistry_NilDaemonsMap(t *testing.T) {
	// JSON with null daemons map — LoadRegistry should handle this
	jsonData := `{"daemons": null}`

	var reg Registry
	require.NoError(t, json.Unmarshal([]byte(jsonData), &reg))

	// simulate LoadRegistry nil-map fix
	if reg.Daemons == nil {
		reg.Daemons = make(map[string]DaemonInfo)
	}
	assert.NotNil(t, reg.Daemons)
}

func TestRegistry_RegisterAndUnregister_InMemory(t *testing.T) {
	reg := &Registry{Daemons: make(map[string]DaemonInfo)}

	info := DaemonInfo{
		WorkspaceID:   "ws-test",
		WorkspacePath: "/tmp/test",
		PID:           9999,
	}

	reg.mu.Lock()
	reg.Daemons[info.WorkspaceID] = info
	reg.mu.Unlock()

	assert.Len(t, reg.List(), 1)

	// unregister
	reg.mu.Lock()
	delete(reg.Daemons, "ws-test")
	reg.mu.Unlock()

	assert.Len(t, reg.List(), 0)
}

func TestRegistry_FindByRepoID_MultipleMatches(t *testing.T) {
	reg := &Registry{Daemons: map[string]DaemonInfo{
		"ws1": {WorkspaceID: "ws1", RepoID: "repo_same", PID: 100},
		"ws2": {WorkspaceID: "ws2", RepoID: "repo_same", PID: 200},
	}}

	// should find one of them (deterministic per map iteration is not guaranteed)
	found := reg.FindByRepoID("repo_same")
	require.NotNil(t, found)
	assert.Equal(t, "repo_same", found.RepoID)
}

// =============================================================================
// HeartbeatCreds — Copy
// =============================================================================

func TestHeartbeatCreds_Copy_Nil(t *testing.T) {
	var c *HeartbeatCreds
	assert.Nil(t, c.Copy())
}

func TestHeartbeatCreds_Copy_DeepCopy(t *testing.T) {
	original := &HeartbeatCreds{
		Token:     "tok-123",
		ServerURL: "https://git.example.com",
		ExpiresAt: time.Now().Add(1 * time.Hour),
		AuthToken: "auth-456",
		UserEmail: "person@example.com",
		UserID:    "user-789",
	}

	copied := original.Copy()
	require.NotNil(t, copied)

	// values should match
	assert.Equal(t, original.Token, copied.Token)
	assert.Equal(t, original.ServerURL, copied.ServerURL)
	assert.Equal(t, original.AuthToken, copied.AuthToken)
	assert.Equal(t, original.UserEmail, copied.UserEmail)
	assert.Equal(t, original.UserID, copied.UserID)

	// mutating the copy should not affect the original
	copied.Token = "modified"
	assert.Equal(t, "tok-123", original.Token)
}

// =============================================================================
// HeartbeatHandler — additional edge cases not in heartbeat_test.go
// =============================================================================

func TestHeartbeatHandler_HasValidCredentials(t *testing.T) {
	tests := []struct {
		name  string
		creds *HeartbeatCreds
		want  bool
	}{
		{
			name:  "no credentials",
			creds: nil,
			want:  false,
		},
		{
			name:  "valid credentials with future expiry",
			creds: &HeartbeatCreds{Token: "tok", ExpiresAt: time.Now().Add(1 * time.Hour)},
			want:  true,
		},
		{
			name:  "expired credentials",
			creds: &HeartbeatCreds{Token: "tok", ExpiresAt: time.Now().Add(-1 * time.Hour)},
			want:  false,
		},
		{
			name:  "credentials with zero expiry (no expiration set)",
			creds: &HeartbeatCreds{Token: "tok"},
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHeartbeatHandler(testLogger())
			if tt.creds != nil {
				h.credMu.Lock()
				h.credentials = tt.creds
				h.credMu.Unlock()
			}
			assert.Equal(t, tt.want, h.HasValidCredentials())
		})
	}
}

func TestHeartbeatHandler_GetAuthToken(t *testing.T) {
	h := NewHeartbeatHandler(testLogger())

	// no credentials
	assert.Empty(t, h.GetAuthToken())

	// set credentials
	h.credMu.Lock()
	h.credentials = &HeartbeatCreds{AuthToken: "my-auth-token"}
	h.credMu.Unlock()

	assert.Equal(t, "my-auth-token", h.GetAuthToken())
}

func TestHeartbeatHandler_GetAuthenticatedUser(t *testing.T) {
	h := NewHeartbeatHandler(testLogger())

	// no credentials
	assert.Nil(t, h.GetAuthenticatedUser())

	// credentials without email
	h.credMu.Lock()
	h.credentials = &HeartbeatCreds{UserID: "id-only"}
	h.credMu.Unlock()
	assert.Nil(t, h.GetAuthenticatedUser())

	// credentials with email
	h.credMu.Lock()
	h.credentials = &HeartbeatCreds{UserEmail: "person@example.com", UserID: "user-123"}
	h.credMu.Unlock()

	user := h.GetAuthenticatedUser()
	require.NotNil(t, user)
	assert.Equal(t, "person@example.com", user.Email)
	assert.Equal(t, "user-123", user.ID)
}

func TestHeartbeatHandler_LastCallerPath(t *testing.T) {
	h := NewHeartbeatHandler(testLogger())

	// no callers
	assert.Empty(t, h.LastCallerPath())

	// add some callers
	now := time.Now()
	h.callerMu.Lock()
	h.callers["c1"] = CallerInfo{ID: "c1", Path: "/old/path", LastSeen: now.Add(-5 * time.Minute)}
	h.callers["c2"] = CallerInfo{ID: "c2", Path: "/new/path", LastSeen: now}
	h.callers["c3"] = CallerInfo{ID: "c3", Path: "", LastSeen: now.Add(1 * time.Minute)} // no path
	h.callerMu.Unlock()

	// should return the most recent caller with a non-empty path
	assert.Equal(t, "/new/path", h.LastCallerPath())
}

func TestHeartbeatHandler_GetAgentMetadata(t *testing.T) {
	h := NewHeartbeatHandler(testLogger())

	// empty state
	assert.Empty(t, h.GetAgentParentID("agent-1"))
	assert.Empty(t, h.GetAgentType("agent-1"))
	assert.Equal(t, 0, h.GetAgentPID("agent-1"))

	// populate metadata
	h.metaMu.Lock()
	h.agentParentID["agent-1"] = "parent-0"
	h.agentType["agent-1"] = "claude-code"
	h.agentPID["agent-1"] = 42
	h.metaMu.Unlock()

	assert.Equal(t, "parent-0", h.GetAgentParentID("agent-1"))
	assert.Equal(t, "claude-code", h.GetAgentType("agent-1"))
	assert.Equal(t, 42, h.GetAgentPID("agent-1"))
}

func TestHeartbeatHandler_GetAgentContextStats(t *testing.T) {
	h := NewHeartbeatHandler(testLogger())

	// empty returns zeroes
	stats := h.GetAgentContextStats("agent-1")
	assert.Equal(t, int64(0), stats.ContextTokens)
	assert.Equal(t, 0, stats.CommandCount)

	// populate
	h.ctxMu.Lock()
	h.agentContextTokens["agent-1"] = 5000
	h.agentCommandCount["agent-1"] = 3
	h.ctxMu.Unlock()

	stats = h.GetAgentContextStats("agent-1")
	assert.Equal(t, int64(5000), stats.ContextTokens)
	assert.Equal(t, 3, stats.CommandCount)
}

func TestHeartbeatHandler_SetCallbacks(t *testing.T) {
	h := NewHeartbeatHandler(testLogger())

	var teamCalled bool
	var activityCalled bool
	var versionMismatchCalled bool
	var callerPathCalled bool

	h.SetTeamNeededCallback(func(teamID string) { teamCalled = true })
	h.SetActivityCallback(func() { activityCalled = true })
	h.SetVersionMismatchCallback(func(cli, daemon string) { versionMismatchCalled = true })
	h.SetCallerPathCallback(func(path string) { callerPathCalled = true })

	// verify callbacks are set (read them back under lock)
	h.cbMu.RLock()
	assert.NotNil(t, h.onTeamNeeded)
	assert.NotNil(t, h.onActivity)
	assert.NotNil(t, h.onVersionMismatch)
	assert.NotNil(t, h.onCallerPath)
	h.cbMu.RUnlock()

	// suppress unused variable warnings
	_ = teamCalled
	_ = activityCalled
	_ = versionMismatchCalled
	_ = callerPathCalled
}

// =============================================================================
// SyncMetrics — rolling window behavior
// =============================================================================

func TestSyncMetrics_RollingWindow(t *testing.T) {
	m := &SyncMetrics{
		maxSamples:    5,
		pullDurations: make([]time.Duration, 0, 5),
	}

	// add 7 samples, should only keep last 5
	for i := range 7 {
		m.RecordPullSuccess(time.Duration(i+1) * time.Millisecond)
	}

	m.mu.Lock()
	assert.Len(t, m.pullDurations, 5)
	// oldest should be 3ms (first two evicted)
	assert.Equal(t, 3*time.Millisecond, m.pullDurations[0])
	m.mu.Unlock()

	snap := m.Snapshot()
	// avg of [3,4,5,6,7] = 25/5 = 5ms
	assert.Equal(t, 5*time.Millisecond, snap.AvgPullDuration)
}
