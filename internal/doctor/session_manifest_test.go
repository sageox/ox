package doctor

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestManifestGapsForSession_NoGapsWhenAllPresent: a meta.Files manifest
// with both LFS and git artifacts produces no gaps when everything is on
// disk in the expected form.
func TestManifestGapsForSession_NoGapsWhenAllPresent(t *testing.T) {
	dir := t.TempDir()

	// Storage=lfs entry: write a real LFS pointer file.
	pointer := "version https://git-lfs.github.com/spec/v1\noid sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789\nsize 1234\n"
	mustWrite(t, filepath.Join(dir, "raw.jsonl"), []byte(pointer))

	// Storage=git entry: write a real JSON blob.
	mustWrite(t, filepath.Join(dir, "summary.json"), []byte(`{"title":"x"}`))

	meta := &lfs.SessionMeta{
		Files: map[string]lfs.FileRef{
			"raw.jsonl":    {Storage: lfs.StorageLFS, OID: "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", Size: 1234},
			"summary.json": lfs.NewGitFileRef(13),
		},
	}

	gaps := manifestGapsForSession(dir, "sess1", meta)
	if len(gaps) != 0 {
		t.Fatalf("expected no gaps, got %v", gaps)
	}
}

// TestManifestGapsForSession_FlagsMissingFile: an entry in the manifest
// with no on-disk file is reported.
func TestManifestGapsForSession_FlagsMissingFile(t *testing.T) {
	dir := t.TempDir()
	meta := &lfs.SessionMeta{
		Files: map[string]lfs.FileRef{
			"summary.json": lfs.NewGitFileRef(42),
		},
	}
	gaps := manifestGapsForSession(dir, "sess2", meta)
	if len(gaps) != 1 {
		t.Fatalf("want 1 gap, got %d", len(gaps))
	}
	if gaps[0].Filename != "summary.json" || gaps[0].Reason != "file missing" {
		t.Errorf("unexpected gap: %+v", gaps[0])
	}
}

// TestManifestGapsForSession_LegacyEntryTreatedAsLFS: a FileRef with no
// Storage field (the pre-refactor format) is treated as Storage=lfs and
// passes when an LFS pointer is on disk.
func TestManifestGapsForSession_LegacyEntryTreatedAsLFS(t *testing.T) {
	dir := t.TempDir()
	pointer := "version https://git-lfs.github.com/spec/v1\noid sha256:0000000000000000000000000000000000000000000000000000000000000001\nsize 50\n"
	mustWrite(t, filepath.Join(dir, "raw.jsonl"), []byte(pointer))

	meta := &lfs.SessionMeta{
		Files: map[string]lfs.FileRef{
			// no Storage field — legacy
			"raw.jsonl": {OID: "sha256:0000000000000000000000000000000000000000000000000000000000000001", Size: 50},
		},
	}
	gaps := manifestGapsForSession(dir, "sess3", meta)
	if len(gaps) != 0 {
		t.Fatalf("legacy LFS entry should not trigger a gap; got %v", gaps)
	}
}

// TestManifestGapsForSession_GitEntryWithPointerOnDiskFlags: if a
// Storage=git entry has been clobbered by an LFS pointer (regression we
// explicitly guard against in WritePointerFiles), the check flags it.
func TestManifestGapsForSession_GitEntryWithPointerOnDiskFlags(t *testing.T) {
	dir := t.TempDir()
	pointer := "version https://git-lfs.github.com/spec/v1\noid sha256:0000000000000000000000000000000000000000000000000000000000000002\nsize 99\n"
	mustWrite(t, filepath.Join(dir, "summary.json"), []byte(pointer))

	meta := &lfs.SessionMeta{
		Files: map[string]lfs.FileRef{
			"summary.json": lfs.NewGitFileRef(99),
		},
	}
	gaps := manifestGapsForSession(dir, "sess4", meta)
	if len(gaps) != 1 {
		t.Fatalf("want 1 gap, got %d (%+v)", len(gaps), gaps)
	}
	if gaps[0].Reason != "expected git blob, found LFS pointer" {
		t.Errorf("unexpected gap reason: %q", gaps[0].Reason)
	}
}

