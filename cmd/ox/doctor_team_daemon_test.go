package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExtraTeamContextsFromStatus covers ox-baz5.5: checkTeamContextHealth's
// LFS nested-pointer scan used to walk only localCfg.TeamContexts — the
// repo's declared list — and silently never visit a team-context clone the
// daemon syncs for another reason (observed: a secondary "SageOx Internal"
// clone that was diverged and permanently dirty, while doctor's "Team
// Context" section reported clean in ~23ms because it never looked). This is
// the pure filtering logic behind daemonSyncedTeamContexts, tested without a
// live daemon IPC connection.
func TestExtraTeamContextsFromStatus(t *testing.T) {
	t.Run("daemon-synced context missing from configured is surfaced", func(t *testing.T) {
		status := &daemon.StatusData{
			Workspaces: map[string][]daemon.WorkspaceSyncStatus{
				"team-context": {
					{Path: "/configured/path", Exists: true, TeamID: "t1", TeamName: "Configured"},
					{Path: "/other/path", Exists: true, TeamID: "t2", TeamName: "Other Team"},
				},
			},
		}
		configured := []config.TeamContext{{Path: "/configured/path", TeamID: "t1"}}

		extra := extraTeamContextsFromStatus(status, configured)

		assert.Len(t, extra, 1, "must surface exactly the team context missing from configured")
		assert.Equal(t, "/other/path", extra[0].Path)
		assert.Equal(t, "t2", extra[0].TeamID)
		assert.Equal(t, "Other Team", extra[0].TeamName)
	})

	t.Run("fully configured daemon workspaces produce nothing extra", func(t *testing.T) {
		status := &daemon.StatusData{
			Workspaces: map[string][]daemon.WorkspaceSyncStatus{
				"team-context": {
					{Path: "/configured/path", Exists: true, TeamID: "t1"},
				},
			},
		}
		configured := []config.TeamContext{{Path: "/configured/path", TeamID: "t1"}}

		assert.Empty(t, extraTeamContextsFromStatus(status, configured),
			"must not re-surface a context the caller already scans")
	})

	t.Run("non-existent daemon workspace is skipped", func(t *testing.T) {
		// Exists=false means the daemon knows about the workspace but hasn't
		// cloned it yet — nothing on disk to scan for nested LFS pointers.
		status := &daemon.StatusData{
			Workspaces: map[string][]daemon.WorkspaceSyncStatus{
				"team-context": {
					{Path: "/not/cloned/yet", Exists: false, TeamID: "t3"},
				},
			},
		}
		assert.Empty(t, extraTeamContextsFromStatus(status, nil))
	})

	t.Run("no team-context key in workspaces produces nothing", func(t *testing.T) {
		status := &daemon.StatusData{Workspaces: map[string][]daemon.WorkspaceSyncStatus{
			"ledger": {{Path: "/ledger/path", Exists: true}},
		}}
		assert.Empty(t, extraTeamContextsFromStatus(status, nil))
	})

	t.Run("nil configured list still dedupes against itself", func(t *testing.T) {
		status := &daemon.StatusData{
			Workspaces: map[string][]daemon.WorkspaceSyncStatus{
				"team-context": {
					{Path: "/a", Exists: true, TeamID: "a"},
					{Path: "/b", Exists: true, TeamID: "b"},
				},
			},
		}
		extra := extraTeamContextsFromStatus(status, nil)
		assert.Len(t, extra, 2, "with nothing configured, every existing daemon-synced context is extra")
	})
}

// TestDaemonSyncedTeamContexts_NoDaemon proves the fail-safe direction: when
// no daemon is reachable (or ping times out), the function degrades to "no
// extra contexts" rather than erroring or blocking the doctor check it feeds.
// This scan is a best-effort supplement to the configured list, never a
// replacement — losing it must never make checkTeamContextHealth fail.
//
// The client is pointed at a socket path guaranteed not to exist, rather
// than relying on "no daemon happens to be running for this test process's
// resolved workspace" — daemon.CurrentWorkspaceID() memoizes via sync.Once
// per process, so that assumption can silently break depending on test
// execution order and the host environment (flagged by CodeRabbit on #877).
func TestDaemonSyncedTeamContexts_NoDaemon(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "definitely-does-not-exist.sock")
	client := daemon.NewClientWithSocket(sock)

	got := daemonSyncedTeamContextsVia(client, []config.TeamContext{{Path: "/x"}})
	assert.Nil(t, got)
}

// TestDaemonSyncedTeamContexts_NilClient covers the other fail-safe input:
// daemonSyncedTeamContexts itself can hand daemonSyncedTeamContextsVia a nil
// *daemon.Client (NewClientForCurrentRepoWithTimeout's documented zero case);
// this must degrade the same way, not panic.
func TestDaemonSyncedTeamContexts_NilClient(t *testing.T) {
	got := daemonSyncedTeamContextsVia(nil, []config.TeamContext{{Path: "/x"}})
	assert.Nil(t, got)
}

