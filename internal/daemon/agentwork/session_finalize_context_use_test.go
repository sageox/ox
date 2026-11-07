package agentwork

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/session/contextuse"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFinalize_ContextUseRollupFromTrace: the daemon door derives
// context_use from the context-trace.jsonl beside the real raw.jsonl and,
// because it is a locked RMW, every field it does not own survives.
func TestFinalize_ContextUseRollupFromTrace(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipGit = true
	handler.skipLFS = true

	name := "2026-05-01T20-04-testuser-OxCTXU"
	ledgerPath := createTestSession(t, name, nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", name)
	seeded := seedRichMeta(t, sessionDir, name)

	w := contexttrace.NewWriter(sessionDir)
	require.NoError(t, w.Append(contexttrace.Event{
		Type: contexttrace.EventProvided, Timestamp: contexttrace.Now(), Source: contexttrace.SourceLocalRecall,
		Seq: 1, Ref: "2026-04-30T10-00-testuser-OxPrior", RefType: "session",
	}))
	require.NoError(t, w.Append(contexttrace.Event{
		Type: contexttrace.EventUsed, Timestamp: contexttrace.Now(), Source: contexttrace.SourceOxCLI,
		Seq: 3, Ref: "2026-04-30T10-00-testuser-OxPrior", RefType: "session",
		Evidence: contexttrace.EvidenceCited, SurfacedSeq: 1,
	}))
	require.NoError(t, w.Append(contexttrace.Event{
		Type: contexttrace.EventUsed, Timestamp: contexttrace.Now(), Source: contexttrace.SourceOxCLI,
		Seq: 4, Ref: "docs/adr/001.md", RefType: "doc", Evidence: contexttrace.EvidenceJudged,
	}))

	item := &WorkItem{
		ID: "ctxu", Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    filepath.Join(sessionDir, "raw.jsonl"),
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}
	require.NoError(t, handler.ProcessResult(item, &RunResult{
		Output: preserveSuccessOutput, Duration: time.Second, ExitCode: 0,
	}))

	got, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assertRichMetaPreserved(t, got, seeded)

	require.NotNil(t, got.ContextUse, "daemon finalize must derive context_use from the trace")
	assert.Equal(t, 1, got.ContextUse.Surfaced)
	assert.Equal(t, 1, got.ContextUse.UsedProvable)
	assert.Equal(t, 1, got.ContextUse.UsedJudged, "judged uses are counted...")
	assert.NotEqual(t, "", got.ContextUse.DerivedCategory)
	assert.Equal(t, "", got.SageoxScoreSource, "no score file → no source")
}

// TestFinalize_NoTraceLeavesContextUseUntouched is the negative control: a
// session with no trace neither gains a rollup nor loses one a CLI door wrote.
func TestFinalize_NoTraceLeavesContextUseUntouched(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipGit = true
	handler.skipLFS = true

	name := "2026-05-01T20-04-testuser-OxCTXN"
	ledgerPath := createTestSession(t, name, nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", name)
	seedRichMeta(t, sessionDir, name)

	run := func(id string) *lfs.SessionMeta {
		item := &WorkItem{
			ID: id, Type: sessionFinalizeType,
			Payload: &SessionFinalizePayload{
				SessionDir: sessionDir,
				RawPath:    filepath.Join(sessionDir, "raw.jsonl"),
				Missing:    requiredArtifacts,
				LedgerPath: ledgerPath,
			},
		}
		require.NoError(t, handler.ProcessResult(item, &RunResult{
			Output: preserveSuccessOutput, Duration: time.Second, ExitCode: 0,
		}))
		got, err := lfs.ReadSessionMeta(sessionDir)
		require.NoError(t, err)
		return got
	}

	assert.Nil(t, run("ctxn1").ContextUse, "no trace → no rollup")

	// a rollup written by a CLI door survives a re-finalize with no trace
	prior := &lfs.SessionMeta{}
	require.NoError(t, lfs.MutateSessionMeta(t.Context(), sessionDir, func(cur *lfs.SessionMeta) (*lfs.SessionMeta, error) {
		cur.ContextUse = &contextuse.Rollup{Surfaced: 7, DerivedCategory: "none"}
		*prior = *cur
		return cur, nil
	}))
	got := run("ctxn2")
	require.NotNil(t, got.ContextUse)
	assert.Equal(t, prior.ContextUse.Surfaced, got.ContextUse.Surfaced)
}
