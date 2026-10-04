package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/sageox/ox/internal/plan"
)

// plan_review_durability_test.go proves a review POST tells the page the truth
// about durability: committed and pushed reflect a real Ledger git repo with a
// real bare remote, a failed push leaves a marker the prime-triggered flush
// clears, concurrent rounds never collide on .git/index.lock, and a plan
// commit never sweeps up an unrelated staged Ledger change.

// reviewDurabilityResp is the /feedback (and /accept, /reopen, /approve)
// response decoded strictly, so a renamed or extra field fails the test
// instead of silently passing a substring match.
type reviewDurabilityResp struct {
	OK        bool    `json:"ok"`
	Saved     bool    `json:"saved"`
	RoundID   *string `json:"round_id,omitempty"`
	Duplicate *bool   `json:"duplicate,omitempty"`
	Committed bool    `json:"committed"`
	Pushed    bool    `json:"pushed"`
	Notified  *bool   `json:"notified,omitempty"`
}

func decodeReviewResp(t *testing.T, body string) reviewDurabilityResp {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	var r reviewDurabilityResp
	if err := dec.Decode(&r); err != nil {
		t.Fatalf("decode response %q: %v", body, err)
	}
	return r
}

type durableReviewFixture struct {
	ledger, origin, planDir string
	srv                     *httptest.Server
	rounds                  chan int
	activity                chan struct{}
	notifies                *atomic.Int32
}

// newDurableReviewFixture builds a Ledger with a bare origin, a plan dir in it,
// and the live review handler wired to it. The package-level seams are
// restored on cleanup, so these tests must not run in parallel.
func newDurableReviewFixture(t *testing.T) *durableReviewFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("short: real ledger git repo with bare remote push")
	}
	ledger := t.TempDir()
	origin := initGitLedgerWithOrigin(t, ledger)
	planDir := filepath.Join(ledger, "data", "plans", "2026-10-01-durable")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "plan.md"), []byte("# Durable\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	prevLedger, prevNotify := planLedgerPathFor, notifyReviewFeedback
	planLedgerPathFor = func(string) (string, error) { return ledger, nil }
	notifies := &atomic.Int32{}
	notifyReviewFeedback = func(_, _, _ string, _ int) error {
		notifies.Add(1)
		return nil
	}
	t.Cleanup(func() { planLedgerPathFor, notifyReviewFeedback = prevLedger, prevNotify })

	rounds := make(chan int, 64)
	activity := make(chan struct{}, 1)
	h := liveReviewHandlerWithActivity("root", "durable", planDir, "http://x", "secret",
		newBroadcaster(), rounds, make(chan struct{}, 1), activity)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &durableReviewFixture{ledger: ledger, origin: origin, planDir: planDir, srv: srv,
		rounds: rounds, activity: activity, notifies: notifies}
}

func roundBody(id, anchor string) string {
	return fmt.Sprintf(`{"id":%q,"reviewer":"Person A","items":[{"anchor":%q,"section":"Risks","status":"request-change","note":"tighten"}]}`, id, anchor)
}

// remoteFiles lists every path on the bare remote's main branch.
func remoteFiles(t *testing.T, origin string) string {
	t.Helper()
	return runGitInDir(t, origin, "ls-tree", "-r", "--name-only", "main")
}

func feedbackRoundsOnRemote(t *testing.T, origin string) int {
	t.Helper()
	n := 0
	for _, line := range strings.Split(remoteFiles(t, origin), "\n") {
		if strings.Contains(line, "/feedback/round-") {
			n++
		}
	}
	return n
}

// TestPlanReviewDurability_FeedbackCommittedAndPushed: the happy path — the
// round is on the bare remote when the response says pushed=true.
func TestPlanReviewDurability_FeedbackCommittedAndPushed(t *testing.T) {
	f := newDurableReviewFixture(t)

	code, body := reviewPOSTBody(t, f.srv.URL+"/feedback", "secret", roundBody("round-0001", "h1"))
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	r := decodeReviewResp(t, body)
	if !r.OK || !r.Saved || !r.Committed || !r.Pushed {
		t.Fatalf("want saved+committed+pushed, got %+v", r)
	}
	if r.RoundID == nil || *r.RoundID != "round-0001" || r.Duplicate == nil || *r.Duplicate || r.Notified == nil || !*r.Notified {
		t.Fatalf("round metadata wrong: %s", body)
	}
	if !strings.Contains(runGitInDir(t, f.origin, "log", "--format=%s", "main"), "plan: 2026-10-01-durable") {
		t.Fatal("remote log has no plan commit")
	}
	if n := feedbackRoundsOnRemote(t, f.origin); n != 1 {
		t.Fatalf("remote should carry 1 round, got %d", n)
	}
	select {
	case <-f.activity:
	default:
		t.Fatal("a successful POST must signal activity (idle reset)")
	}

	// /accept reports the same durability fields and also resets idle
	code, body = reviewPOSTBody(t, f.srv.URL+"/accept", "secret", `{"anchor":"h1"}`)
	if code != http.StatusOK {
		t.Fatalf("accept status %d: %s", code, body)
	}
	if a := decodeReviewResp(t, body); !a.Saved || !a.Committed || !a.Pushed {
		t.Fatalf("accept durability fields: %+v", a)
	}
	select {
	case <-f.activity:
	default:
		t.Fatal("/accept must reset the idle timer too")
	}
	// resolutions are one file per entry under feedback/resolutions/
	if !strings.Contains(remoteFiles(t, f.origin), "feedback/resolutions/") {
		t.Fatal("accept resolution was not pushed")
	}
}

