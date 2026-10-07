package lfs

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stagePlainSession stages plain raw.jsonl content for a session whose meta.json
// names username, the state a finalize with a failed LFS upload leaves behind.
func stagePlainSession(t *testing.T, ledger, session, username string, content []byte) string {
	t.Helper()
	dir := filepath.Join(ledger, "sessions", session)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), content, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"session_name":"`+session+`","username":"`+username+`","title":"t"}`+"\n"), 0o644))
	git(t, ledger, "add", "--sparse", "sessions/"+session)
	return filepath.Join(dir, "raw.jsonl")
}

// Reconcile must not deadlock behind the finalize it is waiting on: an own
// session's staged plain artifact is uploaded and replaced by its pointer, then
// the repair continues. Failure prevented: reconcile refused with "must contain
// an LFS pointer" while finalize stayed paused until the push recovered.
func TestReconcile_UploadsOwnStagedPlainArtifactInsteadOfRefusing(t *testing.T) {
	ledger, _ := initLedgerWithRemote(t)
	recoverable := []byte("recoverable recording\n")
	_, _, cachePath, ref := commitMissingSessionPointer(t, ledger, "wedged", recoverable)
	require.NoError(t, os.MkdirAll(filepath.Dir(cachePath), 0o700))
	require.NoError(t, os.WriteFile(cachePath, recoverable, 0o600))
	content := []byte("{\"type\":\"user\",\"content\":\"finalize never uploaded this\"}\n")
	rawPath := stagePlainSession(t, ledger, "own", "ryan", content)
	client, store := newFakeUploadStore(t, false, false)
	ctx := withReconcileOwner(context.Background(), "ryan")

	result, err := reconcileUnpushedPointers(ctx, ledger, nil, func() (*Client, error) { return client, nil })

	require.NoError(t, err)
	assert.Equal(t, 1, result.RecoveredUploads)
	assert.True(t, IsPointerFile(rawPath), "the plain artifact is replaced by its pointer")
	pointer, readErr := ReadPointerFile(rawPath)
	require.NoError(t, readErr)
	assert.Equal(t, NewFileRef(content).BareOID(), pointer.BareOID())
	assert.Equal(t, content, store.stored[NewFileRef(content).BareOID()], "the blob exists on the store")
	assert.Equal(t, recoverable, store.stored[ref.BareOID()])
	assert.Equal(t, content, mustReadFile(t, filepath.Join(ledger, ".sageox", "cache", "sessions", "own", "raw.jsonl")), "the bytes stay in the cache")
	assert.Contains(t, git(t, ledger, "show", ":sessions/own/raw.jsonl"), "version https://git-lfs", "the pointer is what is staged")
}

// A teammate's staged plain artifact is never uploaded on their behalf: reconcile
// refuses and names the file.
func TestReconcile_RefusesTeammateStagedPlainArtifact(t *testing.T) {
	ledger, _ := initLedgerWithRemote(t)
	recoverable := []byte("recoverable recording\n")
	_, _, cachePath, _ := commitMissingSessionPointer(t, ledger, "wedged", recoverable)
	require.NoError(t, os.MkdirAll(filepath.Dir(cachePath), 0o700))
	require.NoError(t, os.WriteFile(cachePath, recoverable, 0o600))
	content := []byte("a teammate's recording\n")
	rawPath := stagePlainSession(t, ledger, "theirs", "tess", content)
	client, store := newFakeUploadStore(t, false, false)
	ctx := withReconcileOwner(context.Background(), "ryan")

	_, err := reconcileUnpushedPointers(ctx, ledger, nil, func() (*Client, error) { return client, nil })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "sessions/theirs/raw.jsonl")
	assert.Equal(t, content, mustReadFile(t, rawPath), "the teammate's file is untouched")
	assert.NotContains(t, store.stored, NewFileRef(content).BareOID())
}

// metadata disappearing during the real upload must not panic, manufacture new
// metadata, or replace either staged or working content with a pointer.
func TestReconcile_MetadataDisappearsDuringOwnArtifactUpload(t *testing.T) {
	ledger, _ := initLedgerWithRemote(t)
	content := []byte("recording retained during a concurrent removal\n")
	rawPath := stagePlainSession(t, ledger, "own", "person-a", content)
	metaPath := filepath.Join(filepath.Dir(rawPath), "meta.json")
	client, store := newFakeUploadStore(t, false, false)
	store.beforeStore = func() { require.NoError(t, os.Remove(metaPath)) }
	ctx := withReconcileOwner(context.Background(), "person-a")
	_, err := uploadOwnStagedPlainArtifacts(ctx, ledger, client, slog.Default())
	require.ErrorContains(t, err, "meta.json for own disappeared")
	assert.NoFileExists(t, metaPath)
	assert.Equal(t, content, mustReadFile(t, rawPath))
	staged, readErr := exec.Command("git", "-C", ledger, "show", ":sessions/own/raw.jsonl").Output()
	require.NoError(t, readErr)
	assert.Equal(t, content, staged)
	assert.Equal(t, content, mustReadFile(t, filepath.Join(ledger, ".sageox", "cache", "sessions", "own", "raw.jsonl")))
	assert.Equal(t, content, store.stored[NewFileRef(content).BareOID()])
}

// A coworker editing the worktree copy while its bytes are being uploaded must
// win: the file is left alone, metadata is untouched, and the bytes are still
// safe in the cache and on the store.
func TestReconcile_WorktreeEditedDuringOwnArtifactUploadIsRefused(t *testing.T) {
	ledger, _ := initLedgerWithRemote(t)
	content := []byte("recording edited mid-upload\n")
	rawPath := stagePlainSession(t, ledger, "own", "person-a", content)
	metaPath := filepath.Join(filepath.Dir(rawPath), "meta.json")
	originalMeta := mustReadFile(t, metaPath)
	edited := append(append([]byte{}, content...), []byte("a late edit\n")...)
	client, store := newFakeUploadStore(t, false, false)
	store.beforeStore = func() {
		require.NoError(t, os.WriteFile(rawPath, edited, 0o644))
		future := time.Now().Add(2 * time.Second)
		require.NoError(t, os.Chtimes(rawPath, future, future))
	}
	ctx := withReconcileOwner(context.Background(), "person-a")
	_, err := uploadOwnStagedPlainArtifacts(ctx, ledger, client, slog.Default())
	require.ErrorContains(t, err, "worktree differs from the staged bytes")
	assert.Equal(t, edited, mustReadFile(t, rawPath), "the coworker's edit survives")
	assert.Equal(t, originalMeta, mustReadFile(t, metaPath), "metadata is not touched after a refusal")
	assert.Equal(t, content, store.stored[NewFileRef(content).BareOID()], "the staged bytes still reached the store")
}

func TestPointerMatches_RejectsGarbageAndMismatch(t *testing.T) {
	ref := NewFileRef([]byte("x"))
	assert.False(t, pointerMatches([]byte("not a pointer"), ref))
	other := NewFileRef([]byte("y"))
	assert.False(t, pointerMatches([]byte(FormatPointer(other.OID, other.Size)), ref))
	assert.True(t, pointerMatches([]byte(FormatPointer(ref.OID, ref.Size)), ref))
}

// A worktree copy that vanished during the upload is reported, not recreated.
func TestReconcile_WorktreeRemovedDuringOwnArtifactUploadIsReported(t *testing.T) {
	ledger, _ := initLedgerWithRemote(t)
	content := []byte("recording removed mid-upload\n")
	rawPath := stagePlainSession(t, ledger, "own", "person-a", content)
	client, store := newFakeUploadStore(t, false, false)
	store.beforeStore = func() { require.NoError(t, os.Remove(rawPath)) }
	ctx := withReconcileOwner(context.Background(), "person-a")
	_, err := uploadOwnStagedPlainArtifacts(ctx, ledger, client, slog.Default())
	require.ErrorContains(t, err, "inspect worktree copy")
	assert.NoFileExists(t, rawPath, "nothing is written back over a removed copy")
}

// A path that became a directory during the upload cannot be read as the
// staged file: reported, nothing written.
func TestReconcile_WorktreeReplacedByDirectoryDuringOwnArtifactUploadIsReported(t *testing.T) {
	ledger, _ := initLedgerWithRemote(t)
	content := []byte("recording replaced mid-upload\n")
	rawPath := stagePlainSession(t, ledger, "own", "person-a", content)
	client, store := newFakeUploadStore(t, false, false)
	store.beforeStore = func() {
		require.NoError(t, os.Remove(rawPath))
		require.NoError(t, os.Mkdir(rawPath, 0o755))
	}
	ctx := withReconcileOwner(context.Background(), "person-a")
	_, err := uploadOwnStagedPlainArtifacts(ctx, ledger, client, slog.Default())
	require.ErrorContains(t, err, "inspect worktree copy")
	assert.DirExists(t, rawPath)
}
