package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteInitialSessionMetaConsumesScoreOnlyAfterDurableWrite(t *testing.T) {
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	agentID := "OxScorePreserve"
	require.NoError(t, session.WriteSageoxScore(agentID, 0.75, "caught a lifecycle defect"))

	newBuilder := func() *lfs.SessionMetaBuilder {
		return lfs.NewSessionMeta(
			"2026-08-31T12-00-ryan-OxScore",
			"Ryan",
			agentID,
			"codex",
			time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
		)
	}

	t.Run("failed metadata write preserves score for retry", func(t *testing.T) {
		sessionDir := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(sessionDir, "meta.json"), 0o755))

		meta, err := writeInitialSessionMeta(sessionDir, agentID, newBuilder())

		require.Error(t, err)
		assert.Nil(t, meta)
		score, readErr := session.ReadSageoxScore(agentID)
		require.NoError(t, readErr)
		require.NotNil(t, score, "failed write must leave retry input intact")
		assert.Equal(t, 0.75, score.Score)
	})

	t.Run("successful metadata write consumes score", func(t *testing.T) {
		sessionDir := t.TempDir()

		meta, err := writeInitialSessionMeta(sessionDir, agentID, newBuilder())

		require.NoError(t, err)
		require.NotNil(t, meta)
		require.NotNil(t, meta.SageoxScore)
		assert.Equal(t, 0.75, *meta.SageoxScore)
		assert.Equal(t, "critical", meta.SageoxScoreCategory)
		assert.Equal(t, "caught a lifecycle defect", meta.SageoxScoreReason)
		persisted, readErr := lfs.ReadSessionMeta(sessionDir)
		require.NoError(t, readErr)
		require.NotNil(t, persisted.SageoxScore)
		assert.Equal(t, *meta.SageoxScore, *persisted.SageoxScore)

		score, readErr := session.ReadSageoxScore(agentID)
		require.NoError(t, readErr)
		assert.Nil(t, score, "durable metadata owns the score after a successful write")
	})

	t.Run("retry without carrier preserves persisted score", func(t *testing.T) {
		retryAgentID := agentID + "Retry"
		require.NoError(t, session.WriteSageoxScore(retryAgentID, 0.8, "survive post-meta upload failure"))
		sessionDir := t.TempDir()
		firstBuilder := lfs.NewSessionMeta(
			"2026-08-31T12-00-ryan-OxScoreRetry",
			"Ryan",
			retryAgentID,
			"codex",
			time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
		)

		first, err := writeInitialSessionMeta(sessionDir, retryAgentID, firstBuilder)
		require.NoError(t, err)
		require.NotNil(t, first.SageoxScore)
		carrier, readErr := session.ReadSageoxScore(retryAgentID)
		require.NoError(t, readErr)
		require.Nil(t, carrier, "first durable metadata write consumes the carrier")

		retryBuilder := lfs.NewSessionMeta(
			first.SessionName,
			first.Username,
			retryAgentID,
			first.AgentType,
			first.CreatedAt,
		)
		retried, err := writeInitialSessionMeta(sessionDir, retryAgentID, retryBuilder)

		require.NoError(t, err)
		require.NotNil(t, retried.SageoxScore)
		assert.Equal(t, 0.8, *retried.SageoxScore)
		assert.Equal(t, "critical", retried.SageoxScoreCategory)
		assert.Equal(t, "survive post-meta upload failure", retried.SageoxScoreReason)
	})

	t.Run("unreadable score blocks metadata and preserves carrier", func(t *testing.T) {
		require.NoError(t, session.WriteSageoxScore(agentID, 0.5, "must survive corruption"))
		scorePath := filepath.Join(paths.CacheDir(), "scores", agentID+".json")
		require.NoError(t, os.WriteFile(scorePath, []byte("{corrupt"), 0o600))
		sessionDir := t.TempDir()

		meta, err := writeInitialSessionMeta(sessionDir, agentID, newBuilder())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "read SageOx score")
		assert.Nil(t, meta)
		_, statErr := os.Stat(scorePath)
		require.NoError(t, statErr, "unreadable retry carrier must remain")
		_, metaErr := os.Stat(filepath.Join(sessionDir, "meta.json"))
		assert.ErrorIs(t, metaErr, os.ErrNotExist)
	})
}
