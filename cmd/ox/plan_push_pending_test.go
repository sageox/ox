package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePlanPushMarkerFile drops a raw marker file into a Ledger's pending dir.
func writePlanPushMarkerFile(t *testing.T, ledger, name, body string) {
	t.Helper()
	dir := filepath.Join(ledger, filepath.FromSlash(planPushPendingDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A repeated failure must keep the first-failure time and count attempts, and
// a later success must clear the marker; that is what tells an operator how
// long a plan has been stranded off the remote.
func TestRecordPlanPushOutcome_RepeatedFailureThenSuccess(t *testing.T) {
	ledger := t.TempDir()
	planDir := filepath.Join(ledger, "data", "plans", "p1")
	// must exist: ledgerRelPath resolves symlinks (macOS /var -> /private/var)
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	recordPlanPushOutcome(ctx, ledger, planDir, errors.New("remote unreachable"))
	first, err := listPlanPushPending(ledger)
	if err != nil || len(first) != 1 {
		t.Fatalf("want one marker, got %+v err=%v", first, err)
	}
	recordPlanPushOutcome(ctx, ledger, planDir, errors.New("still unreachable"))
	second, err := listPlanPushPending(ledger)
	if err != nil || len(second) != 1 {
		t.Fatalf("want one marker after second failure, got %d (err %v)", len(second), err)
	}
	got := second[0]
	if got.PlanDir != "data/plans/p1" || got.FailedAttempts != 2 || got.LastError != "still unreachable" {
		t.Fatalf("marker = %+v", got)
	}
	if !got.FirstFailedAt.Equal(first[0].FirstFailedAt) {
		t.Fatalf("first_failed_at moved: %v -> %v", first[0].FirstFailedAt, got.FirstFailedAt)
	}

	recordPlanPushOutcome(ctx, ledger, planDir, nil)
	if left, err := listPlanPushPending(ledger); err != nil || len(left) != 0 {
		t.Fatalf("success must clear the marker, %d left (err %v)", len(left), err)
	}
	// clearing when nothing is pending is a no-op, not an error
	recordPlanPushOutcome(ctx, ledger, planDir, nil)
}

func TestRecordPlanPushOutcome_IgnoresNonLedgerAndUnwritableMarkerDir(t *testing.T) {
	ctx := context.Background()

	// a plan dir outside the Ledger has nothing a retry could push
	ledger := t.TempDir()
	recordPlanPushOutcome(ctx, ledger, t.TempDir(), errors.New("boom"))
	if _, err := os.Stat(filepath.Join(ledger, filepath.FromSlash(planPushPendingDir))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no marker dir expected for a non-ledger plan, stat err=%v", err)
	}

	// the marker dir path is occupied by a file: the write fails, is logged,
	// and never panics or surfaces to the caller
	blocked := t.TempDir()
	if err := os.MkdirAll(filepath.Join(blocked, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, filepath.FromSlash(planPushPendingDir)), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	blockedPlan := filepath.Join(blocked, "data", "plans", "p")
	if err := os.MkdirAll(blockedPlan, 0o755); err != nil {
		t.Fatal(err)
	}
	recordPlanPushOutcome(ctx, blocked, blockedPlan, errors.New("boom"))
	if _, err := listPlanPushPending(blocked); err == nil {
		t.Fatal("listing a marker dir that is a file must error")
	}
}

func TestListPlanPushPending_SkipsJunkMarkers(t *testing.T) {
	ledger := t.TempDir()
	if got, err := listPlanPushPending(ledger); err != nil || got != nil {
		t.Fatalf("missing dir must mean nothing pending: %+v err=%v", got, err)
	}

	writePlanPushMarkerFile(t, ledger, "ok.json", `{"plan_dir":"data/plans/ok"}`)
	writePlanPushMarkerFile(t, ledger, "notes.txt", `{"plan_dir":"data/plans/txt"}`)
	writePlanPushMarkerFile(t, ledger, ".marker-123.json", `{"plan_dir":"data/plans/temp"}`)
	writePlanPushMarkerFile(t, ledger, "torn.json", `{"plan_dir":`)
	writePlanPushMarkerFile(t, ledger, "empty.json", `{"plan_dir":""}`)
	writePlanPushMarkerFile(t, ledger, "escape.json", `{"plan_dir":"../../etc"}`)
	if err := os.Mkdir(filepath.Join(ledger, filepath.FromSlash(planPushPendingDir), "sub.json"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := listPlanPushPending(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].PlanDir != "data/plans/ok" {
		t.Fatalf("want only the valid marker, got %+v", got)
	}
}

// A marker whose plan dir is gone (renamed by backfill, re-cloned) must be
// dropped rather than retried forever.
func TestFlushPendingPlanPushes_DropsMarkerForMissingPlanDir(t *testing.T) {
	ledger := t.TempDir()
	writePlanPushMarkerFile(t, ledger, "data__plans__gone.json", `{"plan_dir":"data/plans/gone"}`)

	ok, failed, err := flushPendingPlanPushes(context.Background(), ledger)
	if err != nil || ok != 0 || failed != 0 {
		t.Fatalf("ok=%d failed=%d err=%v", ok, failed, err)
	}
	if left, err := listPlanPushPending(ledger); err != nil || len(left) != 0 {
		t.Fatalf("marker for a missing plan dir must be dropped, %d left (err %v)", len(left), err)
	}
}

func TestFlushPendingPlanPushes_StillFailingKeepsMarkerAndCounts(t *testing.T) {
	f := newDurableReviewFixture(t)
	runGitInDir(t, f.ledger, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
	if code, body := reviewPOSTBody(t, f.srv.URL+"/feedback", "secret", roundBody("round-0001", "h1")); code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}

	ok, failed, err := flushPendingPlanPushes(context.Background(), f.ledger)
	if err != nil || ok != 0 || failed != 1 {
		t.Fatalf("ok=%d failed=%d err=%v", ok, failed, err)
	}
	left, err := listPlanPushPending(f.ledger)
	if err != nil || len(left) != 1 || left[0].FailedAttempts != 2 {
		t.Fatalf("a failed retry must keep the marker and count it: %+v (err %v)", left, err)
	}
}

func TestPlanFlushPendingCmd_PushesAndReports(t *testing.T) {
	f := newDurableReviewFixture(t)
	runGitInDir(t, f.ledger, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
	if code, body := reviewPOSTBody(t, f.srv.URL+"/feedback", "secret", roundBody("round-0001", "h1")); code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	runGitInDir(t, f.ledger, "remote", "set-url", "origin", f.origin)

	t.Chdir(f.ledger) // flush-pending resolves the project from the cwd's git root
	var out bytes.Buffer
	planFlushPendingCmd.SetOut(&out)
	planFlushPendingCmd.SetContext(context.Background())
	t.Cleanup(func() { planFlushPendingCmd.SetOut(nil) })
	if err := planFlushPendingCmd.RunE(planFlushPendingCmd, nil); err != nil {
		t.Fatalf("flush-pending: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "plan push retry: 1 pushed, 0 still pending" {
		t.Fatalf("output = %q", got)
	}
	if n := feedbackRoundsOnRemote(t, f.origin); n != 1 {
		t.Fatalf("round should reach the remote, got %d", n)
	}
}

func TestPlanFlushPendingCmd_Errors(t *testing.T) {
	prev := planLedgerPathFor
	t.Cleanup(func() { planLedgerPathFor = prev })
	planFlushPendingCmd.SetContext(context.Background())

	t.Run("outside a git repo", func(t *testing.T) {
		t.Chdir(t.TempDir())
		err := planFlushPendingCmd.RunE(planFlushPendingCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "not inside a git repository") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no ledger configured", func(t *testing.T) {
		repo := t.TempDir()
		runGitInDir(t, repo, "init", "-q")
		t.Chdir(repo)
		planLedgerPathFor = func(string) (string, error) { return "", errors.New("no ledger") }
		err := planFlushPendingCmd.RunE(planFlushPendingCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "no ledger") {
			t.Fatalf("err = %v", err)
		}
	})
}

// kickPendingPlanPushes runs inside every prime, so each early return must be
// silent and leave markers untouched for the next attempt.
func TestKickPendingPlanPushes_EarlyReturnsLeaveMarkers(t *testing.T) {
	prev := planLedgerPathFor
	t.Cleanup(func() { planLedgerPathFor = prev })

	planLedgerPathFor = func(string) (string, error) { return "", errors.New("no ledger") }
	kickPendingPlanPushes(t.TempDir())

	empty := t.TempDir()
	planLedgerPathFor = func(string) (string, error) { return empty, nil }
	kickPendingPlanPushes(t.TempDir())

	// pending, but under `go test` the ox binary cannot be resolved, so the
	// detached flush is never started and the marker must survive
	ledger := t.TempDir()
	writePlanPushMarkerFile(t, ledger, "data__plans__p.json", `{"plan_dir":"data/plans/p"}`)
	planLedgerPathFor = func(string) (string, error) { return ledger, nil }
	kickPendingPlanPushes(t.TempDir())
	if left, err := listPlanPushPending(ledger); err != nil || len(left) != 1 {
		t.Fatalf("marker must survive a kick that could not start, %d left (err %v)", len(left), err)
	}
}
