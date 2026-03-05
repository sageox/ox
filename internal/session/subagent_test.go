package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubagentRegistry(t *testing.T) {
	tmpDir := t.TempDir()
	sessionPath := filepath.Join(tmpDir, "test-session")
	require.NoError(t, os.MkdirAll(sessionPath, 0755))

	registry, err := NewSubagentRegistry(sessionPath)
	require.NoError(t, err)
	require.NotNil(t, registry)

	t.Run("register subagent", func(t *testing.T) {
		subSession := &SubagentSession{
			SubagentID:      "Oxa1b2",
			ParentSessionID: "Oxp1q2",
			Summary:         "analyzed infrastructure",
			EntryCount:      42,
			DurationMs:      5000,
			Model:           "claude-sonnet-4",
			AgentType:       "claude-code",
		}

		err := registry.Register(subSession)
		require.NoError(t, err)

		// verify file was created
		_, err = os.Stat(filepath.Join(sessionPath, subagentsFilename))
		require.NoError(t, err)
	})

	t.Run("list subagents", func(t *testing.T) {
		sessions, err := registry.List()
		require.NoError(t, err)
		require.Len(t, sessions, 1)

		assert.Equal(t, "Oxa1b2", sessions[0].SubagentID)
		assert.Equal(t, "analyzed infrastructure", sessions[0].Summary)
		assert.Equal(t, 42, sessions[0].EntryCount)
	})

	t.Run("register multiple subagents", func(t *testing.T) {
		subSession2 := &SubagentSession{
			SubagentID: "Oxc3d4",
			Summary:    "reviewed security",
		}

		err := registry.Register(subSession2)
		require.NoError(t, err)

		sessions, err := registry.List()
		require.NoError(t, err)
		require.Len(t, sessions, 2)
	})

	t.Run("count subagents", func(t *testing.T) {
		count, err := registry.Count()
		require.NoError(t, err)
		assert.Equal(t, 2, count)
	})
}

func TestSubagentRegistryEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	sessionPath := filepath.Join(tmpDir, "empty-session")
	require.NoError(t, os.MkdirAll(sessionPath, 0755))

	registry, err := NewSubagentRegistry(sessionPath)
	require.NoError(t, err)

	t.Run("list empty", func(t *testing.T) {
		sessions, err := registry.List()
		require.NoError(t, err)
		assert.Nil(t, sessions)
	})

	t.Run("count empty", func(t *testing.T) {
		count, err := registry.Count()
		require.NoError(t, err)
		assert.Equal(t, 0, count)
	})
}

func TestSubagentRegistryValidation(t *testing.T) {
	t.Run("empty session path", func(t *testing.T) {
		_, err := NewSubagentRegistry("")
		require.Error(t, err)
	})

	t.Run("nil session", func(t *testing.T) {
		tmpDir := t.TempDir()
		registry, _ := NewSubagentRegistry(tmpDir)
		err := registry.Register(nil)
		require.Error(t, err)
	})

	t.Run("empty subagent_id", func(t *testing.T) {
		tmpDir := t.TempDir()
		registry, _ := NewSubagentRegistry(tmpDir)
		err := registry.Register(&SubagentSession{})
		require.Error(t, err)
	})
}

func TestReportSubagentComplete(t *testing.T) {
	tmpDir := t.TempDir()
	sessionPath := filepath.Join(tmpDir, "parent-session")
	require.NoError(t, os.MkdirAll(sessionPath, 0755))

	t.Run("successful report", func(t *testing.T) {
		opts := SubagentCompleteOptions{
			SubagentID:        "Oxs1t2",
			ParentSessionPath: sessionPath,
			Summary:           "completed task",
			EntryCount:        10,
			DurationMs:        2000,
		}

		err := ReportSubagentComplete(opts)
		require.NoError(t, err)

		// verify session was registered
		sessions, err := GetSubagentSessions(sessionPath)
		require.NoError(t, err)
		require.Len(t, sessions, 1)
		assert.Equal(t, "Oxs1t2", sessions[0].SubagentID)
	})

	t.Run("missing subagent_id", func(t *testing.T) {
		opts := SubagentCompleteOptions{
			ParentSessionPath: sessionPath,
		}

		err := ReportSubagentComplete(opts)
		require.Error(t, err)
	})

	t.Run("missing parent path", func(t *testing.T) {
		opts := SubagentCompleteOptions{
			SubagentID: "Oxa1b2",
		}

		err := ReportSubagentComplete(opts)
		require.Error(t, err)
	})

	t.Run("nonexistent parent path", func(t *testing.T) {
		opts := SubagentCompleteOptions{
			SubagentID:        "Oxa1b2",
			ParentSessionPath: "/nonexistent/path",
		}

		err := ReportSubagentComplete(opts)
		require.Error(t, err)
	})
}

