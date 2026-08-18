package proc

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestFindAgentAncestorPID_ReturnsLiveProcess(t *testing.T) {
	pid := FindAgentAncestorPID()
	if pid <= 0 {
		t.Fatalf("FindAgentAncestorPID returned non-positive PID: %d", pid)
	}
	if !IsAlive(pid) {
		t.Errorf("FindAgentAncestorPID returned dead PID %d", pid)
	}
}

func TestFindAgentAncestorPID_NotSelf(t *testing.T) {
	self := os.Getpid()
	pid := FindAgentAncestorPID()
	if pid == self {
		t.Errorf("FindAgentAncestorPID returned current process PID %d", pid)
	}
}

func TestParentPID_Self(t *testing.T) {
	pid := os.Getpid()
	ppid, err := parentPID(pid)
	if err != nil {
		t.Fatalf("parentPID(%d): %v", pid, err)
	}
	if ppid != os.Getppid() {
		t.Errorf("parentPID(%d) = %d, want %d (os.Getppid)", pid, ppid, os.Getppid())
	}
}

func TestProcessName_Self(t *testing.T) {
	pid := os.Getpid()
	name := processName(pid)
	if name == "" {
		t.Errorf("processName(%d) returned empty string", pid)
	}
}

func TestMatchesAgent(t *testing.T) {
	known := []string{"claude", "cursor", "windsurf", "aider"}
	cases := []struct {
		name string
		hint string
		want bool
	}{
		{"claude", "claude", true},
		{"cursor", "", true},
		{"windsurf", "", true},
		{"aider", "aider", true},
		{"bash", "", false},
		{"sh", "", false},
		{"ox", "", false},
		{"", "", false},
		// hint match without being in known list
		{"myagent", "myagent", true},
	}
	for _, tc := range cases {
		got := matchesAgent(tc.name, tc.hint, known)
		if got != tc.want {
			t.Errorf("matchesAgent(%q, %q, ...) = %v, want %v", tc.name, tc.hint, got, tc.want)
		}
	}
}

// IsAlive must distinguish live from dead. On Windows isAliveProc returned
// `proc != nil`, and os.FindProcess never fails there, so IsAlive was always
// true — a caller using it to decide whether to restart something would trust a
// dead record forever. These assertions hold on every platform.
func TestIsAlive(t *testing.T) {
	if !IsAlive(os.Getpid()) {
		t.Error("the running test process should report as alive")
	}
	for _, pid := range []int{0, -1} {
		if IsAlive(pid) {
			t.Errorf("IsAlive(%d) should be false", pid)
		}
	}

	// A reaped child: the PID is valid but names nothing running.
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn no-op helper: %v", err)
	}
	if IsAlive(cmd.Process.Pid) {
		t.Errorf("reaped pid %d should report as dead", cmd.Process.Pid)
	}
}

func TestTerminateRejectsInvalidPID(t *testing.T) {
	for _, pid := range []int{0, -1} {
		if err := Terminate(pid); err == nil {
			t.Errorf("Terminate(%d) should error rather than signal something arbitrary", pid)
		}
	}
}

func TestTerminateStopsAProcess(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$", "-test.timeout=60s")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn helper: %v", err)
	}
	exited := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(exited)
	}()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	if err := Terminate(cmd.Process.Pid); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Error("expected the process to exit after Terminate")
	}
}
