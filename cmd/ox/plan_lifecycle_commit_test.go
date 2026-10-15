package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/plan"
	"github.com/spf13/cobra"
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
	recordPlanPushOutcome(context.Background(), f.ledger, f.planDir, time.Now(), errors.New("commit failed"), planEventFiles...)

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

// TestPlanLifecycle_ReRunReportsWhetherItSharedTheEvent: `ox plan abandon`
// whose push failed is run again, which records nothing new and retries the
// push. While the push still fails the re-run says the abandonment is not
// shared; once it can push, it publishes it. Failure prevented: the re-run
// reports only "no change", and the abandonment stays on one laptop.
func TestPlanLifecycle_ReRunReportsWhetherItSharedTheEvent(t *testing.T) {
	f := lifecycleFixture(t)
	runGitInDir(t, f.ledger, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
	for _, run := range []string{"the first run", "a re-run"} {
		out, err := runLifecycleVerbForTest(t, f.planDir, plan.EventAbandoned, true, lifecycleVerbFlags{Reason: "replaced"})
		if err != nil {
			t.Fatal(err)
		}
		if res := decodeStrictLifecycleJSON[planLifecycleResult](t, out); res.Share == nil || res.Share.Shared {
			t.Fatalf("%s must report the abandonment not shared, got %s", run, out)
		}
	}
	runGitInDir(t, f.ledger, "remote", "set-url", "origin", f.origin)
	if _, err := runLifecycleVerbForTest(t, f.planDir, plan.EventAbandoned, true, lifecycleVerbFlags{Reason: "replaced"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(runGitInDir(t, f.origin, "show", "main:data/plans/2026-10-01-durable/events.jsonl"), `"abandoned"`) {
		t.Fatal("the re-run must publish the abandonment")
	}
}

// TestPlanLifecycle_ATeammatesApprovalSurvivesTheAuthorsSessionStop: Sam
// approves on Sam's own clone while Devon's session is still recording;
// Devon's session stop then stamps the session id onto Devon's event lines
// and pushes over the approval. Failure prevented: the merge keeps Devon's
// restamped "created" line after the approval, and the whole team reads the
// plan as a draft again, saved twice.
func TestPlanLifecycle_ATeammatesApprovalSurvivesTheAuthorsSessionStop(t *testing.T) {
	f := newDurableReviewFixture(t)
	ctx := context.Background()
	if err := plan.AppendEvent(ctx, f.planDir, plan.Event{PlanID: "pln_lifecycle01", Kind: plan.EventCreated, Status: plan.PlanStatusDraft, SessionName: "devon-session"}); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, f.ledger, "add", "data")
	runGitInDir(t, f.ledger, "commit", "-q", "-m", "plan: created")
	runGitInDir(t, f.ledger, "push", "-q")

	sam := filepath.Join(t.TempDir(), "sam")
	runGitInDir(t, filepath.Dir(sam), "clone", "-q", "-b", "main", f.origin, sam)
	samPlan := filepath.Join(sam, "data", "plans", "2026-10-01-durable")
	if _, err := plan.AppendPlanEvent(ctx, samPlan, plan.EventApproved, plan.PlanEventFields{}); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, sam, "commit", "-q", "-am", "plan: approved")
	runGitInDir(t, sam, "push", "-q")

	// Devon's session stop, as reconcileProducedPlansAtStop runs it
	if _, err := plan.BackfillSessionID(ctx, f.planDir, "devon-session", "ses_devon"); err != nil {
		t.Fatal(err)
	}
	if err := commitAndPushPlanDir(ctx, f.ledger, f.planDir); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, sam, "pull", "-q", "--rebase")
	if got := plan.CurrentStatus(samPlan); got != plan.PlanStatusApproved {
		t.Fatalf("after Devon's session stop the team reads the plan as %q, want approved", got)
	}
	if got := planRevisionCount(samPlan); got != 1 {
		t.Fatalf("after Devon's session stop the plan counts %d revisions, want 1", got)
	}
}

