package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agenttask"
)

// chdirToGitRepo creates a git repo dir and chdirs into it so findGitRoot
// resolves to it. Returns the root.
func chdirToGitRepo(t *testing.T) string {
	t.Helper()
	root := testGitRepo(t)
	if err := os.MkdirAll(filepath.Join(root, ".sageox"), 0o755); err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestCheckAgentTasksStuck_NoQueue(t *testing.T) {
	chdirToGitRepo(t)
	res := checkAgentTasksStuck(false)
	if !res.skipped {
		t.Fatalf("expected skipped with no queue, got %+v", res)
	}
}

func TestCheckAgentTasksStuck_HealthyQueue(t *testing.T) {
	root := chdirToGitRepo(t)
	if _, err := agenttask.Enqueue(root, &agenttask.Task{Title: "ready task"}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	res := checkAgentTasksStuck(false)
	if !res.passed || res.warning {
		t.Fatalf("expected passed (not warning) for healthy queue, got %+v", res)
	}
}

func TestCheckAgentTasksStuck_PoisonWarns(t *testing.T) {
	root := chdirToGitRepo(t)
	store, err := agenttask.NewStore(root)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Add(&agenttask.Task{Title: "poison"}); err != nil {
		t.Fatalf("add task: %v", err)
	}

	// inflate attempts past the threshold: claim with a dead PID, then read so
	// reconcile reclaims it (dead claimer); each Claim bumps attempts.
	const deadPID = 2000000000 // implausible PID; proc.IsAlive returns false
	for i := 0; i < maxTaskAttempts; i++ {
		claimed, err := store.Claim(agenttask.ClaimOptions{AgentID: "Oxpois", PID: deadPID, Lease: time.Hour})
		if err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		if claimed == nil {
			t.Fatalf("claim %d returned nil", i)
		}
		_, _ = store.Ready("") // reconcile reclaims the dead-claimer task
	}

	res := checkAgentTasksStuck(false)
	if !res.warning {
		t.Fatalf("expected warning for poison task, got %+v", res)
	}

	// --fix cancels it
	res = checkAgentTasksStuck(true)
	if !res.passed || res.warning {
		t.Fatalf("expected passed after fix, got %+v", res)
	}
	tasks, err := store.List(true)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, tk := range tasks {
		if !tk.IsTerminal() {
			t.Fatalf("expected poison task canceled, still: %s", tk.Status)
		}
	}
}
