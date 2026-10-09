package lfs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// a blocked plan makes the full reconcile reach the staged-artifact repair.
// Failure to preserve or upload an own recording must stop before any pointer,
// metadata, staged content, or history is replaced.
func TestReconcile_OwnArtifactFailuresKeepOriginalState(t *testing.T) {
	if testing.Short() {
		t.Skip("short: repeated full reconciliation against real Git repositories")
	}
	for _, mode := range []string{"cache_parent_not_directory", "conflicting_cache", "store_drops_upload", "already_pointer", "registered_git_content", "unstaged_pointer", "unstaged_empty", "worktree_removed"} {
		t.Run(mode, func(t *testing.T) {
			ledger, bare := initLedgerWithRemote(t)
			planDir := filepath.Join(ledger, "data", "plans", "blocked-plan")
			require.NoError(t, os.MkdirAll(planDir, 0o755))
			missing := NewFileRef([]byte("unavailable prior plan render"))
			require.NoError(t, os.WriteFile(filepath.Join(planDir, "plan.html"), []byte(FormatPointer(missing.OID, missing.Size)), 0o644))
			git(t, ledger, "add", "--sparse", "data/plans/blocked-plan/plan.html")
			git(t, ledger, "commit", "--no-verify", "-m", "retain unavailable plan pointer")

			content := []byte("recording retained until durable upload\n")
			artifact := content
			ref := NewFileRef(content)
			if mode == "already_pointer" {
				artifact = []byte(FormatPointer(ref.OID, ref.Size))
			}
			rawPath := stagePlainSession(t, ledger, "own", "person-a", artifact)
			worktree := artifact
			switch mode {
			case "unstaged_pointer":
				other := NewFileRef([]byte("a separate unstaged recording"))
				worktree = []byte(FormatPointer(other.OID, other.Size))
				require.NoError(t, os.WriteFile(rawPath, worktree, 0o644))
			case "unstaged_empty":
				worktree = []byte{}
				require.NoError(t, os.WriteFile(rawPath, worktree, 0o644))
			case "worktree_removed":
				require.NoError(t, os.Remove(rawPath))
			}
			metaPath := filepath.Join(filepath.Dir(rawPath), "meta.json")
			if mode == "already_pointer" || mode == "registered_git_content" {
				meta, err := ReadSessionMeta(filepath.Dir(rawPath))
				require.NoError(t, err)
				if mode == "registered_git_content" {
					ref = FileRef{Storage: StorageGit, Size: int64(len(content))}
				}
				meta.Files = map[string]FileRef{"raw.jsonl": ref}
				data, err := json.Marshal(meta)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(metaPath, data, 0o644))
				git(t, ledger, "add", "--sparse", "sessions/own/meta.json")
			}
			originalMeta := mustReadFile(t, metaPath)
			cachePath := filepath.Join(ledger, ".sageox", "cache", "sessions", "own", "raw.jsonl")
			var existingCache []byte
			switch mode {
			case "cache_parent_not_directory":
				require.NoError(t, os.MkdirAll(filepath.Join(ledger, ".sageox"), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(ledger, ".sageox", "cache"), []byte("preserved obstruction"), 0o644))
			case "conflicting_cache":
				existingCache = []byte("different prior recording bytes\n")
				require.NoError(t, os.MkdirAll(filepath.Dir(cachePath), 0o700))
				require.NoError(t, os.WriteFile(cachePath, existingCache, 0o600))
			}
			client, store := newFakeUploadStore(t, false, mode == "store_drops_upload")
			localHead := git(t, ledger, "rev-parse", "HEAD")
			remoteHead := git(t, bare, "rev-parse", "HEAD")
			ctx := withReconcileOwner(context.Background(), "person-a")
			_, err := reconcileUnpushedPointers(ctx, ledger, nil, func() (*Client, error) { return client, nil })
			require.Error(t, err)
			switch mode {
			case "cache_parent_not_directory", "conflicting_cache":
				assert.ErrorContains(t, err, "upload own staged session artifacts before LFS reconcile")
				assert.ErrorContains(t, err, "keep sessions/own/raw.jsonl in the Ledger cache")
				assert.Zero(t, store.putAttempts, "cache preservation must happen before network upload")
			case "store_drops_upload":
				assert.ErrorContains(t, err, "upload own staged session artifacts before LFS reconcile")
				assert.ErrorContains(t, err, "upload staged sessions/own/raw.jsonl")
				assert.ErrorContains(t, err, "not found on the store after upload")
				assert.Equal(t, 1, store.putAttempts)
				assert.Equal(t, content, mustReadFile(t, cachePath))
			case "unstaged_pointer", "unstaged_empty", "worktree_removed":
				assert.ErrorContains(t, err, "upload own staged session artifacts before LFS reconcile")
				if mode == "worktree_removed" {
					assert.ErrorContains(t, err, "inspect worktree")
				} else {
					assert.ErrorContains(t, err, "refusing to overwrite unstaged content")
				}
				assert.Equal(t, content, mustReadFile(t, cachePath))
				assert.Equal(t, content, store.stored[ref.BareOID()])
			case "already_pointer", "registered_git_content":
				var blocked *UnrecoverablePointersError
				require.True(t, errors.As(err, &blocked), "registered artifacts should reach the original missing-plan verdict: %v", err)
				assert.Zero(t, store.putAttempts, "an existing pointer or Git artifact needs no upload")
				assert.NoFileExists(t, cachePath)
			}
			if mode == "worktree_removed" {
				assert.NoFileExists(t, rawPath)
			} else {
				assert.Equal(t, worktree, mustReadFile(t, rawPath))
			}
			assert.Equal(t, originalMeta, mustReadFile(t, metaPath))
			staged, readErr := exec.Command("git", "-C", ledger, "show", ":sessions/own/raw.jsonl").Output()
			require.NoError(t, readErr)
			assert.Equal(t, artifact, staged)
			assert.Equal(t, mode == "already_pointer" || mode == "unstaged_pointer", IsPointerFile(rawPath))
			if existingCache != nil {
				assert.Equal(t, existingCache, mustReadFile(t, cachePath))
			}
			assert.Equal(t, localHead, git(t, ledger, "rev-parse", "HEAD"))
			assert.Equal(t, remoteHead, git(t, bare, "rev-parse", "HEAD"))
			if mode != "unstaged_pointer" && mode != "unstaged_empty" && mode != "worktree_removed" {
				assert.Empty(t, store.stored, "failed or skipped repairs cannot invent durable content")
			}
		})
	}
}
