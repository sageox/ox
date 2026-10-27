package lfs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/session/contextuse"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionMeta_ContextUseRoundTrip: the finalize-time rollup and the score
// source survive write/read, and legacy meta.json without them reads back
// with nil / "" (no schema bump).
func TestSessionMeta_ContextUseRoundTrip(t *testing.T) {
	rollup := &contextuse.Rollup{
		Surfaced:     3,
		UsedProvable: 1,
		UsedReported: 1,
		ByEvidence:   map[string]int{"read": 1, "cited": 0, "reported": 1, "judged": 0},
		Refs: []contextuse.UseRef{{
			Ref: "2026-01-01T00-00-ryan-Ox1", RefType: "session",
			Evidence: contexttrace.EvidenceRead, Grade: contextuse.GradeProvable,
			Seq: 4, MatchedSurfaced: true,
		}},
		DerivedCategory: "moderate",
		DerivedReason:   "1 provable use of 3 surfaced",
	}
	dir := t.TempDir()
	meta := NewSessionMeta("s", "Ryan", "Ox1", "claude-code", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)).
		SageoxScore(0.6, "moderate", "used a prior session").
		SageoxScoreSource(SageoxScoreSourceSelfReported).
		ContextUse(rollup).
		Build()
	require.NoError(t, WriteSessionMetaOnly(dir, meta))

	got, err := ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Equal(t, "self-reported", got.SageoxScoreSource)
	require.NotNil(t, got.ContextUse)
	assert.Equal(t, *rollup, *got.ContextUse)

	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"context_use"`)
	assert.Contains(t, string(raw), `"sageox_score_source": "self-reported"`)
}

func TestSessionMeta_ContextUseOmitempty(t *testing.T) {
	dir := t.TempDir()
	meta := NewSessionMeta("s", "Ryan", "Ox1", "claude-code", time.Now()).ContextUse(nil).Build()
	require.NoError(t, WriteSessionMetaOnly(dir, meta))

	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "context_use")
	assert.NotContains(t, string(raw), "sageox_score_source")

	// legacy meta.json without the keys reads back nil / ""
	var legacy SessionMeta
	require.NoError(t, json.Unmarshal([]byte(`{"version":"1.0","session_name":"s","files":{}}`), &legacy))
	assert.Nil(t, legacy.ContextUse)
	assert.Equal(t, "", legacy.SageoxScoreSource)
}

// TestValidateDraftShape_RejectsContextUse: a draft is a counters-only
// placeholder; carrying a finalize-derived rollup would make every consumer
// believe the session was already finalized and analyzed.
func TestValidateDraftShape_RejectsContextUse(t *testing.T) {
	dir := t.TempDir()
	err := WriteSessionMetaOnly(dir, &SessionMeta{
		SessionName: "s", CreatedAt: time.Now(), Draft: true,
		ContextUse: &contextuse.Rollup{Surfaced: 1, DerivedCategory: "none"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not carry context_use")
	assert.NoFileExists(t, filepath.Join(dir, "meta.json"))

	// negative control: the same rollup on a finalized meta is accepted
	require.NoError(t, WriteSessionMetaOnly(t.TempDir(), &SessionMeta{
		SessionName: "s", CreatedAt: time.Now(),
		ContextUse: &contextuse.Rollup{Surfaced: 1, DerivedCategory: "none"},
	}))
}
