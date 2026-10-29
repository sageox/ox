package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeDraftPlaceholder(t *testing.T, ledger, name string, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(ledger, "sessions", name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	updated := time.Now().Add(-age)
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{
		Version: "1.0", SessionName: name, SessionID: "ses_01950000-0000-7000-8000-0000000000aa",
		Draft: true, CreatedAt: updated, UpdatedAt: &updated,
		Files: map[string]lfs.FileRef{},
	}))
	return dir
}

func draftGit(t *testing.T, ledger string, args ...string) string {
	t.Helper()
	out, err := gitutil.RunGit(t.Context(), ledger, args...)
	require.NoError(t, err)
	return out
}

// Wildcard classes are checked without creating filenames forbidden on Windows.
func TestValidateDraftSessionName_RejectsWildcards(t *testing.T) {
	for _, name := range []string{"*", "draft?", "[ab]"} {
		assert.Error(t, ValidateDraftSessionName(name), name)
	}
	assert.NoError(t, ValidateDraftSessionName("2026-01-01T00-00-testuser-OxDraft"))
}

// A missing, unreadable, or path-shaped candidate cannot authorize deletion.
func TestOrphanedDraftEligibility_RefusesUnclassifiableMetadata(t *testing.T) {
	t.Parallel()
	ledger := t.TempDir()
	dir := writeDraftPlaceholder(t, ledger, "abandoned", 2*OrphanedDraftAge)
	assert.Nil(t, ReadOrphanedDraftMeta(ledger, "missing", nil))
	// This alias reaches valid metadata if name validation is bypassed.
	assert.Nil(t, ReadOrphanedDraftMeta(ledger, "../sessions/abandoned", nil))
	// A directory in place of metadata fails on every OS, even as root.
	require.NoError(t, os.Remove(filepath.Join(dir, "meta.json")))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "meta.json"), 0o755))
	assert.Nil(t, ReadOrphanedDraftMeta(ledger, "abandoned", nil))
}

// Only stale placeholders without local recovery data or artifacts are disposable.
func TestFindOrphanedDrafts_RetainsRecoverableSessions(t *testing.T) {
	t.Parallel()
	ledger, cache := t.TempDir(), t.TempDir()
	for _, name := range []string{"abandoned", "fresh", "cached", "content"} {
		dir := writeDraftPlaceholder(t, ledger, name, 2*OrphanedDraftAge)
		switch name {
		case "fresh":
			writeDraftPlaceholder(t, ledger, name, time.Minute)
		case "cached":
			require.NoError(t, os.MkdirAll(filepath.Join(cache, name), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(cache, name, "raw.jsonl"), []byte("recoverable session\n"), 0o644))
		case "content":
			require.NoError(t, os.WriteFile(filepath.Join(dir, "summary.json"), []byte(`{"summary":"retained work"}`), 0o644))
		}
	}
	orphans, err := FindOrphanedDrafts(ledger, []string{cache})
	require.NoError(t, err)
	assert.Equal(t, []string{"abandoned"}, orphans)
}

// Git inspection must notice hidden artifacts and inspection errors before removal.
func TestDraftIsCleanPlaceholder_RequiresMetadataOnlyGitState(t *testing.T) {
	t.Parallel()
	ledger := t.TempDir()
	dir := writeDraftPlaceholder(t, ledger, "abandoned", 2*OrphanedDraftAge)
	clean, err := DraftIsCleanPlaceholder(t.Context(), ledger, "abandoned")
	require.Error(t, err)
	assert.False(t, clean)

	draftGit(t, ledger, "init", "--quiet")
	draftGit(t, ledger, "add", "sessions")
	draftGit(t, ledger, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--quiet", "-m", "draft")
	clean, err = DraftIsCleanPlaceholder(t.Context(), ledger, "abandoned")
	require.NoError(t, err)
	require.True(t, clean)
	clean, err = DraftIsCleanPlaceholder(t.Context(), ledger, "../sessions/abandoned")
	require.Error(t, err)
	assert.False(t, clean)

	require.NoError(t, os.WriteFile(filepath.Join(ledger, ".git", "info", "exclude"), []byte("raw.jsonl\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte("recoverable session\n"), 0o644))
	clean, err = DraftIsCleanPlaceholder(t.Context(), ledger, "abandoned")
	require.NoError(t, err)
	assert.False(t, clean)

	// A missing checkout file does not make a committed artifact disposable.
	draftGit(t, ledger, "add", "-f", "sessions/abandoned/raw.jsonl")
	draftGit(t, ledger, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--quiet", "-m", "session content")
	require.NoError(t, os.Remove(filepath.Join(dir, "raw.jsonl")))
	clean, err = DraftIsCleanPlaceholder(t.Context(), ledger, "abandoned")
	require.NoError(t, err)
	assert.False(t, clean)
}

// Rollback must survive cancellation without overwriting unrelated staged or new data.
func TestRestoreDraftPlaceholder_ExpiredCallerPreservesIndexAndContent(t *testing.T) {
	t.Parallel()
	ledger := t.TempDir()
	dir := writeDraftPlaceholder(t, ledger, "abandoned", 2*OrphanedDraftAge)
	metaPath := filepath.Join(dir, "meta.json")
	draftGit(t, ledger, "init", "--quiet")
	draftGit(t, ledger, "add", "sessions")
	draftGit(t, ledger, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--quiet", "-m", "draft")

	// Rollback owns only this placeholder, including when unrelated work is staged.
	require.NoError(t, os.WriteFile(filepath.Join(ledger, "note.txt"), []byte("staged work\n"), 0o644))
	draftGit(t, ledger, "add", "note.txt")
	index := draftGit(t, ledger, "write-tree")
	head := draftGit(t, ledger, "rev-parse", "HEAD")
	before, err := os.ReadFile(metaPath)
	require.NoError(t, err)
	clean, err := DraftIsCleanPlaceholder(t.Context(), ledger, "abandoned")
	require.NoError(t, err)
	require.True(t, clean)

	draftGit(t, ledger, "rm", "--", "sessions/abandoned/meta.json")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	contentPath := filepath.Join(dir, "summary.json")
	content := []byte(`{"summary":"arrived during retraction"}`)
	require.NoError(t, os.WriteFile(contentPath, content, 0o644))
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)

	require.NoError(t, RestoreDraftPlaceholder(ctx, ledger, "abandoned"))
	after, err := os.ReadFile(metaPath)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.Equal(t, index, draftGit(t, ledger, "write-tree"))
	assert.Equal(t, head, draftGit(t, ledger, "rev-parse", "HEAD"))
	remaining, err := os.ReadFile(contentPath)
	require.NoError(t, err)
	assert.Equal(t, content, remaining)
	require.Error(t, RestoreDraftPlaceholder(ctx, ledger, "../sessions/abandoned"))
	assert.Equal(t, index, draftGit(t, ledger, "write-tree"))
}
