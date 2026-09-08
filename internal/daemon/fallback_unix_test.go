//go:build !windows

package daemon

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/sageox/ox/internal/selfexec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- KillStaleDaemon tests ---

// setupIsolatedRegistry creates an isolated XDG environment and writes a registry
// with the given entries. Returns the tmp dir.
func setupIsolatedRegistry(t *testing.T, entries map[string]DaemonInfo) string {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	reg := &Registry{Daemons: entries}
	data, err := json.MarshalIndent(reg, "", "  ")
	require.NoError(t, err)

	regDir := filepath.Join(tmpDir, "sageox", "daemon")
	require.NoError(t, os.MkdirAll(regDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(regDir, "registry.json"), data, 0600))

	return tmpDir
}

// startFakeOxDaemon starts a stand-in daemon that satisfies matchesOxDaemon:
// argv[0]'s basename is exactly "ox" and "daemon" is a real argument, the same
// shape the production spawn in ensureDaemonImpl produces.
//
// The old fixtures named the script "ox-daemon-fake" and passed no arguments,
// which only ever matched because the identity check was two independent
// substring searches. Tightening that check to protect reused PIDs from SIGKILL
// necessarily invalidates the old shape — so the fixture has to describe a real
// daemon now, which is the point.
//
// shellBody is the child's signal behavior (how it reacts to SIGTERM).
// Returns the child's PID and a channel closed once the child is reaped, so a
// caller can assert the process actually died rather than sleeping.
func startFakeOxDaemon(t *testing.T, shellBody string) (int, <-chan struct{}) {
	t.Helper()
	return startFakeProcess(t, "ox", shellBody)
}

// startFakeProcess runs sh under the given executable name with a daemon-shaped
// argument list: argv is [<dir>/<name>, -c, <body>, daemon, start, --foreground].
//
// It has to be a symlink to a real executable, not a #! script — the kernel
// rewrites a shebang script's argv[0] to the interpreter, so a script always
// presents as /bin/sh and could never carry the argv[0] a real ox daemon has.
// Getting this wrong is how a negative control passes for the wrong reason.
func startFakeProcess(t *testing.T, name, shellBody string) (int, <-chan struct{}) {
	t.Helper()

	sh, err := exec.LookPath("sh")
	require.NoError(t, err)
	fakeExe := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.Symlink(sh, fakeExe))

	child := exec.Command(fakeExe, "-c", shellBody, "daemon", "start", "--foreground")
	require.NoError(t, child.Start())

	done := make(chan struct{})
	go func() {
		_ = child.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = child.Process.Kill()
		<-done
	})
	return child.Process.Pid, done
}

// fakeDaemonExitsOnTerm is a well-behaved daemon: SIGTERM is enough.
const fakeDaemonExitsOnTerm = "trap 'exit 0' TERM; while true; do sleep 1; done"

func TestKillStaleDaemon_NoRegistryEntry(t *testing.T) {
	setupIsolatedRegistry(t, map[string]DaemonInfo{})

	// should be a no-op, no panics
	KillStaleDaemon("nonexistent")
}

func TestKillStaleDaemon_DeadProcess(t *testing.T) {
	wsID := "deadbeef"
	tmpDir := setupIsolatedRegistry(t, map[string]DaemonInfo{
		wsID: {
			WorkspaceID: wsID,
			PID:         999999999, // very unlikely to be alive
			SocketPath:  "/tmp/nonexistent.sock",
		},
	})

	// create stale PID file so cleanup behavior is actually exercised
	pidPath := filepath.Join(tmpDir, "sageox", "daemon", "daemon-"+wsID+".pid")
	require.NoError(t, os.WriteFile(pidPath, []byte("999999999\n"), 0600))

	KillStaleDaemon(wsID)

	// registry entry should be cleaned up
	reg, err := LoadRegistry()
	require.NoError(t, err)
	assert.Nil(t, reg.FindByWorkspaceID(wsID), "dead entry should be removed from registry")

	// stale PID file should be cleaned up
	_, err = os.Stat(pidPath)
	assert.True(t, os.IsNotExist(err), "stale PID file should be removed")
}

