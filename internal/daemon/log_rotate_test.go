package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeSparseFile creates a file of the given logical size without writing its
// bytes, so size-threshold tests do not spend 10 MB of disk and time.
func writeSparseFile(t *testing.T, path string, size int64) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(size))
	require.NoError(t, f.Close())
}

// isolateDaemonLogs points both daemon log locations at throwaway directories:
// the persistent log through XDG_STATE_HOME, the /tmp fallback through the
// $USER paths.TempDir is keyed on. Without it, a test that creates or removes
// logs touches the developer's real ones (2026-10-07: the real /tmp log
// directory vanished twice while this package's tests ran, and every running
// daemon lost its log file).
func isolateDaemonLogs(t *testing.T) (persistent, temp string) {
	t.Helper()
	stateHome := t.TempDir()
	// the random part of the test's temp directory, unique across processes and
	// runs, which the cleanup below relies on; t.TempDir()'s own base name is a
	// per-test counter ("001")
	user := "ox-test-" + filepath.Base(filepath.Dir(stateHome))
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("USER", user)
	t.Setenv("USERNAME", user)

	persistent, temp = logPaths()
	require.True(t, strings.HasPrefix(persistent, stateHome), "persistent log %q must be under the test's XDG_STATE_HOME", persistent)
	tempRoot := filepath.Dir(paths.TempDir())
	require.Equal(t, user, filepath.Base(tempRoot), "the /tmp log must belong to the test identity, never the developer's")
	t.Cleanup(func() { _ = os.RemoveAll(tempRoot) })
	return persistent, temp
}

// TestRotateDaemonLog pins the startup rotation rule.
//
// Failure prevented: nothing ever rotated the daemon log, so one coworker's
// log reached 96 MB / 528k lines in a week. The daemon opens it O_APPEND and
// inherits it as stdout/stderr, so the only safe place to rotate is before that
// open, and a rotation problem must never stop the daemon from starting.
func TestRotateDaemonLog(t *testing.T) {
	now := time.Date(2026, 10, 28, 20, 48, 58, 0, time.UTC)
	tests := []struct {
		name        string
		size        int64 // -1: no log file
		blockRotted bool  // the rotated name is a non-empty directory, so the rename fails
		wantRotated bool
	}{
		{name: "over the limit is rotated", size: maxDaemonLogBytes + 1, wantRotated: true},
		{name: "exactly at the limit is left alone", size: maxDaemonLogBytes},
		{name: "small log is left alone", size: 1024},
		{name: "empty log is left alone", size: 0},
		{name: "missing log is a no-op", size: -1},
		{name: "rename failure leaves the log in place and does not error", size: maxDaemonLogBytes + 1, blockRotted: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "daemon.log")
			// timestamped, so it never replaces an earlier generation
			rotatedPath := logPath + ".20261028T204858Z"

			if tt.size >= 0 {
				writeSparseFile(t, logPath, tt.size)
			}
			if tt.blockRotted {
				require.NoError(t, os.MkdirAll(filepath.Join(rotatedPath, "child"), 0o700))
			}

			got := rotateDaemonLog(logPath, now)

			assert.Equal(t, tt.wantRotated, got, "return value")
			if tt.wantRotated {
				_, err := os.Stat(logPath)
				assert.True(t, os.IsNotExist(err), "original path must be free for a fresh log")
				info, err := os.Stat(rotatedPath)
				require.NoError(t, err)
				assert.Equal(t, tt.size, info.Size(), "the timestamped generation must hold the rotated content")
				return
			}
			if tt.size >= 0 {
				info, err := os.Stat(logPath)
				require.NoError(t, err, "log must still exist when not rotated")
				assert.Equal(t, tt.size, info.Size(), "log must be untouched when not rotated")
			}
		})
	}
}

