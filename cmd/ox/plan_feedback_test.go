package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/agenttask"
	"github.com/sageox/ox/internal/plan"
	"github.com/spf13/cobra"
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

// TestEnqueuePlanFeedbackTask_MissingMetaIsANoop verifies a plan with no
// meta.json at all (never had provenance recorded) produces no task, no
// error, and no panic — this is the ordinary "nobody to notify" case, not a
// failure.
func TestEnqueuePlanFeedbackTask_MissingMetaIsANoop(t *testing.T) {
	root := t.TempDir()
	planDir := filepath.Join(root, "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := enqueuePlanFeedbackTask(root, planDir, "p", 1); err != nil {
		t.Errorf("missing meta must be a silent no-op, got error: %v", err)
	}
	if agenttask.QueueExists(root) && len(activeTasks(t, root)) != 0 {
		t.Error("missing meta must enqueue nothing")
	}
}

// TestEnqueuePlanFeedbackTask_CorruptMetaIsAnError verifies a plan whose
// meta.json exists but fails to parse returns a real error — not the silent
// nil "nobody to notify" no-op.
// Failure prevented: LoadMeta's read/parse error was previously folded into
// the same branch as "no provenance recorded," so a corrupt meta.json made
// the CLI print no warning and the review server report notified:true, even
// though nothing was ever enqueued (caught in PR #996 review).
func TestEnqueuePlanFeedbackTask_CorruptMetaIsAnError(t *testing.T) {
	root := t.TempDir()
	planDir := filepath.Join(root, "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "meta.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := enqueuePlanFeedbackTask(root, planDir, "p", 1); err == nil {
		t.Error("corrupt meta must return an error, not a silent no-op")
	}
	if agenttask.QueueExists(root) && len(activeTasks(t, root)) != 0 {
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

// TestEnqueuePlanFeedbackTask_SurfacesEnqueueFailure verifies an actual enqueue
// failure (as opposed to "nobody to notify") is (a) returned to the caller and
// (b) logged at Warn or above — not swallowed at Debug where nothing shows at
// default log level.
// Failure prevented (ox#968): human review feedback is saved, the human is
// told it landed, and the authoring coworker is never notified — with no trace
// at default log level and no way for a caller to know it happened.
func TestEnqueuePlanFeedbackTask_SurfacesEnqueueFailure(t *testing.T) {
	root := t.TempDir()
	planDir := filepath.Join(root, "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestPlanMeta(t, planDir, &plan.Provenance{AgentID: "Ox#1", AgentType: "claude"})
	// A regular file at .sageox makes agenttask.NewStore's MkdirAll fail on
	// every attempt — a deterministic, structural enqueue failure (the retry
	// inside enqueuePlanFeedbackTask cannot paper over it).
	if err := os.WriteFile(filepath.Join(root, ".sageox"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	previous := slog.Default()
	// Gate the handler at Warn: if the failure is still logged at Debug (the
	// bug), nothing lands in `logs` and the assertion below fails — proving
	// the level was actually raised, not just that some log call exists.
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	err := enqueuePlanFeedbackTask(root, planDir, "p", 1)
	if err == nil {
		t.Fatal("want an error when the task store cannot be opened, got nil")
	}
	if !strings.Contains(logs.String(), "enqueue notify task failed") {
		t.Errorf("enqueue failure must be logged at Warn level or above; captured log output: %q", logs.String())
	}
}

// TestRunPlanFeedbackApply_WarnsOnNotifyFailure verifies the CLI `ox plan
// feedback apply` path prints a human-visible warning when it cannot notify
// the plan's authoring coworker, instead of quietly reporting success.
// Failure prevented (ox#968): a human applies a feedback export, sees
// "Applied N feedback item(s)" and nothing else, and never learns the
// authoring coworker was never told to look at it.
func TestRunPlanFeedbackApply_WarnsOnNotifyFailure(t *testing.T) {
	root := newPlanStatusTestRepo(t)

	if _, _, err := plan.Save(root, plan.Input{Raw: "# Apply warns\n"}, plan.Result{}, nil, plan.Meta{
		Topic: "Apply warns", Slug: "apply-warns",
		Provenance: &plan.Provenance{AgentID: "Ox#1", AgentType: "claude"},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// A regular file at .sageox/agent_tasks makes agenttask.NewStore's
	// MkdirAll fail deterministically, without disturbing the
	// .sageox/config.json the ledger resolver already depends on.
	if err := os.WriteFile(filepath.Join(root, ".sageox", "agent_tasks"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	feedbackPath := filepath.Join(t.TempDir(), "feedback.json")
	feedback := `{"items":[{"anchor":"h1","status":"comment","note":"hi"}]}`
	if err := os.WriteFile(feedbackPath, []byte(feedback), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	// cli.PrintWarning writes straight to os.Stderr, bypassing cmd.SetErr —
	// swap the real stream (per the codebase's own lesson: asserting on
	// stdout here would go red for the wrong reason and look identical to a
	// silently-swallowed warning).
	oldStderr := os.Stderr
	r, w, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = oldStderr })

	applyErr := runPlanFeedbackApply(cmd, "apply-warns", feedbackPath)

	w.Close()
	os.Stderr = oldStderr
	var stderrBuf bytes.Buffer
	io.Copy(&stderrBuf, r)

	if applyErr != nil {
		t.Fatalf("runPlanFeedbackApply: %v", applyErr)
	}
	if !strings.Contains(stderrBuf.String(), "could not notify the plan's authoring coworker") {
		t.Errorf("want a notify-failure warning on stderr, got %q", stderrBuf.String())
	}
}

// TestEnqueueFailureIsSettled_ClassifiesKnownErrors verifies the retry gate
// distinguishes deterministic agenttask.Enqueue failures (a retry cannot
// possibly help — same input, same outcome) from unclassified ones, which
// are treated as possibly-transient and get a retry.
// Failure prevented: retrying a structurally broken task-store path (a file
// where a directory belongs) on every enqueue just adds latency for a
// failure that will never clear on its own (caught in PR #996 review).
func TestEnqueueFailureIsSettled_ClassifiesKnownErrors(t *testing.T) {
	settled := []string{
		"project root cannot be empty",
		"failed to resolve project root: boom",
		"failed to create task directory: mkdir x: not a directory",
		"task cannot be nil",
		"task title cannot be empty",
		`unknown task kind "bogus" (allowed: doctor, session-finalize, anti-entropy, custom)`,
		"task title exceeds 500 bytes",
		"task body exceeds 4000 bytes",
		"task payload exceeds 2000 bytes",
		`task payload key "token" looks sensitive; payload is surfaced to an AI coworker and must not carry secrets`,
		`new tasks must start "ready", got "in_progress"`,
		"failed to encode payload: json: unsupported value",
	}
	for _, msg := range settled {
		if !enqueueFailureIsSettled(errors.New(msg)) {
			t.Errorf("want settled (no retry) for %q", msg)
		}
	}

	unclassified := []string{
		"failed to open task db: database is locked",
		"failed to insert task: database is locked",
		"integrity_check: corrupt",
		"schema version mismatch: db=1 want=2",
		"context deadline exceeded",
	}
	for _, msg := range unclassified {
		if enqueueFailureIsSettled(errors.New(msg)) {
			t.Errorf("want NOT settled (retryable) for %q", msg)
		}
	}
}
