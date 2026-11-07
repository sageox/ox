package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/consultscan"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/session/contextuse"
)

// liveUseFixture is a recording that surfaced two discussions, re-opened the
// first and named the second in its reply.
func liveUseFixture(t *testing.T) (string, []contexttrace.Event, func() []contexttrace.Event) {
	t.Helper()
	root, state := liveBriefEnv(t)
	traceConversations(t, state, 1, [2]string{liveCnvA, liveTitleA}, [2]string{liveCnvB, liveTitleB})
	writeLiveRaw(t, state,
		liveUser("hi"),
		liveBash("ox conversation show "+liveCnvA, "…"),
		liveAssistant("per "+liveTitleB),
	)
	events, err := contexttrace.ReadEvents(filepath.Join(state.SessionPath, contexttrace.FileName))
	require.NoError(t, err)
	return root, events, func() []contexttrace.Event { return withLiveUses(root, state, events) }
}

func mustRecordingState(t *testing.T, root string) *session.RecordingState {
	t.Helper()
	state, err := session.LoadRecordingStateForAgent(root, usageAgentID)
	require.NoError(t, err)
	require.NotNil(t, state)
	return state
}

func usedOnly(events []contexttrace.Event) []contexttrace.Event {
	var out []contexttrace.Event
	for _, ev := range events {
		if ev.Type == contexttrace.EventUsed {
			ev.Timestamp = ""
			out = append(out, ev)
		}
	}
	return out
}

// TestWithLiveUses_MatchesFinalize: the brief's in-memory evidence is exactly
// what finalize later writes, so a live brief and the finished session agree.
func TestWithLiveUses_MatchesFinalize(t *testing.T) {
	root, _, live := liveUseFixture(t)
	got := usedOnly(live())
	require.Len(t, got, 2)

	state := mustRecordingState(t, root)
	_, err := consultscan.TagSessionReads(filepath.Join(state.SessionPath, "raw.jsonl"), state.SessionPath, consultRoots(root))
	require.NoError(t, err)
	written, err := contexttrace.ReadEvents(filepath.Join(state.SessionPath, contexttrace.FileName))
	require.NoError(t, err)
	assert.Equal(t, usedOnly(written), got)

	// once finalize-style events are in the trace, the brief adds none twice
	after := withLiveUses(root, state, written)
	assert.Len(t, usedOnly(after), 2)
}

// TestWithLiveUses_CachedUseDroppedWhenNoLongerSurfaced: a cached use whose
// item the trace no longer surfaces is not reported.
func TestWithLiveUses_CachedUseDroppedWhenNoLongerSurfaced(t *testing.T) {
	root, events, live := liveUseFixture(t)
	require.Len(t, usedOnly(live()), 2)

	var onlyA []contexttrace.Event
	for _, ev := range events {
		if ev.Ref != liveCnvB {
			onlyA = append(onlyA, ev)
		}
	}
	got := usedOnly(withLiveUses(root, mustRecordingState(t, root), onlyA))
	require.Len(t, got, 1)
	assert.Equal(t, liveCnvA, got[0].Ref)
}

// TestLoadBriefUsesCache_Rescans lists every cache state that must scan from
// zero rather than resume.
func TestLoadBriefUsesCache_Rescans(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "raw.jsonl")
	require.NoError(t, os.WriteFile(raw, []byte(`{"type":"user","content":"hi"}`+"\n"), 0o644))
	info, err := os.Stat(raw)
	require.NoError(t, err)
	sum, err := rawTailSHA(raw, info.Size())
	require.NoError(t, err)
	surfaced := []contextuse.SurfacedRef{{Ref: liveCnvA, RefType: "discussion", Seq: 0}}
	good := briefUsesCache{Session: "s1", Offset: info.Size(), NextIndex: 1, SurfacedCount: 1, TailSHA: sum}

	tests := []struct {
		name     string
		cache    *briefUsesCache
		raw      string // replaces raw.jsonl when set
		surfaced []contextuse.SurfacedRef
		resumes  bool
	}{
		{name: "valid cache resumes", cache: &good, surfaced: surfaced, resumes: true},
		{name: "no cache file", surfaced: surfaced},
		{name: "another session", cache: &briefUsesCache{Session: "s0", Offset: good.Offset, NextIndex: 1, SurfacedCount: 1, TailSHA: sum}, surfaced: surfaced},
		{name: "surfaced set shrank", cache: &good, surfaced: nil},
		{name: "new item inside scanned range", cache: &good, surfaced: append(surfaced, contextuse.SurfacedRef{Ref: liveCnvB, RefType: "discussion", Seq: 0})},
		{name: "new item after scanned range resumes", cache: &good, surfaced: append(surfaced, contextuse.SurfacedRef{Ref: liveCnvB, RefType: "discussion", Seq: 1}), resumes: true},
		{name: "raw rewritten", cache: &good, raw: `{"type":"user","content":"ho"}` + "\n", surfaced: surfaced},
		{name: "raw truncated", cache: &good, raw: "\n", surfaced: surfaced},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := t.TempDir()
			rawPath := filepath.Join(sub, "raw.jsonl")
			body, _ := os.ReadFile(raw)
			if tt.raw != "" {
				body = []byte(tt.raw)
			}
			require.NoError(t, os.WriteFile(rawPath, body, 0o644))
			cachePath := filepath.Join(sub, "cache.json")
			if tt.cache != nil {
				require.NoError(t, fileutil.AtomicWriteJSON(cachePath, tt.cache, 0o644))
			}
			got := loadBriefUsesCache(cachePath, "s1", rawPath, tt.surfaced)
			if tt.resumes {
				assert.Equal(t, good.Offset, got.Offset)
			} else {
				assert.Zero(t, got.Offset)
				assert.Zero(t, got.NextIndex)
			}
		})
	}
}

// TestBriefUsesCachePath: the cache lives in the ledger's local cache, and
// only when the ledger is on disk and the agent id is a plain name.
func TestBriefUsesCachePath(t *testing.T) {
	ledger := t.TempDir()
	assert.Equal(t, filepath.Join(ledger, ".sageox", "cache", "brief-uses", "OxAb12.json"), briefUsesCachePath(ledger, "OxAb12"))
	assert.Empty(t, briefUsesCachePath("", "OxAb12"), "no ledger, no cache")
	assert.Empty(t, briefUsesCachePath(filepath.Join(ledger, "missing"), "OxAb12"), "never creates a ledger dir")
	assert.Empty(t, briefUsesCachePath(ledger, "../x"), "agent id cannot leave the cache dir")
	assert.Empty(t, briefUsesCachePath(ledger, ""))
}
