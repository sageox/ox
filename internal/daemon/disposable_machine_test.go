package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/require"
)

// onNewMachine gives the test an empty daemon state dir, as a machine has
// before its first daemon start.
func onNewMachine(t *testing.T) {
	t.Helper()
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
}

// Failure prevented: a disposable machine reuses a Ledger that a machine which
// went away mid-command left locked. The stale-lock sweep keeps a fresh
// index.lock for an hour and never removes a ref lock, so every commit and
// fetch in the Ledger fails for most of the job.
func TestRemoveLedgerLocksAtStart(t *testing.T) {
	newLedger := func(t *testing.T) string {
		ledger, _ := initGitRepoWithCommit(t)
		return ledger
	}
	// leaveLocks writes the locks a git killed mid-commit and mid-fetch leaves,
	// as fresh as a crash a minute ago.
	leaveLocks := func(t *testing.T, ledger string) {
		branch, err := exec.Command("git", "-C", ledger, "symbolic-ref", "--short", "HEAD").Output()
		require.NoError(t, err)
		for _, lock := range []string{
			"index.lock",
			"packed-refs.lock",
			"refs/heads/" + strings.TrimSpace(string(branch)) + ".lock",
			"refs/remotes/origin/main.lock",
		} {
			path := filepath.Join(ledger, ".git", lock)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.WriteFile(path, nil, 0o644))
		}
	}
	start := func(ledger string, disposable bool) {
		d := New(&Config{LedgerPath: ledger, DisposableMachine: disposable}, discardLogger())
		d.ctx = context.Background()
		d.removeLedgerLocksAtStart()
	}
	commit := func(ledger string) error {
		return exec.Command("git", "-C", ledger, "commit", "--allow-empty", "-q", "-m", "work after the start").Run()
	}

	t.Run("a disposable machine's first start clears every lock", func(t *testing.T) {
		onNewMachine(t)
		ledger := newLedger(t)
		leaveLocks(t, ledger)

		start(ledger, true)

		require.NoError(t, commit(ledger), "the Ledger must take commits again")
		require.NoFileExists(t, filepath.Join(ledger, ".git", "refs", "remotes", "origin", "main.lock"))
	})

	t.Run("a later start on the same machine leaves locks to their holders", func(t *testing.T) {
		onNewMachine(t)
		ledger := newLedger(t)
		start(ledger, true)
		leaveLocks(t, ledger) // as a git on this machine holds them

		start(ledger, true)

		require.FileExists(t, filepath.Join(ledger, ".git", "index.lock"))
		require.Error(t, commit(ledger))
	})

	t.Run("a daemon that waited for the lock leaves alone what the first one's git holds", func(t *testing.T) {
		onNewMachine(t)
		ledger := newLedger(t)
		synctest.Test(t, func(t *testing.T) {
			require.NoError(t, gitutil.WithRepoLock(context.Background(), ledger, func() error {
				go start(ledger, true)
				// The second start has passed its first marker check and is
				// blocked on the in-process repo lock before we write the marker.
				synctest.Wait()
				require.NoError(t, markLedgerLocksCleared(ledger))
				leaveLocks(t, ledger)
				return nil
			}))
			synctest.Wait()

			require.FileExists(t, filepath.Join(ledger, ".git", "index.lock"))
		})
	})

	for _, afterWaiting := range []bool{false, true} {
		t.Run(fmt.Sprintf("an unreadable marker preserves locks after_waiting=%v", afterWaiting), func(t *testing.T) {
			onNewMachine(t)
			ledger := newLedger(t)
			leaveLocks(t, ledger)
			marker := ledgerLocksClearedMarker(ledger)
			require.NoError(t, os.MkdirAll(filepath.Dir(marker), 0o700))
			breakMarker := func(t *testing.T) {
				t.Helper()
				if err := os.Symlink(marker, marker); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				_, err := os.Stat(marker)
				require.Error(t, err)
				require.NotErrorIs(t, err, os.ErrNotExist)
			}
			if !afterWaiting {
				breakMarker(t)
			}
			synctest.Test(t, func(t *testing.T) {
				require.NoError(t, gitutil.WithRepoLock(context.Background(), ledger, func() error {
					go start(ledger, true)
					synctest.Wait()
					if afterWaiting {
						breakMarker(t)
					}
					return nil
				}))
				synctest.Wait()
				require.FileExists(t, filepath.Join(ledger, ".git", "index.lock"))
			})
		})
	}

	t.Run("an unwritable marker preserves locks", func(t *testing.T) {
		onNewMachine(t)
		ledger := newLedger(t)
		leaveLocks(t, ledger)
		marker := ledgerLocksClearedMarker(ledger)
		require.NoError(t, os.MkdirAll(filepath.Dir(marker), 0o700))
		// Stat reports absence, but writing through this dangling symlink fails.
		if err := os.Symlink(filepath.Join(t.TempDir(), "missing", "marker"), marker); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		start(ledger, true)

		require.FileExists(t, filepath.Join(ledger, ".git", "index.lock"))
	})

	t.Run("failed cleanup can be retried", func(t *testing.T) {
		onNewMachine(t)
		ledger := newLedger(t)
		gitDir := filepath.Join(ledger, ".git")
		require.NoError(t, os.Rename(gitDir, gitDir+"-saved"))
		require.NoError(t, os.WriteFile(gitDir, nil, 0o600))

		start(ledger, true)

		require.NoFileExists(t, ledgerLocksClearedMarker(ledger))
		require.NoError(t, os.Remove(gitDir))
		require.NoError(t, os.Rename(gitDir+"-saved", gitDir))
		leaveLocks(t, ledger)
		start(ledger, true)
		require.NoError(t, commit(ledger), "the Ledger must recover on retry")
	})

	t.Run("without the setting fresh locks stay, as before", func(t *testing.T) {
		onNewMachine(t)
		ledger := newLedger(t)
		leaveLocks(t, ledger)

		start(ledger, false)

		require.FileExists(t, filepath.Join(ledger, ".git", "index.lock"))
		require.Error(t, commit(ledger))
	})
}