// TestPruneDaemonLogs pins the retention rule.
//
// Failure prevented: daemon logs moved out of /tmp, which the OS emptied at
// boot, so nothing else deletes them now. Without expiry and a size cap, every
// rotated generation and every abandoned workspace's log stays on disk forever.
func TestPruneDaemonLogs(t *testing.T) {
	now := time.Now()
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	const day = 24 * time.Hour

	write := func(t *testing.T, dir, name string, size int64, modified time.Time) {
		t.Helper()
		path := filepath.Join(dir, name)
		writeSparseFile(t, path, size)
		require.NoError(t, os.Chtimes(path, modified, modified))
	}
	remaining := func(t *testing.T, dir string) []string {
		t.Helper()
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		return names
	}

	t.Run("deletes daemon logs last written more than 30 days ago", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "daemon_repo_ws.log", 1, ago(time.Hour))
		write(t, dir, "daemon_repo_ws.log.20261001T000000Z", 1, ago(29*day))
		write(t, dir, "daemon_repo_ws.log.20260901T000000Z", 1, ago(31*day))
		write(t, dir, "daemon_repo_gone.log", 1, ago(31*day)) // a workspace no daemon has started since
		write(t, dir, "agent-payload.log", 1, ago(31*day))    // not a daemon log

		pruneDaemonLogs(dir, now)

		assert.ElementsMatch(t,
			[]string{"agent-payload.log", "daemon_repo_ws.log", "daemon_repo_ws.log.20261001T000000Z"},
			remaining(t, dir))
	})

	t.Run("deletes the oldest rotated logs until the directory fits the cap", func(t *testing.T) {
		dir := t.TempDir()
		quarter := int64(maxDaemonLogDirBytes / 4)
		// the oldest file is the current log: it counts toward the cap, but a
		// running daemon may be writing it, so the cap never deletes it
		write(t, dir, "daemon_repo_ws.log", 2*quarter, ago(4*day))
		write(t, dir, "daemon_repo_ws.log.1", quarter, ago(3*day))
		write(t, dir, "daemon_repo_ws.log.2", quarter, ago(2*day))
		write(t, dir, "daemon_repo_ws.log.3", quarter, ago(day))

		pruneDaemonLogs(dir, now)

		assert.ElementsMatch(t,
			[]string{"daemon_repo_ws.log", "daemon_repo_ws.log.2", "daemon_repo_ws.log.3"},
			remaining(t, dir))
	})

	// one daemon logging in a loop must not evict another workspace's history,
	// even history older than its own
	t.Run("takes the oldest rotated logs of the workspace holding the most", func(t *testing.T) {
		dir := t.TempDir()
		quarter := int64(maxDaemonLogDirBytes / 4)
		write(t, dir, "daemon_repo_quiet.log.1", 1, ago(20*day))
		write(t, dir, "daemon_repo_quiet.log.2", 1, ago(10*day))
		write(t, dir, "daemon_repo_runaway.log.1", 2*quarter, ago(3*day))
		write(t, dir, "daemon_repo_runaway.log.2", 2*quarter, ago(2*day))
		write(t, dir, "daemon_repo_runaway.log.3", quarter, ago(day))

		pruneDaemonLogs(dir, now)

		assert.ElementsMatch(t,
			[]string{"daemon_repo_quiet.log.1", "daemon_repo_quiet.log.2", "daemon_repo_runaway.log.2", "daemon_repo_runaway.log.3"},
			remaining(t, dir))
	})

	// a runaway's log is rotated by the restart that investigates it, and the
	// prune in that same start must not delete it
	t.Run("keeps each workspace's newest rotated log over the cap", func(t *testing.T) {
		dir := t.TempDir()
		quarter := int64(maxDaemonLogDirBytes / 4)
		write(t, dir, "daemon_repo_a.log", 1, ago(time.Minute))
		write(t, dir, "daemon_repo_a.log.1", quarter, ago(5*day))
		write(t, dir, "daemon_repo_a.log.2", 5*quarter, ago(time.Hour)) // the runaway, just rotated
		write(t, dir, "daemon_repo_b.log.1", quarter, ago(4*day))
		write(t, dir, "daemon_repo_b.log.2", quarter, ago(2*day))

		pruneDaemonLogs(dir, now)

		// still over the cap, but what remains is the runaway's to answer for
		assert.ElementsMatch(t,
			[]string{"daemon_repo_a.log", "daemon_repo_a.log.2", "daemon_repo_b.log.1", "daemon_repo_b.log.2"},
			remaining(t, dir))
	})
}

// Failure prevented: an unlocked `ox daemon restart` racing an auto-start
// renamed the log the other had just created onto the generation it had just
// made, in the same second, replacing that history with an empty file.
func TestOpenLogFile_ConcurrentStartsKeepTheRotatedLog(t *testing.T) {
	for i := 0; i < 20; i++ {
		dir := t.TempDir()
		path := filepath.Join(dir, "daemon_repo_ws.log")
		writeSparseFile(t, path, maxDaemonLogBytes+1)

		var wg sync.WaitGroup
		for j := 0; j < 4; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				f, err := openLogFile(path)
				if assert.NoError(t, err) {
					_ = f.Close()
				}
			}()
		}
		wg.Wait()

		generations, err := filepath.Glob(path + ".*")
		require.NoError(t, err)
		require.Len(t, generations, 1, "one rotation, not one per start")
		info, err := os.Stat(generations[0])
		require.NoError(t, err)
		require.Equal(t, int64(maxDaemonLogBytes+1), info.Size(), "the rotated history must survive the race")
	}
}

// Failure prevented: with no home directory the persistent log path is
// relative, and OpenLog created it in the spawning command's working
// directory, usually the user's repository.
func TestOpenLogFile_RefusesARelativePath(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)

	_, err := openLogFile(filepath.Join("sageox", "logs", "daemon__ws.log"))

	require.ErrorContains(t, err, "not absolute")
	assert.NoDirExists(t, filepath.Join(cwd, "sageox"))
}

