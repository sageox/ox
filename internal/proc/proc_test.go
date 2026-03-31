package proc

import (
	"os"
	"testing"
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
