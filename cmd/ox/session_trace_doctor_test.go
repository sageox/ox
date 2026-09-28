package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/trace/materialize"
	"github.com/sageox/ox/internal/trace/model"
	"github.com/stretchr/testify/require"
)

const doctorTraceID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"

func doctorTraceFixture(t *testing.T, headerCapture bool) (string, string, *model.Capture, []byte) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("OX_XDG_DISABLE", "")
	ledger := t.TempDir()
	cache := filepath.Join(ledger, ".sageox", "cache", "sessions", "2026-09-22T10-00-test-OxTraceDoctor")
	require.NoError(t, os.MkdirAll(cache, 0700))
	capture := &model.Capture{Boundaries: []model.Boundary{{Action: "start", At: time.Now().UTC().Add(-48 * time.Hour), Offsets: map[string]model.Offsets{doctorTraceID: {}}}}}
	meta := session.StoreMeta{Version: "1.0", AgentID: "OxTraceDoctor", AgentType: "claude-code", CreatedAt: capture.Boundaries[0].At, NativeSessions: []lfs.NativeSession{{ID: doctorTraceID}}}
	if headerCapture {
		meta.TraceCapture = capture
	}
	header, err := json.Marshal(map[string]any{"type": "header", "metadata": meta})
	require.NoError(t, err)
	raw := append(header, []byte("\n{\"type\":\"user\",\"content\":\"Recover my recording after a crash\",\"seq\":1}\n")...)
	require.NoError(t, os.WriteFile(filepath.Join(cache, "raw.jsonl"), raw, 0600))
	span := []byte("{\"resourceSpans\":[{\"scopeSpans\":[{\"spans\":[{\"name\":\"before-crash\"}]}]}]}\n")
	spool := filepath.Join(paths.TraceSpoolDir(), doctorTraceID)
	require.NoError(t, os.MkdirAll(spool, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(spool, "traces.jsonl"), span, 0600))
	return ledger, cache, capture, span
}

func TestTraceDoctorStaleMarkerPreservesBoundaries(t *testing.T) {
	ledger, cache, capture, span := doctorTraceFixture(t, false)
	stoppedAt := capture.Boundaries[0].At.Add(time.Minute)
	capture.Boundaries = append(capture.Boundaries, model.Boundary{Action: "stop", At: stoppedAt, Offsets: map[string]model.Offsets{doctorTraceID: {Spans: int64(len(span))}}})
	state := session.RecordingState{AgentID: "OxTraceDoctor", StartedAt: capture.Boundaries[0].At, StoppedAt: &stoppedAt, AgentSessionID: doctorTraceID, NativeSessions: []session.NativeSession{{ID: doctorTraceID}}, Trace: capture}
	data, err := json.Marshal(state)
	require.NoError(t, err)
	marker := filepath.Join(cache, ".recording.json")
	require.NoError(t, os.WriteFile(marker, data, 0600))
	orphans, err := findOrphanedSessionsInDir(filepath.Dir(cache), ledger)
	require.NoError(t, err)
	require.Len(t, orphans, 1)
	require.NoFileExists(t, marker)
	// The header had no capture. Only stamping the stale marker before deletion
	// can preserve this information for the doctor uploader.
	meta, count, err := readCacheSessionMeta(filepath.Join(cache, "raw.jsonl"))
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.NotNil(t, meta.TraceCapture)
	require.Len(t, meta.TraceCapture.Boundaries, 2)
	last := meta.TraceCapture.Boundaries[1]
	require.Equal(t, "stop", last.Action)
	require.Equal(t, model.Offsets{Spans: int64(len(span))}, last.Offsets[doctorTraceID])
	require.Equal(t, meta.TraceCapture, orphans[0].Meta.TraceCapture)
	require.Equal(t, doctorTraceID, meta.NativeSessions[0].ID)
	require.NotNil(t, meta.StoppedAt)
}

