package main

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeSessionSources(t *testing.T) {
	now := time.Now()

	mkSession := func(name string, age time.Duration) session.SessionInfo {
		return session.SessionInfo{
			SessionName: name,
			CreatedAt:   now.Add(-age),
		}
	}

	t.Run("both empty", func(t *testing.T) {
		result := mergeSessionSources(nil, nil)
		assert.Empty(t, result)
	})

	t.Run("primary only", func(t *testing.T) {
		primary := []session.SessionInfo{mkSession("a", 1*time.Hour), mkSession("b", 2*time.Hour)}
		result := mergeSessionSources(primary, nil)
		require.Len(t, result, 2)
		assert.Equal(t, "a", result[0].SessionName, "newest first")
	})

	t.Run("additional only", func(t *testing.T) {
		additional := []session.SessionInfo{mkSession("x", 3*time.Hour)}
		result := mergeSessionSources(nil, additional)
		require.Len(t, result, 1)
		assert.Equal(t, "x", result[0].SessionName)
	})

	t.Run("dedup primary wins", func(t *testing.T) {
		primary := []session.SessionInfo{mkSession("dup", 1*time.Hour)}
		additional := []session.SessionInfo{mkSession("dup", 2*time.Hour)}
		result := mergeSessionSources(primary, additional)
		require.Len(t, result, 1, "duplicate should be deduped")
		// primary's timestamp wins
		assert.Equal(t, now.Add(-1*time.Hour).Unix(), result[0].CreatedAt.Unix())
	})

	t.Run("legacy empty-name dedup by filepath", func(t *testing.T) {
		primary := []session.SessionInfo{
			{SessionName: "", FilePath: "/a/raw.jsonl", CreatedAt: now.Add(-1 * time.Hour)},
			{SessionName: "", FilePath: "/b/raw.jsonl", CreatedAt: now.Add(-2 * time.Hour)},
		}
		additional := []session.SessionInfo{
			{SessionName: "", FilePath: "/a/raw.jsonl", CreatedAt: now.Add(-3 * time.Hour)},
		}
		result := mergeSessionSources(primary, additional)
		require.Len(t, result, 2, "distinct legacy sessions should not be collapsed")
	})

	t.Run("disjoint merge sorted", func(t *testing.T) {
		primary := []session.SessionInfo{mkSession("old", 5*time.Hour)}
		additional := []session.SessionInfo{mkSession("new", 1*time.Hour), mkSession("mid", 3*time.Hour)}
		result := mergeSessionSources(primary, additional)
		require.Len(t, result, 3)
		assert.Equal(t, "new", result[0].SessionName)
		assert.Equal(t, "mid", result[1].SessionName)
		assert.Equal(t, "old", result[2].SessionName)
	})
}
