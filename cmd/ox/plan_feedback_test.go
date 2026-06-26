package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/agenttask"
	"github.com/sageox/ox/internal/plan"
)

// writeTestPlanMeta writes a minimal meta.json with the given provenance into a
// plan dir, the way Save would — so LoadMeta (and the notify path) can read it.
func writeTestPlanMeta(t *testing.T, dir string, prov *plan.Provenance) {
	t.Helper()
	b, err := json.Marshal(plan.Meta{Topic: "T", Slug: "my-plan", Provenance: prov})
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}
}

func activeTasks(t *testing.T, root string) []*agenttask.Task {
	t.Helper()
	store, err := agenttask.NewStore(root)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	tasks, err := store.List(false) // active only
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	return tasks
}

// TestEnqueuePlanFeedbackTask_NotifiesAuthoringAgent verifies submitting feedback
// on a plan enqueues exactly one plan-feedback task, routed to the authoring
// agent type and carrying the slug, and that a second round dedups instead of
// piling up.
// Failure prevented: human review feedback never reaches the coworker that wrote
// the plan — the whole point of closing the loop.
func TestEnqueuePlanFeedbackTask_NotifiesAuthoringAgent(t *testing.T) {
	root := t.TempDir()
	planDir := filepath.Join(root, "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestPlanMeta(t, planDir, &plan.Provenance{AgentID: "Ox#42", AgentType: "claude-code"})

	enqueuePlanFeedbackTask(root, planDir, "my-plan", 2)

	tasks := activeTasks(t, root)
	if len(tasks) != 1 {
		t.Fatalf("want 1 plan-feedback task, got %d", len(tasks))
	}
	got := tasks[0]
	if got.Kind != agenttask.KindPlanFeedback {
		t.Errorf("kind = %q, want %q", got.Kind, agenttask.KindPlanFeedback)
	}
	// claude-code normalizes to claude, and a claude coworker can therefore claim it.
	if got.TargetAgent != "claude" || !got.ClaimableBy("claude-code") {
		t.Errorf("task not routed to the authoring type: target=%q", got.TargetAgent)
	}
	if got.Payload["plan_slug"] != "my-plan" {
		t.Errorf("payload plan_slug = %q, want my-plan", got.Payload["plan_slug"])
	}

	// a human may submit several rounds before the coworker addresses them — dedup
	// keeps it to one active task (keyed on agent+slug).
	enqueuePlanFeedbackTask(root, planDir, "my-plan", 1)
	if tasks := activeTasks(t, root); len(tasks) != 1 {
		t.Errorf("repeat submit must dedup, got %d active tasks", len(tasks))
	}
}

// TestEnqueuePlanFeedbackTask_SkipsUnlinkedPlan verifies a plan with no recorded
// authoring agent enqueues nothing (no one to notify) and never errors.
// Failure prevented: a spurious untargeted task for a plan no live coworker owns.
func TestEnqueuePlanFeedbackTask_SkipsUnlinkedPlan(t *testing.T) {
	root := t.TempDir()
	planDir := filepath.Join(root, "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// provenance present but no agent_id → nobody to notify.
	writeTestPlanMeta(t, planDir, &plan.Provenance{AgentType: "claude"})

	enqueuePlanFeedbackTask(root, planDir, "my-plan", 1)

	if agenttask.QueueExists(root) {
		if tasks := activeTasks(t, root); len(tasks) != 0 {
			t.Errorf("unlinked plan must enqueue nothing, got %d", len(tasks))
		}
	}
}