// A native Claude session can contain multiple recordings. Reclaiming the old
// marker must not attach bytes emitted by a subsequent recording in that spool.
func TestTraceDoctorReclaimDoesNotCaptureLaterRecording(t *testing.T) {
	for _, durableStop := range []bool{false, true} {
		t.Run(fmt.Sprintf("durable-stop=%t", durableStop), func(t *testing.T) {
			ledger, cache, capture, first := doctorTraceFixture(t, false)
			stoppedAt := capture.Boundaries[0].At.Add(time.Minute)
			state := session.RecordingState{AgentID: "OxTraceDoctor", StartedAt: capture.Boundaries[0].At, StoppedAt: &stoppedAt, NativeSessions: []session.NativeSession{{ID: doctorTraceID}}, Trace: capture}
			if durableStop {
				state.RecordTraceBoundary("stop", stoppedAt)
			}
			data, err := json.Marshal(state)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(cache, ".recording.json"), data, 0600))

			later := session.RecordingState{NativeSessions: state.NativeSessions, Trace: &model.Capture{}}
			later.RecordTraceBoundary("start", time.Now().Add(-time.Minute))
			second := bytes.ReplaceAll(first, []byte("before-crash"), []byte("LATER_RECORDING_PRIVATE"))
			require.NoError(t, os.WriteFile(filepath.Join(paths.TraceSpoolDir(), doctorTraceID, "traces.jsonl"), append(append([]byte(nil), first...), second...), 0600))
			later.RecordTraceBoundary("stop", time.Now())

			orphans, err := findOrphanedSessionsInDir(filepath.Dir(cache), ledger)
			require.NoError(t, err)
			require.Len(t, orphans, 1)
			require.Equal(t, 1, orphans[0].EntryCount, "ordinary recording remains recoverable")
			traceCache, prepared := prepareRetryTraces(ledger, orphans[0])
			if durableStop {
				require.NotNil(t, prepared)
				require.Equal(t, int64(1), prepared.Spans)
				require.Equal(t, []model.ByteRange{{0, int64(len(first))}}, prepared.NativeSessions[0].SpansBytes)
				file, err := os.Open(filepath.Join(traceCache, materialize.SpansFile))
				require.NoError(t, err)
				defer file.Close()
				gz, err := gzip.NewReader(file)
				require.NoError(t, err)
				plain, err := io.ReadAll(gz)
				require.NoError(t, err)
				require.NoError(t, gz.Close())
				require.Contains(t, string(plain), "before-crash")
				require.NotContains(t, string(plain), "LATER_RECORDING_PRIVATE")
			} else {
				require.Nil(t, prepared, "StoppedAt alone cannot prove a stop byte offset")
				require.Empty(t, traceCache)
				require.Len(t, orphans[0].Meta.TraceCapture.Boundaries, 1)
			}
			_, next, err := session.MaterializeTraces(ledger, "2026-09-24T10-00-test-OxTraceLater", later.Trace)
			require.NoError(t, err)
			require.Equal(t, int64(1), next.Spans)
			require.Equal(t, []model.ByteRange{{int64(len(first)), int64(len(first) + len(second))}}, next.NativeSessions[0].SpansBytes)
		})
	}
}

func TestTraceDoctorRawOnlyMissingStopFailsClosed(t *testing.T) {
	ledger, cache, _, span := doctorTraceFixture(t, true)
	rawPath := filepath.Join(cache, "raw.jsonl")
	original, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	meta, count, err := readCacheSessionMeta(rawPath)
	require.NoError(t, err)
	orphan := orphanedSession{SessionName: filepath.Base(cache), CachePath: cache, Meta: meta, EntryCount: count}
	for attempt := 0; attempt < 2; attempt++ {
		traceCache, prepared := prepareRetryTraces(ledger, orphan)
		require.Empty(t, traceCache)
		require.Nil(t, prepared, "a start-only carrier cannot prove paused byte ranges")
		require.Len(t, orphan.Meta.TraceCapture.Boundaries, 1, "retries must not invent a stop")
		after, err := os.ReadFile(rawPath)
		require.NoError(t, err)
		require.Equal(t, original, after)
		require.NoFileExists(t, filepath.Join(cache, materialize.SpansFile))
		require.Equal(t, 1, orphan.EntryCount, "ordinary recording remains recoverable")
		require.NoError(t, os.WriteFile(filepath.Join(paths.TraceSpoolDir(), doctorTraceID, "traces.jsonl"), append(append([]byte(nil), span...), span...), 0600))
	}
}