func TestSummarizeSubagents(t *testing.T) {
	t.Run("empty list", func(t *testing.T) {
		summary := SummarizeSubagents(nil)
		assert.Equal(t, 0, summary.Count)
		assert.Equal(t, 0, summary.TotalEntries)
		assert.Equal(t, int64(0), summary.TotalDurationMs)
	})

	t.Run("with subagents", func(t *testing.T) {
		sessions := []*SubagentSession{
			{
				SubagentID: "Oxa1b2",
				EntryCount: 10,
				DurationMs: 1000,
			},
			{
				SubagentID: "Oxc3d4",
				EntryCount: 20,
				DurationMs: 2000,
			},
			{
				SubagentID: "Oxe5f6",
				EntryCount: 30,
				DurationMs: 3000,
			},
		}

		summary := SummarizeSubagents(sessions)
		assert.Equal(t, 3, summary.Count)
		assert.Equal(t, 60, summary.TotalEntries)
		assert.Equal(t, int64(6000), summary.TotalDurationMs)
		assert.Len(t, summary.Subagents, 3)
	})
}

func TestSubagentSessionTimestamp(t *testing.T) {
	tmpDir := t.TempDir()
	sessionPath := filepath.Join(tmpDir, "timestamp-test")
	require.NoError(t, os.MkdirAll(sessionPath, 0755))

	registry, err := NewSubagentRegistry(sessionPath)
	require.NoError(t, err)

	// register without timestamp
	before := time.Now()
	err = registry.Register(&SubagentSession{
		SubagentID: "Oxa1b2",
	})
	require.NoError(t, err)
	after := time.Now()

	sessions, err := registry.List()
	require.NoError(t, err)
	require.Len(t, sessions, 1)

	// verify timestamp was auto-set
	assert.False(t, sessions[0].CompletedAt.IsZero())
	assert.True(t, sessions[0].CompletedAt.After(before) || sessions[0].CompletedAt.Equal(before))
	assert.True(t, sessions[0].CompletedAt.Before(after) || sessions[0].CompletedAt.Equal(after))
}

func TestSubagentRegistry_ConcurrentRegistrations(t *testing.T) {
	tmpDir := t.TempDir()
	sessionPath := filepath.Join(tmpDir, "concurrent-session")
	require.NoError(t, os.MkdirAll(sessionPath, 0755))

	registry, err := NewSubagentRegistry(sessionPath)
	require.NoError(t, err)

	const numGoroutines = 20
	var wg sync.WaitGroup

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sub := &SubagentSession{
				SubagentID: fmt.Sprintf("Ox%04d", idx),
				Summary:    fmt.Sprintf("task %d", idx),
				EntryCount: idx,
				DurationMs: int64(idx * 100),
			}
			regErr := registry.Register(sub)
			assert.NoError(t, regErr, "registration %d should succeed", idx)
		}(i)
	}
	wg.Wait()

	// all registrations should be present
	sessions, err := registry.List()
	require.NoError(t, err)
	assert.Len(t, sessions, numGoroutines,
		"all concurrent registrations should be persisted")

	// verify each entry is complete (not truncated/interleaved)
	ids := map[string]bool{}
	for _, s := range sessions {
		assert.NotEmpty(t, s.SubagentID, "each entry should have a subagent_id")
		ids[s.SubagentID] = true
	}
	assert.Len(t, ids, numGoroutines, "all agent IDs should be unique")
}

func TestSubagentRegistry_RegisterAfterParentStop(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot := setupRecordingTest(t, cacheDir)

	// start parent recording
	parentState, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID: "OxPrnt", AdapterName: "claude-code", Username: "testuser",
	})
	require.NoError(t, err)
	parentSessionPath := parentState.SessionPath

	// stop parent recording (clears .recording.json but folder persists)
	_, err = StopRecording(projectRoot, "OxPrnt")
	require.NoError(t, err)

	// parent session folder should still exist
	_, err = os.Stat(parentSessionPath)
	require.NoError(t, err, "parent session folder should persist after stop")

	// subagent reports completion AFTER parent stop — should succeed
	err = ReportSubagentComplete(SubagentCompleteOptions{
		SubagentID:        "OxChld",
		ParentSessionPath: parentSessionPath,
		Summary:           "late completion",
		EntryCount:        5,
	})
	require.NoError(t, err, "subagent should be able to register after parent stop")

	// verify the registration was recorded
	sessions, err := GetSubagentSessions(parentSessionPath)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, "OxChld", sessions[0].SubagentID)
}

func TestSubagentRegistry_DuplicateRegistration(t *testing.T) {
	tmpDir := t.TempDir()
	sessionPath := filepath.Join(tmpDir, "dedup-test")
	require.NoError(t, os.MkdirAll(sessionPath, 0755))

	registry, err := NewSubagentRegistry(sessionPath)
	require.NoError(t, err)

	// register same subagent ID twice
	for i := 0; i < 2; i++ {
		err := registry.Register(&SubagentSession{
			SubagentID: "OxDupe",
			Summary:    fmt.Sprintf("attempt %d", i),
		})
		require.NoError(t, err)
	}

	// JSONL is append-only — both entries should exist (no dedup)
	sessions, err := registry.List()
	require.NoError(t, err)
	assert.Len(t, sessions, 2, "duplicate registrations should both be stored (append-only)")
	assert.Equal(t, "attempt 0", sessions[0].Summary)
	assert.Equal(t, "attempt 1", sessions[1].Summary)
}
