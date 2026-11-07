package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session/contextuse"
	"github.com/sageox/ox/pkg/sessionsummary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPushSummary_MirrorsContextUseIntoSummaryJSON: the rollup finalize
// stamped on meta.json reaches summary.json, so a summary reader sees the
// same derived evidence without opening meta.json.
func TestPushSummary_MirrorsContextUseIntoSummaryJSON(t *testing.T) {
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	ledger := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = ledger
	require.NoError(t, cmd.Run())
	sessionDir := filepath.Join(ledger, "sessions", "2026-08-31T12-00-ryan-OxMirror")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	rollup := &contextuse.Rollup{
		Surfaced: 2, UsedProvable: 1,
		ByEvidence:      map[string]int{"read": 1, "cited": 0, "reported": 0, "judged": 0},
		DerivedCategory: "moderate",
	}
	meta := lfs.NewSessionMeta("2026-08-31T12-00-ryan-OxMirror", "Ryan", "OxMirror", "claude-code",
		time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)).ContextUse(rollup).Build()
	require.NoError(t, lfs.WriteSessionMetaOnly(sessionDir, meta))

	summaryFile := filepath.Join(t.TempDir(), "summary.json")
	data, _ := json.Marshal(map[string]any{
		"title":            "Session that reused a prior investigation",
		"summary":          "The coworker read a prior session on the same subsystem and shipped the fix without re-deriving it.",
		"outcome":          "success",
		"key_actions":      []string{"read prior session", "shipped fix"},
		"quality_category": "upload",
	})
	require.NoError(t, os.WriteFile(summaryFile, data, 0o644))

	// the later ledger commit/push may fail in a bare temp repo; the mirror
	// lands in summary.json before that step and is what this test owns.
	_ = pushSummaryToLedger(summaryFile, sessionDir)

	written, err := os.ReadFile(filepath.Join(sessionDir, "summary.json"))
	require.NoError(t, err, "summary.json must be written before any ledger git step")
	var got sessionsummary.SummarizeResponse
	require.NoError(t, json.Unmarshal(written, &got))
	require.NotNil(t, got.ContextUse, "context_use must be mirrored from meta.json")
	assert.Equal(t, *rollup, *got.ContextUse)
}
