package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/plan"
)

// lifecycleFixture is a durable review fixture whose plan has an event log on
// the remote and an author's unsaved edit to plan.md in the worktree.
func lifecycleFixture(t *testing.T) *durableReviewFixture {
	t.Helper()
	f := newDurableReviewFixture(t)
	if err := plan.AppendEvent(context.Background(), f.planDir, plan.Event{PlanID: "pln_lifecycle01", Kind: plan.EventCreated, Status: plan.PlanStatusDraft}); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, f.ledger, "add", "data")
	runGitInDir(t, f.ledger, "commit", "-q", "-m", "plan: created")
	runGitInDir(t, f.ledger, "push", "-q")
	if err := os.WriteFile(filepath.Join(f.planDir, "plan.md"), []byte("# Durable\n\nhalf-written\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestPlanLifecycle_CommitsOnlyTheEventFiles: a browser Approve and a CLI
// lifecycle verb each commit and push the event's own files, events.jsonl and
// meta.json. Failure prevented: an approval publishes the author's
// half-written plan.md with it.
func TestPlanLifecycle_CommitsOnlyTheEventFiles(t *testing.T) {
	for name, record := range map[string]func(t *testing.T, f *durableReviewFixture){
		"browser approve": func(t *testing.T, f *durableReviewFixture) {
			if code, body := reviewPOSTBody(t, f.srv.URL+"/approve", "secret", "{}"); code != http.StatusOK {
				t.Fatalf("approve: %d %s", code, body)
			}
		},
		"ox plan abandon": func(t *testing.T, f *durableReviewFixture) {
			if _, err := runLifecycleVerbForTest(t, f.planDir, plan.EventAbandoned, true, lifecycleVerbFlags{Reason: "replaced"}); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := lifecycleFixture(t)
			record(t, f)
			want := []string{"data/plans/2026-10-01-durable/events.jsonl", "data/plans/2026-10-01-durable/meta.json"}
			if got := strings.Fields(runGitInDir(t, f.origin, "show", "--name-only", "--format=", "main")); !slices.Equal(got, want) {
				t.Fatalf("the remote tip's commit carries %v, want only %v", got, want)
			}
			if got := runGitInDir(t, f.ledger, "diff", "--name-only"); got != "data/plans/2026-10-01-durable/plan.md" {
				t.Fatalf("the author's edit must stay uncommitted, unstaged changes %q", got)
			}
			if staged := runGitInDir(t, f.ledger, "diff", "--cached", "--name-only"); staged != "" {
				t.Fatalf("nothing may stay staged, index has %q", staged)
			}
		})
	}
}

// TestPlanLifecycle_RefusesWhileTheLedgerNeedsRepair: while the Ledger holds
// an unrelated conflict, a rebase left in progress, or a git lock, an Approve
// records nothing and says the Ledger needs repair. Failure prevented: the
// event's files are written but cannot commit, and stay uncommitted where the
// daemon's next autostash pull can conflict on them and stop the clone's sync.
func TestPlanLifecycle_RefusesWhileTheLedgerNeedsRepair(t *testing.T) {
	for name, wreck := range map[string]func(t *testing.T, ledger string){
		"unmerged entry": func(t *testing.T, ledger string) {
			blob := strings.TrimSpace(runGitInDir(t, ledger, "rev-parse", "HEAD:.gitignore"))
			update := exec.Command("git", "-C", ledger, "update-index", "--index-info")
			update.Stdin = strings.NewReader(fmt.Sprintf("100644 %s 1\tsessions/s/meta.json\n100644 %[1]s 2\tsessions/s/meta.json\n100644 %[1]s 3\tsessions/s/meta.json\n", blob))
			if out, err := update.CombinedOutput(); err != nil {
				t.Fatalf("stage a conflict: %v\n%s", err, out)
			}
		},
		"rebase in progress": func(t *testing.T, ledger string) {
			if err := os.MkdirAll(filepath.Join(ledger, ".git", "rebase-merge"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"index lock held": func(t *testing.T, ledger string) {
			if err := os.WriteFile(filepath.Join(ledger, ".git", "index.lock"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := lifecycleFixture(t)
			wreck(t, f.ledger)
			events := filepath.Join(f.planDir, "events.jsonl")
			before, err := os.ReadFile(events)
			if err != nil {
				t.Fatal(err)
			}
			if code, body := reviewPOSTBody(t, f.srv.URL+"/approve", "secret", "{}"); code != http.StatusServiceUnavailable || !strings.Contains(body, "repair") {
				t.Fatalf("want 503 naming the repair, got %d %s", code, body)
			}
			if after, err := os.ReadFile(events); err != nil || !bytes.Equal(before, after) {
				t.Fatalf("a refused approval must write nothing (err %v)", err)
			}
		})
	}
}

// TestPlanLifecycle_NeverCommitsOutsideALedgerCheckout: a Ledger dir that is
// not a git checkout, inside some other repository, gets the event recorded
// and reported not committed by both entry points, and that repository gets
// no commit. Failure prevented: git run in the Ledger dir finds the
// enclosing repository (a project's) and commits the event there.
func TestPlanLifecycle_NeverCommitsOutsideALedgerCheckout(t *testing.T) {
	outer := t.TempDir()
	runGitInDir(t, outer, "init", "-q", "-b", "main")
	runGitInDir(t, outer, "commit", "-q", "--allow-empty", "-m", "outer")
	head := runGitInDir(t, outer, "rev-parse", "HEAD")
	ledger := filepath.Join(outer, "ledger")
	planDir := filepath.Join(ledger, "data", "plans", "2026-10-01-p")
	if err := plan.AppendEvent(context.Background(), planDir, plan.Event{PlanID: "pln_outside01", Kind: plan.EventCreated, Status: plan.PlanStatusDraft}); err != nil {
		t.Fatal(err)
	}
	prev := planLedgerPathFor
	planLedgerPathFor = func(string) (string, error) { return ledger, nil }
	t.Cleanup(func() { planLedgerPathFor = prev })

	srv, _, _ := newTestReviewServer(t, planDir)
	code, body := reviewPOSTBody(t, srv.URL+"/approve", "secret", "{}")
	if r := decodeReviewResp(t, body); code != http.StatusOK || !r.Saved || r.Committed {
		t.Fatalf("approve: want saved, not committed; got %d %s", code, body)
	}
	out, err := runLifecycleVerbForTest(t, planDir, plan.EventAbandoned, true, lifecycleVerbFlags{Reason: "replaced"})
	if err != nil || !strings.Contains(out, "not a git checkout") {
		t.Fatalf("abandon: want recorded, not shared; got %v %s", err, out)
	}
	if got := plan.CurrentStatus(planDir); got != plan.PlanStatusAbandoned {
		t.Fatalf("both events must be recorded, status %q", got)
	}
	if got := runGitInDir(t, outer, "rev-parse", "HEAD"); got != head {
		t.Fatal("the enclosing repository got a commit")
	}
	if staged := runGitInDir(t, outer, "diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("the enclosing repository got staged files: %q", staged)
	}
}

// TestPlanLifecycle_RetryCommitsOnlyTheEventFiles: an Approve whose push
// fails leaves a marker owing only the event's files, the retry commits
// nothing else, and a marker owed for the whole dir survives a lifecycle push
// that succeeds. Failure prevented: the retry of an approval publishes the
// author's half-written plan.md, or clears an author's save still waiting to
// be committed.
func TestPlanLifecycle_RetryCommitsOnlyTheEventFiles(t *testing.T) {
	f := lifecycleFixture(t)
	runGitInDir(t, f.ledger, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
	code, body := reviewPOSTBody(t, f.srv.URL+"/approve", "secret", "{}")
	if r := decodeReviewResp(t, body); code != http.StatusOK || !r.Committed || r.Pushed {
		t.Fatalf("want committed, not pushed; got %d %s", code, body)
	}
	markers, err := listPlanPushPending(f.ledger)
	if err != nil || len(markers) != 1 || !slices.Equal(markers[0].Files, planEventFiles) {
		t.Fatalf("want one marker owing only %v, got %+v (err %v)", planEventFiles, markers, err)
	}

	runGitInDir(t, f.ledger, "remote", "set-url", "origin", f.origin)
	if ok, failed, err := flushPendingPlanPushes(context.Background(), f.ledger); err != nil || ok != 1 || failed != 0 {
		t.Fatalf("flush: ok=%d failed=%d err=%v", ok, failed, err)
	}
	if !strings.Contains(runGitInDir(t, f.origin, "show", "main:data/plans/2026-10-01-durable/events.jsonl"), `"approved"`) {
		t.Fatal("the retry must publish the approval")
	}
	if got := runGitInDir(t, f.origin, "show", "main:data/plans/2026-10-01-durable/plan.md"); got != "# Durable" {
		t.Fatalf("the retry must not publish the author's edit, remote plan.md is %q", got)
	}

	writePlanPushMarkerFile(t, f.ledger, "data__plans__2026-10-01-durable.json", `{"plan_dir":"data/plans/2026-10-01-durable"}`)
	if code, body := reviewPOSTBody(t, f.srv.URL+"/approve", "secret", "{}"); code != http.StatusOK {
		t.Fatalf("re-approve: %d %s", code, body)
	}
	if markers, err := listPlanPushPending(f.ledger); err != nil || len(markers) != 1 || len(markers[0].Files) != 0 {
		t.Fatalf("the whole-dir marker must survive, got %+v (err %v)", markers, err)
	}
}

// TestPlanLifecycle_ReapproveCommitsAnEarlierApprovalLeftUncommitted: an
// approval whose commit failed is recorded but uncommitted, with a marker for
// its retry; a second Approve, which records nothing new, commits and
// publishes it. Failure prevented: the second Approve pushes the old tip,
// reports it synced, clears the marker, and strands the approval uncommitted.
func TestPlanLifecycle_ReapproveCommitsAnEarlierApprovalLeftUncommitted(t *testing.T) {
	f := lifecycleFixture(t)
	if _, err := plan.AppendPlanEvent(context.Background(), f.planDir, plan.EventApproved, plan.PlanEventFields{}); err != nil {
		t.Fatal(err)
	}
	recordPlanPushOutcome(context.Background(), f.ledger, f.planDir, errors.New("commit failed"), planEventFiles...)

	code, body := reviewPOSTBody(t, f.srv.URL+"/approve", "secret", "{}")
	if r := decodeReviewResp(t, body); code != http.StatusOK || !r.Pushed {
		t.Fatalf("re-approve: want pushed, got %d %s", code, body)
	}
	if !strings.Contains(runGitInDir(t, f.origin, "show", "main:data/plans/2026-10-01-durable/events.jsonl"), `"approved"`) {
		t.Fatal("the earlier approval must reach the remote")
	}
	if markers, err := listPlanPushPending(f.ledger); err != nil || len(markers) != 0 {
		t.Fatalf("the marker clears once the approval is published, got %+v (err %v)", markers, err)
	}
}

// TestAwaitSnapshot_SeesALegacyApprovalAfterBackfill: a plan saved before
// event logs existed keeps its approval in meta.json alone; once `ox plan
// backfill` gives it a first event, the wait still ends as approved. Failure
// prevented: the backfill's draft first event outranks the legacy approval,
// and an AI coworker waits on an approved plan until its timeout.
func TestAwaitSnapshot_SeesALegacyApprovalAfterBackfill(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"topic":"Old","slug":"old","status":"approved"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := plan.ApplyBackfillMeta(context.Background(), dir, "Old plan", "old-plan"); err != nil {
		t.Fatal(err)
	}
	if res, done := awaitSnapshot(dir); !done || res.Status != "approved" {
		t.Fatalf("want the wait over as approved, got done=%v %+v", done, res)
	}
}

// TestAwaitSnapshot_ReadsTheEventLogStatus: a plan its event log says is
// approved ends the wait even where meta.json, a snapshot two clones can
// leave disagreeing with the log, still says draft. Failure prevented: an AI
// coworker keeps waiting on a plan its reviewer already approved.
func TestAwaitSnapshot_ReadsTheEventLogStatus(t *testing.T) {
	dir := seedPlanEvents(t)
	if err := plan.AppendEvent(context.Background(), dir, plan.Event{PlanID: "pln_cmdtest0000000000001", Kind: plan.EventApproved, Status: plan.PlanStatusApproved}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"topic":"t","status":"draft"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if res, done := awaitSnapshot(dir); !done || res.Status != "approved" {
		t.Fatalf("want the wait over as approved, got done=%v %+v", done, res)
	}
}
