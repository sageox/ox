package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBatchDeleteSessionsFromLedger_SingleSession(t *testing.T) {
	barePath, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)

	// create and push a session to the ledger so there's something to delete
	sessionName := "2026-01-01T00-00-testuser-OxDel1"
	writeSessionFiles(t, clonePath, sessionName)
	require.NoError(t, commitAndPushLedger(clonePath, sessionName))

	// verify it exists on remote before deletion
	verifyBefore := cloneBare(t, barePath)
	_, err := os.Stat(filepath.Join(verifyBefore, "sessions", sessionName, "meta.json"))
	require.NoError(t, err, "session should exist on remote before deletion")

	// delete it
	removed, err := batchDeleteSessionsFromLedger(clonePath, []string{sessionName})
	require.NoError(t, err)
	assert.Equal(t, 1, removed)

	// verify it's gone from remote
	verifyAfter := cloneBare(t, barePath)
	_, err = os.Stat(filepath.Join(verifyAfter, "sessions", sessionName))
	assert.True(t, os.IsNotExist(err), "session dir should not exist on remote after deletion")

	// verify commit message
	msg := runGit(t, verifyAfter, "log", "-1", "--format=%s")
	assert.Contains(t, msg, sessionName)
}

func TestBatchDeleteSessionsFromLedger_MultipleSessions(t *testing.T) {
	barePath, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)

	sessions := []string{
		"2026-01-01T00-00-testuser-OxDm1",
		"2026-01-01T00-01-testuser-OxDm2",
		"2026-01-01T00-02-testuser-OxDm3",
	}
	for _, name := range sessions {
		writeSessionFiles(t, clonePath, name)
		require.NoError(t, commitAndPushLedger(clonePath, name))
	}

	removed, err := batchDeleteSessionsFromLedger(clonePath, sessions)
	require.NoError(t, err)
	assert.Equal(t, 3, removed)

	// all three gone from remote
	verifyClone := cloneBare(t, barePath)
	for _, name := range sessions {
		_, err := os.Stat(filepath.Join(verifyClone, "sessions", name))
		assert.True(t, os.IsNotExist(err), "%s should be gone from remote", name)
	}

	// single commit for batch deletion
	msg := runGit(t, verifyClone, "log", "-1", "--format=%s")
	assert.Contains(t, msg, "3 session(s)")
}

func TestBatchDeleteSessionsFromLedger_NonexistentSession(t *testing.T) {
	_, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)

	// try to delete a session that was never created
	removed, err := batchDeleteSessionsFromLedger(clonePath, []string{"nonexistent-OxGone"})
	assert.Error(t, err, "should error when no sessions could be staged")
	assert.Equal(t, 0, removed)
	assert.Contains(t, err.Error(), "no sessions could be staged")
}

// TestBatchDeleteSessionsFromLedger_PushConflict verifies that deletion succeeds
// even when the remote has diverged (another push happened between commit and push).
// This is the regression test for the original bug: the old code used bare `git push`
// without retry, then did `git reset HEAD~1` on failure — which left files deleted
// locally but the commit rolled back, so the remote still had the session.
func TestBatchDeleteSessionsFromLedger_PushConflict(t *testing.T) {
	barePath, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)

	// create a session to delete
	sessionName := "2026-01-01T00-00-testuser-OxConf"
	writeSessionFiles(t, clonePath, sessionName)
	require.NoError(t, commitAndPushLedger(clonePath, sessionName))

	// push a conflicting commit from another clone to create divergence
	otherClone := cloneBare(t, barePath)
	conflictFile := filepath.Join(otherClone, "conflict-marker.txt")
	require.NoError(t, os.WriteFile(conflictFile, []byte("diverge"), 0644))
	runGit(t, otherClone, "add", "conflict-marker.txt")
	runGit(t, otherClone, "commit", "--no-verify", "-m", "concurrent work")
	runGit(t, otherClone, "push")

	// now delete — push will get non-fast-forward, must handle via rebase retry
	removed, err := batchDeleteSessionsFromLedger(clonePath, []string{sessionName})
	require.NoError(t, err, "deletion must succeed after rebase retry on diverged remote")
	assert.Equal(t, 1, removed)

	// verify actually gone from remote
	verifyClone := cloneBare(t, barePath)
	_, err = os.Stat(filepath.Join(verifyClone, "sessions", sessionName))
	assert.True(t, os.IsNotExist(err), "session must be deleted from remote even after conflict")

	// verify the concurrent commit survived
	_, err = os.Stat(filepath.Join(verifyClone, "conflict-marker.txt"))
	assert.NoError(t, err, "concurrent commit must not be lost during rebase")
}

