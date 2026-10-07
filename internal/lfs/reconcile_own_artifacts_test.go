package lfs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