func TestKillStaleDaemon_AliveButReachable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping daemon IPC test in short mode")
	}

	// Use /tmp directly to keep the socket path short — macOS limits Unix socket
	// paths to 104 chars, and t.TempDir() generates paths under /var/folders/...
	// which are too long when combined with the daemon socket suffix.
	tmpDir, err := os.MkdirTemp("/tmp", "oxd-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	// start a child process to act as the "stale daemon"
	child := exec.Command("sleep", "300")
	require.NoError(t, child.Start())
	childPID := child.Process.Pid
	childDone := make(chan struct{})
	go func() {
		_ = child.Wait()
		close(childDone)
	}()

	wsID := "reachable"
	socketPath := filepath.Join(tmpDir, "sageox", "daemon", "daemon-"+wsID+".sock")
	require.NoError(t, os.MkdirAll(filepath.Dir(socketPath), 0700))

	// create a mock socket that responds to ping and stop, then kills the child
	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	stopCalled := make(chan struct{}, 1)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 4096)
			n, _ := conn.Read(buf)
			var msg Message
			if json.Unmarshal(buf[:n], &msg) == nil {
				switch msg.Type {
				case MsgTypePing:
					resp := Response{Success: true, Data: json.RawMessage(`"pong"`)}
					data, _ := json.Marshal(resp)
					conn.Write(append(data, '\n'))
				case MsgTypeStop:
					resp := Response{Success: true}
					data, _ := json.Marshal(resp)
					conn.Write(append(data, '\n'))
					// simulate graceful shutdown: kill the child process
					_ = child.Process.Kill()
					stopCalled <- struct{}{}
				}
			}
			conn.Close()
		}
	}()
	defer listener.Close()
	t.Cleanup(func() {
		_ = child.Process.Kill()
		<-childDone
	})

	// write registry entry with the child's PID
	reg := &Registry{Daemons: map[string]DaemonInfo{
		wsID: {
			WorkspaceID: wsID,
			PID:         childPID,
			SocketPath:  socketPath,
		},
	}}
	data, _ := json.MarshalIndent(reg, "", "  ")
	regPath := filepath.Join(tmpDir, "sageox", "daemon", "registry.json")
	require.NoError(t, os.WriteFile(regPath, data, 0600))

	KillStaleDaemon(wsID)

	// IPC stop should have been called (graceful path)
	select {
	case <-stopCalled:
		// good — graceful stop was used
	case <-time.After(2 * time.Second):
		t.Fatal("expected IPC stop to be called")
	}
}

func TestKillStaleDaemon_StopAckedPidAlive(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping daemon IPC escalation test in short mode")
	}

	// Use /tmp directly to keep the socket path short — macOS limits Unix socket
	// paths to 104 chars, and t.TempDir() generates paths under /var/folders/...
	// which are too long when combined with the daemon socket suffix.
	tmpDir, err := os.MkdirTemp("/tmp", "oxd-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	childPID, childDone := startFakeOxDaemon(t, fakeDaemonExitsOnTerm)

	wsID := "stop-acked"
	socketPath := filepath.Join(tmpDir, "sageox", "daemon", "daemon-"+wsID+".sock")
	require.NoError(t, os.MkdirAll(filepath.Dir(socketPath), 0700))

	// mock daemon ACKs Stop but does NOT kill the child — simulates a hung daemon
	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 4096)
			n, _ := conn.Read(buf)
			var msg Message
			if json.Unmarshal(buf[:n], &msg) == nil {
				switch msg.Type {
				case MsgTypePing:
					resp := Response{Success: true, Data: json.RawMessage(`"pong"`)}
					data, _ := json.Marshal(resp)
					conn.Write(append(data, '\n'))
				case MsgTypeStop:
					// ACK stop but DON'T kill the process
					resp := Response{Success: true}
					data, _ := json.Marshal(resp)
					conn.Write(append(data, '\n'))
				}
			}
			conn.Close()
		}
	}()
	defer listener.Close()

	reg := &Registry{Daemons: map[string]DaemonInfo{
		wsID: {WorkspaceID: wsID, PID: childPID, SocketPath: socketPath},
	}}
	data, _ := json.MarshalIndent(reg, "", "  ")
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, "sageox", "daemon", "registry.json"), data, 0600))

	err = KillStaleDaemon(wsID)
	require.NoError(t, err, "should succeed via SIGTERM escalation")

	// child should be dead — SIGTERM escalation should have killed it
	select {
	case <-childDone:
		// child was reaped — escalation to SIGTERM worked
	case <-time.After(3 * time.Second):
		t.Fatal("child process did not exit after IPC→SIGTERM escalation")
	}
}