// TestBatchDeleteSessionsFromLedger_LocalCommitPreservedOnPushFailure verifies that
// if push fails permanently, the local deletion commit is preserved (not rolled back).
// The old buggy code did `git reset HEAD~1` on push failure, which destroyed the
// local commit and left working tree files deleted — a corrupted state where the
// daemon's next `git pull` would restore files from remote.
func TestBatchDeleteSessionsFromLedger_LocalCommitPreservedOnPushFailure(t *testing.T) {
	_, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)

	// create a session
	sessionName := "2026-01-01T00-00-testuser-OxFail"
	writeSessionFiles(t, clonePath, sessionName)
	require.NoError(t, commitAndPushLedger(clonePath, sessionName))

	// break the remote so push fails permanently
	runGit(t, clonePath, "remote", "set-url", "origin", "/nonexistent/bare/repo.git")

	commitsBefore := commitCount(t, clonePath)

	removed, err := batchDeleteSessionsFromLedger(clonePath, []string{sessionName})
	require.Error(t, err, "should fail when remote is unreachable")
	assert.Equal(t, 0, removed, "returns 0 on push failure")

	// the key invariant: local commit must still exist even though push failed
	// old bug: `git reset HEAD~1` destroyed the commit AND left files deleted
	commitsAfter := commitCount(t, clonePath)
	assert.Equal(t, commitsBefore+1, commitsAfter,
		"deletion commit must be preserved locally even after push failure (old bug: git reset HEAD~1 destroyed it)")

	// verify the session files are actually gone from working tree (committed deletion)
	_, statErr := os.Stat(filepath.Join(clonePath, "sessions", sessionName, "meta.json"))
	assert.True(t, os.IsNotExist(statErr),
		"session files should be deleted from working tree (deletion was committed)")
}

func TestBatchDeleteSessionsFromLedger_UsesCanonicalPushLedger(t *testing.T) {
	barePath, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)

	// create two sessions
	for _, name := range []string{
		"2026-01-01T00-00-testuser-OxCp1",
		"2026-01-01T00-01-testuser-OxCp2",
	} {
		writeSessionFiles(t, clonePath, name)
		require.NoError(t, commitAndPushLedger(clonePath, name))
	}

	// push diverging commit from other clone
	otherClone := cloneBare(t, barePath)
	require.NoError(t, os.WriteFile(filepath.Join(otherClone, "other.txt"), []byte("x"), 0644))
	runGit(t, otherClone, "add", "other.txt")
	runGit(t, otherClone, "commit", "--no-verify", "-m", "diverge")
	runGit(t, otherClone, "push")

	// batch delete both — exercises pushLedger retry path
	removed, err := batchDeleteSessionsFromLedger(clonePath, []string{
		"2026-01-01T00-00-testuser-OxCp1",
		"2026-01-01T00-01-testuser-OxCp2",
	})
	require.NoError(t, err)
	assert.Equal(t, 2, removed)

	// verify both gone
	verifyClone := cloneBare(t, barePath)
	for _, name := range []string{"OxCp1", "OxCp2"} {
		matches, _ := filepath.Glob(filepath.Join(verifyClone, "sessions", "*"+name))
		assert.Empty(t, matches, "%s should be gone from remote", name)
	}
}

func TestCountSubstantiveEntries(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected int
	}{
		{
			name:     "empty file",
			content:  "",
			expected: 0,
		},
		{
			name:     "header only",
			content:  `{"metadata":{"agent_id":"Ox1"}}` + "\n",
			expected: 0,
		},
		{
			name:     "header plus one entry",
			content:  `{"metadata":{"agent_id":"Ox1"}}` + "\n" + `{"type":"assistant","content":"hello"}` + "\n",
			expected: 1,
		},
		{
			name:     "header plus entries and footer",
			content:  `{"metadata":{}}` + "\n" + `{"type":"user"}` + "\n" + `{"type":"assistant"}` + "\n" + `{"entry_count":2}` + "\n",
			expected: 3, // counts footer as a line too — that's fine, >0 is what matters
		},
		{
			name:     "nonexistent file",
			content:  "", // won't be written
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "nonexistent file" {
				assert.Equal(t, 0, session.CountSubstantiveEntries("/nonexistent/raw.jsonl"))
				return
			}
			tmpDir := t.TempDir()
			path := filepath.Join(tmpDir, "raw.jsonl")
			require.NoError(t, os.WriteFile(path, []byte(tt.content), 0644))
			assert.Equal(t, tt.expected, session.CountSubstantiveEntries(path))
		})
	}
}

// TestPushLedger_UsedByBatchDelete is a contract test: verifies that pushLedger
// exists and works with the same context pattern used by batchDeleteSessionsFromLedger.
func TestPushLedger_UsedByBatchDelete(t *testing.T) {
	barePath, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)

	// create a commit to push
	require.NoError(t, os.WriteFile(filepath.Join(clonePath, "test.txt"), []byte("x"), 0644))
	runGit(t, clonePath, "add", "test.txt")
	runGit(t, clonePath, "commit", "--no-verify", "-m", "test")

	// push using context.Background() — the pattern batchDeleteSessionsFromLedger uses
	err := pushLedger(context.Background(), clonePath)
	require.NoError(t, err)

	// verify it reached remote
	verifyClone := cloneBare(t, barePath)
	_, err = os.Stat(filepath.Join(verifyClone, "test.txt"))
	assert.NoError(t, err, "pushed file should exist on remote")
}
