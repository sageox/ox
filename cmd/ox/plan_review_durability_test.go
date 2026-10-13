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
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/plan"
	"github.com/sageox/ox/pkg/planreview"
)

// plan_review_durability_test.go proves a review POST tells the page the truth
// about durability against a real Ledger git repo with a real bare remote: a
// review commit carries exactly its record, a failed commit or push leaves a
// recovery entry for that operation only, the retry publishes the original
// record and nothing else, concurrent rounds never collide on .git/index.lock,
// and records from racing clones and retried requests all survive as one
// logical submission each.

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

// newDurableReviewFixture builds a Ledger with a bare origin and a committed
// plan dir in it, and the live review handler wired to it. The package-level
// seams are restored on cleanup, so these tests must not run in parallel.
func newDurableReviewFixture(t *testing.T) *durableReviewFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("short: real ledger git repo with bare remote push")
	}
	ledger := reviewTestLedger(t)
	origin := strings.TrimSpace(runGitInDir(t, ledger, "remote", "get-url", "origin"))
	planDir := filepath.Join(ledger, "data", "plans", "2026-10-01-durable")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"plan.md": "# Durable\n", "meta.json": `{"topic":"Durable"}`} {
		if err := os.WriteFile(filepath.Join(planDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGitInDir(t, ledger, "add", "data")
	runGitInDir(t, ledger, "commit", "-q", "-m", "plan: 2026-10-01-durable")
	runGitInDir(t, ledger, "push", "-q")

	prevNotify := notifyReviewFeedback
	notifies := &atomic.Int32{}
	notifyReviewFeedback = func(_, _, _ string, _ int) error {
		notifies.Add(1)
		return nil
	}
	t.Cleanup(func() { notifyReviewFeedback = prevNotify })

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
	return strings.Count(remoteFiles(t, origin), "/feedback/round-")
}

// TestPlanReviewDurability_FeedbackCommittedAndPushed: the happy path — the
// round is on the bare remote when the response says pushed=true, in a commit
// of its own, and its recovery entry is gone.
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
	if !strings.Contains(runGitInDir(t, f.origin, "log", "--format=%s", "main"), "plan review: 2026-10-01-durable") {
		t.Fatal("remote log has no review commit")
	}
	if n := feedbackRoundsOnRemote(t, f.origin); n != 1 {
		t.Fatalf("remote should carry 1 round, got %d", n)
	}
	if left, err := listReviewPending(f.ledger); err != nil || len(left) != 0 {
		t.Fatalf("a published record must leave no recovery entry, got %+v (err %v)", left, err)
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
	if !strings.Contains(remoteFiles(t, f.origin), "feedback/resolutions/") {
		t.Fatal("accept resolution was not pushed")
	}
}

// TestPlanReviewDurability_PushedMeansOnTheRemoteTip: a push that succeeds
// without putting the record on the branch the Ledger tracks is not reported
// published, and the record's entry stays for the retry; a branch tracking no
// upstream is verified on the ref its push to origin updates. Failure
// prevented: the page tells the reviewer "synced" for a record no teammate can
// see, or a published record is retried forever.
func TestPlanReviewDurability_PushedMeansOnTheRemoteTip(t *testing.T) {
	post := func(f *durableReviewFixture) reviewDurabilityResp {
		t.Helper()
		code, body := reviewPOSTBody(t, f.srv.URL+"/feedback", "secret", roundBody("round-0001", "h1"))
		if code != http.StatusOK {
			t.Fatalf("status %d: %s", code, body)
		}
		return decodeReviewResp(t, body)
	}
	t.Run("push lands on another branch", func(t *testing.T) {
		f := newDurableReviewFixture(t)
		runGitInDir(t, f.ledger, "config", "remote.origin.push", "refs/heads/main:refs/heads/elsewhere")
		if r := post(f); !r.Saved || !r.Committed || r.Pushed {
			t.Fatalf("want saved+committed, not published; got %+v", r)
		}
		if left, err := listReviewPending(f.ledger); err != nil || len(left) != 1 {
			t.Fatalf("an unverified record must keep its entry, got %+v (err %v)", left, err)
		}
	})
	t.Run("branch tracks no upstream", func(t *testing.T) {
		f := newDurableReviewFixture(t)
		runGitInDir(t, f.ledger, "config", "push.default", "current")
		runGitInDir(t, f.ledger, "branch", "--unset-upstream")
		if r := post(f); !r.Pushed {
			t.Fatalf("a record the push put on origin must be published, got %+v", r)
		}
		if left, err := listReviewPending(f.ledger); err != nil || len(left) != 0 {
			t.Fatalf("a verified record must clear its entry, got %+v (err %v)", left, err)
		}
	})
}

// TestPlanReviewDurability_RetryPublishesOnlyTheOriginalRecord is the
// acceptance proof for exact-set recovery. Round A's commit fails, round B's
// push fails, and the author leaves plan.md, meta.json and a companion dirty.
// Round C then publishes, carrying B's commit with it, yet neither A's entry
// nor B's is cleared by C's success. The retry `ox agent prime` starts (a new
// process, here its flush) commits A alone, verifies A and B on the remote and
// clears exactly those entries. Failure prevented: a retry publishes the
// author's half-written files under a reviewer's round, or another operation's
// success clears a record that never reached the team.
func TestPlanReviewDurability_RetryPublishesOnlyTheOriginalRecord(t *testing.T) {
	f := newDurableReviewFixture(t)
	post := func(id, anchor string) reviewDurabilityResp {
		t.Helper()
		code, body := reviewPOSTBody(t, f.srv.URL+"/feedback", "secret", roundBody(id, anchor))
		if code != http.StatusOK {
			t.Fatalf("POST %s: %d %s", id, code, body)
		}
		return decodeReviewResp(t, body)
	}
	pending := func() map[string]bool {
		t.Helper()
		entries, err := listReviewPending(f.ledger)
		if err != nil {
			t.Fatal(err)
		}
		ids := map[string]bool{}
		for _, e := range entries {
			ids[e.ID] = true
		}
		return ids
	}

	// A: a live index.lock blocks the commit
	lock := filepath.Join(f.ledger, ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if r := post("round-000A", "hA"); !r.Saved || r.Committed || r.Pushed {
		t.Fatalf("A: want saved only, got %+v", r)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}

	// the author edits while the remote is down; B commits but cannot push
	runGitInDir(t, f.ledger, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
	for name, body := range map[string]string{"plan.md": "# Durable\n\nhalf-written\n", "meta.json": `{"topic":"Durable","status":"editing"}`, "companions/sketch.html": "<p>wip</p>"} {
		path := filepath.Join(f.planDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if r := post("round-000B", "hB"); !r.Saved || !r.Committed || r.Pushed {
		t.Fatalf("B: want saved+committed, not pushed; got %+v", r)
	}
	if st := runGitInDir(t, f.ledger, "status", "--porcelain", "--untracked-files=all"); strings.Contains(st, "ox-plan-review-pending") {
		t.Fatalf("recovery entries must live under .git/, never as Ledger files: %q", st)
	}

	// C publishes, and B's commit with it; neither A's entry nor B's is C's to clear
	runGitInDir(t, f.ledger, "remote", "set-url", "origin", f.origin)
	if r := post("round-000C", "hC"); !r.Pushed {
		t.Fatalf("C: want published, got %+v", r)
	}
	if got := pending(); !got["round-000A"] || !got["round-000B"] || got["round-000C"] {
		t.Fatalf("after C, want entries for A and B only, got %v", got)
	}

	ok, failed, err := flushPendingReviewRecords(context.Background(), f.ledger)
	if err != nil || ok != 2 || failed != 0 {
		t.Fatalf("flush: ok=%d failed=%d err=%v", ok, failed, err)
	}
	if got := pending(); len(got) != 0 {
		t.Fatalf("flush must clear A and B, left %v", got)
	}
	if n := feedbackRoundsOnRemote(t, f.origin); n != 3 {
		t.Fatalf("remote rounds = %d, want 3", n)
	}
	// every review commit touched exactly one record; the author's edits
	// reached no commit and are still in the worktree
	for _, sha := range strings.Fields(runGitInDir(t, f.origin, "log", "--format=%H", "--grep=plan review:", "main")) {
		files := strings.Fields(runGitInDir(t, f.origin, "show", "--name-only", "--format=", sha))
		if len(files) != 1 || !strings.Contains(files[0], "/feedback/round-") {
			t.Fatalf("review commit %s carries %v, want one round record", sha, files)
		}
	}
	if got := runGitInDir(t, f.origin, "show", "main:data/plans/2026-10-01-durable/plan.md"); got != "# Durable" {
		t.Fatalf("remote plan.md = %q, want the committed original", got)
	}
	unstaged := runGitInDir(t, f.ledger, "diff", "--name-only")
	staged := runGitInDir(t, f.ledger, "diff", "--cached", "--name-only")
	untracked := runGitInDir(t, f.ledger, "ls-files", "--others", "--exclude-standard")
	if !strings.Contains(unstaged, "durable/plan.md") || !strings.Contains(unstaged, "durable/meta.json") || staged != "" || !strings.Contains(untracked, "durable/companions/sketch.html") {
		t.Fatalf("the author's edits must stay uncommitted and unstaged: unstaged=%q staged=%q untracked=%q", unstaged, staged, untracked)
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

// TestPlanReviewDurability_RacingClonesAndLostResponses: three clones of one
// Ledger publish rounds and resolutions in turn, so every push after the first
// meets a moved head and rebases onto it. Devon's round is also retried after a
// lost response: on the clone that stored it (answered as a duplicate, nothing
// written) and on a clone that never saw it (a second, equivalent copy). Every
// record reaches the remote without force-push or Git repair, and a fresh
// clone assembles Devon's round once. Failure prevented: review data kept in a
// shared file loses one side to the push rebase, or a retried request becomes
// a second submission.
func TestPlanReviewDurability_RacingClonesAndLostResponses(t *testing.T) {
	if testing.Short() {
		t.Skip("short: three Ledger clones pushing to a bare remote")
	}
	origin := initGitLedgerWithOrigin(t, t.TempDir())
	const rel = "data/plans/2026-10-01-race"
	clone := func() string {
		dir := filepath.Join(t.TempDir(), "ledger")
		runGitInDir(t, filepath.Dir(dir), "clone", "-q", "-b", "main", origin, dir)
		runGitInDir(t, dir, "config", "user.email", "test@example.com")
		runGitInDir(t, dir, "config", "user.name", "Test")
		runGitInDir(t, dir, "config", "commit.gpgsign", "false")
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	prev := planLedgerPathFor
	planLedgerPathFor = func(gitRoot string) (string, error) { return gitRoot, nil }
	t.Cleanup(func() { planLedgerPathFor = prev })
	publish := func(ledger string, rec planreview.File) planreview.Receipt {
		t.Helper()
		st, r, err := publishReviewRecord(context.Background(), ledger, filepath.Join(ledger, filepath.FromSlash(rel)), rec)
		if err != nil || !st.Pushed {
			t.Fatalf("publish %s from %s: %+v (err %v)", rec.Path, ledger, st, err)
		}
		return r
	}
	round := func(who, frozen string) planreview.File {
		t.Helper()
		q, err := planreview.ParseRoundRequest([]byte(frozen))
		if err != nil {
			t.Fatal(err)
		}
		rec, err := q.Record("race", who, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}
	devon := `{"id":"round-devon1","reviewer":"Devon","items":[{"anchor":"h1","status":"request-change","note":"bound it"}]}`
	c0, c1, c2 := clone(), clone(), clone()

	resolve := func(ledger, note string) {
		t.Helper()
		rec, err := newResolutionRecord("h1", planreview.StateAddressed, "", note, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		publish(ledger, rec)
	}
	// c1, c2 and then c0 each first publish onto a head another clone moved
	first := publish(c0, round("Devon", devon))
	firstCommit := strings.TrimSpace(runGitInDir(t, c0, "rev-parse", "HEAD"))
	if r := publish(c0, round("Devon", devon)); r.State != planreview.ReceiptAlreadyPublished || r.Path != first.Path {
		t.Fatalf("a retry on the clone holding the round must be answered with it, got %+v", r)
	}
	if r := publish(c1, round("Devon", devon)); r.State != planreview.ReceiptCreated {
		t.Fatalf("a clone that never saw the round stores its own copy, got %+v", r)
	}
	publish(c1, round("Riley", `{"id":"round-riley1","items":[{"anchor":"h1","status":"flag","note":"why"}]}`))
	resolve(c1, "by clone 1")
	publish(c2, round("Sam", `{"id":"round-sam001","items":[{"anchor":"h1","status":"comment","note":"ok"}]}`))
	resolve(c2, "by clone 2")
	resolve(c0, "by clone 0")

	files := remoteFiles(t, origin)
	if rounds, res := strings.Count(files, "/feedback/round-"), strings.Count(files, "/feedback/resolutions/"); rounds != 4 || res != 3 {
		t.Fatalf("remote holds %d rounds and %d resolutions, want 4 (one an equivalent copy) and 3", rounds, res)
	}
	runGitInDir(t, origin, "merge-base", "--is-ancestor", firstCommit, "main") // no history was rewritten
	rev := plan.ReadReview(filepath.Join(clone(), filepath.FromSlash(rel)))
	if !rev.Complete() || len(rev.Conflicts) != 0 || len(rev.Rounds) != 3 || len(rev.Items) != 3 {
		t.Fatalf("a fresh clone must assemble 3 rounds and 3 items with nothing missing or conflicting, got %+v", rev)
	}
	for _, it := range rev.Items {
		if it.Open || it.Resolution == nil || it.Resolution.State != planreview.StateAddressed {
			t.Fatalf("every reviewer's item should be closed as addressed, got %+v", it)
		}
	}
}

// TestPlanReviewCommit_CarriesOnlyTheRecord: a review commit holds its one
// record; someone else's staged Ledger change stays staged and the plan's
// uncommitted files stay uncommitted.
func TestPlanReviewCommit_CarriesOnlyTheRecord(t *testing.T) {
	f := newDurableReviewFixture(t)
	if err := os.WriteFile(filepath.Join(f.ledger, "data", "unrelated.txt"), []byte("not a plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, f.ledger, "add", "data/unrelated.txt")
	if err := os.WriteFile(filepath.Join(f.planDir, "notes.md"), []byte("draft\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if code, body := reviewPOSTBody(t, f.srv.URL+"/feedback", "secret", roundBody("round-0001", "h1")); code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	committed := strings.Fields(runGitInDir(t, f.ledger, "show", "--name-only", "--format=", "HEAD"))
	if len(committed) != 1 || !strings.HasPrefix(committed[0], "data/plans/2026-10-01-durable/feedback/round-") {
		t.Fatalf("review commit must carry exactly its record, got %v", committed)
	}
	if staged := runGitInDir(t, f.ledger, "diff", "--cached", "--name-only"); !strings.Contains(staged, "data/unrelated.txt") {
		t.Fatalf("unrelated change must remain staged, got %q", staged)
	}
}

// TestPlanReviewDurability_RetriedRoundIsOneSubmission: a resent round (same
// id, same marks) is a duplicate that still reports durability but never
// re-notifies or re-signals; the same id with different marks is refused with
// 409 and nothing is stored. Failure prevented: a retry doubles a reviewer's
// marks or pings the agent twice, or an edited resend is told "already saved"
// and dropped.
func TestPlanReviewDurability_RetriedRoundIsOneSubmission(t *testing.T) {
	f := newDurableReviewFixture(t)
	var published, original string
	for i, wantDup := range []bool{false, true} {
		if wantDup {
			// an editor or a tool reformatted the stored copy
			published, original = reformatRounds(t, f.planDir)
		}
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
	if code, body := reviewPOSTBody(t, f.srv.URL+"/feedback", "secret", roundBody("round-0001", "h2")); code != http.StatusConflict {
		t.Fatalf("the same id with different marks must be 409, got %d %s", code, body)
	}
	if got := f.notifies.Load(); got != 1 {
		t.Fatalf("agent notified %d times, want 1", got)
	}
	if got := len(f.rounds); got != 1 {
		t.Fatalf("only the first post may signal a round, got %d signals", got)
	}
	if n := countRoundFiles(t, f.planDir); n != 1 {
		t.Fatalf("round files = %d, want 1", n)
	}
	if got := runGitInDir(t, f.origin, "show", "main:data/plans/2026-10-01-durable/"+published); got != strings.TrimSpace(original) {
		t.Fatalf("a resend must never publish the reformatted copy over the committed record, remote holds:\n%s", got)
	}
}

// reformatRounds rewrites the one round record in planDir as compact JSON with
// CRLF endings, the same operation in other bytes, and returns its path
// relative to the plan dir and its original bytes.
func reformatRounds(t *testing.T, planDir string) (string, string) {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join(planDir, "feedback", "round-*.json"))
	if len(paths) != 1 {
		t.Fatalf("want one round record, got %v", paths)
	}
	b, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths[0], append(compact.Bytes(), '\r', '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return "feedback/" + filepath.Base(paths[0]), string(b)
}

// TestPlanReviewDurability_EntryOutlivesAnAttemptThatNeverWroteItsRecord: an
// attempt stopped after writing its recovery entry and before writing the
// record, so nobody was told it was saved. A resend with other marks is
// refused against that entry; the same request reuses the entry's record (one
// copy, not two) and publishes it; and the flush restores and publishes an
// entry whose record the worktree never got. Failure prevented: a crash
// between the two writes loses the frozen review, or its retry stores a
// second copy.
func TestPlanReviewDurability_EntryOutlivesAnAttemptThatNeverWroteItsRecord(t *testing.T) {
	f := newDurableReviewFixture(t)
	rel, err := ledgerRelPath(f.ledger, f.planDir)
	if err != nil {
		t.Fatal(err)
	}
	freeze := func(body string) reviewPending {
		t.Helper()
		q, err := planreview.ParseRoundRequest([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		rec, err := q.Record("durable", q.Reviewer, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		e := reviewPending{PlanDir: rel, Plan: filepath.Base(f.planDir), Kind: "round", ID: q.ID, Path: rec.Path, SHA256: sha256Hex(rec.Data), Data: rec.Data}
		if err := writeReviewPending(e.path(f.ledger), e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	stopped := freeze(roundBody("round-000S", "hS"))
	if code, body := reviewPOSTBody(t, f.srv.URL+"/feedback", "secret", roundBody("round-000S", "hX")); code != http.StatusConflict {
		t.Fatalf("other marks under a saved operation's id must be 409, got %d %s", code, body)
	}
	// a write the resend cannot make leaves the earlier attempt's entry alone
	blocker := filepath.Join(f.planDir, "feedback")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _ := reviewPOSTBody(t, f.srv.URL+"/feedback", "secret", roundBody("round-000S", "hS")); code != http.StatusInternalServerError {
		t.Fatalf("a blocked write must be 500, got %d", code)
	}
	if _, err := readReviewPending(stopped.path(f.ledger)); err != nil {
		t.Fatalf("a failed resend must keep the entry an earlier attempt saved: %v", err)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	code, body := reviewPOSTBody(t, f.srv.URL+"/feedback", "secret", roundBody("round-000S", "hS"))
	if r := decodeReviewResp(t, body); code != http.StatusOK || !r.Pushed || *r.Duplicate {
		t.Fatalf("the resend must publish the frozen record as a first save, got %d %s", code, body)
	}
	if !strings.Contains(remoteFiles(t, f.origin), stopped.Path) || countRoundFiles(t, f.planDir) != 1 {
		t.Fatalf("the resend must publish the entry's record %s and no other copy", stopped.Path)
	}

	lost := freeze(roundBody("round-000L", "hL"))
	if ok, failed, err := flushPendingReviewRecords(context.Background(), f.ledger); err != nil || ok != 1 || failed != 0 {
		t.Fatalf("flush: ok=%d failed=%d err=%v", ok, failed, err)
	}
	if b, err := os.ReadFile(filepath.Join(f.planDir, filepath.FromSlash(lost.Path))); err != nil || !bytes.Equal(b, lost.Data) {
		t.Fatalf("the flush must restore the record to the worktree (err %v)", err)
	}
	if !strings.Contains(remoteFiles(t, f.origin), lost.Path) {
		t.Fatalf("the flush must publish %s", lost.Path)
	}
	if left, err := listReviewPending(f.ledger); err != nil || len(left) != 0 {
		t.Fatalf("both entries must clear once verified, left %+v (err %v)", left, err)
	}
}

// TestPlanReviewDurability_UnrelatedConflictDefersTheCommit pins the H13
// decision (spec 3.7): while an unrelated index entry is unmerged the Ledger
// refuses every commit, so a review is saved but not committed, its record is
// not left staged for some other commit to carry, and the first retry after
// the conflict clears publishes it. Failure prevented: a conflict elsewhere
// loses or strands a review, or a squash publishes bytes the review commit
// refused.
func TestPlanReviewDurability_UnrelatedConflictDefersTheCommit(t *testing.T) {
	f := newDurableReviewFixture(t)
	blob := strings.TrimSpace(runGitInDir(t, f.ledger, "rev-parse", "HEAD:.gitignore"))
	conflict := fmt.Sprintf("100644 %s 1\tsessions/s/meta.json\n100644 %[1]s 2\tsessions/s/meta.json\n100644 %[1]s 3\tsessions/s/meta.json\n", blob)
	update := exec.Command("git", "-C", f.ledger, "update-index", "--index-info")
	update.Stdin = strings.NewReader(conflict)
	if out, err := update.CombinedOutput(); err != nil {
		t.Fatalf("stage a conflict: %v\n%s", err, out)
	}

	code, body := reviewPOSTBody(t, f.srv.URL+"/feedback", "secret", roundBody("round-0001", "h1"))
	if r := decodeReviewResp(t, body); code != http.StatusOK || !r.Saved || r.Committed {
		t.Fatalf("want saved, not committed, during the conflict; got %d %s", code, body)
	}
	if staged := runGitInDir(t, f.ledger, "diff", "--cached", "--name-only"); strings.Contains(staged, "feedback/") {
		t.Fatalf("a refused record must not stay staged, index has:\n%s", staged)
	}

	runGitInDir(t, f.ledger, "update-index", "--force-remove", "sessions/s/meta.json")
	if ok, failed, err := flushPendingReviewRecords(context.Background(), f.ledger); err != nil || ok != 1 || failed != 0 {
		t.Fatalf("flush after the conflict clears: ok=%d failed=%d err=%v", ok, failed, err)
	}
	if n := feedbackRoundsOnRemote(t, f.origin); n != 1 {
		t.Fatalf("the deferred round must reach the remote, got %d", n)
	}
}

// TestPlanReviewDurability_EntrySurvivesAWriteThatFailsLate: a write that
// fails after its record is on disk (Publish syncs the directory last) keeps
// the operation's entry, so the retry lands the record; an entry that cannot
// be read is never written over by a resend. Failure prevented: a reviewer's
// feedback is left on disk with nothing to publish it, or an unreadable entry
// holding the only copy is replaced.
func TestPlanReviewDurability_EntrySurvivesAWriteThatFailsLate(t *testing.T) {
	f := newDurableReviewFixture(t)
	prev := publishReviewFile
	publishReviewFile = func(planDir string, rec planreview.File) (planreview.Receipt, error) {
		r, err := prev(planDir, rec)
		if err == nil {
			err = errors.New("sync directory: injected")
		}
		return r, err
	}
	t.Cleanup(func() { publishReviewFile = prev })
	if code, _ := reviewPOSTBody(t, f.srv.URL+"/feedback", "secret", roundBody("round-0001", "h1")); code != http.StatusInternalServerError {
		t.Fatalf("a failed write must be 500, got %d", code)
	}
	publishReviewFile = prev
	if ok, failed, err := flushPendingReviewRecords(context.Background(), f.ledger); err != nil || ok != 1 || failed != 0 {
		t.Fatalf("the kept entry must land the record: ok=%d failed=%d err=%v", ok, failed, err)
	}
	if n := feedbackRoundsOnRemote(t, f.origin); n != 1 {
		t.Fatalf("remote rounds = %d, want 1", n)
	}

	// an unreadable entry for an operation blocks that operation
	rel, err := ledgerRelPath(f.ledger, f.planDir)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := reviewPendingPath(f.ledger, filepath.Base(f.planDir), "round", "round-0002")
	if err := os.WriteFile(corrupt, []byte(`{"plan_dir":"`+rel+`"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _ := reviewPOSTBody(t, f.srv.URL+"/feedback", "secret", roundBody("round-0002", "h2")); code != http.StatusInternalServerError {
		t.Fatalf("a resend over an unreadable entry must be 500, got %d", code)
	}
	if b, err := os.ReadFile(corrupt); err != nil || string(b) != `{"plan_dir":"`+rel+`"` {
		t.Fatalf("the unreadable entry must be left as it was (err %v): %q", err, b)
	}
}

// TestFlushPending_RenamedPlanDir: `ox plan backfill` renames a plan dir with
// its records and publishes them. An entry naming the old dir is cleared once
// the remote tip holds its bytes, and kept while it does not. Failure
// prevented: every prime retries a published record forever, or drops one
// that never reached the team.
func TestFlushPending_RenamedPlanDir(t *testing.T) {
	f := newDurableReviewFixture(t)
	lock := filepath.Join(f.ledger, ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	reviewPOST(t, f.srv.URL+"/feedback", "secret", roundBody("round-000A", "hA"))
	reviewPOST(t, f.srv.URL+"/feedback", "secret", roundBody("round-000B", "hB"))
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	entries, err := listReviewPending(f.ledger)
	if err != nil || len(entries) != 2 {
		t.Fatalf("want two pending entries, got %+v (err %v)", entries, err)
	}

	// the backfill publishes the renamed dir with A's record only
	for _, e := range entries {
		if e.ID == "round-000B" {
			if err := os.Remove(filepath.Join(f.planDir, filepath.FromSlash(e.Path))); err != nil {
				t.Fatal(err)
			}
		}
	}
	runGitInDir(t, f.ledger, "mv", "data/plans/2026-10-01-durable", "data/plans/2026-10-01-renamed")
	runGitInDir(t, f.ledger, "add", "data/plans/2026-10-01-renamed")
	runGitInDir(t, f.ledger, "commit", "-q", "-m", "plan: backfill 1 title(s)")
	runGitInDir(t, f.ledger, "push", "-q")

	ok, failed, err := flushPendingReviewRecords(context.Background(), f.ledger)
	if err != nil || ok != 1 || failed != 1 {
		t.Fatalf("flush: ok=%d failed=%d err=%v", ok, failed, err)
	}
	left, err := listReviewPending(f.ledger)
	if err != nil || len(left) != 1 || left[0].ID != "round-000B" {
		t.Fatalf("only the unpublished record's entry may stay, left %+v (err %v)", left, err)
	}
}

// TestPlanReviewDurability_ResendFollowsARenamedPlan: `ox plan backfill`
// renames a plan dir while two of its rounds are pending, one never written.
// Resent from the renamed dir, each finds its entry by the plan's id and lands
// there. Failure prevented: the record is committed under the old dir, where
// no one opening the renamed plan sees it, and its entry is cleared.
func TestPlanReviewDurability_ResendFollowsARenamedPlan(t *testing.T) {
	f := newDurableReviewFixture(t)
	if err := plan.AppendEvent(context.Background(), f.planDir, plan.Event{PlanID: "pln_renamed01", Kind: plan.EventCreated}); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(f.ledger, ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	rounds := map[string]string{"round-000A": "hA", "round-000B": "hB"}
	for id, anchor := range rounds {
		reviewPOST(t, f.srv.URL+"/feedback", "secret", roundBody(id, anchor))
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	entries, err := listReviewPending(f.ledger)
	if err != nil || len(entries) != 2 {
		t.Fatalf("want two pending entries, got %+v (err %v)", entries, err)
	}
	for _, e := range entries {
		if e.ID == "round-000B" { // its attempt stopped before writing the record
			if err := os.Remove(filepath.Join(f.planDir, filepath.FromSlash(e.Path))); err != nil {
				t.Fatal(err)
			}
		}
	}
	runGitInDir(t, f.ledger, "mv", "data/plans/2026-10-01-durable", "data/plans/2026-10-01-renamed")
	runGitInDir(t, f.ledger, "add", "data/plans/2026-10-01-renamed")
	runGitInDir(t, f.ledger, "commit", "-q", "-m", "plan: backfill 1 title(s)")
	runGitInDir(t, f.ledger, "push", "-q")

	h := liveReviewHandler("", "durable", filepath.Join(f.ledger, "data", "plans", "2026-10-01-renamed"), "http://x", "secret",
		newBroadcaster(), make(chan int, 8), make(chan struct{}, 1))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	for id, anchor := range rounds {
		if code := reviewPOST(t, srv.URL+"/feedback", "secret", roundBody(id, anchor)); code != http.StatusOK {
			t.Fatalf("resend %s: %d", id, code)
		}
	}
	ref, err := gitutil.UpstreamTrackingRef(context.Background(), f.ledger)
	if err != nil {
		t.Fatal(err)
	}
	remote := runGitInDir(t, f.ledger, "ls-tree", "-r", "--name-only", ref, "--", "data/plans")
	for _, e := range entries {
		if !strings.Contains(remote, "data/plans/2026-10-01-renamed/"+e.Path) || strings.Contains(remote, "data/plans/2026-10-01-durable/") {
			t.Fatalf("%s must be on the remote under the renamed dir only, remote tree:\n%s", e.ID, remote)
		}
	}
	if left, err := listReviewPending(f.ledger); err != nil || len(left) != 0 {
		t.Fatalf("both entries must clear, left %+v (err %v)", left, err)
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