func TestKillStaleDaemon_AliveButUnreachable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process kill test in short mode")
	}

	tmpDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	childPID, childDone := startFakeOxDaemon(t, fakeDaemonExitsOnTerm)

	// verify child is alive
	require.NoError(t, signalProcess(childPID, 0), "child should be alive")

	wsID := "unreachable"
	// no socket file — daemon is unreachable via IPC
	regDir := filepath.Join(tmpDir, "sageox", "daemon")
	require.NoError(t, os.MkdirAll(regDir, 0700))
	reg := &Registry{Daemons: map[string]DaemonInfo{
		wsID: {
			WorkspaceID: wsID,
			PID:         childPID,
			SocketPath:  filepath.Join(regDir, "daemon-"+wsID+".sock"), // doesn't exist
		},
	}}
	data, _ := json.MarshalIndent(reg, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(regDir, "registry.json"), data, 0600))

	KillStaleDaemon(wsID)

	// child should exit after SIGTERM — wait for reaper goroutine
	select {
	case <-childDone:
		// child was reaped — SIGTERM worked
	case <-time.After(3 * time.Second):
		t.Fatal("child process did not exit after SIGTERM")
	}
}

// --- process identity tests ---

// TestProcessCmdline_CurrentProcess is the darwin regression gate for
// isOxDaemonProcess. The implementation used to read /proc/<pid>/cmdline and
// nothing else; macOS has no /proc, so it returned false for EVERY pid. Its
// caller KillStaleDaemon reacts to false by unregistering the entry and
// deleting the pid file and socket while leaving the process ALIVE and
// untracked — one leaked orphan per cleanup, and the SIGTERM/SIGKILL
// escalation below it unreachable.
//
// Red-first proof: delete the ps(1) fallback in processCmdline and this test
// fails on macOS while still passing on Linux — the exact platform split that
// let the bug ship.
func TestProcessCmdline_CurrentProcess(t *testing.T) {
	cmdline, ok := processCmdline(os.Getpid())

	require.True(t, ok, "processCmdline must resolve the running process on %s", runtime.GOOS)
	assert.Contains(t, cmdline, ".test", "cmdline should name the compiled test binary")
}

// TestPsCmdline_CurrentProcess covers the ps(1) strategy directly. On Linux
// /proc always wins, so without this test the macOS-only path would be dead in
// the coverage profile — which is precisely how the darwin bug went unnoticed.
func TestPsCmdline_CurrentProcess(t *testing.T) {
	cmdline, ok := psCmdline(os.Getpid())

	require.True(t, ok, "ps(1) must resolve the running process on %s", runtime.GOOS)
	assert.Contains(t, cmdline, ".test")
}

func TestPsCmdline_UnknownPID(t *testing.T) {
	_, ok := psCmdline(999999999)

	assert.False(t, ok)
}

// TestProcCmdline_MatchesPlatform pins the /proc strategy to the platform that
// actually has /proc, so a future "simplification" back to a single strategy
// fails here rather than silently on macOS only.
func TestProcCmdline_MatchesPlatform(t *testing.T) {
	_, ok := procCmdline(os.Getpid())

	assert.Equal(t, runtime.GOOS == "linux", ok, "/proc is a Linux-only interface")
}

func TestProcessCmdline_UnknownPID(t *testing.T) {
	_, ok := processCmdline(999999999) // very unlikely to be alive

	assert.False(t, ok, "unknown pid must not resolve to a command line")
}

func TestIsOxDaemonProcess_RejectsUnrelatedProcess(t *testing.T) {
	// PID-reuse guard: a live process that is not an ox daemon must never be
	// signaled, on any platform.
	child := exec.Command("sleep", "300")
	require.NoError(t, child.Start())
	childDone := make(chan struct{})
	go func() {
		_ = child.Wait()
		close(childDone)
	}()
	t.Cleanup(func() {
		_ = child.Process.Kill()
		<-childDone
	})

	assert.False(t, isOxDaemonProcess(child.Process.Pid))
}

func TestIsOxDaemonProcess_AcceptsOxDaemon(t *testing.T) {
	pid, _ := startFakeOxDaemon(t, fakeDaemonExitsOnTerm)

	assert.True(t, isOxDaemonProcess(pid))
}

// --- SIGTERM -> SIGKILL escalation ---
//
// The child here is a REAL live process (so liveness, the poll loop, and the
// registry cleanup are all real); only signal DELIVERY is simulated, through
// signalProcessFn. That is deliberate. The obvious fixture — a shell with
// `trap` ignoring SIGTERM — was tried first and passed while silently taking
// the SIGTERM branch under parallel load, i.e. it asserted the right outcome
// without ever running the escalation it existed to cover. Simulating delivery
// makes which rung of the ladder ran an assertion instead of a hope.