// TestPlanLifecycle_AFailedCommitIsUnstagedAndRetried: a lifecycle commit
// that fails after its event is written reports it recorded but not
// committed, leaves nothing staged for a later commit to sweep in, and each
// entry point's retry then publishes only the event's files. Failure
// prevented: the event's files stay staged for an unrelated commit to carry,
// or the event is never retried.
func TestPlanLifecycle_AFailedCommitIsUnstagedAndRetried(t *testing.T) {
	for _, tc := range []struct {
		name, event   string
		record, retry func(t *testing.T, f *durableReviewFixture)
	}{
		{
			name: "browser approve, retried by the flush", event: `"approved"`,
			record: func(t *testing.T, f *durableReviewFixture) {
				code, body := reviewPOSTBody(t, f.srv.URL+"/approve", "secret", "{}")
				if r := decodeReviewResp(t, body); code != http.StatusOK || !r.Saved || r.Committed {
					t.Fatalf("want saved, not committed; got %d %s", code, body)
				}
			},
			retry: func(t *testing.T, f *durableReviewFixture) {
				if ok, failed, err := flushPendingPlanPushes(context.Background(), f.ledger); err != nil || ok != 1 || failed != 0 {
					t.Fatalf("flush: ok=%d failed=%d err=%v", ok, failed, err)
				}
			},
		},
		{
			name: "ox plan abandon, retried by the flush", event: `"abandoned"`,
			record: func(t *testing.T, f *durableReviewFixture) {
				out, err := runLifecycleVerbForTest(t, f.planDir, plan.EventAbandoned, true, lifecycleVerbFlags{Reason: "replaced"})
				if err != nil || !strings.Contains(out, "ledger commit failed") {
					t.Fatalf("want recorded and reported not shared, got %v %s", err, out)
				}
			},
			retry: func(t *testing.T, f *durableReviewFixture) {
				if ok, failed, err := flushPendingPlanPushes(context.Background(), f.ledger); err != nil || ok != 1 || failed != 0 {
					t.Fatalf("flush: ok=%d failed=%d err=%v", ok, failed, err)
				}
			},
		},
		{
			name: "ox plan abandon, retried by running it again", event: `"abandoned"`,
			record: func(t *testing.T, f *durableReviewFixture) {
				out, err := runLifecycleVerbForTest(t, f.planDir, plan.EventAbandoned, true, lifecycleVerbFlags{Reason: "replaced"})
				if err != nil || !strings.Contains(out, "ledger commit failed") {
					t.Fatalf("want recorded and reported not shared, got %v %s", err, out)
				}
			},
			retry: func(t *testing.T, f *durableReviewFixture) {
				if _, err := runLifecycleVerbForTest(t, f.planDir, plan.EventAbandoned, true, lifecycleVerbFlags{Reason: "replaced"}); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := lifecycleFixture(t)
			prev := commitPlanSnapshot
			t.Cleanup(func() { commitPlanSnapshot = prev })
			commitPlanSnapshot = func(context.Context, string, string, ...string) (bool, error) {
				return false, errors.New("disk full")
			}
			tc.record(t, f)
			if staged := runGitInDir(t, f.ledger, "diff", "--cached", "--name-only"); staged != "" {
				t.Fatalf("a failed commit must leave nothing staged, index has %q", staged)
			}
			commitPlanSnapshot = prev
			tc.retry(t, f)
			if !strings.Contains(runGitInDir(t, f.origin, "show", "main:data/plans/2026-10-01-durable/events.jsonl"), tc.event) {
				t.Fatalf("the retry must publish the %s event", tc.event)
			}
			if got := runGitInDir(t, f.origin, "show", "main:data/plans/2026-10-01-durable/plan.md"); got != "# Durable" {
				t.Fatalf("the retry must not publish the author's edit, remote plan.md is %q", got)
			}
		})
	}
}

// TestPlanLifecycle_ACanceledCommitStillLands: the caller cancels (Ctrl-C, a
// reviewer closing the page) while the event's commit holds the repo lock.
// Failure prevented: the cancellation kills git mid-commit, which can leave
// index.lock behind and block every commit and pull on the clone for an hour.
func TestPlanLifecycle_ACanceledCommitStillLands(t *testing.T) {
	f := lifecycleFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prev := commitPlanSnapshot
	t.Cleanup(func() { commitPlanSnapshot = prev })
	commitPlanSnapshot = func(gctx context.Context, repo, msg string, paths ...string) (bool, error) {
		cancel()
		return prev(gctx, repo, msg, paths...)
	}
	if _, err := commitPlanEvent(ctx, f.ledger, f.planDir, plan.EventApproved, plan.PlanEventFields{}); err != nil {
		t.Fatalf("a commit that already holds the lock must finish: %v", err)
	}
	if staged := runGitInDir(t, f.ledger, "diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("index has %q left staged", staged)
	}
	if got := runGitInDir(t, f.ledger, "show", "HEAD:data/plans/2026-10-01-durable/events.jsonl"); !strings.Contains(got, `"approved"`) {
		t.Fatal("the approval is not in the commit")
	}
}

