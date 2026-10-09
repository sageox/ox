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
// reconcile will merely push; reconcile itself never deletes anything.
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
	assert.Equal(t, content, uploaded[ref.BareOID()])
	git(t, ledger, "push", "--quiet")
	assert.Equal(t, git(t, ledger, "rev-parse", "HEAD"), git(t, bare, "rev-parse", "HEAD"))
}

// Unrecoverable pointers at ANY count must not block restoring the rest:
// recoverable blobs upload first, then reconcile refuses, names every path and
// OID, and removes nothing.
func TestReconcile_UnrecoverableAnyCountStillUploadsRecoverable(t *testing.T) {
	for _, unrecoverableCount := range []int{1, 2, 7} {
		t.Run(fmt.Sprintf("%d unrecoverable", unrecoverableCount), func(t *testing.T) {
			ledger, _ := initLedgerWithRemote(t)
			missing := make(map[string]bool)
			var recoverableOIDs []string
			unrecoverableOIDs := make(map[string]string)
			for i := 0; i < 3+unrecoverableCount; i++ {
				name := fmt.Sprintf("s%02d", i)
				content := []byte("content " + name + "\n")
				_, _, cachePath, ref := commitMissingSessionPointer(t, ledger, name, content)
				missing[ref.BareOID()] = true
				if i < 3 {
					require.NoError(t, os.MkdirAll(filepath.Dir(cachePath), 0o700))
					require.NoError(t, os.WriteFile(cachePath, content, 0o600))
					recoverableOIDs = append(recoverableOIDs, ref.BareOID())
				} else {
					unrecoverableOIDs["sessions/"+name+"/raw.jsonl"] = ref.OID
				}
			}
			headBefore := git(t, ledger, "rev-parse", "HEAD")
			client, uploaded := recoveryLFSServer(t, missing, 0)

			result, err := reconcileUnpushedPointers(context.Background(), ledger, nil, func() (*Client, error) { return client, nil })

			var unrecoverable *UnrecoverablePointersError
			require.True(t, errors.As(err, &unrecoverable), "got %v", err)
			assert.Equal(t, 3, unrecoverable.Uploaded)
			require.Len(t, unrecoverable.Pointers, unrecoverableCount)
			for _, p := range unrecoverable.Pointers {
				assert.Equal(t, unrecoverableOIDs[p.Path], p.OID)
				assert.Contains(t, err.Error(), p.Path)
				assert.Contains(t, err.Error(), p.OID)
			}
			assert.Contains(t, err.Error(), "will not be removed")
			assert.Equal(t, 3, result.RecoveredUploads)
			assert.True(t, result.Changed())
			for _, oid := range recoverableOIDs {
				assert.Contains(t, uploaded, oid)
			}
			assert.Len(t, uploaded, 3)
			assert.Equal(t, headBefore, git(t, ledger, "rev-parse", "HEAD"), "no commit, no squash")
			for path := range unrecoverableOIDs {
				assert.FileExists(t, filepath.Join(ledger, path), "the pointer file stays untouched")
			}
		})
	}
}