// Failure prevented: an hour into a job on a disposable machine, GC reclones
// the Ledger the daemon has just cloned.
func TestSyncScheduler_GCCheckInterval(t *testing.T) {
	for _, tc := range []struct {
		name        string
		projectRoot string
		disposable  bool
		want        time.Duration
	}{
		{"a project", "/project", false, time.Hour},
		{"a project on a disposable machine", "/project", true, 0},
		{"no project", "", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.GCCheckInterval = time.Hour
			cfg.ProjectRoot = tc.projectRoot
			cfg.DisposableMachine = tc.disposable
			require.Equal(t, tc.want, NewSyncScheduler(cfg, discardLogger()).gcCheckInterval())
		})
	}
}

// Failure prevented: on a disposable machine the daemon's autofix rewrites
// files in every Ledger under the home, not only this job's.
func TestStartAutofix_OffOnADisposableMachine(t *testing.T) {
	for _, disposable := range []bool{false, true} {
		t.Run(fmt.Sprintf("disposable_machine=%v", disposable), func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.ProjectRoot = t.TempDir()
			cfg.DisposableMachine = disposable
			d := New(cfg, discardLogger())
			ctx, cancel := context.WithCancel(context.Background())
			d.ctx = ctx

			d.startAutofix()
			cancel()
			d.wg.Wait()

			require.Equal(t, !disposable, d.autofixSched != nil, "autofix scheduler started")
		})
	}
}

// Failure prevented: a disposable machine's daemon clones its Ledger mid-run
// and keeps running without the parts that need one (Ledger pulls, session
// finalization, the Ledger watcher) for the rest of the job.
func TestCloneInBackground_FirstLedgerCloneCallsBack(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Git clone")
	}
	for _, startedWithLedger := range []bool{false, true} {
		t.Run(fmt.Sprintf("started_with_ledger=%v", startedWithLedger), func(t *testing.T) {
			s, ledgerPath := newCloneLifecycleScheduler(t, setupCloneLifecycleLedgerHTTP(t))
			if startedWithLedger {
				s.config.LedgerPath = ledgerPath
			}
			var cloned []string
			s.SetLedgerClonedCallback(func(path string) { cloned = append(cloned, path) })

			require.True(t, s.addClone())
			s.cloneInBackground(context.Background(), s.workspaceRegistry.GetLedger().CloneURL, ledgerPath, "ledger", "ledger")

			requireCloneRecorded(t, s, "ledger", ledgerPath)
			if startedWithLedger {
				require.Empty(t, cloned, "a daemon that started with its Ledger has it wired")
			} else {
				require.Equal(t, []string{ledgerPath}, cloned)
			}
		})
	}
}