// TestPlanLifecycle_ACanceledPushNeverLeavesARebase: a Ctrl-C reaches `ox
// plan abandon` while its push rebases over a teammate's commit. Failure
// prevented: the cancel kills the rebase and skips its abort, and the Ledger
// stays mid-rebase, refusing every plan write until it is repaired.
func TestPlanLifecycle_ACanceledPushNeverLeavesARebase(t *testing.T) {
	f := lifecycleFixture(t)
	teammate := filepath.Join(t.TempDir(), "teammate")
	runGitInDir(t, filepath.Dir(teammate), "clone", "-q", "-b", "main", f.origin, teammate)
	if err := os.WriteFile(filepath.Join(teammate, "notes.md"), []byte("teammate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, teammate, "add", "notes.md")
	runGitInDir(t, teammate, "commit", "-q", "-m", "teammate")
	runGitInDir(t, teammate, "push", "-q")

	// git runs post-checkout mid-rebase: the hook marks that moment, then
	// holds the rebase open until the test has canceled
	hookDir := t.TempDir()
	rebasing, release := filepath.Join(hookDir, "rebasing"), filepath.Join(hookDir, "release")
	hook := fmt.Sprintf(`#!/bin/sh
[ -d "$(git rev-parse --git-dir)/rebase-merge" ] || exit 0
touch %q
i=0
while [ ! -e %q ] && [ $i -lt 1000 ]; do sleep 0.01; i=$((i+1)); done
`, rebasing, release)
	if err := os.WriteFile(filepath.Join(f.ledger, ".git", "hooks", "post-checkout"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			if _, err := os.Stat(rebasing); err == nil {
				cancel()
				_ = os.WriteFile(release, nil, 0o644)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	cmd.Flags().Bool("json", true, "")
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := runPlanLifecycleVerbOnDir(cmd, "", f.planDir, "durable", plan.EventAbandoned, lifecycleVerbFlags{Reason: "replaced"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rebasing); err != nil {
		t.Fatal("the push never rebased, so nothing was canceled mid-rebase")
	}
	if gitutil.IsRebaseInProgress(f.ledger) {
		t.Fatal("the canceled push left the Ledger mid-rebase")
	}
	if !strings.Contains(runGitInDir(t, f.origin, "show", "main:data/plans/2026-10-01-durable/events.jsonl"), `"abandoned"`) {
		t.Fatal("the abandonment did not reach the remote")
	}
}

// TestPlanCommits_NeverResolveAConflictTheyDidNotMake: while a plan file is
// left conflicted (an autostash re-apply the daemon could not merge), neither
// a lifecycle retry's file commit nor an authoring commit stages it, so the
// conflict stays where the daemon and `ox doctor` look for it. Failure
// prevented: `git add` marks the file resolved with its conflict markers
// inside, the conflict signal disappears, and every later commit of the plan
// fails on the markers.
func TestPlanCommits_NeverResolveAConflictTheyDidNotMake(t *testing.T) {
	for name, files := range map[string][]string{"lifecycle retry": planEventFiles, "authoring commit": nil} {
		t.Run(name, func(t *testing.T) {
			f := lifecycleFixture(t)
			const meta = "data/plans/2026-10-01-durable/meta.json"
			blob := strings.TrimSpace(runGitInDir(t, f.ledger, "rev-parse", "HEAD:"+meta))
			update := exec.Command("git", "-C", f.ledger, "update-index", "--index-info")
			update.Stdin = strings.NewReader(fmt.Sprintf("0 %[1]s 0\t%[2]s\n100644 %[3]s 1\t%[2]s\n100644 %[3]s 2\t%[2]s\n100644 %[3]s 3\t%[2]s\n", strings.Repeat("0", len(blob)), meta, blob))
			if out, err := update.CombinedOutput(); err != nil {
				t.Fatalf("stage a conflict: %v\n%s", err, out)
			}
			head := runGitInDir(t, f.ledger, "rev-parse", "HEAD")

			if err := commitAndPushPlanDir(context.Background(), f.ledger, f.planDir, files...); !errors.Is(err, errLedgerNotSafe) {
				t.Fatalf("want a refusal naming the repair, got %v", err)
			}
			if got := runGitInDir(t, f.ledger, "ls-files", "-u", "--", meta); got == "" {
				t.Fatal("the conflict must stay in the index")
			}
			if got := runGitInDir(t, f.ledger, "rev-parse", "HEAD"); got != head {
				t.Fatal("no commit may land while the conflict stands")
			}
		})
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