// TestDaemonSyncedTeamContexts_Success drives daemonSyncedTeamContextsVia
// through a real (fake) IPC round trip — ping, then status — against a
// minimal Unix-socket server speaking the daemon's actual newline-delimited
// JSON protocol. This is the path TestDaemonSyncedTeamContexts_NoDaemon
// can't reach: a daemon that responds and reports a real extra context.
func TestDaemonSyncedTeamContexts_Success(t *testing.T) {
	sock := startFakeDaemon(t, func(msg daemon.Message) daemon.Response {
		switch msg.Type {
		case daemon.MsgTypePing:
			return daemon.Response{Success: true}
		case daemon.MsgTypeStatus:
			status := daemon.StatusData{
				Workspaces: map[string][]daemon.WorkspaceSyncStatus{
					"team-context": {
						{Path: "/other/path", Exists: true, TeamID: "t2", TeamName: "Other Team"},
					},
				},
			}
			data, err := json.Marshal(status)
			require.NoError(t, err)
			return daemon.Response{Success: true, Data: data}
		default:
			return daemon.Response{Success: false, Error: "unexpected message type in test fake: " + msg.Type}
		}
	})

	got := daemonSyncedTeamContextsVia(daemon.NewClientWithSocket(sock), nil)

	assert.Len(t, got, 1)
	assert.Equal(t, "/other/path", got[0].Path)
	assert.Equal(t, "Other Team", got[0].TeamName)
}

// TestDaemonSyncedTeamContexts_CallsThroughToVia is a smoke test for
// daemonSyncedTeamContexts itself — the thin CWD-based wrapper that
// TestDaemonSyncedTeamContexts_Success and _NoDaemon deliberately bypass by
// injecting a client. It can't control what daemon (if any) answers for the
// test process's real working directory, so it only asserts the call
// completes without panicking; the behavior for every daemon-response shape
// is already proven against daemonSyncedTeamContextsVia above.
func TestDaemonSyncedTeamContexts_CallsThroughToVia(t *testing.T) {
	assert.NotPanics(t, func() {
		daemonSyncedTeamContexts(nil)
	})
}

// startFakeDaemon starts a minimal Unix-socket server speaking the daemon
// IPC wire format (one newline-delimited JSON Message in, one
// newline-delimited JSON Response out) and returns its socket path. respond
// computes the reply for each received message; the server handles exactly
// one message per connection, matching Client.sendMessage's connect-write-
// read-close pattern.
func startFakeDaemon(t *testing.T, respond func(daemon.Message) daemon.Response) string {
	t.Helper()
	// os.TempDir(), not t.TempDir(): a long test name nests t.TempDir() deep
	// enough to exceed macOS's ~104-char AF_UNIX path limit ("bind: invalid
	// argument"). Same workaround as internal/daemon/friction_test.go.
	sock := filepath.Join(os.TempDir(), fmt.Sprintf("ox-fake-daemon-%d.sock", time.Now().UnixNano()))
	t.Cleanup(func() { os.Remove(sock) })
	listener, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				line, err := bufio.NewReader(c).ReadBytes('\n')
				if err != nil {
					return
				}
				var msg daemon.Message
				if err := json.Unmarshal(line, &msg); err != nil {
					return
				}
				data, err := json.Marshal(respond(msg))
				if err != nil {
					return
				}
				data = append(data, '\n')
				_, _ = c.Write(data)
			}(conn)
		}
	}()

	return sock
}

// TestScanExtraTeamContexts covers the actual scan-and-report behavior
// ox-baz5.5 exists to restore: a daemon-synced-but-unconfigured team context
// with a real nested LFS pointer must produce a doctor warning, not silently
// pass. Uses the same nested-pointer fixture shape as
// TestFindDoubleEncodedLFSPointerPaths (doctor_team_lfs_test.go), reused
// here against the "extra" (daemon-only) code path instead of the configured
// one.
func TestScanExtraTeamContexts(t *testing.T) {
	t.Run("nested pointer in an extra context produces a warning", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not installed")
		}
		repo := t.TempDir()
		run := func(args ...string) {
			t.Helper()
			cmd := exec.Command("git", args...)
			cmd.Dir = repo
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "git %v: %s", args, out)
		}
		run("init", "-q")
		run("config", "user.email", "test@example.com")
		run("config", "user.name", "Test User")

		inner := []byte(lfs.FormatPointer("sha256:inner", 9914))
		outer := lfs.FormatPointer("sha256:outer", int64(len(inner)))
		path := filepath.Join(repo, "frame.jpg")
		require.NoError(t, os.WriteFile(path, []byte(outer), 0o644))
		run("add", "frame.jpg")
		run("commit", "-q", "-m", "outer pointer")
		require.NoError(t, os.WriteFile(path, inner, 0o644))

		extra := []config.TeamContext{{TeamID: "other", TeamName: "Other Team", Path: repo}}
		checks := scanExtraTeamContexts(extra, doctorOptions{fix: false})

		require.Len(t, checks, 1)
		assert.True(t, checks[0].warning)
		assert.Contains(t, checks[0].name, "Other Team")
	})

	t.Run("clean extra context produces no checks", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not installed")
		}
		repo := t.TempDir()
		cmd := exec.Command("git", "init", "-q", repo)
		require.NoError(t, cmd.Run())

		extra := []config.TeamContext{{TeamID: "other", Path: repo}}
		assert.Empty(t, scanExtraTeamContexts(extra, doctorOptions{fix: false}))
	})

	t.Run("empty extra list produces no checks", func(t *testing.T) {
		assert.Empty(t, scanExtraTeamContexts(nil, doctorOptions{fix: false}))
	})
}