// TestPlanReviewDurability_PushFailureMarksPendingAndFlushRecovers: an
// unreachable remote is reported (pushed=false), recorded durably, and the
// prime-triggered flush lands it once the remote is back.
func TestPlanReviewDurability_PushFailureMarksPendingAndFlushRecovers(t *testing.T) {
	f := newDurableReviewFixture(t)
	runGitInDir(t, f.ledger, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))

	code, body := reviewPOSTBody(t, f.srv.URL+"/feedback", "secret", roundBody("round-0001", "h1"))
	if code != http.StatusOK {
		t.Fatalf("a push failure must not fail the save: %d %s", code, body)
	}
	r := decodeReviewResp(t, body)
	if !r.Saved || !r.Committed || r.Pushed {
		t.Fatalf("want saved+committed, not pushed; got %+v", r)
	}
	pending, err := listPlanPushPending(f.ledger)
	if err != nil || len(pending) != 1 || pending[0].PlanDir != "data/plans/2026-10-01-durable" {
		t.Fatalf("want one pending marker for the plan, got %+v (err %v)", pending, err)
	}
	if pending[0].FailedAttempts != 1 || pending[0].LastError == "" {
		t.Fatalf("marker should record the failure: %+v", pending[0])
	}
	// the marker lives under .git/, so no ledger status can ever show it
	if st := runGitInDir(t, f.ledger, "status", "--porcelain", "--untracked-files=all"); strings.Contains(st, "plan-push-pending") {
		t.Fatalf("marker must be ignored, never an untracked Ledger file: %q", st)
	}

	runGitInDir(t, f.ledger, "remote", "set-url", "origin", f.origin)
	ok, failed, err := flushPendingPlanPushes(context.Background(), f.ledger)
	if err != nil || ok != 1 || failed != 0 {
		t.Fatalf("flush: ok=%d failed=%d err=%v", ok, failed, err)
	}
	if left, err := listPlanPushPending(f.ledger); err != nil || len(left) != 0 {
		t.Fatalf("flush must clear the marker, %d left (err %v)", len(left), err)
	}
	if n := feedbackRoundsOnRemote(t, f.origin); n != 1 {
		t.Fatalf("recovered round should be on the remote, got %d", n)
	}
}

// TestPlanReviewDurability_ConcurrentRoundsAllCommitted: 8 simultaneous
// submits never trip .git/index.lock and every round reaches the remote.
func TestPlanReviewDurability_ConcurrentRoundsAllCommitted(t *testing.T) {
	f := newDurableReviewFixture(t)
	const n = 8
	var wg sync.WaitGroup
	results := make([]string, n)
	codes := make([]int, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], results[i] = reviewPOSTBody(t, f.srv.URL+"/feedback", "secret",
				roundBody(fmt.Sprintf("round-%04d", i), fmt.Sprintf("h%d", i)))
		}()
	}
	wg.Wait()
	for i := range n {
		if codes[i] != http.StatusOK {
			t.Fatalf("post %d: %d %s", i, codes[i], results[i])
		}
		if r := decodeReviewResp(t, results[i]); !r.Committed || !r.Pushed {
			t.Fatalf("post %d not durable: %s", i, results[i])
		}
	}
	if got := feedbackRoundsOnRemote(t, f.origin); got != n {
		t.Fatalf("want %d rounds on the remote, got %d", n, got)
	}
}

