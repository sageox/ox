package session

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Phantom header-only session accumulation ---
//
// Eager writes at recording start (ses_/c link, header line, context-trace.jsonl)
// turned an abandoned session from an empty dir into a header-only dir WITH
// content. The marker-based ghost cleanup then structurally could not reclaim
// it: once .recording.json was removed at finalize, the dir was invisible to
// every later pass, and removeEmptyDir could not delete a non-empty dir. Result:
// ~10k header-only orphans on one machine.
//
// CleanupOrphanedStubsInDir is the marker-independent reclaimer. These tests pin
// the exact accumulation case and every non-phantom that must survive.

// headerLine is the sole entry an abandoned recording ever captured.
const headerLine = `{"metadata":{"agent_id":"OxTEST"},"type":"header"}` + "\n"

// makeStub builds a session dir with the given files, then ages the directory
// past orphanStubGracePeriod so the sweep will consider it. Writing files bumps
// the dir mtime to now, so the Chtimes MUST come last.
func makeStub(t *testing.T, base, name string, files map[string]string, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(base, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for fname, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fname), []byte(content), 0o644))
	}
	old := time.Now().Add(-age)
	require.NoError(t, os.Chtimes(dir, old, old))
	return dir
}

// TestCleanupOrphanedStubsInDir_RemovesMarkerlessHeaderOnly is THE regression:
// a header-only recording that lost its .recording.json marker (the steady state
// of every accumulated phantom) must be reclaimed. It fails against the old
// removeEmptyDir path, which could never delete a dir holding raw.jsonl +
// context-trace.jsonl.
func TestCleanupOrphanedStubsInDir_RemovesMarkerlessHeaderOnly(t *testing.T) {
	base := t.TempDir()
	dir := makeStub(t, base, "2026-08-25T19-11-ryan-Ox4kYq", map[string]string{
		"raw.jsonl":           headerLine,
		"context-trace.jsonl": `{"type":"provided"}` + "\n",
	}, 2*orphanStubGracePeriod)

	result := CleanupOrphanedStubsInDir(base)

	assert.Equal(t, 1, result.Removed, "a marker-less header-only stub is a phantom and must be reclaimed")
	assert.NoDirExists(t, dir, "the whole stub dir (header + context-trace) must be removed")
}

// TestCleanupOrphanedStubsInDir_RemovesMissingRawAndDeadMarker covers the other
// two phantom shapes: no raw.jsonl at all, and a header-only dir that still
// carries a dead-PID marker.
func TestCleanupOrphanedStubsInDir_RemovesMissingRawAndDeadMarker(t *testing.T) {
	base := t.TempDir()
	noRaw := makeStub(t, base, "2026-08-25T19-11-ryan-OxNONE", map[string]string{
		"context-trace.jsonl": `{"type":"provided"}` + "\n",
	}, 2*orphanStubGracePeriod)
	// 0x7FFFFFFF is above any real pid_max — guaranteed dead.
	deadMarker := makeStub(t, base, "2026-08-25T19-11-ryan-OxDEAD", map[string]string{
		"raw.jsonl":   headerLine,
		recordingFile: `{"parent_pid":2147483647}`,
	}, 2*orphanStubGracePeriod)

	result := CleanupOrphanedStubsInDir(base)

	assert.Equal(t, 2, result.Removed)
	assert.NoDirExists(t, noRaw)
	assert.NoDirExists(t, deadMarker)
}

// TestCleanupOrphanedStubsInDir_KeepsNonPhantoms is the negative-control suite:
// every dir here would survive the feature being removed, so it proves the sweep
// is discriminating rather than a blanket rm.
func TestCleanupOrphanedStubsInDir_KeepsNonPhantoms(t *testing.T) {
	base := t.TempDir()

	// substantive: a real transcript (header + user turn) — a recoverable orphan.
	substantive := makeStub(t, base, "2026-08-25T19-11-ryan-OxREAL", map[string]string{
		"raw.jsonl": headerLine + `{"type":"user"}` + "\n",
	}, 2*orphanStubGracePeriod)

	// pointer stub: transcript lives in the content store.
	pointer := filepath.Join(base, "2026-08-25T19-11-ryan-OxPTR")
	require.NoError(t, os.MkdirAll(pointer, 0o755))
	writePointerStub(t, filepath.Join(pointer, "raw.jsonl"))
	old := time.Now().Add(-2 * orphanStubGracePeriod)
	require.NoError(t, os.Chtimes(pointer, old, old))

	// finalized/draft: meta.json present.
	finalized := makeStub(t, base, "2026-08-25T19-11-ryan-OxMETA", map[string]string{
		"raw.jsonl": headerLine,
		"meta.json": `{"session_id":"ses_x"}`,
	}, 2*orphanStubGracePeriod)

	// live recording: header-only but a marker with THIS (alive) process's PID.
	live := makeStub(t, base, "2026-08-25T19-11-ryan-OxLIVE", map[string]string{
		"raw.jsonl": headerLine,
	}, 2*orphanStubGracePeriod)
	require.NoError(t, os.WriteFile(filepath.Join(live, recordingFile),
		[]byte(`{"parent_pid":`+strconv.Itoa(os.Getpid())+`}`), 0o644))

	// too young: header-only but inside the grace period.
	young := makeStub(t, base, "2026-08-25T19-11-ryan-OxYNG", map[string]string{
		"raw.jsonl": headerLine,
	}, orphanStubGracePeriod/2)

	result := CleanupOrphanedStubsInDir(base)

	assert.Equal(t, 0, result.Removed, "no non-phantom may be reclaimed")
	assert.DirExists(t, substantive, "a recoverable transcript must survive")
	assert.DirExists(t, pointer, "a content-store pointer must survive")
	assert.DirExists(t, finalized, "a finalized/draft session must survive")
	assert.DirExists(t, live, "a live recording must survive")
	assert.DirExists(t, young, "a just-primed session inside the grace period must survive")
}

