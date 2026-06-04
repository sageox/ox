package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/agenttask"
	"github.com/sageox/ox/internal/config"
)

// setupTaskProject creates an initialized project, points OX_PROJECT_ROOT at it,
// and returns the root so findProjectRoot resolves deterministically.
func setupTaskProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	sageox := filepath.Join(root, ".sageox")
	if err := os.MkdirAll(sageox, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sageox, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// resolve symlinks so the override path matches findProjectRoot's EvalSymlinks
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvProjectRoot, resolved)
	cfg = &config.Config{} // JSON default
	return resolved
}

func testInst() *agentinstance.Instance {
	return &agentinstance.Instance{AgentID: "Oxtest", AgentType: "claude"}
}

func TestRunAgentTasks_ListEmpty(t *testing.T) {
	setupTaskProject(t)
	var buf bytes.Buffer
	if err := runAgentTasks(&buf, testInst(), []string{"list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"count": 0`) {
		t.Fatalf("expected empty count, got: %s", out)
	}
}

func TestRunAgentTasks_ClaimAndComplete(t *testing.T) {
	root := setupTaskProject(t)

	// producer enqueues two tasks
	if _, err := agenttask.Enqueue(root, &agenttask.Task{Title: "low", Priority: 30}); err != nil {
		t.Fatal(err)
	}
	if _, err := agenttask.Enqueue(root, &agenttask.Task{Title: "urgent", Priority: 1}); err != nil {
		t.Fatal(err)
	}

	// next claims the highest priority
	var buf bytes.Buffer
	if err := runAgentTasks(&buf, testInst(), []string{"next"}); err != nil {
		t.Fatalf("next: %v", err)
	}
	if !strings.Contains(buf.String(), "agent_task_claimed") || !strings.Contains(buf.String(), "urgent") {
		t.Fatalf("expected to claim urgent task, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "SUBAGENT") {
		t.Fatalf("expected subagent-dispatch guidance in claim output, got: %s", buf.String())
	}

	// find the claimed id and complete it
	store, _ := agenttask.NewStore(root)
	tasks, _ := store.List(false)
	var claimedID string
	for _, tk := range tasks {
		if tk.Status == agenttask.StatusInProgress {
			claimedID = tk.ID
		}
	}
	if claimedID == "" {
		t.Fatalf("no in-progress task found")
	}

	buf.Reset()
	if err := runAgentTasks(&buf, testInst(), []string{"done", claimedID, "--result", "ok"}); err != nil {
		t.Fatalf("done: %v", err)
	}
	got, _ := store.Get(claimedID)
	if got.Status != agenttask.StatusCompleted || got.Result != "ok" {
		t.Fatalf("expected completed with result, got %+v", got)
	}
}

func TestRunAgentTasks_Cancel(t *testing.T) {
	root := setupTaskProject(t)
	_, _ = agenttask.Enqueue(root, &agenttask.Task{Title: "obsolete"})
	store, _ := agenttask.NewStore(root)
	tasks, _ := store.List(false)
	id := tasks[0].ID

	var buf bytes.Buffer
	if err := runAgentTasks(&buf, testInst(), []string{"cancel", id, "--reason", "dupe"}); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	got, _ := store.Get(id)
	if got.Status != agenttask.StatusCanceled {
		t.Fatalf("expected canceled, got %s", got.Status)
	}
}

func TestRunAgentTasks_TextMode(t *testing.T) {
	root := setupTaskProject(t)
	cfg.Text = true
	_, _ = agenttask.Enqueue(root, &agenttask.Task{Title: "a thing", Priority: 5, Kind: "doctor"})

	var buf bytes.Buffer
	if err := runAgentTasks(&buf, testInst(), []string{"list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(buf.String(), "a thing") || !strings.Contains(buf.String(), "ready") {
		t.Fatalf("unexpected text output: %s", buf.String())
	}
}

func TestRunAgentTasks_NextEmpty(t *testing.T) {
	setupTaskProject(t)
	var buf bytes.Buffer
	if err := runAgentTasks(&buf, testInst(), []string{"next"}); err != nil {
		t.Fatalf("next: %v", err)
	}
	if !strings.Contains(buf.String(), `"claimed": false`) {
		t.Fatalf("expected claimed=false, got: %s", buf.String())
	}
}

func TestRunAgentTasks_TargetAgentFiltering(t *testing.T) {
	root := setupTaskProject(t)
	_, _ = agenttask.Enqueue(root, &agenttask.Task{Title: "codex-only", TargetAgent: "codex", Priority: 1})
	_, _ = agenttask.Enqueue(root, &agenttask.Task{Title: "anyone", Priority: 5})

	// claude agent cannot see the codex-targeted task in its ready list
	var buf bytes.Buffer
	if err := runAgentTasks(&buf, testInst(), []string{"list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	// list shows all tasks regardless of target (visibility), but next must skip
	buf.Reset()
	if err := runAgentTasks(&buf, testInst(), []string{"next"}); err != nil {
		t.Fatalf("next: %v", err)
	}
	if !strings.Contains(buf.String(), "anyone") {
		t.Fatalf("claude should claim 'anyone', not codex-targeted, got: %s", buf.String())
	}
}

// --- surfacing throttle ---

func TestEmitAgentTasks_Throttle(t *testing.T) {
	root := setupTaskProject(t)
	t.Setenv("AGENT_ENV", "claude")
	_, _ = agenttask.Enqueue(root, &agenttask.Task{Title: "surfaced", Priority: 1})

	var buf bytes.Buffer
	emitAgentTasks(&buf, root, "Oxtest")
	if !strings.Contains(buf.String(), "scheduled agent task") || !strings.Contains(buf.String(), "SUBAGENT") {
		t.Fatalf("expected first surface to emit, got: %s", buf.String())
	}

	// second call with unchanged ready set is throttled (no output)
	buf.Reset()
	emitAgentTasks(&buf, root, "Oxtest")
	if buf.Len() != 0 {
		t.Fatalf("expected throttled second surface to be silent, got: %s", buf.String())
	}

	// a new task changes the signature → surfaces again
	_, _ = agenttask.Enqueue(root, &agenttask.Task{Title: "another", Priority: 2})
	buf.Reset()
	emitAgentTasks(&buf, root, "Oxtest")
	if buf.Len() == 0 {
		t.Fatalf("expected changed ready set to surface again")
	}
}

func TestEmitAgentTasks_NoTasksSilent(t *testing.T) {
	root := setupTaskProject(t)
	t.Setenv("AGENT_ENV", "claude")
	var buf bytes.Buffer
	emitAgentTasks(&buf, root, "Oxtest")
	if buf.Len() != 0 {
		t.Fatalf("expected silence with no tasks, got: %s", buf.String())
	}
}

func TestEmitAgentTasks_RespectsTargetAgent(t *testing.T) {
	root := setupTaskProject(t)
	t.Setenv("AGENT_ENV", "claude")
	// task targeted at codex — a claude agent must not be nudged about it
	_, _ = agenttask.Enqueue(root, &agenttask.Task{Title: "codex job", TargetAgent: "codex"})

	var buf bytes.Buffer
	emitAgentTasks(&buf, root, "Oxtest")
	if buf.Len() != 0 {
		t.Fatalf("claude should not be nudged about codex-targeted task, got: %s", buf.String())
	}
}