// swallowSignals installs a signalProcessFn that reports every signal it is
// asked to deliver and drops the named ones on the floor, while passing
// liveness checks and everything else through to the real syscall.
func swallowSignals(t *testing.T, dropped ...syscall.Signal) *[]syscall.Signal {
	t.Helper()

	var delivered []syscall.Signal
	original := signalProcessFn
	t.Cleanup(func() { signalProcessFn = original })

	signalProcessFn = func(pid int, sig syscall.Signal) error {
		if sig == 0 {
			return original(pid, sig) // liveness must stay honest
		}
		delivered = append(delivered, sig)
		if slices.Contains(dropped, sig) {
			return nil // "sent" successfully, arrived nowhere
		}
		return original(pid, sig)
	}
	return &delivered
}

// registerStaleDaemon writes an isolated registry naming pid as the workspace's
// daemon, with a socket path that does not exist so IPC is unreachable and
// cleanup goes straight to the signal ladder.
func registerStaleDaemon(t *testing.T, wsID string, pid int) {
	t.Helper()

	tmpDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)
	regDir := filepath.Join(tmpDir, "sageox", "daemon")
	require.NoError(t, os.MkdirAll(regDir, 0700))

	reg := &Registry{Daemons: map[string]DaemonInfo{
		wsID: {WorkspaceID: wsID, PID: pid, SocketPath: filepath.Join(regDir, "daemon-"+wsID+".sock")},
	}}
	data, err := json.MarshalIndent(reg, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(regDir, "registry.json"), data, 0600))
}

// TestKillStaleDaemon_SigtermIgnored_EscalatesToSigkill: without the
// escalation, a daemon that ignores SIGTERM blocks every subsequent
// `ox daemon start` for that workspace, permanently.
func TestKillStaleDaemon_SigtermIgnored_EscalatesToSigkill(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping signal escalation test in short mode")
	}

	pid, done := startFakeOxDaemon(t, fakeDaemonExitsOnTerm)
	registerStaleDaemon(t, "sigterm-ignored", pid)
	delivered := swallowSignals(t, sigTERM)

	require.NoError(t, KillStaleDaemon("sigterm-ignored"),
		"escalation should reap a daemon that ignored SIGTERM")

	assert.Contains(t, *delivered, sigKILL,
		"SIGTERM going unanswered must escalate, not be treated as sufficient")
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("child survived the escalation")
	}

	reg, err := LoadRegistry()
	require.NoError(t, err)
	assert.Nil(t, reg.FindByWorkspaceID("sigterm-ignored"), "registry entry should be cleaned up")
}

