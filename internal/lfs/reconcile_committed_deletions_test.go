package lfs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/sacred"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Committed deletions (e.g. the doctor untracking draft artifacts) are history
// reconcile will merely push; they must not count toward the mass-delete guard,
// which only covers deletions reconcile itself performs. The guard on
// reconcile's own deletions is covered by
// TestReconcile_MassDeletionPreflightDoesNotMutate.
func TestReconcile_CommittedDeletionsDoNotTripSacredGuard(t *testing.T) {
	ledger, bare := initLedgerWithRemote(t)
	drafts := make(map[string]string)
	for i := 0; i <= sacred.MassDeleteThreshold+1; i++ {
		drafts[filepath.ToSlash(filepath.Join("sessions", "draft-"+string(rune('a'+i)), "draft.md"))] = "draft"
	}
	writeAndCommit(t, ledger, "add drafts", drafts)
	git(t, ledger, "push", "--quiet")
	for rel := range drafts {
		git(t, ledger, "rm", "--quiet", "--sparse", rel)
	}
	git(t, ledger, "commit", "-m", "untrack drafts", "--no-verify")
	require.Greater(t, len(drafts), sacred.MassDeleteThreshold)

	content := []byte("recoverable recording\n")
	_, _, cachePath, ref := commitMissingSessionPointer(t, ledger, "keep", content)
	require.NoError(t, os.MkdirAll(filepath.Dir(cachePath), 0o700))
	require.NoError(t, os.WriteFile(cachePath, content, 0o600))
	client, uploaded := recoveryLFSServer(t, map[string]bool{ref.BareOID(): true}, 0)

	result, err := reconcileUnpushedPointers(context.Background(), ledger, nil, func() (*Client, error) { return client, nil })

	require.NoError(t, err)
	assert.Equal(t, 1, result.RecoveredUploads)
	assert.Zero(t, result.Replaced)
	assert.Equal(t, content, uploaded[ref.BareOID()])
	git(t, ledger, "push", "--quiet")
	assert.Equal(t, git(t, ledger, "rev-parse", "HEAD"), git(t, bare, "rev-parse", "HEAD"))
}

// One handful of unrecoverable pointers must not block restoring the rest:
// recoverable blobs upload first, then the guard refuses only the replacements
// and names every path.
func TestReconcile_UnrecoverableOverThresholdStillUploadsRecoverable(t *testing.T) {
	ledger, _ := initLedgerWithRemote(t)
	missing := make(map[string]bool)
	var recoverableOIDs []string
	var unrecoverablePaths []string
	for i := 0; i < 3+sacred.MassDeleteThreshold+2; i++ {
		name := fmt.Sprintf("s%02d", i)
		content := []byte("content " + name + "\n")
		_, _, cachePath, ref := commitMissingSessionPointer(t, ledger, name, content)
		missing[ref.BareOID()] = true
		if i < 3 {
			require.NoError(t, os.MkdirAll(filepath.Dir(cachePath), 0o700))
			require.NoError(t, os.WriteFile(cachePath, content, 0o600))
			recoverableOIDs = append(recoverableOIDs, ref.BareOID())
		} else {
			unrecoverablePaths = append(unrecoverablePaths, "sessions/"+name+"/raw.jsonl")
		}
	}
	require.Len(t, unrecoverablePaths, sacred.MassDeleteThreshold+2)
	headBefore := git(t, ledger, "rev-parse", "HEAD")
	client, uploaded := recoveryLFSServer(t, missing, 0)

	result, err := reconcileUnpushedPointers(context.Background(), ledger, nil, func() (*Client, error) { return client, nil })

	var unrecoverable *UnrecoverablePointersError
	require.True(t, errors.As(err, &unrecoverable), "got %v", err)
	assert.Equal(t, 3, unrecoverable.Uploaded)
	assert.ElementsMatch(t, unrecoverablePaths, unrecoverable.Paths)
	assert.Equal(t, 3, result.RecoveredUploads)
	assert.True(t, result.Changed())
	assert.Zero(t, result.Replaced)
	for _, oid := range recoverableOIDs {
		assert.Contains(t, uploaded, oid)
	}
	assert.Len(t, uploaded, 3)
	assert.Equal(t, headBefore, git(t, ledger, "rev-parse", "HEAD"))
	for _, path := range unrecoverablePaths {
		assert.FileExists(t, filepath.Join(ledger, path))
	}
}
