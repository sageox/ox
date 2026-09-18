package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/pkg/sessionprovenance"
	"github.com/stretchr/testify/require"
)

const sourceTestID = "01a0a62a-f6d2-7a62-9213-fd142782db93"

// TestReadSourceRecordReturnsNilForMissingRecord prevents callers (capture,
// import, exclusion) from treating "no record has ever been written for this
// native session" as an error instead of the normal first-encounter case.
func TestReadSourceRecordReturnsNilForMissingRecord(t *testing.T) {
	record, err := ReadSourceRecord(t.TempDir(), sourceTestID)
	require.NoError(t, err)
	require.Nil(t, record)
}

// TestReadSourceRecordRejectsCorruptOrInvalidPayloads guards against a torn
// write or a downgrade-incompatible record silently being treated as "no
// coverage yet," which would let a capture re-record bytes a newer server
// already excluded.
func TestReadSourceRecordRejectsCorruptOrInvalidPayloads(t *testing.T) {
	rel, err := sessionprovenance.Path(sourceTestID)
	require.NoError(t, err)
	cases := map[string]string{
		"invalid_json":      "{not json",
		"wrong_version":     `{"version":2,"agent":"codex","native_session_id":"` + sourceTestID + `"}`,
		"wrong_agent":       `{"version":1,"agent":"claude-code","native_session_id":"` + sourceTestID + `"}`,
		"identity_mismatch": `{"version":1,"agent":"codex","native_session_id":"01a0a62a-f6d2-7a62-9213-fd142782db94"}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			ledger := t.TempDir()
			path := filepath.Join(ledger, rel)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
			require.NoError(t, os.WriteFile(path, []byte(content), 0600))
			_, err := ReadSourceRecord(ledger, sourceTestID)
			require.Error(t, err)
		})
	}
}

// TestWriteSourceRecordRejectsInvalidRecord ensures a record that fails its
// own Validate() (e.g. wrong version, malformed coverage) can never reach
// disk, where a reader would trust it uncritically.
func TestWriteSourceRecordRejectsInvalidRecord(t *testing.T) {
	err := WriteSourceRecord(t.TempDir(), &sessionprovenance.Record{Version: 2, Agent: "codex", NativeSessionID: sourceTestID})
	require.Error(t, err)
}

// TestWriteSourceRecordSurfacesDirectoryCreationFailure prevents a silent
// no-op write: if the source-sources directory can't be created (e.g. a
// path component collides with a regular file), the caller must see an
// error rather than believe the record was persisted.
func TestWriteSourceRecordSurfacesDirectoryCreationFailure(t *testing.T) {
	ledger := t.TempDir()
	// "data" is a plain file, so MkdirAll("data/session-sources/codex") must fail.
	require.NoError(t, os.WriteFile(filepath.Join(ledger, "data"), []byte("x"), 0600))
	err := WriteSourceRecord(ledger, &sessionprovenance.Record{Version: 1, Agent: "codex", NativeSessionID: sourceTestID})
	require.Error(t, err)
}

// TestWriteSourceRecordSurfacesRenameFailure prevents a failed atomic
// replace (e.g. the destination path is unexpectedly a directory) from
// being silently swallowed while its temp file is cleaned up — the caller
// must see the error rather than believe the record was persisted.
func TestWriteSourceRecordSurfacesRenameFailure(t *testing.T) {
	ledger := t.TempDir()
	rel, err := sessionprovenance.Path(sourceTestID)
	require.NoError(t, err)
	target := filepath.Join(ledger, rel)
	require.NoError(t, os.MkdirAll(target, 0700)) // occupy the destination path with a directory
	err = WriteSourceRecord(ledger, &sessionprovenance.Record{Version: 1, Agent: "codex", NativeSessionID: sourceTestID})
	require.Error(t, err)
}

// TestWriteSourceRecordThenReadRoundTrips is the baseline happy path every
// other WriteSourceRecord/ReadSourceRecord test builds on.
func TestWriteSourceRecordThenReadRoundTrips(t *testing.T) {
	ledger := t.TempDir()
	record := &sessionprovenance.Record{Version: 1, Agent: "codex", NativeSessionID: sourceTestID, Generation: "gen-1"}
	require.NoError(t, WriteSourceRecord(ledger, record))
	got, err := ReadSourceRecord(ledger, sourceTestID)
	require.NoError(t, err)
	require.Equal(t, "gen-1", got.Generation)
}

// TestExcludeNativeSessionCreatesRecordWhenNoneExists prevents deletion
// intent from being lost when a native session has never been captured
// before — the exclusion must still land so a LATER capture attempt is
// blocked from ever recording the excluded bytes.
func TestExcludeNativeSessionCreatesRecordWhenNoneExists(t *testing.T) {
	ledger := t.TempDir()
	require.NoError(t, ExcludeNativeSession(ledger, sourceTestID, "deleted", 0, 100))
	record, err := ReadSourceRecord(ledger, sourceTestID)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.True(t, record.Excludes(10, 20))
}

// TestExcludeNativeSessionDeduplicatesIdenticalExclusion prevents an
// idempotent retry (e.g. a repeated CLI invocation after a crash) from
// piling up duplicate exclusion entries, which would bloat the record file
// forever on every retry of the same deletion.
func TestExcludeNativeSessionDeduplicatesIdenticalExclusion(t *testing.T) {
	ledger := t.TempDir()
	require.NoError(t, ExcludeNativeSession(ledger, sourceTestID, "deleted", 0, 100))
	require.NoError(t, ExcludeNativeSession(ledger, sourceTestID, "deleted", 0, 100))
	record, err := ReadSourceRecord(ledger, sourceTestID)
	require.NoError(t, err)
	require.Len(t, record.Exclusions, 1)
}

// TestExcludeNativeSessionAppendsDistinctExclusions proves multiple distinct
// exclusions (different reasons or ranges) accumulate instead of the second
// call clobbering the first — losing an earlier exclusion would let those
// bytes be captured after all.
func TestExcludeNativeSessionAppendsDistinctExclusions(t *testing.T) {
	ledger := t.TempDir()
	require.NoError(t, ExcludeNativeSession(ledger, sourceTestID, "paused", 0, 50))
	require.NoError(t, ExcludeNativeSession(ledger, sourceTestID, "deleted", 100, -1))
	record, err := ReadSourceRecord(ledger, sourceTestID)
	require.NoError(t, err)
	require.Len(t, record.Exclusions, 2)
	require.True(t, record.Excludes(200, 300), "end=-1 must exclude all future bytes")
}
