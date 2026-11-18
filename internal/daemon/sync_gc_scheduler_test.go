package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

type gcSchedulerLog struct {
	slog.Handler
	onRecord func(slog.Record)
}

func (h *gcSchedulerLog) Handle(ctx context.Context, record slog.Record) error {
	h.onRecord(record)
	return h.Handler.Handle(ctx, record)
}

// A stalled preservation push must not stop scheduler heartbeats, and
// rejection must leave the Ledger and the GC lock ready for another sync.
func TestPeriodicGC_HeartbeatContinuesDuringRejectedLedgerPush(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a shell Git hook")
	}
	isolateCredentials(t)
	bare, local, _ := setupRacingLedger(t)
	gitInDir(t, local, "commit", "--allow-empty", "-m", "local-only change")
	localTip, err := runGitOut(t, local, "rev-parse", "HEAD")
	require.NoError(t, err, localTip)
	remoteTip, err := runGitOut(t, bare, "rev-parse", "HEAD")
	require.NoError(t, err, remoteTip)
	require.NoError(t, os.WriteFile(filepath.Join(bare, "hooks", "pre-receive"), []byte("#!/bin/sh\nexit 1\n"), 0o700))
	s := gcTestScheduler(t)
	s.config.LedgerPath = local
	s.config.SyncIntervalRead = time.Hour
	s.config.TeamContextSyncInterval = 0
	s.config.VersionCheckInterval = 0
	s.config.GCCheckInterval = time.Minute
	s.config.CodeDBCheckInterval = 0
	s.config.LedgerCheckInterval = 0
	s.config.GitHubSyncInterval = 0

	synctest.Test(t, func(t *testing.T) {
		s.workspaceRegistry.mu.Lock()
		s.workspaceRegistry.configCacheDuration = time.Hour
		primeConfigCacheLocked(s.workspaceRegistry)
		s.workspaceRegistry.ledger = &WorkspaceState{
			ID: "ledger", Type: WorkspaceTypeLedger, Path: local, Exists: true, CloneURL: bare,
		}
		s.workspaceRegistry.mu.Unlock()
		s.mu.Lock()
		s.lastWedgeCheck = time.Now()
		s.mu.Unlock()
		s.triggerChan = make(chan struct{}, 1)
		pushStarted, releasePush := make(chan struct{}), make(chan struct{})
		pushError := make(chan string, 1)
		initialHeartbeat, nextHeartbeat := make(chan struct{}), make(chan struct{})
		heartbeatCount := 0
		var firstPush sync.Once
		var firstRejection sync.Once
		var release sync.Once
		s.logger = slog.New(&gcSchedulerLog{
			Handler: slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}),
			onRecord: func(record slog.Record) {
				switch record.Message {
				case "no endpoint available for heartbeat":
					heartbeatCount++
					switch heartbeatCount {
					case 1:
						close(initialHeartbeat)
					case 2:
						close(nextHeartbeat)
					}
				case "gc: pushing unpushed commits before reclone":
					firstPush.Do(func() {
						close(pushStarted)
						<-releasePush
					})
				case "gc: skipping reclone, cannot push unpushed commits":
					firstRejection.Do(func() {
						record.Attrs(func(attr slog.Attr) bool {
							if attr.Key == "error" {
								pushError <- attr.Value.String()
								return false
							}
							return true
						})
					})
				}
			},
		})
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		defer func() {
			cancel()
			release.Do(func() { close(releasePush) })
			<-done
		}()
		go func() {
			defer close(done)
			s.Start(ctx)
		}()
		await := func(ch <-chan struct{}, message string) {
			t.Helper()
			select {
			case <-ch:
			case <-time.After(6 * time.Minute):
				t.Fatal(message)
			}
		}
		await(initialHeartbeat, "scheduler did not start")
		await(pushStarted, "periodic GC did not reach the preservation push")
		await(nextHeartbeat, "periodic GC blocked the scheduler's next heartbeat")
		release.Do(func() { close(releasePush) })
		select {
		case err := <-pushError:
			require.True(t, strings.Contains(err, "pre-receive hook declined"), err)
		case <-time.After(time.Minute):
			t.Fatal("expected the bare remote to reject the GC push")
		}
		synctest.Wait()
		require.Zero(t, atomic.LoadInt32(&s.gcInProgress), "GC must release single-flight after rejection")
	})
	gotLocal, err := runGitOut(t, local, "rev-parse", "HEAD")
	require.NoError(t, err, gotLocal)
	gotRemote, err := runGitOut(t, bare, "rev-parse", "HEAD")
	require.NoError(t, err, gotRemote)
	require.Equal(t, localTip, gotLocal)
	require.Equal(t, remoteTip, gotRemote)
	require.NoFileExists(t, local+".gc-swap-lock")
}
