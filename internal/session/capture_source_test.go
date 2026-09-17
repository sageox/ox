package session

import (
	"testing"

	"github.com/sageox/ox/pkg/sessionprovenance"
	"github.com/stretchr/testify/require"
)

func TestCaptureSourceRangesRespectNativePauseBoundaries(t *testing.T) {
	state := &RecordingState{StartOffset: 10, Lifecycle: []LifecycleEvent{{Action: LifecycleActionPause, Offset: 20, SourceOffsetKnown: true}, {Action: LifecycleActionResume, Offset: 30, SourceOffsetKnown: true}, {Action: LifecycleActionPause, Offset: 40, SourceOffsetKnown: true}}}
	spans, err := captureSourceRanges(state, 50)
	require.NoError(t, err)
	require.Equal(t, []sessionprovenance.Range{{Start: 10, End: 20}, {Start: 30, End: 40}}, spans)
	state.Lifecycle[0].SourceOffsetKnown = false
	_, err = captureSourceRanges(state, 50)
	require.ErrorContains(t, err, "legacy pause")
}
func TestRecordSourceCoverageNeverErasesExclusionsOrDuplicates(t *testing.T) {
	ledger := t.TempDir()
	id := "01a0a62a-f6d2-7a62-9213-fd142782db91"
	require.NoError(t, ExcludeNativeSession(ledger, id, "paused", 20, 30))
	source := &sessionprovenance.Source{Version: 1, Agent: "codex", NativeSessionID: id, Generation: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Ranges: []sessionprovenance.Range{{Start: 0, End: 20}, {Start: 30, End: 50}}}
	oid := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	_, err := RecordSourceCoverage(ledger, "session", oid, source)
	require.NoError(t, err)
	before, err := ReadSourceRecord(ledger, id)
	require.NoError(t, err)
	_, err = RecordSourceCoverage(ledger, "session", oid, source)
	require.NoError(t, err)
	after, err := ReadSourceRecord(ledger, id)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Len(t, after.Coverage, 2)
	require.True(t, after.Excludes(20, 21))
	_, err = RecordSourceCoverage(ledger, "duplicate", oid, source)
	require.ErrorContains(t, err, "conflicting")
	source.Ranges = []sessionprovenance.Range{{Start: 0, End: 50}}
	_, err = RecordSourceCoverage(ledger, "resurrection", oid, source)
	require.ErrorContains(t, err, "excluded")
}
