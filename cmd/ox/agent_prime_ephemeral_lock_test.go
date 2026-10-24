package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/fileutil"
)

// Two primes share one team-context directory. The whole materialization must
// hold the directory's lock, or one prime can clear a stage directory the other
// is still writing. Observed from inside the write: a second acquisition of
// the same lock must time out while the first write is in flight.
func TestWriteTeamContextLocked_HoldsTheDirectoryLockForTheWholeWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "team-ctx")
	orig := materializeTeamContext
	t.Cleanup(func() { materializeTeamContext = orig })

	var peerErr error
	materializeTeamContext = func(teamCtxDir string, _ *api.TeamContextContentResponse) error {
		done := make(chan struct{})
		go func() {
			defer close(done)
			peerErr = fileutil.WithFileLockTimeout(context.Background(), teamCtxDir, 50*time.Millisecond, func() error { return nil })
		}()
		<-done
		return nil
	}

	require.NoError(t, writeTeamContextLocked(context.Background(), dir, &api.TeamContextContentResponse{}))
	require.Error(t, peerErr, "a second prime got the lock while the first was still writing")
}