// TestKillStaleDaemon_SigkillIneffective_ReportsFailure: reporting success for
// a daemon that is still alive is worse than reporting failure — the caller
// starts a second daemon on the same socket.
func TestKillStaleDaemon_SigkillIneffective_ReportsFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping signal escalation test in short mode")
	}

	tests := []struct {
		name     string
		signalFn func(t *testing.T, pid int)
	}{
		{
			name: "both signals delivered but ignored",
			signalFn: func(t *testing.T, pid int) {
				swallowSignals(t, sigTERM, sigKILL)
			},
		},
		{
			name: "SIGKILL itself is rejected by the kernel",
			signalFn: func(t *testing.T, pid int) {
				original := signalProcessFn
				t.Cleanup(func() { signalProcessFn = original })
				signalProcessFn = func(pid int, sig syscall.Signal) error {
					switch sig {
					case 0:
						return original(pid, sig)
					case sigKILL:
						return syscall.EPERM // e.g. the pid is not ours anymore
					default:
						return nil
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pid, _ := startFakeOxDaemon(t, fakeDaemonExitsOnTerm)
			registerStaleDaemon(t, "wedged", pid)
			tt.signalFn(t, pid)

			err := KillStaleDaemon("wedged")

			require.Error(t, err, "a surviving daemon must never be reported as stopped")
			assert.Contains(t, err.Error(), "SIGKILL")
		})
	}
}

// --- buildDaemonArgs tests ---

func TestBuildDaemonArgs_WithRepo(t *testing.T) {
	args := buildDaemonArgs("sageox/ox")
	assert.Equal(t, []string{"daemon", "start", "--foreground", "--repo=sageox/ox"}, args)
}

func TestBuildDaemonArgs_WithoutRepo(t *testing.T) {
	args := buildDaemonArgs("")
	assert.Equal(t, []string{"daemon", "start", "--foreground"}, args)
	for _, arg := range args {
		assert.NotContains(t, arg, "--repo")
	}
}

// --- process identity: exactness is a safety property ---

// TestMatchesOxDaemon is the PID-reuse guard's contract. KillStaleDaemon
// consults it immediately before SIGTERM and then SIGKILL, so a false positive
// is a stranger's process being killed — on macOS, where the ps(1) fallback
// only just made this code path live.
//
// Red-first proof: restore the old
// strings.Contains(cmdline, "ox") && strings.Contains(cmdline, "daemon")
// and every "not ours" row below flips to true.
func TestMatchesOxDaemon(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cmdline string
		want    bool
	}{
		{"production spawn", "/opt/homebrew/bin/ox daemon start --foreground", true},
		{"production spawn with repo", "/usr/local/bin/ox daemon start --foreground --repo=sageox/ox", true},
		{"bare relative invocation", "./ox daemon start", true},
		{"global flag before subcommand", "/usr/local/bin/ox --verbose daemon start", true},

		{"firefox", "/usr/bin/firefox --daemon", false},
		{"podman", "/usr/bin/podman daemon", false},
		{"toxiproxy", "/usr/local/bin/toxiproxy daemon", false},
		{"unrelated binary under an ox-ish path", "/Users/rox/bin/redis-server --daemonize", false},
		{"ox test binary", "/tmp/go-build123/b001/ox.test daemon start", false},
		{"ox without the daemon subcommand", "/usr/local/bin/ox status", false},
		{"daemon named in a flag value, not a subcommand", "/usr/local/bin/ox log --grep=daemon", false},
		{"argv[0] only", "/usr/local/bin/ox", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, matchesOxDaemon(tt.cmdline))
		})
	}
}

// TestIsOxDaemonProcess_RejectsForeignDaemonSubcommand is the end-to-end half
// of the guard: a LIVE process that says "daemon" but is not ox must never be
// classified as ours, because classification is what authorizes the signal.
func TestIsOxDaemonProcess_RejectsForeignDaemonSubcommand(t *testing.T) {
	// identical argument list to a real daemon — only argv[0] differs
	pid, _ := startFakeProcess(t, "notox", fakeDaemonExitsOnTerm)

	assert.False(t, isOxDaemonProcess(pid),
		"a non-ox process running a `daemon` subcommand must not be signalable")
}

// --- daemon spawn refuses to re-exec a test binary ---

// TestEnsureDaemonImpl_RefusesToReexecTestBinary drives the real daemon-start
// path to the point where it would exec the current binary. Before
// internal/selfexec, that exec was `ox.test daemon start`, which re-ran this
// entire package's suite in an un-timeout-ed grandchild — forever, one
// generation spawning the next.
//
// Unlike the cmd/ox call sites, this one MUST surface the refusal: starting the
// daemon is the caller's actual request, so returning nil would report success
// for a daemon that does not exist.
func TestEnsureDaemonImpl_RefusesToReexecTestBinary(t *testing.T) {
	setupIsolatedRegistry(t, map[string]DaemonInfo{})

	err := ensureDaemonImpl(false)

	require.Error(t, err)
	assert.ErrorIs(t, err, selfexec.ErrUnderTest)
}

// TestIsOxDaemonProcess_UnknownPID: a pid with no resolvable command line is
// unidentifiable, and unidentifiable must never mean signalable.
func TestIsOxDaemonProcess_UnknownPID(t *testing.T) {
	t.Parallel()

	assert.False(t, isOxDaemonProcess(999999999))
}

// TestPsCmdline_EmptyOutput: ps can exit 0 and print nothing (a process that
// vanished between the liveness check and the query). Empty is not a command
// line, and treating it as one would feed "" to matchesOxDaemon.
func TestPsCmdline_EmptyOutput(t *testing.T) {
	original := psCommand
	t.Cleanup(func() { psCommand = original })

	tests := []struct {
		name string
		out  string
		want bool
	}{
		{"empty", "", false},
		{"whitespace only", "  \n\t\n", false},
		{"real command line", "/usr/local/bin/ox daemon start\n", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			psCommand = func(context.Context, int) ([]byte, error) { return []byte(tt.out), nil }

			_, ok := psCmdline(os.Getpid())

			assert.Equal(t, tt.want, ok)
		})
	}
}