func mustWrite(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestFindManifestGapsInLedger_E2E covers the ledger-walk + aggregation
// path: a sessions/ tree containing a clean session, a session with a
// missing summary.json, and a sessions-without-meta directory (must be
// skipped silently). Failure prevented: a regression in directory
// iteration, per-session error swallowing, or scanned-count accounting
// would slip past the unit-level manifestGapsForSession tests.
func TestFindManifestGapsInLedger_E2E(t *testing.T) {
	sessionsDir := t.TempDir()

	// session A: clean — manifest matches disk
	mkSession(t, sessionsDir, "A",
		map[string]lfs.FileRef{
			"raw.jsonl":    {Storage: lfs.StorageLFS, OID: "sha256:aaa", Size: 100},
			"summary.json": lfs.NewGitFileRef(20),
		},
		map[string][]byte{
			"raw.jsonl":    []byte(pointerLiteral("sha256:aaa", 100)),
			"summary.json": []byte(`{"title":"ok"}`),
		},
	)

	// session B: meta lists summary.json but file missing
	mkSession(t, sessionsDir, "B",
		map[string]lfs.FileRef{
			"raw.jsonl":    {Storage: lfs.StorageLFS, OID: "sha256:bbb", Size: 50},
			"summary.json": lfs.NewGitFileRef(99),
		},
		map[string][]byte{
			"raw.jsonl": []byte(pointerLiteral("sha256:bbb", 50)),
			// summary.json intentionally absent
		},
	)

	// session C: no meta.json — must be silently skipped, NOT counted
	cDir := filepath.Join(sessionsDir, "C")
	require.NoError(t, os.MkdirAll(cDir, 0755))
	mustWrite(t, filepath.Join(cDir, "stray.txt"), []byte("ignored"))

	gaps, scanned, err := findManifestGapsInLedger(sessionsDir)
	require.NoError(t, err)

	assert.Equal(t, 2, scanned, "session C (no meta.json) must NOT count toward scanned")
	require.Len(t, gaps, 1, "exactly one gap expected (B's missing summary.json)")
	assert.Equal(t, "B", gaps[0].SessionName)
	assert.Equal(t, "summary.json", gaps[0].Filename)
	assert.Equal(t, "file missing", gaps[0].Reason)
}

// TestFindManifestGapsInLedger_NoSessionsDir: a missing sessions/ dir
// must surface as os.IsNotExist so Run() reports the no-sessions-yet
// happy state, not a hard failure.
func TestFindManifestGapsInLedger_NoSessionsDir(t *testing.T) {
	_, _, err := findManifestGapsInLedger(filepath.Join(t.TempDir(), "missing"))
	assert.True(t, os.IsNotExist(err))
}

func mkSession(t *testing.T, sessionsDir, name string, files map[string]lfs.FileRef, contents map[string][]byte) {
	t.Helper()
	sd := filepath.Join(sessionsDir, name)
	require.NoError(t, os.MkdirAll(sd, 0755))
	meta := &lfs.SessionMeta{
		Version: "1.0", SessionName: name, AgentID: "Ox" + name, AgentType: "claude-code",
		Files: files,
	}
	require.NoError(t, lfs.WriteSessionMetaOnly(sd, meta))
	for fname, content := range contents {
		mustWrite(t, filepath.Join(sd, fname), content)
	}
}

// pointerLiteral returns a syntactically-valid LFS pointer file body.
// We hand-format rather than calling lfs.FormatPointer to keep the test
// independent of internal helper changes.
func pointerLiteral(oid string, size int64) string {
	return "version https://git-lfs.github.com/spec/v1\noid " + oid + "\nsize " + strconv.FormatInt(size, 10) + "\n"
}
