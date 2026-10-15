package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/pkg/planreview"
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

// A marker's read, merge and write wait for its lock, even under a canceled
// context, as a reviewer closing the page mid-Approve cancels it. Failure
// prevented: two processes failing on one plan dir drop what the other's
// marker owes, or a canceled Approve leaves its approval with no retry.
func TestRecordPlanPushOutcome_WaitsForTheMarkerLock(t *testing.T) {
	ledger := t.TempDir()
	planDir := filepath.Join(ledger, "data", "plans", "p")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	held, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		_ = fileutil.WithFileLock(context.Background(), planPushMarkerPath(ledger, "data/plans/p"), func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	go func() {
		recordPlanPushOutcome(ctx, ledger, planDir, time.Now(), errors.New("push canceled"), planEventFiles...)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("the marker update ran or gave up while its lock was held")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	<-done
	if markers, err := listPlanPushPending(ledger); err != nil || len(markers) != 1 || !slices.Equal(markers[0].Files, planEventFiles) {
		t.Fatalf("want the retry marker once the lock is free, got %+v (err %v)", markers, err)
	}
}

// A marker owed for the whole dir stays owed for it whichever attempt fails
// next. Failure prevented: a later lifecycle failure narrows a marker an
// author's save left owing the whole dir, and the save is never retried.
func TestRecordPlanPushOutcome_MergesWhatEachAttemptOwes(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name          string
		first, second []string
		want          []string
	}{
		{"whole dir, then files", nil, []string{"events.jsonl"}, nil},
		{"files, then whole dir", []string{"events.jsonl"}, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger := t.TempDir()
			planDir := filepath.Join(ledger, "data", "plans", "p")
			if err := os.MkdirAll(planDir, 0o755); err != nil {
				t.Fatal(err)
			}
			recordPlanPushOutcome(ctx, ledger, planDir, time.Now(), errors.New("offline"), tc.first...)
			recordPlanPushOutcome(ctx, ledger, planDir, time.Now(), errors.New("offline"), tc.second...)
			markers, err := listPlanPushPending(ledger)
			if err != nil || len(markers) != 1 || !slices.Equal(markers[0].Files, tc.want) {
				t.Fatalf("want one marker owing %v, got %+v (err %v)", tc.want, markers, err)
			}
		})
	}
}

