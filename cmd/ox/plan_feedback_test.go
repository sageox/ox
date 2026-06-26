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

// TestEnqueuePlanFeedbackTask_ReenqueuesAfterCompletion verifies feedback raised
// AFTER the coworker addressed a prior round still notifies — dedup only blocks
// an ACTIVE task, so completing one frees the key for the next.
// Failure prevented: a second round of feedback is silently swallowed by dedup
// and the coworker never learns the human came back.
func TestEnqueuePlanFeedbackTask_ReenqueuesAfterCompletion(t *testing.T) {
	root := t.TempDir()
	planDir := filepath.Join(root, "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestPlanMeta(t, planDir, &plan.Provenance{AgentID: "Ox#7", AgentType: "claude"})

	enqueuePlanFeedbackTask(root, planDir, "p", 1)
	if n := len(activeTasks(t, root)); n != 1 {
		t.Fatalf("first round: want 1 task, got %d", n)
	}

	// the coworker claims + completes it (addressed the round)
	store, err := agenttask.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(agenttask.ClaimOptions{AgentID: "Ox#7", AgentType: "claude"})
	if err != nil || claimed == nil {
		store.Close()
		t.Fatalf("claim: %v / %v", claimed, err)
	}
	if err := store.Complete(claimed.ID, "addressed"); err != nil {
		store.Close()
		t.Fatalf("complete: %v", err)
	}
	store.Close()

	enqueuePlanFeedbackTask(root, planDir, "p", 1)
	if n := len(activeTasks(t, root)); n != 1 {
		t.Errorf("post-completion feedback must enqueue a fresh task, active=%d", n)
	}
}

// TestEnqueuePlanFeedbackTask_WrongAgentTypeCannotClaim verifies a task for a
// claude-authored plan is invisible to a different coworker type, and visible to
// the authoring type. Failure prevented: feedback routed to a coworker that can't
// act on it (or leaked to an unrelated one).
func TestEnqueuePlanFeedbackTask_WrongAgentTypeCannotClaim(t *testing.T) {
	root := t.TempDir()
	planDir := filepath.Join(root, "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestPlanMeta(t, planDir, &plan.Provenance{AgentID: "Ox#9", AgentType: "claude-code"})
	enqueuePlanFeedbackTask(root, planDir, "p", 1)

	store, err := agenttask.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if r, _ := store.Ready("codex"); len(r) != 0 {
		t.Errorf("a codex coworker must not see a claude plan's feedback, got %d", len(r))
	}
	if r, _ := store.Ready("claude"); len(r) != 1 { // claude-code normalizes to claude
		t.Errorf("the authoring (claude) type must see it, got %d", len(r))
	}
}

// TestEnqueuePlanFeedbackTask_UntargetedWhenNoAgentType verifies a plan whose
// authoring agent id is known but whose TYPE was not recorded still notifies,
// untargeted, so any coworker can pick it up — feedback is never lost to a
// missing type.
func TestEnqueuePlanFeedbackTask_UntargetedWhenNoAgentType(t *testing.T) {
	root := t.TempDir()
	planDir := filepath.Join(root, "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestPlanMeta(t, planDir, &plan.Provenance{AgentID: "Ox#3"}) // no AgentType
	enqueuePlanFeedbackTask(root, planDir, "p", 1)

	tasks := activeTasks(t, root)
	if len(tasks) != 1 || tasks[0].TargetAgent != "" {
		t.Fatalf("untargeted task expected, got %+v", tasks)
	}
	store, err := agenttask.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if r, _ := store.Ready("codex"); len(r) != 1 {
		t.Errorf("untargeted feedback must be visible to any type, got %d", len(r))
	}
}

// TestEnqueuePlanFeedbackTask_BestEffortOnBadMeta verifies a missing or corrupt
// meta.json produces no task and no panic — the notify is best-effort and must
// never break the feedback write that already succeeded.
func TestEnqueuePlanFeedbackTask_BestEffortOnBadMeta(t *testing.T) {
	// missing meta.json
	root := t.TempDir()
	planDir := filepath.Join(root, "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	enqueuePlanFeedbackTask(root, planDir, "p", 1)
	if agenttask.QueueExists(root) && len(activeTasks(t, root)) != 0 {
		t.Error("missing meta must enqueue nothing")
	}

	// corrupt meta.json
	root2 := t.TempDir()
	planDir2 := filepath.Join(root2, "plan")
	if err := os.MkdirAll(planDir2, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir2, "meta.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	enqueuePlanFeedbackTask(root2, planDir2, "p", 1)
	if agenttask.QueueExists(root2) && len(activeTasks(t, root2)) != 0 {
		t.Error("corrupt meta must enqueue nothing")
	}
}

// TestEnqueuePlanFeedbackTask_GuardsEmptyArgs verifies the guard short-circuits
// on any empty argument (no queue is even materialized).
func TestEnqueuePlanFeedbackTask_GuardsEmptyArgs(t *testing.T) {
	root := t.TempDir()
	planDir := filepath.Join(root, "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestPlanMeta(t, planDir, &plan.Provenance{AgentID: "Ox#1", AgentType: "claude"})
	enqueuePlanFeedbackTask("", planDir, "p", 1)
	enqueuePlanFeedbackTask(root, "", "p", 1)
	enqueuePlanFeedbackTask(root, planDir, "", 1)
	if agenttask.QueueExists(root) {
		if n := len(activeTasks(t, root)); n != 0 {
			t.Errorf("guarded no-op calls must not enqueue, got %d", n)
		}
	}
}
