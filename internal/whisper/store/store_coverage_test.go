package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpen_CreatesDeepDirectory(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "deep", "nested", "dir", "whisper.db")
	s, err := Open(dbPath)
	require.NoError(t, err)
	defer s.Close()

	_, err = os.Stat(filepath.Dir(dbPath))
	assert.NoError(t, err, "directory should be auto-created")
}

func TestOpen_RecoveryFromGarbageFile(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "corrupt.db")

	// write garbage to simulate corruption
	require.NoError(t, os.WriteFile(dbPath, []byte("not a sqlite database at all!!!"), 0644))

	// Open should auto-recover by deleting and recreating
	s, err := Open(dbPath)
	require.NoError(t, err)
	defer s.Close()

	// should be functional after recovery
	err = s.Add(makeEntry("recovery-test", "topic", ImportanceNormal, time.Now()))
	assert.NoError(t, err)
}

func TestAdd_WithMetadataMap(t *testing.T) {
	t.Parallel()
	s := mustOpen(t)

	entry := WhisperEntry{
		ID:         "meta-coverage",
		Scope:      "ledger",
		Type:       WhisperStructural,
		Source:     "test",
		Topic:      "metadata-cov",
		Content:    "entry with metadata",
		Importance: ImportanceNormal,
		CreatedAt:  time.Now(),
		Metadata: map[string]string{
			"file_path": "/src/main.go",
			"line":      "42",
		},
	}

	require.NoError(t, s.Add(entry))

	got, err := s.GetWhispers("agent-meta-cov", AttentionAll, nil)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "/src/main.go", got[0].Metadata["file_path"])
	assert.Equal(t, "42", got[0].Metadata["line"])
}

func TestAdd_WithAllOptionalFields(t *testing.T) {
	t.Parallel()
	s := mustOpen(t)

	entry := WhisperEntry{
		ID:            "all-fields-cov",
		Scope:         "team",
		Type:          WhisperTimeBased,
		Source:        "test-source",
		Topic:         "all-fields",
		Content:       "content",
		Importance:    ImportanceCritical,
		CreatedAt:     time.Now(),
		AgentID:       "agent-fill",
		PrincipalID:   "person-fill",
		PrincipalType: "human",
		TeamID:        "team-fill",
	}

	require.NoError(t, s.Add(entry))

	got, err := s.GetWhispers("reader-cov", AttentionAll, nil)
	require.NoError(t, err)
	require.Len(t, got, 1)

	assert.Equal(t, "agent-fill", got[0].AgentID)
	assert.Equal(t, "person-fill", got[0].PrincipalID)
	assert.Equal(t, "human", got[0].PrincipalType)
	assert.Equal(t, "team-fill", got[0].TeamID)
	assert.Equal(t, WhisperTimeBased, got[0].Type)
}