func TestTraceDoctorStampFailureRetainsFrozenMarker(t *testing.T) {
	ledger, cache, capture, span := doctorTraceFixture(t, false)
	stoppedAt := capture.Boundaries[0].At.Add(time.Minute)
	capture.Boundaries = append(capture.Boundaries, model.Boundary{Action: "stop", At: stoppedAt, Offsets: map[string]model.Offsets{doctorTraceID: {Spans: int64(len(span))}}})
	state := session.RecordingState{AgentID: "OxTraceDoctor", StartedAt: capture.Boundaries[0].At, StoppedAt: &stoppedAt, AgentSessionID: doctorTraceID, NativeSessions: []session.NativeSession{{ID: doctorTraceID}}, Trace: capture}
	data, err := json.Marshal(state)
	require.NoError(t, err)
	marker := filepath.Join(cache, ".recording.json")
	require.NoError(t, os.WriteFile(marker, data, 0600))
	rawPath := filepath.Join(cache, "raw.jsonl")
	backup := filepath.Join(cache, "raw.backup")
	require.NoError(t, os.Rename(rawPath, backup))
	require.NoError(t, os.Mkdir(rawPath, 0700)) // appending footer must fail on every platform
	orphans, err := findOrphanedSessionsInDir(filepath.Dir(cache), ledger)
	require.NoError(t, err)
	require.Empty(t, orphans)
	require.FileExists(t, marker, "failed carrier write must not destroy the sole durable boundary")
	persisted, err := session.ReadRecordingStateFile(cache)
	require.NoError(t, err)
	require.NotNil(t, persisted.Trace)
	require.Len(t, persisted.Trace.Boundaries, 2)
	frozen := persisted.Trace.Boundaries[1]
	require.NotNil(t, persisted.StoppedAt)
	require.Equal(t, "stop", frozen.Action)
	require.Equal(t, model.Offsets{Spans: int64(len(span))}, frozen.Offsets[doctorTraceID])
	// Restore the carrier and receive another export before the next doctor scan.
	require.NoError(t, os.Remove(rawPath))
	require.NoError(t, os.Rename(backup, rawPath))
	require.NoError(t, os.WriteFile(filepath.Join(paths.TraceSpoolDir(), doctorTraceID, "traces.jsonl"), append(append([]byte(nil), span...), span...), 0600))
	orphans, err = findOrphanedSessionsInDir(filepath.Dir(cache), ledger)
	require.NoError(t, err)
	require.Len(t, orphans, 1)
	require.NoFileExists(t, marker)
	require.Len(t, orphans[0].Meta.TraceCapture.Boundaries, 2)
	require.Equal(t, frozen, orphans[0].Meta.TraceCapture.Boundaries[1], "second scan cannot widen a previously persisted stop")
	require.Equal(t, persisted.StoppedAt, orphans[0].Meta.StoppedAt, "retry preserves the original stop timestamp too")
}

// A paused recording can lose its marker after a hook footer-write failure.
// Its start-only raw header must never be expanded to the current spool EOF.
func TestTraceDoctorHookStampFailureOmitsPausedTrace(t *testing.T) {
	for _, door := range []string{"end", "clear"} {
		t.Run(door, func(t *testing.T) {
			projectRoot, _ := setupTestProject(t)
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			t.Setenv("OX_XDG_DISABLE", "")
			t.Setenv("SAGEOX_DAEMON", "false")
			state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{AgentID: "OxTraceFailedHook", AdapterName: "claude-code", Username: "test", AgentSessionID: doctorTraceID})
			require.NoError(t, err)
			state.Trace = &model.Capture{Boundaries: []model.Boundary{{Action: "start", At: state.StartedAt, Offsets: map[string]model.Offsets{doctorTraceID: {}}}}}
			meta := session.StoreMeta{Version: "1.0", AgentID: state.AgentID, AgentType: "claude-code", CreatedAt: state.StartedAt, NativeSessions: state.NativeSessions, TraceCapture: state.Trace}
			header, err := json.Marshal(map[string]any{"type": "header", "metadata": meta})
			require.NoError(t, err)
			rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
			raw := append(header, []byte("\n{\"type\":\"user\",\"content\":\"Ordinary session remains recoverable\",\"seq\":1}\n")...)
			require.NoError(t, os.WriteFile(rawPath, raw, 0600))
			// Only .recording.json knows the pause. The header was written at start.
			state.RecordTraceBoundary("pause", time.Now())
			require.NoError(t, session.SaveRecordingState(projectRoot, state))
			spool := filepath.Join(paths.TraceSpoolDir(), doctorTraceID)
			require.NoError(t, os.MkdirAll(spool, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(spool, "traces.jsonl"), []byte("{\"resourceSpans\":[{\"scopeSpans\":[{\"spans\":[{\"name\":\"PRIVATE_PAUSED_SPAN\"}]}]}]}\n"), 0600))
			backup := filepath.Join(state.SessionPath, "raw.backup")
			require.NoError(t, os.Rename(rawPath, backup))
			require.NoError(t, os.Mkdir(rawPath, 0700))
			ctx := &HookContext{Phase: phaseEnd, AgentType: "claude-code", ProjectRoot: projectRoot, Marker: &SessionMarker{AgentID: state.AgentID}}
			if door == "end" {
				require.NoError(t, handleEnd(ctx))
			} else {
				stopSessionForClear(ctx, state.AgentID)
			}
			require.NoFileExists(t, filepath.Join(state.SessionPath, ".recording.json"))
			require.NoError(t, os.Remove(rawPath))
			require.NoError(t, os.Rename(backup, rawPath))
			recovered, count, err := readCacheSessionMeta(rawPath)
			require.NoError(t, err)
			require.Equal(t, 1, count)
			require.Len(t, recovered.TraceCapture.Boundaries, 1)
			orphan := orphanedSession{SessionName: filepath.Base(state.SessionPath), CachePath: state.SessionPath, Meta: recovered, EntryCount: count}
			cache, trace := prepareRetryTraces(t.TempDir(), orphan)
			require.Empty(t, cache)
			require.Nil(t, trace, "paused bytes cannot be attached from a stale start-only header")
			after, err := os.ReadFile(rawPath)
			require.NoError(t, err)
			require.Equal(t, raw, after, "trace omission must preserve the ordinary session")
		})
	}
}