// Failure prevented: a start that cannot get the log lock, because another
// start holds it, must still open the log and start its daemon; only rotation
// and pruning wait for a later start.
func TestOpenLogFile_BusyLockStillOpensTheLog(t *testing.T) {
	if testing.Short() {
		t.Skip("short: waits out the 1 s log lock")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon_repo_ws.log")
	writeSparseFile(t, path, maxDaemonLogBytes+1)
	held, release := make(chan struct{}), make(chan struct{})
	go func() {
		_ = fileutil.WithFileLockTimeout(context.Background(), dir, time.Second, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	defer close(release)

	f, err := openLogFile(path)

	require.NoError(t, err)
	require.NoError(t, f.Close())
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, int64(maxDaemonLogBytes+1), info.Size(), "rotation waits for a start that gets the lock")
}

// Failure prevented: a session started in a subdirectory (repo/web) spawned
// the daemon from there, and its log was named after that subdirectory, so
// `ox daemon logs` at the project root never found it.
func TestLogPaths_NameTheProjectNotTheSubdirectory(t *testing.T) {
	t.Setenv(config.EnvProjectRoot, "")
	root := createInitializedDir(t, "repo_0a1b2c3d-0000-4000-8000-000000000001")
	sub := filepath.Join(root, "web")
	require.NoError(t, os.Mkdir(sub, 0o700))

	t.Chdir(root)
	atRoot, _ := logPaths()
	t.Chdir(sub)
	inSub, _ := logPaths()

	assert.Equal(t, atRoot, inSub)
	assert.Contains(t, filepath.Base(atRoot), "repo_0a1b2c3d")
}

// TestLogPath_ReadsTheLogTheDaemonWrites pins which log `ox daemon logs` shows.
//
// Failure prevented: a daemon writes the file it was spawned with, and that is
// not always the persistent current log. One spawned by an older ox keeps its
// /tmp log after re-executing the upgraded binary, one that cannot use the
// persistent directory logs to /tmp, and one that slept out a restart-loop
// throttle while another start rotated its log writes the rotated generation.
// Reading only the persistent current log shows a dead daemon's output, or
// none, while the live one writes elsewhere.
func TestLogPath_ReadsTheLogTheDaemonWrites(t *testing.T) {
	tests := []struct {
		name string
		ages map[string]time.Duration // the logs that exist, by role, and how long ago each was written
		want string                   // the role LogPath must return
	}{
		{name: "no log yet", want: "persistent"},
		{name: "only a /tmp log, from a daemon upgraded in place", ages: map[string]time.Duration{"temp": 0}, want: "temp"},
		{name: "/tmp log written last, by a daemon that fell back", ages: map[string]time.Duration{"persistent": time.Hour, "temp": 0}, want: "temp"},
		{name: "persistent log written last, /tmp log left from before", ages: map[string]time.Duration{"persistent": 0, "temp": time.Hour}, want: "persistent"},
		{name: "generation written last, by a daemon whose log was rotated while it was throttled", ages: map[string]time.Duration{"persistent": time.Hour, "generation": 0}, want: "generation"},
		{name: "another workspace's log written last is not this workspace's", ages: map[string]time.Duration{"persistent": time.Hour, "other": 0}, want: "persistent"},
		{name: "/tmp generation written last, by a fallen-back daemon whose log was rotated", ages: map[string]time.Duration{"persistent": time.Hour, "tempGeneration": 0}, want: "tempGeneration"},
		{name: "another workspace's /tmp log written last is not this workspace's", ages: map[string]time.Duration{"persistent": time.Hour, "tempOther": 0}, want: "persistent"},
		{name: "a lookalike log from a repo_id embedding this log's name is not this workspace's", ages: map[string]time.Duration{"persistent": time.Hour, "lookalike": 0}, want: "persistent"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			persistent, temp := isolateDaemonLogs(t)
			path := map[string]string{
				"persistent":     persistent,
				"temp":           temp,
				"generation":     persistent + ".20261028T204858Z",
				"other":          filepath.Join(filepath.Dir(persistent), "daemon_repo_other_0badc0de.log"),
				"tempGeneration": temp + ".20261028T204858Z",
				"tempOther":      filepath.Join(filepath.Dir(temp), "daemon_repo_other_0badc0de.log"),
				"lookalike":      persistent + ".x_0badc0de.log",
			}
			now := time.Now()
			for role, age := range tt.ages {
				require.NoError(t, os.MkdirAll(filepath.Dir(path[role]), 0o700))
				require.NoError(t, os.WriteFile(path[role], nil, 0o600))
				require.NoError(t, os.Chtimes(path[role], now.Add(-age), now.Add(-age)))
			}

			assert.Equal(t, path[tt.want], LogPath())
		})
	}
}
