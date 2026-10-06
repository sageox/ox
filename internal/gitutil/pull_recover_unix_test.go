//go:build !windows

package gitutil

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: a pull killed by its timeout leaves .git/rebase-merge;
// every caller of the shared pull helpers (daemon, push retry, doctor) must be
// able to clear it in the same call instead of wedging the next one.
func TestRecoverPullTimeoutInRebase(t *testing.T) {
	f := newLedgerFixture(t)
	f.write(f.local, "local.txt", "local")
	f.commitAll(f.local, "local unpushed")
	f.cloudWrites("remote.txt", "remote", "remote advance")
	f.git(f.local, "fetch", "origin")

	// park a real rebase mid-flight on a `break` step, as a killed pull would
	cmd := exec.Command("git", "rebase", "-i", "--autostash", "@{u}")
	cmd.Dir = f.local
	cmd.Env = append(os.Environ(), // safe: git subprocess in a temp fixture repo, not the ox CLI
		"GIT_SEQUENCE_EDITOR=sed -i.bak '1i\\\nbreak'", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	require.True(t, IsRebaseInProgress(f.local), "fixture must leave a rebase behind")

	found, abortErr := RecoverPullTimeoutInRebase(context.Background(), f.local, "ledger", 1, slog.Default())
	require.NoError(t, abortErr)
	assert.True(t, found)
	assert.False(t, IsRebaseInProgress(f.local))
	assert.NoFileExists(t, filepath.Join(f.local, ".git", "index.lock"))
	assert.Equal(t, "1", f.git(f.local, "rev-list", "--count", "@{u}..HEAD"), "unpushed commit must survive")

	found, abortErr = RecoverPullTimeoutInRebase(context.Background(), f.local, "ledger", 1, slog.Default())
	assert.False(t, found, "no rebase means nothing to recover")
	assert.NoError(t, abortErr)
}

func TestCommitsAhead(t *testing.T) {
	f := newLedgerFixture(t)
	assert.Equal(t, 0, CommitsAhead(context.Background(), f.local), "in sync with upstream")

	f.write(f.local, "a.txt", "a")
	f.commitAll(f.local, "one")
	f.write(f.local, "b.txt", "b")
	f.commitAll(f.local, "two")
	assert.Equal(t, 2, CommitsAhead(context.Background(), f.local))

	// no upstream or not a repo reads as 0 so the default budget applies
	assert.Equal(t, 0, CommitsAhead(context.Background(), t.TempDir()))
}

func TestPullTimedOut(t *testing.T) {
	t.Parallel()
	expired, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-expired.Done()
	canceled, cancel2 := context.WithCancel(context.Background())
	cancel2()
	someErr := errors.New("boom")

	assert.True(t, PullTimedOut(expired, someErr))
	assert.False(t, PullTimedOut(expired, nil), "success is never a timeout")
	assert.False(t, PullTimedOut(canceled, someErr), "caller cancel is not a timeout")
	assert.False(t, PullTimedOut(context.Background(), someErr))
}
