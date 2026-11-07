package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeContextTraceFixture writes a trace with one surfaced session and one
// provable (read) use of it next to where raw.jsonl would live.
func writeContextTraceFixture(t *testing.T, dir string) {
	t.Helper()
	w := contexttrace.NewWriter(dir)
	require.NoError(t, w.Append(contexttrace.Event{
		Type: contexttrace.EventProvided, Timestamp: contexttrace.Now(), Source: contexttrace.SourceLocalRecall,
		Seq: 2, Ref: "2026-01-01T00-00-ryan-OxPrior", RefType: "session",
	}))
	require.NoError(t, w.Append(contexttrace.Event{
		Type: contexttrace.EventUsed, Timestamp: contexttrace.Now(), Source: contexttrace.SourceOxCLI,
		Seq: 5, Ref: "2026-01-01T00-00-ryan-OxPrior", RefType: "session",
		Evidence: contexttrace.EvidenceRead, SurfacedSeq: 2,
	}))
}

// TestWriteInitialSessionMeta_ContextUse: the CLI finalize door stamps
// context_use from the local trace and omits it when no trace was recorded.
// Failure prevented: a session that demonstrably used team context finalizes
// with no evidence of it, so recap / attribution fall back to self-report.
func TestWriteInitialSessionMeta_ContextUse(t *testing.T) {
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	newBuilder := func(agentID string) *lfs.SessionMetaBuilder {
		return lfs.NewSessionMeta("2026-08-31T12-00-ryan-"+agentID, "Ryan", agentID, "claude-code",
			time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC))
	}

	t.Run("trace present writes context_use", func(t *testing.T) {
		agentID := "OxCtxUse1"
		recordingDir := t.TempDir()
		writeContextTraceFixture(t, recordingDir)
		sessionDir := t.TempDir()

		meta, err := writeInitialSessionMeta(sessionDir, agentID, newBuilder(agentID), contextUseSource{
			TraceDir: recordingDir, ProducedCommits: 1,
		})
		require.NoError(t, err)
		require.NotNil(t, meta.ContextUse)
		assert.Equal(t, 1, meta.ContextUse.Surfaced)
		assert.Equal(t, 1, meta.ContextUse.UsedProvable)
		require.Len(t, meta.ContextUse.Refs, 1)
		assert.True(t, meta.ContextUse.Refs[0].MatchedSurfaced)

		persisted, err := lfs.ReadSessionMeta(sessionDir)
		require.NoError(t, err)
		require.NotNil(t, persisted.ContextUse, "rollup must reach meta.json on disk")
		assert.Equal(t, *meta.ContextUse, *persisted.ContextUse)
		assert.Equal(t, "", persisted.SageoxScoreSource, "no score file consumed → no source")
	})

	t.Run("no trace omits context_use", func(t *testing.T) {
		agentID := "OxCtxUse2"
		sessionDir := t.TempDir()
		meta, err := writeInitialSessionMeta(sessionDir, agentID, newBuilder(agentID), contextUseSource{
			TraceDir: t.TempDir(),
		})
		require.NoError(t, err)
		assert.Nil(t, meta.ContextUse)
		raw, err := os.ReadFile(filepath.Join(sessionDir, "meta.json"))
		require.NoError(t, err)
		assert.NotContains(t, string(raw), "context_use")
	})

	t.Run("consumed score file stamps self-reported source", func(t *testing.T) {
		agentID := "OxCtxUse3"
		require.NoError(t, session.WriteSageoxScore(agentID, 0.75, "used a prior session"))
		sessionDir := t.TempDir()
		meta, err := writeInitialSessionMeta(sessionDir, agentID, newBuilder(agentID), contextUseSource{})
		require.NoError(t, err)
		assert.Equal(t, lfs.SageoxScoreSourceSelfReported, meta.SageoxScoreSource)

		// retry after the score carrier was consumed preserves the source too
		retried, err := writeInitialSessionMeta(sessionDir, agentID, newBuilder(agentID), contextUseSource{})
		require.NoError(t, err)
		require.NotNil(t, retried.SageoxScore)
		assert.Equal(t, lfs.SageoxScoreSourceSelfReported, retried.SageoxScoreSource)
	})
}
