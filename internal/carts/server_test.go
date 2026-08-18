package carts

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

var errStubUnreachable = errors.New("stub: endpoint unreachable")

// TestRunningServerPort covers the recorded-server reuse decision: it returns
// the saved port only when the PID is live AND the endpoint answers, and errors
// otherwise.
//
// Failure prevented: before #780 the liveness check (proc.Signal(nil)) always
// errored, so a live server was never detected and every call started a second
// sql-server. The "unreachable endpoint (stale)" case guards the follow-up
// validation — a live PID alone is not enough, because a crashed server can
// leave stale pid/port files while the OS reassigns its PID to an unrelated
// process; reusing that port would fail later with "connection refused".
func TestRunningServerPort(t *testing.T) {
	tests := []struct {
		name     string
		pid      string // "self" live, "dead" reaped, "" no file, else literal
		port     string // port file content; "" means no port file
		ping     error  // simulated endpoint probe result
		wantErr  bool
		wantPort int
	}{
		{name: "no pid file", pid: "", wantErr: true},
		{name: "live pid, reachable endpoint", pid: "self", port: "50422", ping: nil, wantPort: 50422},
		{name: "live pid, unreachable endpoint (stale)", pid: "self", port: "1", ping: errStubUnreachable, wantErr: true},
		{name: "dead pid", pid: "dead", port: "50422", ping: nil, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()

			// Stub the endpoint probe per-case so the reuse branches are
			// exercised without a real dolt server.
			orig := pingEndpoint
			pingEndpoint = func(string, int, time.Duration) error { return tc.ping }
			t.Cleanup(func() { pingEndpoint = orig })

			switch tc.pid {
			case "":
				// no pid file
			case "self":
				writeStateFile(t, dir, pidFileName, strconv.Itoa(os.Getpid()))
			case "dead":
				writeStateFile(t, dir, pidFileName, strconv.Itoa(deadPID(t)))
			default:
				writeStateFile(t, dir, pidFileName, tc.pid)
			}
			if tc.port != "" {
				writeStateFile(t, dir, portFileName, tc.port)
			}

			port, err := runningServerPort(dir)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got port %d", port)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if port != tc.wantPort {
				t.Fatalf("port = %d, want %d", port, tc.wantPort)
			}
		})
	}
}

// TestStopServer covers the decision to SIGNAL a recorded PID, which needs the
// same proof reuse does — for a stronger reason.
//
// Failure prevented: StopServer signaled whatever PID the state file named. A
// crash leaves a stale record behind while the OS is free to reassign that PID,
// so `ox carts` stopping its server could interrupt an unrelated process. Reuse
// of a bad record merely fails a command; signaling one kills a stranger.
func TestStopServer(t *testing.T) {
	// livingHelper starts a child that stays up until the test ends. It returns
	// the PID and a channel closed once the child has exited AND been reaped:
	// proc.IsAlive alone would report a signaled-but-unreaped child as alive,
	// because a zombie still answers.
	livingHelper := func(t *testing.T) (int, <-chan struct{}) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=TestSleepHelper")
		cmd.Env = append(os.Environ(), "OX_CARTS_SLEEP_HELPER=1")
		if err := cmd.Start(); err != nil {
			t.Fatalf("spawn sleep helper: %v", err)
		}
		exited := make(chan struct{})
		go func() {
			_, _ = cmd.Process.Wait()
			close(exited)
		}()
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		return cmd.Process.Pid, exited
	}

	stubPing := func(t *testing.T, result error) {
		t.Helper()
		orig := pingEndpoint
		pingEndpoint = func(string, int, time.Duration) error { return result }
		t.Cleanup(func() { pingEndpoint = orig })
	}

	t.Run("leaves an unrelated live process alone when the endpoint is stale", func(t *testing.T) {
		dir := t.TempDir()
		pid, exited := livingHelper(t)
		writeStateFile(t, dir, pidFileName, strconv.Itoa(pid))
		writeStateFile(t, dir, portFileName, "50422")
		stubPing(t, errStubUnreachable)

		if err := StopServer(dir); err != nil {
			t.Fatalf("StopServer: %v", err)
		}
		select {
		case <-exited:
			t.Fatal("signaled an unrelated process on the strength of a stale record")
		case <-time.After(500 * time.Millisecond):
		}
		assertStateCleared(t, dir)
	})

	t.Run("stops the server when the recorded endpoint answers", func(t *testing.T) {
		dir := t.TempDir()
		pid, exited := livingHelper(t)
		writeStateFile(t, dir, pidFileName, strconv.Itoa(pid))
		writeStateFile(t, dir, portFileName, "50422")
		stubPing(t, nil)

		if err := StopServer(dir); err != nil {
			t.Fatalf("StopServer: %v", err)
		}
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Fatal("expected the verified server process to be terminated")
		}
		assertStateCleared(t, dir)
	})

	t.Run("clears a record naming a dead process", func(t *testing.T) {
		dir := t.TempDir()
		writeStateFile(t, dir, pidFileName, strconv.Itoa(deadPID(t)))
		writeStateFile(t, dir, portFileName, "50422")
		stubPing(t, nil)

		if err := StopServer(dir); err != nil {
			t.Fatalf("StopServer: %v", err)
		}
		assertStateCleared(t, dir)
	})

	t.Run("clears a record missing its port file", func(t *testing.T) {
		dir := t.TempDir()
		writeStateFile(t, dir, pidFileName, strconv.Itoa(os.Getpid()))
		stubPing(t, nil)

		if err := StopServer(dir); err != nil {
			t.Fatalf("StopServer: %v", err)
		}
		assertStateCleared(t, dir)
	})

	t.Run("no record is a no-op", func(t *testing.T) {
		if err := StopServer(t.TempDir()); err != nil {
			t.Fatalf("StopServer on an empty dir: %v", err)
		}
	})
}

// TestSleepHelper is not a test: it is the body of the long-lived child process
// spawned by TestStopServer, and exits immediately unless that env var is set.
func TestSleepHelper(t *testing.T) {
	if os.Getenv("OX_CARTS_SLEEP_HELPER") != "1" {
		t.Skip("helper process entry point; not a standalone test")
	}
	time.Sleep(60 * time.Second)
}

func assertStateCleared(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{pidFileName, portFileName} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s should be cleared; a stale record wedges future starts", name)
		}
	}
}

// deadPID returns a PID that has certainly exited: it runs the test binary as a
// no-op child (`-test.run=^$` matches no test) and reaps it. Portable across
// platforms, unlike shelling out to `true`, and it fails rather than skips when
// the helper cannot run so the dead-PID assertion is never silently bypassed.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn no-op helper: %v", err)
	}
	return cmd.Process.Pid
}

func writeStateFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