// Failure prevented: the daemon never restarts after the Ledger's first clone,
// or the restarted daemon clears locks in the clone this machine just made,
// where ox's own git commands may already be running. Driven through
// initComponents, so a restart hook that is never wired in fails it too.
func TestInitComponents_RestartsAfterFirstLedgerClone(t *testing.T) {
	for _, tc := range []struct {
		name              string
		disposable        bool
		startedWithLedger bool
		wantRestart       bool
	}{
		{"disposable machine started without its Ledger", true, false, true},
		{"disposable machine started with its Ledger", true, true, false},
		{"other machines keep running", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledgerPath := ""
			if tc.startedWithLedger {
				ledgerPath = t.TempDir()
			}
			d := initComponentsForTest(t, tc.disposable, ledgerPath)

			if !tc.wantRestart {
				require.Nil(t, d.scheduler.onLedgerCloned)
				return
			}
			require.NotNil(t, d.scheduler.onLedgerCloned, "the daemon never hears that the Ledger's first clone landed")
			cloned := filepath.Join(t.TempDir(), "ledger")
			marker := ledgerLocksClearedMarker(cloned)
			require.NoError(t, os.MkdirAll(marker, 0o700))
			d.scheduler.onLedgerCloned(cloned)
			require.False(t, d.RestartRequested(), "restarting without a marker would clear this machine's live locks")
			require.NoError(t, os.Remove(marker))

			d.scheduler.onLedgerCloned(cloned)
			require.True(t, d.RestartRequested())
			require.FileExists(t, ledgerLocksClearedMarker(cloned), "the restarted daemon must not clear this machine's own locks")
		})
	}
}

// Failure prevented: the version-mismatch restart, now routed through
// requestRestart, no longer restarts the daemon after an ox upgrade.
func TestInitComponents_VersionMismatchRequestsRestart(t *testing.T) {
	d := initComponentsForTest(t, false, "")

	d.heartbeat.Handle("cli", json.RawMessage(`{"cli_version":"999.0.0"}`))

	require.True(t, d.RestartRequested())
}

// Failure prevented: on a disposable machine the daemon stops finalizing
// sessions, so they reach the Ledger without a summary and a crashed one is
// never uploaded. Sessions there finalize as on a laptop, which
// `ox session wait` relies on.
func TestInitComponents_DisposableMachineFinalizesSessionsLikeALaptop(t *testing.T) {
	for _, disposable := range []bool{false, true} {
		t.Run(fmt.Sprintf("disposable_machine=%v", disposable), func(t *testing.T) {
			ledger := t.TempDir()
			d := initComponentsForTest(t, disposable, ledger)
			require.NotNil(t, d.sessionFinalizeHandler, "the daemon finds no session to finalize")

			// a stopped session with a turn, as session end hands one over
			const name = "2026-10-20T10-00-devon-OxFin1"
			dir := filepath.Join(ledger, ".sageox", "cache", "sessions", name)
			require.NoError(t, os.MkdirAll(dir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(`{"type":"user","content":"a turn"}`+"\n"), 0o600))
			(&daemonServiceImpl{d: d}).SessionFinalize(SessionFinalizeIPCPayload{SessionName: name, CachePath: dir})

			require.True(t, d.agentWorker.HasPendingWork(), "the daemon did not take the session handed over at session end")
		})
	}
}

// initComponentsForTest runs the daemon's real component setup with no project,
// against throwaway state, config, and credential directories.
func initComponentsForTest(t *testing.T, disposable bool, ledgerPath string) *Daemon {
	t.Helper()
	onNewMachine(t)
	isolateCredentialsWithDir(t)
	home := t.TempDir()
	for name, dir := range map[string]string{"XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "data", "XDG_CACHE_HOME": "cache"} {
		t.Setenv(name, filepath.Join(home, dir))
	}
	t.Setenv("OX_USER_CONFIG", filepath.Join(home, "user-config.yaml"))
	cfg := DefaultConfig()
	cfg.DisposableMachine = disposable
	cfg.LedgerPath = ledgerPath
	d := New(cfg, discardLogger())
	d.ctx, d.cancel = context.WithCancel(context.Background())
	t.Cleanup(func() {
		d.cancel()
		d.releaseGlobalSyncLease()
	})
	d.initComponents()
	return d
}