// TestPlanCommit_PathspecExcludesUnrelatedStagedChange: a plan commit carries
// the plan dir only; someone else's staged Ledger change stays staged.
func TestPlanCommit_PathspecExcludesUnrelatedStagedChange(t *testing.T) {
	f := newDurableReviewFixture(t)
	other := filepath.Join(f.ledger, "data", "unrelated.txt")
	if err := os.WriteFile(other, []byte("not a plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, f.ledger, "add", "data/unrelated.txt")

	if code, body := reviewPOSTBody(t, f.srv.URL+"/feedback", "secret", roundBody("round-0001", "h1")); code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	committed := runGitInDir(t, f.ledger, "show", "--name-only", "--format=", "HEAD")
	if strings.Contains(committed, "unrelated.txt") {
		t.Fatalf("plan commit swept an unrelated staged file:\n%s", committed)
	}
	if !strings.Contains(committed, "data/plans/2026-10-01-durable/") {
		t.Fatalf("plan commit should carry the plan dir:\n%s", committed)
	}
	if staged := runGitInDir(t, f.ledger, "diff", "--cached", "--name-only"); !strings.Contains(staged, "data/unrelated.txt") {
		t.Fatalf("unrelated change must remain staged, got %q", staged)
	}
}

// TestPlanReviewDurability_DuplicateRoundNotRenotified: a retried round
// (ErrDuplicateRound) is a success, still commits, never re-notifies.
func TestPlanReviewDurability_DuplicateRoundNotRenotified(t *testing.T) {
	f := newDurableReviewFixture(t)
	prevSave := saveReviewFeedback
	seen := map[string]bool{}
	var mu sync.Mutex
	saveReviewFeedback = func(dir string, set plan.FeedbackSet, now time.Time) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		key := set.Items[0].Anchor
		if seen[key] {
			return filepath.Join(dir, "feedback", "existing.json"), plan.ErrDuplicateRound
		}
		seen[key] = true
		return prevSave(dir, set, now)
	}
	t.Cleanup(func() { saveReviewFeedback = prevSave })

	for i, wantDup := range []bool{false, true} {
		code, body := reviewPOSTBody(t, f.srv.URL+"/feedback", "secret", roundBody("round-0001", "h1"))
		if code != http.StatusOK {
			t.Fatalf("post %d: %d %s", i, code, body)
		}
		r := decodeReviewResp(t, body)
		if r.Duplicate == nil || *r.Duplicate != wantDup || !r.Saved || !r.Committed || !r.Pushed {
			t.Fatalf("post %d: %s", i, body)
		}
		if wantDup && (r.Notified == nil || *r.Notified) {
			t.Fatalf("duplicate must report notified=false: %s", body)
		}
	}
	if got := f.notifies.Load(); got != 1 {
		t.Fatalf("agent notified %d times, want 1", got)
	}
	if got := len(f.rounds); got != 1 {
		t.Fatalf("duplicate must not re-signal a round, got %d signals", got)
	}
}

// TestPlanReviewDurability_OversizeBodyRejected413: an over-limit body is
// refused outright, never truncated into a confusing parse error.
func TestPlanReviewDurability_OversizeBodyRejected413(t *testing.T) {
	srv, _, _ := newTestReviewServer(t, t.TempDir())
	big := `{"items":[{"anchor":"a","note":"` + strings.Repeat("x", reviewBodyLimit) + `"}]}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/feedback", bytes.NewBufferString(big))
	req.Header.Set("X-Review-Token", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("want 413, got %d", resp.StatusCode)
	}
}

// Every review dir must be watched, and a dir that cannot be watched must be
// an error: a silently partial watch set leaves the live page stale and
// `await` waiting until its timeout.
func TestAddPlanReviewWatches(t *testing.T) {
	t.Run("watches plan, feedback and resolutions", func(t *testing.T) {
		planDir := t.TempDir()
		w, err := fsnotify.NewWatcher()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = w.Close() }()
		if err := addPlanReviewWatches(w, planDir); err != nil {
			t.Fatalf("addPlanReviewWatches: %v", err)
		}
		want := map[string]bool{
			planDir:                            true,
			filepath.Join(planDir, "feedback"): true,
			filepath.Join(planDir, "feedback", "resolutions"): true,
		}
		got := w.WatchList()
		if len(got) != len(want) {
			t.Fatalf("watch list = %v", got)
		}
		for _, p := range got {
			if !want[p] {
				t.Fatalf("unexpected watch %q in %v", p, got)
			}
		}
	})
	t.Run("feedback path is a file", func(t *testing.T) {
		planDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(planDir, "feedback"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		w, err := fsnotify.NewWatcher()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = w.Close() }()
		if err := addPlanReviewWatches(w, planDir); err == nil {
			t.Fatal("want an error when the resolutions dir cannot be created")
		}
	})
	t.Run("watch registration fails", func(t *testing.T) {
		w, err := fsnotify.NewWatcher()
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if err := addPlanReviewWatches(w, t.TempDir()); !errors.Is(err, fsnotify.ErrClosed) {
			t.Fatalf("err = %v, want fsnotify.ErrClosed", err)
		}
	})
}

// A watch set that cannot be built degrades live reload but must not stop the
// review server: watchPlanDir logs and keeps serving until ctx ends.
func TestWatchPlanDir_DegradesWhenWatchesFail(t *testing.T) {
	planDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(planDir, "feedback"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		watchPlanDir(ctx, planDir, newBroadcaster())
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watchPlanDir did not return after cancel")
	}
}
