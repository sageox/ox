package session

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/trace/materialize"
	"github.com/sageox/ox/internal/trace/model"
	"github.com/stretchr/testify/require"
)

func TestMaterializeTracesUsesDurableRecordingWindows(t *testing.T) {
	traceBoundaryEnvironment(t, true)
	ledger := t.TempDir()
	const name = "2026-09-24T10-00-test-OxTraceWindows"
	store, err := NewStore(ledger)
	require.NoError(t, err)
	cache := store.CacheSessionPath(name)
	spool := filepath.Join(paths.TraceSpoolDir(), traceBoundaryID)
	require.NoError(t, os.MkdirAll(spool, 0700))
	var exported []byte
	appendSpan := func(name string) {
		t.Helper()
		exported = append(exported, []byte(fmt.Sprintf(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"name":%q,"attributes":[{"key":"user.email","value":{"stringValue":"PRIVATE_EMAIL"}}]}]}]}]}`+"\n", name))...)
		require.NoError(t, os.WriteFile(filepath.Join(spool, "traces.jsonl"), exported, 0600))
	}
	appendSpan("BEFORE_RECORDING")
	state := &RecordingState{SessionPath: cache, AgentType: "claude-code", AgentSessionID: traceBoundaryID, StartedAt: time.Now().UTC(), NativeSessions: []NativeSession{{ID: strings.ToUpper(traceBoundaryID)}, {ID: traceBoundaryID}, {ID: "invalid"}}}
	state.initializeTraceCapture()
	require.Len(t, state.Trace.Boundaries[0].Offsets, 1, "native IDs must be normalized, validated, and deduplicated")
	appendSpan("kept-before-pause")
	state.RecordTraceBoundary("pause", time.Now())
	appendSpan("PAUSED_RECORDING")
	state.RecordTraceBoundary("resume", time.Now())
	appendSpan("kept-after-pause")
	state.RecordTraceBoundary("stop", time.Now())
	appendSpan("AFTER_RECORDING")

	// Finalization checks the raw carrier against independent local capture
	// state, even after the live recording marker is gone.
	require.NoError(t, os.MkdirAll(cache, 0700))
	raw := filepath.Join(cache, "raw.jsonl")
	require.NoError(t, os.WriteFile(raw, []byte("{\"type\":\"header\",\"metadata\":{\"agent_type\":\"claude-code\"}}\n{\"type\":\"user\",\"content\":\"Preserve my recording\"}\n"), 0600))
	require.NoError(t, StampRawCarrier(raw, CarrierStamp{TraceCapture: state.Trace}))
	stored, err := ReadSessionFromPath(raw)
	require.NoError(t, err)
	gotCache, meta, err := MaterializeTraces(ledger, cache, stored.Meta.TraceCapture)
	require.NoError(t, err)
	require.Equal(t, cache, gotCache)
	require.Equal(t, int64(2), meta.Spans)
	require.Equal(t, int64(2), meta.Scrubbed["user.email"])
	require.Positive(t, meta.PausedBytesSkipped)
	require.Positive(t, meta.LateBytes)
	file, err := os.Open(filepath.Join(cache, materialize.SpansFile))
	require.NoError(t, err)
	defer file.Close()
	gz, err := gzip.NewReader(file)
	require.NoError(t, err)
	plain, err := io.ReadAll(gz)
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	require.Contains(t, string(plain), "kept-before-pause")
	require.Contains(t, string(plain), "kept-after-pause")
	for _, excluded := range []string{"BEFORE_RECORDING", "PAUSED_RECORDING", "AFTER_RECORDING", "PRIVATE_EMAIL"} {
		require.NotContains(t, string(plain), excluded)
	}
	require.NoDirExists(t, filepath.Join(ledger, "sessions", name), "trace materialization must never populate the tracked session path")
}

func TestMaterializeTracesRejectsUnprovenOrCorruptCapture(t *testing.T) {
	for _, missingStop := range []bool{true, false} {
		t.Run(fmt.Sprintf("missing-stop=%t", missingStop), func(t *testing.T) {
			traceBoundaryEnvironment(t, true)
			ledger := t.TempDir()
			const name = "2026-09-24T10-00-test-OxTraceFailure"
			store, err := NewStore(ledger)
			require.NoError(t, err)
			cache := store.CacheSessionPath(name)
			state := &RecordingState{SessionPath: cache, AgentType: "claude-code", AgentSessionID: traceBoundaryID, StartedAt: time.Now()}
			state.initializeTraceCapture()
			spool := filepath.Join(paths.TraceSpoolDir(), traceBoundaryID)
			require.NoError(t, os.MkdirAll(spool, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(spool, "traces.jsonl"), []byte("corrupt export\n"), 0600))
			if !missingStop {
				state.RecordTraceBoundary("stop", time.Now())
			}
			require.NoError(t, os.MkdirAll(cache, 0700))
			raw := []byte("{\"type\":\"user\",\"content\":\"Ordinary recording remains recoverable\"}\n")
			require.NoError(t, os.WriteFile(filepath.Join(cache, "raw.jsonl"), raw, 0600))
			_, meta, err := MaterializeTraces(ledger, cache, state.Trace)
			require.Error(t, err)
			require.Nil(t, meta)
			for _, filename := range []string{materialize.SpansFile, materialize.EventsFile} {
				require.NoFileExists(t, filepath.Join(cache, filename))
			}
			after, err := os.ReadFile(filepath.Join(cache, "raw.jsonl"))
			require.NoError(t, err)
			require.Equal(t, raw, after)
		})
	}
}

func TestMaterializeTracesOptOutAndInvalidDestination(t *testing.T) {
	traceBoundaryEnvironment(t, true)
	ledger := filepath.Join(t.TempDir(), "absent")
	cache, meta, err := MaterializeTraces(ledger, "recording", nil)
	require.NoError(t, err)
	require.Empty(t, cache)
	require.Nil(t, meta)
	require.NoDirExists(t, ledger, "opted-out recordings must not create trace storage")
	for _, name := range []string{"", ".", "..", "../escape", string(filepath.Separator)} {
		t.Run(name, func(t *testing.T) {
			cache, meta, err := MaterializeTraces(ledger, name, &model.Capture{})
			require.Error(t, err)
			require.Empty(t, cache)
			require.Nil(t, meta)
			require.NoDirExists(t, ledger)
		})
	}
	dir := t.TempDir()
	require.NoError(t, writeLocalTraceCapture(dir, &model.Capture{}))
	cache, meta, err = MaterializeTraces("", dir, &model.Capture{})
	require.ErrorIs(t, err, ErrEmptyPath)
	require.Empty(t, cache)
	require.Nil(t, meta)
}

func TestMaterializeTracesRejectsForgedRecordingWindows(t *testing.T) {
	traceBoundaryEnvironment(t, true)
	ledger := t.TempDir()
	store, err := NewStore(ledger)
	require.NoError(t, err)
	cache := store.CacheSessionPath("local-recording")
	state := &RecordingState{SessionPath: cache, AgentType: "claude-code", AgentSessionID: traceBoundaryID, StartedAt: time.Now()}
	state.initializeTraceCapture()
	state.RecordTraceBoundary("pause", time.Now())
	spool := filepath.Join(paths.TraceSpoolDir(), traceBoundaryID)
	require.NoError(t, os.MkdirAll(spool, 0700))
	private := []byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"name":"PRIVATE_PAUSED_TRACE"}]}]}]}` + "\n")
	require.NoError(t, os.WriteFile(filepath.Join(spool, "traces.jsonl"), private, 0600))
	state.RecordTraceBoundary("resume", time.Now())
	state.RecordTraceBoundary("stop", time.Now())
	require.Len(t, state.Trace.Boundaries, 4)
	forged := &model.Capture{Boundaries: []model.Boundary{state.Trace.Boundaries[0], state.Trace.Boundaries[3]}}

	for _, tc := range []struct {
		name, dir string
		capture   *model.Capture
	}{
		{"shared-transcript", filepath.Join(ledger, "sessions", "teammate-recording"), forged},
		{"same-name-in-ledger", filepath.Join(ledger, "sessions", "local-recording"), state.Trace},
		{"forged-cache-transcript", cache, forged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, meta, err := MaterializeTraces(ledger, tc.dir, tc.capture)
			require.Error(t, err)
			require.Contains(t, err.Error(), "local", "reject authorization before attempting to read the valid spool range")
			require.Empty(t, got)
			require.Nil(t, meta)
			for _, filename := range []string{materialize.SpansFile, materialize.EventsFile} {
				require.NoFileExists(t, filepath.Join(store.CacheSessionPath(filepath.Base(tc.dir)), filename))
			}
		})
	}
	// Neither an untrusted marker nor an ordinary boundary update may promote
	// a forged history or introduce a different native session into local state.
	untrusted := &RecordingState{SessionPath: filepath.Join(ledger, "sessions", "teammate-recording"), Trace: forged}
	untrusted.RecordTraceBoundary("stop", time.Now())
	require.Nil(t, untrusted.Trace)
	_, err = readLocalTraceCapture(untrusted.SessionPath)
	require.Error(t, err, "recovery cannot create local authorization")
	state.NativeSessions = append(state.NativeSessions, NativeSession{ID: traceBoundarySecondID})
	state.Trace = forged
	state.RecordTraceBoundary("stop", time.Now())
	require.Nil(t, state.Trace)
	local, err := readLocalTraceCapture(cache)
	require.NoError(t, err)
	require.Len(t, local.Boundaries, 4)
	_, meta, err := MaterializeTraces(ledger, cache, local)
	require.NoError(t, err)
	require.Zero(t, meta.Spans)
	require.EqualValues(t, len(private), meta.PausedBytesSkipped)
}

func TestTraceBoundariesUnavailableConsentFailsClosed(t *testing.T) {
	traceBoundaryEnvironment(t, true)
	require.NoError(t, os.WriteFile(os.Getenv("OX_USER_CONFIG"), []byte("trace: [invalid"), 0600))
	state := &RecordingState{AgentType: "claude-code", AgentSessionID: traceBoundaryID, StartedAt: time.Now()}
	state.initializeTraceCapture()
	require.Nil(t, state.Trace, "invalid consent configuration must not enable capture")
	state.RecordTraceBoundary("stop", time.Now())
	require.Nil(t, state.Trace, "a boundary cannot opt a recording into capture")
	var absent *RecordingState
	require.NotPanics(t, func() { absent.RecordTraceBoundary("stop", time.Now()) })
	require.NoDirExists(t, paths.TraceSpoolDir())
}