func TestGetWhispers_AttentionLevels(t *testing.T) {
	t.Parallel()
	s := mustOpen(t)
	now := time.Now()

	require.NoError(t, s.Add(
		makeEntry("af-crit", "topic", ImportanceCritical, now.Add(-3*time.Second)),
		makeEntry("af-norm", "topic", ImportanceNormal, now.Add(-2*time.Second)),
		makeEntry("af-ambi", "topic", ImportanceAmbient, now.Add(-1*time.Second)),
	))

	tests := []struct {
		name      string
		attention Attention
		wantCount int
	}{
		{"focused_only_critical", AttentionFocused, 1},
		{"normal_crit_plus_normal", AttentionNormal, 2},
		{"all_levels", AttentionAll, 3},
		{"unknown_defaults_normal", Attention("bogus"), 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agentID := "attention-cov-" + tt.name
			got, err := s.GetWhispers(agentID, tt.attention, nil)
			require.NoError(t, err)
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestGetWhispers_TopicFilter(t *testing.T) {
	t.Parallel()
	s := mustOpen(t)
	now := time.Now()

	require.NoError(t, s.Add(
		makeEntry("tf1", "lint", ImportanceNormal, now.Add(-3*time.Second)),
		makeEntry("tf2", "build", ImportanceNormal, now.Add(-2*time.Second)),
		makeEntry("tf3", "discovery", ImportanceNormal, now.Add(-1*time.Second)),
	))

	got, err := s.GetWhispers("topic-filter-cov", AttentionAll, []string{"lint", "build"})
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestRelayed_DifferentScopes(t *testing.T) {
	t.Parallel()
	s := mustOpen(t)

	require.NoError(t, s.MarkRelayed("murmur-scope", "ledger"))

	// should be relayed for ledger scope
	relayed, err := s.IsRelayed("murmur-scope", "ledger")
	require.NoError(t, err)
	assert.True(t, relayed)

	// should NOT be relayed for team scope
	relayed, err = s.IsRelayed("murmur-scope", "team")
	require.NoError(t, err)
	assert.False(t, relayed)
}

func TestRelayed_DoubleMarkIdempotent(t *testing.T) {
	t.Parallel()
	s := mustOpen(t)

	require.NoError(t, s.MarkRelayed("idem-murmur", "ledger"))
	require.NoError(t, s.MarkRelayed("idem-murmur", "ledger"))

	relayed, err := s.IsRelayed("idem-murmur", "ledger")
	require.NoError(t, err)
	assert.True(t, relayed)
}

func TestRemoveCursor_ThenFullRead(t *testing.T) {
	t.Parallel()
	s := mustOpen(t)
	now := time.Now()

	require.NoError(t, s.Add(makeEntry("rc-cov1", "topic", ImportanceNormal, now)))

	// read to set cursor
	_, err := s.GetWhispers("agent-rc-cov", AttentionAll, nil)
	require.NoError(t, err)

	// remove cursor
	require.NoError(t, s.RemoveCursor("agent-rc-cov"))

	// add new entry
	require.NoError(t, s.Add(makeEntry("rc-cov2", "topic", ImportanceNormal, now.Add(time.Second))))

	// should see all entries (cursor was removed)
	got, err := s.GetWhispers("agent-rc-cov", AttentionAll, nil)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestPrune_WithRetention(t *testing.T) {
	t.Parallel()
	s := mustOpen(t)

	oldTime := time.Now().Add(-48 * time.Hour)
	newTime := time.Now()

	require.NoError(t, s.Add(
		makeEntry("prune-old", "topic", ImportanceNormal, oldTime),
		makeEntry("prune-new", "topic", ImportanceNormal, newTime),
	))

	result, err := s.Prune(24 * time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(1), result.WhispersDeleted)

	got, err := s.GetWhispers("prune-cov-agent", AttentionAll, nil)
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, "prune-new", got[0].ID)
}

func TestDBPath_Coverage(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "test-dbpath.db")
	s, err := Open(dbPath)
	require.NoError(t, err)
	defer s.Close()

	assert.Equal(t, dbPath, s.DBPath())
}

func TestCheckIntegrity_Coverage(t *testing.T) {
	t.Parallel()
	s := mustOpen(t)
	assert.NoError(t, s.CheckIntegrity())
}

func TestImportanceForAttention_Coverage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		attention Attention
		wantLen   int
	}{
		{AttentionFocused, 1},
		{AttentionNormal, 2},
		{AttentionAll, 3},
		{Attention("xyz"), 2},
	}

	for _, tt := range tests {
		t.Run(string(tt.attention), func(t *testing.T) {
			t.Parallel()
			got := importanceForAttention(tt.attention)
			assert.Len(t, got, tt.wantLen)
		})
	}
}

func TestEnforceMaxSize_UnderLimitNoOp(t *testing.T) {
	t.Parallel()
	s := mustOpen(t)
	err := s.EnforceMaxSize(100 * 1024 * 1024)
	assert.NoError(t, err)
}

func TestEnforceMaxSize_MissingDB(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "removed.db")
	s, err := Open(dbPath)
	require.NoError(t, err)
	defer s.Close()

	require.NoError(t, os.Remove(dbPath))
	err = s.EnforceMaxSize(1024)
	assert.NoError(t, err, "should handle missing file gracefully")
}

func TestCursorAdvancement_Coverage(t *testing.T) {
	t.Parallel()
	s := mustOpen(t)
	now := time.Now()

	require.NoError(t, s.Add(makeEntry("ca-batch1", "topic", ImportanceNormal, now)))

	got, err := s.GetWhispers("cursor-adv-cov", AttentionAll, nil)
	require.NoError(t, err)
	assert.Len(t, got, 1)

	require.NoError(t, s.Add(makeEntry("ca-batch2", "topic", ImportanceNormal, now.Add(2*time.Second))))

	got, err = s.GetWhispers("cursor-adv-cov", AttentionAll, nil)
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, "ca-batch2", got[0].ID)
}

func TestGetWhispers_EmptyStore(t *testing.T) {
	t.Parallel()
	s := mustOpen(t)

	got, err := s.GetWhispers("empty-store-cov", AttentionAll, nil)
	require.NoError(t, err)
	assert.NotNil(t, got, "should return empty slice, not nil")
	assert.Len(t, got, 0)
}

func TestNilIfEmpty_Coverage(t *testing.T) {
	t.Parallel()
	assert.Nil(t, nilIfEmpty(""))
	assert.NotNil(t, nilIfEmpty("test"))
	assert.Equal(t, "test", *nilIfEmpty("test"))
}

func TestOpen_InvalidPath(t *testing.T) {
	t.Parallel()
	_, err := Open("/dev/null/impossible/whisper.db")
	assert.Error(t, err)
}

func TestClose_Idempotent(t *testing.T) {
	t.Parallel()
	s := mustOpen(t)
	assert.NoError(t, s.Close())
	assert.NoError(t, s.Close())
	assert.NoError(t, s.Close())
}

func TestIsRelayed_NotMarked(t *testing.T) {
	t.Parallel()
	s := mustOpen(t)

	relayed, err := s.IsRelayed("nonexistent-murmur", "ledger")
	require.NoError(t, err)
	assert.False(t, relayed)
}

func TestCheckIntegrity_AfterOperations(t *testing.T) {
	t.Parallel()
	s := mustOpen(t)
	now := time.Now()

	require.NoError(t, s.Add(makeEntry("integrity-1", "topic", ImportanceNormal, now)))
	require.NoError(t, s.MarkRelayed("integrity-1", "ledger"))
	_, err := s.GetWhispers("integrity-agent", AttentionAll, nil)
	require.NoError(t, err)

	assert.NoError(t, s.CheckIntegrity())
}

func TestPrune_EmptyStore(t *testing.T) {
	t.Parallel()
	s := mustOpen(t)

	result, err := s.Prune(24 * time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(0), result.WhispersDeleted)
}