// TestClassifyRawFile_UnreadablePresentFileIsNotMissing guards the deletion
// path: a raw.jsonl that exists but cannot be opened (permission / transient
// I/O) must NOT classify as RawMissing (which the sweep would reap). Only a
// genuinely absent file is RawMissing; anything present-but-unreadable fails
// safe to RawSubstantive.
func TestClassifyRawFile_UnreadablePresentFileIsNotMissing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "raw.jsonl")
	require.NoError(t, os.WriteFile(p, []byte(headerLine+`{"type":"user"}`+"\n"), 0o644))
	require.NoError(t, os.Chmod(p, 0o000))
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })

	// Running as root defeats mode bits — skip rather than assert a false pass.
	if f, err := os.Open(p); err == nil {
		_ = f.Close()
		t.Skip("cannot make file unreadable (running as root?)")
	}

	assert.Equal(t, RawSubstantive, ClassifyRawFile(p),
		"a present-but-unreadable raw.jsonl must never classify as RawMissing (deletable)")
}

// TestCleanupOrphanedStubsInDir_KeepsMalformedMarker guards the P1: a
// .recording.json that exists but cannot be parsed (an incomplete/concurrent
// write — exactly what an ACTIVE recording looks like mid-flight) must fail
// closed, not fall through to os.RemoveAll and destroy a live session.
func TestCleanupOrphanedStubsInDir_KeepsMalformedMarker(t *testing.T) {
	base := t.TempDir()
	dir := makeStub(t, base, "2026-08-25T19-11-ryan-OxMalf", map[string]string{
		"raw.jsonl":   headerLine,
		recordingFile: `{"parent_pid": not-valid-json`,
	}, 2*orphanStubGracePeriod)

	result := CleanupOrphanedStubsInDir(base)

	assert.Equal(t, 0, result.Removed, "an unparseable marker must fail closed (keep)")
	assert.DirExists(t, dir, "a marker present but unreadable may be a live session mid-write")
}

// TestCleanupOrphanedStubsInDir_KeepsOversizedContent guards the destructive
// failure mode: a user turn larger than the 256 KiB scan buffer must NOT be
// misread as a header-only phantom and deleted. Classification fails safe to
// RawSubstantive on an incomplete scan, so the dir survives.
func TestCleanupOrphanedStubsInDir_KeepsOversizedContent(t *testing.T) {
	base := t.TempDir()
	bigUserLine := `{"type":"user","content":"` + strings.Repeat("x", 300*1024) + `"}` + "\n"
	dir := makeStub(t, base, "2026-08-25T19-11-ryan-OxBig1", map[string]string{
		"raw.jsonl": headerLine + bigUserLine,
	}, 2*orphanStubGracePeriod)

	result := CleanupOrphanedStubsInDir(base)

	assert.Equal(t, 0, result.Removed, "a session with an oversized real entry must never be reaped")
	assert.DirExists(t, dir, "content too large for the scan buffer must fail safe to keep")
}

// TestClassifyAndUserTurn_FailSafeOnOversizedLine pins the classification
// contract directly: a line past the 256 KiB buffer stops the scan with an
// error, and both classifiers must fail safe rather than report "empty".
func TestClassifyAndUserTurn_FailSafeOnOversizedLine(t *testing.T) {
	dir := t.TempDir()

	// Header only, but the header line itself exceeds the buffer.
	bigHeader := filepath.Join(dir, "bighdr.jsonl")
	require.NoError(t, os.WriteFile(bigHeader,
		[]byte(`{"type":"header","x":"`+strings.Repeat("y", 300*1024)+`"}`+"\n"), 0o644))
	assert.Equal(t, RawSubstantive, ClassifyRawFile(bigHeader),
		"an unreadable (oversized) line must not classify as RawHeaderOnly")

	// Header + an oversized user entry.
	bigUser := filepath.Join(dir, "biguser.jsonl")
	require.NoError(t, os.WriteFile(bigUser,
		[]byte(headerLine+`{"type":"user","content":"`+strings.Repeat("z", 300*1024)+`"}`+"\n"), 0o644))
	assert.True(t, HasUserTurn(bigUser),
		"an oversized user entry must fail safe to 'has user turn', not suppress the session")
}

// TestCleanupOrphanedStubsInDir_ErrorPaths covers non-existent and non-directory
// inputs: the sweep must return a zero result without panicking, never treating
// an unreadable location as work done.
func TestCleanupOrphanedStubsInDir_ErrorPaths(t *testing.T) {
	t.Run("missing directory", func(t *testing.T) {
		result := CleanupOrphanedStubsInDir(filepath.Join(t.TempDir(), "does-not-exist"))
		assert.Equal(t, 0, result.Removed)
		assert.Empty(t, result.Names)
	})

	t.Run("path is a file, not a directory", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "a-file")
		require.NoError(t, os.WriteFile(f, []byte("x"), 0o644))
		assert.NotPanics(t, func() {
			result := CleanupOrphanedStubsInDir(f)
			assert.Equal(t, 0, result.Removed)
		})
	})

	t.Run("non-directory entries in the sweep dir are skipped", func(t *testing.T) {
		base := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(base, "stray.txt"), []byte("x"), 0o644))
		result := CleanupOrphanedStubsInDir(base)
		assert.Equal(t, 0, result.Removed)
		assert.FileExists(t, filepath.Join(base, "stray.txt"), "a stray file must be left untouched")
	})
}