// TestRecordPlanPushOutcome_KeepsAFailureRecordedAfterTheAttemptStarted: one
// action's push succeeds while another's commit fails and records its marker.
// Failure prevented: the earlier attempt's success clears the later failure's
// marker, and nothing retries that event.
func TestRecordPlanPushOutcome_KeepsAFailureRecordedAfterTheAttemptStarted(t *testing.T) {
	ctx := context.Background()
	ledger := t.TempDir()
	planDir := filepath.Join(ledger, "data", "plans", "p")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	recordPlanPushOutcome(ctx, ledger, planDir, time.Now(), errors.New("commit failed"), planEventFiles...)
	recordPlanPushOutcome(ctx, ledger, planDir, started, nil, planEventFiles...)
	if markers, err := listPlanPushPending(ledger); err != nil || len(markers) != 1 {
		t.Fatalf("the later failure must keep its marker, got %+v (err %v)", markers, err)
	}
	recordPlanPushOutcome(ctx, ledger, planDir, time.Now(), nil, planEventFiles...)
	if markers, err := listPlanPushPending(ledger); err != nil || len(markers) != 0 {
		t.Fatalf("an attempt that began after the failure clears it, got %+v (err %v)", markers, err)
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

	recordPlanPushOutcome(ctx, ledger, planDir, time.Now(), errors.New("remote unreachable"))
	first, err := listPlanPushPending(ledger)
	if err != nil || len(first) != 1 {
		t.Fatalf("want one marker, got %+v err=%v", first, err)
	}
	recordPlanPushOutcome(ctx, ledger, planDir, time.Now(), errors.New("still unreachable"))
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

	recordPlanPushOutcome(ctx, ledger, planDir, time.Now(), nil)
	if left, err := listPlanPushPending(ledger); err != nil || len(left) != 0 {
		t.Fatalf("success must clear the marker, %d left (err %v)", len(left), err)
	}
	// clearing when nothing is pending is a no-op, not an error
	recordPlanPushOutcome(ctx, ledger, planDir, time.Now(), nil)
}

func TestRecordPlanPushOutcome_IgnoresNonLedgerAndUnwritableMarkerDir(t *testing.T) {
	ctx := context.Background()

	// a plan dir outside the Ledger has nothing a retry could push
	ledger := t.TempDir()
	recordPlanPushOutcome(ctx, ledger, t.TempDir(), time.Now(), errors.New("boom"))
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
	recordPlanPushOutcome(ctx, blocked, blockedPlan, time.Now(), errors.New("boom"))
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

// A retry that fails again must keep both kinds of entry — an author's
// plan-dir marker, whose attempts it counts, and a review operation's record.
func TestFlushPending_StillFailingKeepsEntriesAndCounts(t *testing.T) {
	f := newDurableReviewFixture(t)
	// a live index.lock fails the review commit, at the write and at the retry
	lock := filepath.Join(f.ledger, ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if code, body := reviewPOSTBody(t, f.srv.URL+"/feedback", "secret", roundBody("round-0001", "h1")); code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	ok, failed, err := flushPendingReviewRecords(context.Background(), f.ledger)
	if err != nil || ok != 0 || failed != 1 {
		t.Fatalf("review flush: ok=%d failed=%d err=%v", ok, failed, err)
	}
	if left, err := listReviewPending(f.ledger); err != nil || len(left) != 1 {
		t.Fatalf("a failed retry must keep the review entry: %+v (err %v)", left, err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}

	runGitInDir(t, f.ledger, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
	if err := commitPlanToLedger("root", f.planDir); err == nil {
		t.Fatal("an authoring push to a missing remote must fail")
	}
	ok, failed, err = flushPendingPlanPushes(context.Background(), f.ledger)
	if err != nil || ok != 0 || failed != 1 {
		t.Fatalf("plan-dir flush: ok=%d failed=%d err=%v", ok, failed, err)
	}
	if left, err := listPlanPushPending(f.ledger); err != nil || len(left) != 1 || left[0].FailedAttempts != 2 {
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

// TestFlushPending_LandsRetriesCarriedByADaemonReclone runs the daemon's GC
// reclone over a Ledger holding three retries — a review round and a plan
// edit whose commits a live index.lock refused, and a review entry whose
// record was never written — and then flush-pending in the new clone.
// Failure prevented (#1356): the reclone deletes the old clone's .git/ and
// the retries in it, so `ox agent prime` no longer retries the round or the
// plan edit, and the unwritten record, held only by its entry, is lost.
func TestFlushPending_LandsRetriesCarriedByADaemonReclone(t *testing.T) {
	f := newDurableReviewFixture(t)
	// the GC accepts a Ledger reclone only once it has a sessions/ dir
	if err := os.MkdirAll(filepath.Join(f.ledger, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.ledger, "sessions", ".gitkeep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, f.ledger, "add", "sessions")
	runGitInDir(t, f.ledger, "commit", "-q", "-m", "sessions")
	runGitInDir(t, f.ledger, "push", "-q")
	// a fresh clone checks out the remote's HEAD, as it does from a real server
	runGitInDir(t, f.origin, "symbolic-ref", "HEAD", "refs/heads/main")

	lock := filepath.Join(f.ledger, ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if code, body := reviewPOSTBody(t, f.srv.URL+"/feedback", "secret", roundBody("round-0001", "h1")); code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	if err := os.WriteFile(filepath.Join(f.planDir, "plan.md"), []byte("# Durable v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := commitPlanToLedger("root", f.planDir); err == nil {
		t.Fatal("a plan commit under a live index.lock must fail")
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	rel, err := ledgerRelPath(f.ledger, f.planDir)
	if err != nil {
		t.Fatal(err)
	}
	q, err := planreview.ParseRoundRequest([]byte(roundBody("round-000L", "hL")))
	if err != nil {
		t.Fatal(err)
	}
	rec, err := q.Record("durable", q.Reviewer, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	lost := reviewPending{PlanDir: rel, Plan: filepath.Base(f.planDir), Kind: "round", ID: q.ID, Path: rec.Path, SHA256: sha256Hex(rec.Data), Data: rec.Data}
	if err := writeReviewPending(lost.path(f.ledger), lost); err != nil {
		t.Fatal(err)
	}

	retries := func() map[string]string {
		t.Helper()
		got := map[string]string{}
		for _, dir := range []string{planPushPendingDir, planReviewPendingDir} {
			entries, err := os.ReadDir(filepath.Join(f.ledger, filepath.FromSlash(dir)))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			for _, e := range entries {
				b, err := os.ReadFile(filepath.Join(f.ledger, filepath.FromSlash(dir), e.Name()))
				if err != nil {
					t.Fatal(err)
				}
				got[dir+"/"+e.Name()] = string(b)
			}
		}
		return got
	}
	before := retries()
	if len(before) != 3 {
		t.Fatalf("want a plan push marker and two review entries before the reclone, got %v", slices.Sorted(maps.Keys(before)))
	}
	oldGit, err := os.Stat(filepath.Join(f.ledger, ".git"))
	if err != nil {
		t.Fatal(err)
	}

	// the daemon's own forced-GC entry point, pointed at this Ledger
	prevFileTransport := gitserver.TestAllowFileTransport
	gitserver.TestAllowFileTransport = true
	t.Cleanup(func() { gitserver.TestAllowFileTransport = prevFileTransport })
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".sageox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveLocalConfig(project, &config.LocalConfig{Ledger: &config.LedgerConfig{Path: f.ledger}}); err != nil {
		t.Fatal(err)
	}
	cfg := daemon.DefaultConfig()
	cfg.ProjectRoot = project
	scheduler := daemon.NewSyncScheduler(cfg, slog.New(slog.DiscardHandler))
	if err := scheduler.WorkspaceRegistry().LoadFromConfig(); err != nil {
		t.Fatal(err)
	}
	if !scheduler.WorkspaceRegistry().SetLedgerCloneURL("file://" + f.origin) {
		t.Fatal("the registry must hold the Ledger from config.local.toml")
	}
	if resp := scheduler.TriggerGC(context.Background()); !resp.LedgerTriggered || len(resp.Errors) > 0 {
		t.Fatalf("the Ledger reclone must succeed: %+v", resp)
	}
	if newGit, err := os.Stat(filepath.Join(f.ledger, ".git")); err != nil || os.SameFile(oldGit, newGit) {
		t.Fatalf("the GC must replace the clone (err %v)", err)
	}
	if after := retries(); !maps.Equal(after, before) {
		t.Fatalf("the new clone must hold every retry byte for byte\nbefore: %v\nafter:  %v",
			slices.Sorted(maps.Keys(before)), slices.Sorted(maps.Keys(after)))
	}

	t.Chdir(f.ledger) // flush-pending resolves the project from the cwd's git root
	var out bytes.Buffer
	planFlushPendingCmd.SetOut(&out)
	planFlushPendingCmd.SetContext(context.Background())
	t.Cleanup(func() { planFlushPendingCmd.SetOut(nil) })
	if err := planFlushPendingCmd.RunE(planFlushPendingCmd, nil); err != nil {
		t.Fatalf("flush-pending: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "plan push retry: 3 pushed, 0 still pending" {
		t.Fatalf("output = %q", got)
	}
	if n := feedbackRoundsOnRemote(t, f.origin); n != 2 || !strings.Contains(remoteFiles(t, f.origin), lost.Path) {
		t.Fatalf("both review records must reach the remote, %d rounds there", n)
	}
	if got := runGitInDir(t, f.origin, "show", "main:"+rel+"/plan.md"); got != "# Durable v2" {
		t.Fatalf("the plan edit must reach the remote, plan.md = %q", got)
	}
	if left := retries(); len(left) != 0 {
		t.Fatalf("every retry must clear once it lands, left %v", slices.Sorted(maps.Keys(left)))
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

// TestListReviewPending_TrustsOnlyIntactEntries verifies the retry reads only
// entries it can trust: one whose bytes no longer hash to its recorded
// SHA-256, one naming a record outside the Ledger, and one that is not an
// entry at all are skipped, never published. Failure prevented: a retry
// publishes changed bytes under the original operation, or writes outside the
// plan (contract 3.4).
func TestListReviewPending_TrustsOnlyIntactEntries(t *testing.T) {
	ledger := t.TempDir()
	good := reviewPending{PlanDir: "data/plans/p", Plan: "p", Kind: "round", ID: "round-0001",
		Path: "feedback/round-x.json", Data: []byte(`{"id":"round-0001"}`)}
	good.SHA256 = sha256Hex(good.Data)
	if err := writeReviewPending(good.path(ledger), good); err != nil {
		t.Fatal(err)
	}
	tampered, escaping := good, good
	tampered.ID, tampered.Data = "round-0002", []byte(`{"id":"round-0002","edited":true}`)
	escaping.ID, escaping.PlanDir = "round-0003", "../outside"
	for _, e := range []reviewPending{tampered, escaping} {
		if err := writeReviewPending(e.path(ledger), e); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(ledger, filepath.FromSlash(planReviewPendingDir))
	for name, body := range map[string]string{"junk.json": "{not json", ".tmp-123": "{}", "notes.txt": "x"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := listReviewPending(ledger)
	if err != nil || len(got) != 1 || got[0].ID != "round-0001" {
		t.Fatalf("want only the intact entry, got %+v (err %v)", got, err)
	}
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

	// a review entry alone is pending work too, and survives the same way
	reviewsOnly := t.TempDir()
	e := reviewPending{PlanDir: "data/plans/p", Plan: "p", Kind: "round", ID: "round-0001", Path: "feedback/r.json", Data: []byte("{}")}
	e.SHA256 = sha256Hex(e.Data)
	if err := writeReviewPending(e.path(reviewsOnly), e); err != nil {
		t.Fatal(err)
	}
	planLedgerPathFor = func(string) (string, error) { return reviewsOnly, nil }
	kickPendingPlanPushes(t.TempDir())
	if left, err := listReviewPending(reviewsOnly); err != nil || len(left) != 1 {
		t.Fatalf("a review entry must survive a kick that could not start, %d left (err %v)", len(left), err)
	}

	// a temp file a crash left behind is not pending work, so no retry is
	// attempted; the entry above is, so one is (the log is the only witness:
	// the retry cannot start under go test)
	var logs bytes.Buffer
	prevLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prevLog) })
	tmpOnly := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpOnly, filepath.FromSlash(planReviewPendingDir)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpOnly, filepath.FromSlash(planReviewPendingDir), ".tmp-123"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	planLedgerPathFor = func(string) (string, error) { return tmpOnly, nil }
	kickPendingPlanPushes(t.TempDir())
	if strings.Contains(logs.String(), "plan push retry") {
		t.Fatalf("a temp file alone must not start a retry, logged:\n%s", logs.String())
	}
	planLedgerPathFor = func(string) (string, error) { return reviewsOnly, nil }
	kickPendingPlanPushes(t.TempDir())
	if !strings.Contains(logs.String(), "plan push retry not started") {
		t.Fatalf("a review entry must attempt a retry, logged:\n%s", logs.String())
	}

	// an unreadable marker dir is an early return, not a crash
	broken := t.TempDir()
	if err := os.MkdirAll(filepath.Join(broken, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, filepath.FromSlash(planPushPendingDir)), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	planLedgerPathFor = func(string) (string, error) { return broken, nil }
	kickPendingPlanPushes(t.TempDir())
}
