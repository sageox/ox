package session

import (
	"os"
	"path/filepath"
	"strconv"
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
