package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented (sageox/ox#1505): redaction cleans the working files, the
// holding-commit amendment fails, and every later retry scans clean files, so
// HEAD keeps the credential and publication is held forever. The recorded
// repair must complete on retry, using only the bytes it produced, and must
// never amend a newer HEAD or replace another writer's staged work.
func TestPrePushGate_ResumesFailedRedactionAmend(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Git amendment failure and retry")
	}
	const name = "2026-11-07-resume"
	// Synthetic key: AWS's documented EXAMPLE key is allowlisted by the
	// publication validator, which this test needs to see refuse.
	const canary = "AKIA" + "QWERTYUIOPASDFGH"
	rel := "sessions/" + name + "/raw.jsonl"

	setup := func(t *testing.T) (work, remote, holding string) {
		work = makeLedgerWithCommit(t, map[string]string{rel: `{"text":"` + canary + `"}` + "\n"})
		seedSessionMeta(t, filepath.Join(work, "sessions", name), name)
		mustGit(t, work, "add", "--sparse", "sessions/"+name+"/meta.json")
		mustGit(t, work, "commit", "--amend", "--no-edit", "--no-verify")
		holding = mustGitCapture(t, work, "rev-parse", "HEAD")
		remote = mustGitCapture(t, work, "remote", "get-url", "origin")

		// Blank committer identity so the scoped amendment cannot be written.
		t.Setenv("GIT_COMMITTER_NAME", "")
		err := runGate(t, work)
		require.Error(t, err, "amendment without a committer identity must fail")
		require.Equal(t, holding, mustGitCapture(t, work, "rev-parse", "HEAD"))
		require.Contains(t, mustGitCapture(t, work, "show", "HEAD:"+rel), canary)
		raw, err := os.ReadFile(filepath.Join(work, rel))
		require.NoError(t, err)
		require.NotContains(t, string(raw), canary, "working file is redacted before the amendment")
		require.FileExists(t, filepath.Join(work, filepath.FromSlash(pendingRedactionAmendRel)))
		t.Setenv("GIT_COMMITTER_NAME", "ox-test")
		return work, remote, holding
	}

	t.Run("retry completes the repair", func(t *testing.T) {
		work, remote, holding := setup(t)
		redacted := mustGitCapture(t, work, "hash-object", rel)
		// Without the record the outgoing-object validator holds publication.
		require.Error(t, gitutil.PushWithRetry(context.Background(), work, gitutil.PushOpts{}))
		assertRemoteObjectsCleanOf(t, remote, canary)

		// A later clean append belongs to the next snapshot, not this repair.
		f, err := os.OpenFile(filepath.Join(work, rel), os.O_APPEND|os.O_WRONLY, 0)
		require.NoError(t, err)
		_, err = f.WriteString(`{"text":"later"}` + "\n")
		require.NoError(t, err)
		require.NoError(t, f.Close())

		require.NoError(t, runGate(t, work))
		assert.NotEqual(t, holding, mustGitCapture(t, work, "rev-parse", "HEAD"))
		assert.Equal(t, mustGitCapture(t, work, "rev-parse", holding+"^"), mustGitCapture(t, work, "rev-parse", "HEAD^"),
			"the holding commit is replaced, not extended")
		assert.Equal(t, redacted, mustGitCapture(t, work, "rev-parse", "HEAD:"+rel), "only the recorded bytes are amended")
		assert.Contains(t, mustGitCapture(t, work, "show", "HEAD:sessions/"+name+"/meta.json"), "redactions")
		assert.Equal(t, rel, mustGitCapture(t, work, "diff", "--name-only"), "the later append stays uncommitted")
		assert.Empty(t, mustGitCapture(t, work, "diff", "--cached", "--name-only"))
		assert.NoFileExists(t, filepath.Join(work, filepath.FromSlash(pendingRedactionAmendRel)))

		require.NoError(t, gitutil.PushWithRetry(context.Background(), work, gitutil.PushOpts{}))
		assert.Equal(t, mustGitCapture(t, work, "rev-parse", "HEAD"), mustGitCapture(t, remote, "rev-parse", "main"))
		assertRemoteObjectsCleanOf(t, remote, canary)
	})

	t.Run("newer HEAD is never amended", func(t *testing.T) {
		work, remote, _ := setup(t)
		other := filepath.Join(work, "sessions", "other", "summary.md")
		require.NoError(t, os.MkdirAll(filepath.Dir(other), 0o755))
		require.NoError(t, os.WriteFile(other, []byte("unrelated\n"), 0o644))
		mustGit(t, work, "add", "--sparse", "sessions/other/summary.md")
		mustGit(t, work, "commit", "-m", "newer snapshot", "--no-verify")
		newer := mustGitCapture(t, work, "rev-parse", "HEAD")

		err := runGate(t, work)
		require.ErrorContains(t, err, "newer Ledger commits")
		assert.Equal(t, newer, mustGitCapture(t, work, "rev-parse", "HEAD"))
		assert.FileExists(t, filepath.Join(work, filepath.FromSlash(pendingRedactionAmendRel)))
		require.Error(t, gitutil.PushWithRetry(context.Background(), work, gitutil.PushOpts{}))
		assertRemoteObjectsCleanOf(t, remote, canary)

		// Once history no longer contains the holding commit the record is obsolete.
		mustGit(t, work, "reset", "--hard", "origin/main")
		require.NoError(t, runGate(t, work))
		assert.NoFileExists(t, filepath.Join(work, filepath.FromSlash(pendingRedactionAmendRel)))
	})

	t.Run("another writer's staged bytes are kept", func(t *testing.T) {
		work, remote, holding := setup(t)
		require.NoError(t, os.WriteFile(filepath.Join(work, rel), []byte(`{"text":"other writer"}`+"\n"), 0o644))
		mustGit(t, work, "add", "--sparse", rel)
		staged := mustGitCapture(t, work, "rev-parse", ":"+rel)

		err := runGate(t, work)
		require.ErrorContains(t, err, "refusing to replace staged changes")
		assert.Equal(t, holding, mustGitCapture(t, work, "rev-parse", "HEAD"))
		assert.Equal(t, staged, mustGitCapture(t, work, "rev-parse", ":"+rel))
		require.Error(t, gitutil.PushWithRetry(context.Background(), work, gitutil.PushOpts{}))
		assertRemoteObjectsCleanOf(t, remote, canary)
	})

	t.Run("another writer's staged mode is kept", func(t *testing.T) {
		work, remote, holding := setup(t)
		mustGit(t, work, "update-index", "--chmod=+x", "--", rel)

		err := runGate(t, work)
		require.ErrorContains(t, err, "refusing to replace staged changes")
		assert.Equal(t, holding, mustGitCapture(t, work, "rev-parse", "HEAD"))
		assert.Contains(t, mustGitCapture(t, work, "ls-files", "--stage", "--", rel), "100755")
		require.Error(t, gitutil.PushWithRetry(context.Background(), work, gitutil.PushOpts{}))
		assertRemoteObjectsCleanOf(t, remote, canary)
	})
}

func runGate(t *testing.T, work string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return gitutil.WithRepoLock(ctx, work, func() error {
		return runPrePushSecretGate(ctx, work)
	})
}
