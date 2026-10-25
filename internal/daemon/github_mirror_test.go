package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/flags"
	gh "github.com/sageox/ox/internal/github"
	"github.com/sageox/ox/internal/githubmirror"
	"github.com/sageox/ox/internal/ledger"
)

// The mirror relay's customer promise: a teammate's PR or issue shows up on the
// team board once, stays current, costs GitHub almost nothing when nothing
// changed, and never disturbs the Ledger sync. These tests drive the relay
// through fakes for GitHub and the mirror API and a fixed clock.

var mirrorTestNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

var (
	mirrorMember   = githubmirror.Author{Login: "devon-dev", ID: 5550101, Association: "MEMBER", Type: "User"}
	mirrorReviewer = githubmirror.Author{Login: "avery-dev", ID: 5550102, Association: "MEMBER", Type: "User"}
	mirrorBot      = githubmirror.Author{Login: "dependabot[bot]", ID: 49699333, Association: "NONE", Type: "Bot"}
)

// --- fakes ---------------------------------------------------------------

// mirrorFakeFetcher is GitHub. Like the real one it returns only items updated
// at or after `since`, newest first, unless returnAll simulates the overlap
// window re-listing everything.
type mirrorFakeFetcher struct {
	mu             sync.Mutex
	repo           githubmirror.Repo
	prs            []githubmirror.SourcePR
	issues         []githubmirror.SourceIssue
	issueComments  map[int][]githubmirror.SourceComment
	reviewComments map[int][]githubmirror.SourceComment
	reviews        map[int][]githubmirror.SourceReview
	files          map[int][]string
	returnAll      bool
	// fail returns an error for one call: method name and item number (0 for
	// repo-level calls).
	fail func(method string, number int) error
	// hang makes a call block until its context ends: a GitHub that stopped
	// answering. ListIssueComments and the two listings honor it.
	hang func(method string, number int) bool
	// listPartial is how many of the newest items a hung listing returns with
	// its context error: the pages GitHub served before it went quiet.
	listPartial int
	release     chan struct{} // closed at test end so no hung call outlives the test
	releaseOnce sync.Once
	calls       map[string]int
	sinces      map[string]time.Time
	owners      []string // owner argument of every call, as the fetcher saw it
}

func newMirrorFakeFetcher() *mirrorFakeFetcher {
	return &mirrorFakeFetcher{
		repo:           githubmirror.Repo{Owner: "acme", Name: "api", FullName: "acme/api", ID: 42},
		issueComments:  map[int][]githubmirror.SourceComment{},
		reviewComments: map[int][]githubmirror.SourceComment{},
		reviews:        map[int][]githubmirror.SourceReview{},
		files:          map[int][]string{},
		release:        make(chan struct{}),
		calls:          map[string]int{},
		sinces:         map[string]time.Time{},
	}
}

func (f *mirrorFakeFetcher) releaseHung() { f.releaseOnce.Do(func() { close(f.release) }) }

// hung blocks while the call is configured to hang, and reports why it stopped.
func (f *mirrorFakeFetcher) hung(ctx context.Context, method string, number int) error {
	f.mu.Lock()
	hang := f.hang
	f.mu.Unlock()
	if hang == nil || !hang(method, number) {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-f.release:
		return errors.New("fake github released at test end")
	}
}

func (f *mirrorFakeFetcher) record(method, owner string, number int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[method]++
	f.owners = append(f.owners, owner)
	if f.fail != nil {
		return f.fail(method, number)
	}
	return nil
}

func (f *mirrorFakeFetcher) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

// detailCalls counts the per-item calls — the ones the budget exists to bound.
func (f *mirrorFakeFetcher) detailCalls() int {
	return f.count("ListIssueComments") + f.count("ListReviewComments") + f.count("ListReviews") + f.count("ListPRFiles")
}

func (f *mirrorFakeFetcher) totalCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for _, n := range f.calls {
		total += n
	}
	return total
}

func (f *mirrorFakeFetcher) since(method string) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sinces[method]
}

func (f *mirrorFakeFetcher) Repo(_ context.Context, owner, _ string) (githubmirror.Repo, error) {
	if err := f.record("Repo", owner, 0); err != nil {
		return githubmirror.Repo{}, err
	}
	return f.repo, nil
}

func (f *mirrorFakeFetcher) ListPullRequests(ctx context.Context, owner, _ string, since time.Time) ([]githubmirror.SourcePR, error) {
	if err := f.record("ListPullRequests", owner, 0); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.sinces["ListPullRequests"] = since
	var out []githubmirror.SourcePR
	for _, pr := range f.prs {
		if f.returnAll || !pr.UpdatedAt.Before(since) {
			out = append(out, pr)
		}
	}
	slices.SortStableFunc(out, func(a, b githubmirror.SourcePR) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	partial := min(f.listPartial, len(out))
	f.mu.Unlock()
	if err := f.hung(ctx, "ListPullRequests", 0); err != nil {
		// like the real fetcher: a listing that ran out of time keeps the pages
		// it finished, newest first
		return out[:partial], err
	}
	return out, nil
}

func (f *mirrorFakeFetcher) ListIssues(_ context.Context, owner, _ string, since time.Time) ([]githubmirror.SourceIssue, error) {
	if err := f.record("ListIssues", owner, 0); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sinces["ListIssues"] = since
	var out []githubmirror.SourceIssue
	for _, is := range f.issues {
		if f.returnAll || !is.UpdatedAt.Before(since) {
			out = append(out, is)
		}
	}
	slices.SortStableFunc(out, func(a, b githubmirror.SourceIssue) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	return out, nil
}

func (f *mirrorFakeFetcher) ListIssueComments(ctx context.Context, owner, _ string, number int) ([]githubmirror.SourceComment, error) {
	if err := f.record("ListIssueComments", owner, number); err != nil {
		return nil, err
	}
	if err := f.hung(ctx, "ListIssueComments", number); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.issueComments[number]), nil
}

func (f *mirrorFakeFetcher) ListReviewComments(_ context.Context, owner, _ string, number int) ([]githubmirror.SourceComment, error) {
	if err := f.record("ListReviewComments", owner, number); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reviewComments[number]), nil
}

func (f *mirrorFakeFetcher) ListReviews(_ context.Context, owner, _ string, number int) ([]githubmirror.SourceReview, error) {
	if err := f.record("ListReviews", owner, number); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reviews[number]), nil
}

func (f *mirrorFakeFetcher) ListPRFiles(_ context.Context, owner, _ string, number, _ int) ([]string, bool, error) {
	if err := f.record("ListPRFiles", owner, number); err != nil {
		return nil, false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.files[number]), false, nil
}

// setPR replaces (or adds) a PR by number.
func (f *mirrorFakeFetcher) setPR(pr githubmirror.SourcePR) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.prs {
		if f.prs[i].Number == pr.Number {
			f.prs[i] = pr
			return
		}
	}
	f.prs = append(f.prs, pr)
}

func (f *mirrorFakeFetcher) addComment(number int, c githubmirror.SourceComment) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issueComments[number] = append(f.issueComments[number], c)
}

// mirrorFakeRelay is the SageOx mirror API.
type mirrorFakeRelay struct {
	mu         sync.Mutex
	requests   []githubmirror.RelayRequest
	teams      []string // the team ref of each request, parallel to requests
	repoStatus string   // default "enabled"
	// fail returns an error for one request, before any response is built.
	fail func(req githubmirror.RelayRequest) error
	// verdict picks an item's outcome; default is accepted.
	verdict func(it githubmirror.Item) (status, reason string)
	// respond, when set, replaces the whole response (for malformed replies).
	respond func(req githubmirror.RelayRequest) *githubmirror.RelayResponse
}

func (r *mirrorFakeRelay) RelayGitHubMirrorItems(ctx context.Context, teamRef string, req githubmirror.RelayRequest) (*githubmirror.RelayResponse, error) {
	// like the real client, a call made on a finished context goes nowhere
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("network error: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req)
	r.teams = append(r.teams, teamRef)
	if r.fail != nil {
		if err := r.fail(req); err != nil {
			return nil, err
		}
	}
	if r.respond != nil {
		return r.respond(req), nil
	}
	resp := &githubmirror.RelayResponse{RepoStatus: githubmirror.RepoEnabled}
	if r.repoStatus != "" {
		resp.RepoStatus = r.repoStatus
	}
	for _, it := range req.Items {
		status, reason := githubmirror.ResultAccepted, ""
		if r.verdict != nil {
			status, reason = r.verdict(it)
		}
		resp.Results = append(resp.Results, githubmirror.ItemResult{
			SourceKey: githubmirror.SourceKey(req.Repo.Owner, req.Repo.Name, it.Kind, it.Number),
			Status:    status,
			Reason:    reason,
		})
	}
	return resp, nil
}

func (r *mirrorFakeRelay) requestCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

func (r *mirrorFakeRelay) batchSizes() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	var sizes []int
	for _, req := range r.requests {
		sizes = append(sizes, len(req.Items))
	}
	return sizes
}

// relayedTo lists the item numbers of every request sent to the given team.
func (r *mirrorFakeRelay) relayedTo(teamRef string) []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	var numbers []int
	for i, req := range r.requests {
		if r.teams[i] != teamRef {
			continue
		}
		for _, it := range req.Items {
			numbers = append(numbers, it.Number)
		}
	}
	return numbers
}

// relayedNumbers lists the item numbers of every request, in order.
func (r *mirrorFakeRelay) relayedNumbers() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	var numbers []int
	for _, req := range r.requests {
		for _, it := range req.Items {
			numbers = append(numbers, it.Number)
		}
	}
	return numbers
}

// mirrorLogBuffer is a log sink safe for the concurrent cycles some tests start.
type mirrorLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *mirrorLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *mirrorLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// --- harness -------------------------------------------------------------

type mirrorHarness struct {
	t          *testing.T
	relayer    *GitHubMirrorRelayer
	fetcher    *mirrorFakeFetcher
	relay      *mirrorFakeRelay
	logs       *mirrorLogBuffer
	ledgerPath string
	teamPath   string

	mu           sync.Mutex
	now          time.Time
	enabled      bool
	authToken    string
	teamRef      string
	teamPathFunc func() string
	budget       int
	cycleTimeout time.Duration
	enabledFunc  func() bool // replaces the enabled switch when set
	owner, repo  string
	noPRs        bool
	noIssues     bool
}

type mirrorHarnessOption func(*mirrorHarness)

func withMirrorBudget(n int) mirrorHarnessOption { return func(h *mirrorHarness) { h.budget = n } }

func withMirrorCycleTimeout(d time.Duration) mirrorHarnessOption {
	return func(h *mirrorHarness) { h.cycleTimeout = d }
}

func withMirrorEnabledFunc(fn func() bool) mirrorHarnessOption {
	return func(h *mirrorHarness) { h.enabledFunc = fn }
}

func newMirrorHarness(t *testing.T, opts ...mirrorHarnessOption) *mirrorHarness {
	t.Helper()
	h := &mirrorHarness{
		t:          t,
		fetcher:    newMirrorFakeFetcher(),
		relay:      &mirrorFakeRelay{},
		logs:       &mirrorLogBuffer{},
		ledgerPath: t.TempDir(),
		teamPath:   t.TempDir(),
		now:        mirrorTestNow,
		enabled:    true,
		authToken:  "sageox-token",
		teamRef:    "team_1",
		owner:      "acme",
		repo:       "api",
	}
	for _, opt := range opts {
		opt(h)
	}
	t.Cleanup(h.fetcher.releaseHung)
	enabled := func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.enabled
	}
	if h.enabledFunc != nil {
		enabled = h.enabledFunc
	}
	logger := slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h.relayer = NewGitHubMirrorRelayer(GitHubMirrorDeps{
		Enabled: enabled,
		AuthToken: func() string {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.authToken
		},
		Team: func() (string, string) {
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.teamPathFunc != nil {
				return h.teamRef, h.teamPathFunc()
			}
			return h.teamRef, h.teamPath
		},
		NewFetcher: func(string) githubmirror.Fetcher { return h.fetcher },
		NewRelay:   func(string) MirrorRelayClient { return h.relay },
		Now: func() time.Time {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.now
		},
		DetailBudget: h.budget,
		CycleTimeout: h.cycleTimeout,
		Logger:       logger,
	})
	return h
}

// setTeam changes the team the project belongs to, as re-running ox init does.
func (h *mirrorHarness) setTeam(ref string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.teamRef = ref
}

func (h *mirrorHarness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *mirrorHarness) advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.now = h.now.Add(d)
}

func (h *mirrorHarness) setEnabled(on bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.enabled = on
}

func (h *mirrorHarness) target() MirrorTarget {
	return MirrorTarget{
		LedgerPath:   h.ledgerPath,
		Owner:        h.owner,
		Repo:         h.repo,
		GitHubToken:  "github-token",
		PullRequests: !h.noPRs,
		Issues:       !h.noIssues,
	}
}

func (h *mirrorHarness) run() {
	h.t.Helper()
	h.relayer.Run(context.Background(), h.target())
}

func (h *mirrorHarness) state() *githubmirror.State {
	h.t.Helper()
	st, err := githubmirror.LoadState(h.ledgerPath)
	require.NoError(h.t, err)
	return st
}

func (h *mirrorHarness) key(kind string, number int) string {
	return githubmirror.SourceKey("acme", "api", kind, number)
}

func (h *mirrorHarness) prKey(n int) string    { return h.key(githubmirror.KindPullRequest, n) }
func (h *mirrorHarness) issueKey(n int) string { return h.key(githubmirror.KindIssue, n) }

// totalCalls is every call made to GitHub or the mirror API; the gates promise
// it stays at zero.
func (h *mirrorHarness) totalCalls() int { return h.fetcher.totalCalls() + h.relay.requestCount() }

// --- builders ------------------------------------------------------------

// mirrorPR is a live member-authored PR created an hour before it was last
// updated, so its newest human activity is its creation.
func mirrorPR(number int, updated time.Time) githubmirror.SourcePR {
	return githubmirror.SourcePR{
		Number:    number,
		Title:     fmt.Sprintf("PR %d", number),
		Body:      "description of the change",
		State:     "open",
		Author:    mirrorMember,
		Labels:    []string{"daemon"},
		CreatedAt: updated.Add(-time.Hour),
		UpdatedAt: updated,
		HTMLURL:   fmt.Sprintf("https://github.com/acme/api/pull/%d", number),
	}
}

func mirrorIssue(number int, updated time.Time) githubmirror.SourceIssue {
	return githubmirror.SourceIssue{
		Number:    number,
		Title:     fmt.Sprintf("Issue %d", number),
		Body:      "what is wrong",
		State:     "open",
		Author:    mirrorMember,
		CreatedAt: updated.Add(-time.Hour),
		UpdatedAt: updated,
		HTMLURL:   fmt.Sprintf("https://github.com/acme/api/issues/%d", number),
	}
}

func mirrorComment(id int64, author githubmirror.Author, body string, at time.Time) githubmirror.SourceComment {
	return githubmirror.SourceComment{ID: id, Author: author, Body: body, CreatedAt: at, UpdatedAt: at}
}

// seedColdStart gives the fetcher a small repo: three live items and one PR
// that GitHub shows as recently updated but whose last human activity is
// already past the 90-day window.
func seedColdStart(h *mirrorHarness) {
	now := h.clock()
	h.fetcher.prs = []githubmirror.SourcePR{
		mirrorPR(1, now.Add(-2*time.Hour)),
		mirrorPR(2, now.Add(-5*time.Hour)),
	}
	h.fetcher.issues = []githubmirror.SourceIssue{mirrorIssue(3, now.Add(-3*time.Hour))}
	stale := mirrorPR(4, now.Add(-24*time.Hour))
	stale.CreatedAt = now.Add(-120 * 24 * time.Hour)
	h.fetcher.prs = append(h.fetcher.prs, stale)
}

// seedPRs adds n PRs, numbered 1..n, updated 1h, 2h, ... n hours ago.
func seedPRs(h *mirrorHarness, n int) {
	now := h.clock()
	for i := 1; i <= n; i++ {
		h.fetcher.prs = append(h.fetcher.prs, mirrorPR(i, now.Add(-time.Duration(i)*time.Hour)))
	}
}

// --- cold start and steady state -----------------------------------------

// Failure prevented: a teammate's new daemon relays dead history, or relays
// nothing, or never records that its first pass finished — so every later
// cycle repeats the 90-day crawl.
func TestGitHubMirror_ColdStartRelaysOnlyUnexpiredItemsAndSetsCursor(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	seedColdStart(h)

	h.run()

	assert.ElementsMatch(t, []int{1, 2, 3}, h.relay.relayedNumbers(), "the expired PR must not be relayed")
	assert.Equal(t, []int{3}, h.relay.batchSizes(), "one batch")
	assert.Equal(t, h.clock().Add(-githubmirror.Window), h.fetcher.since("ListPullRequests"), "cold start lists the last 90 days")
	assert.Equal(t, h.clock().Add(-githubmirror.Window), h.fetcher.since("ListIssues"))

	st := h.state()
	assert.True(t, st.ColdStartDone)
	assert.Equal(t, h.clock().Add(-2*time.Hour), st.PullRequestCursor, "each kind's cursor is its newest listed updated_at")
	assert.Equal(t, h.clock().Add(-3*time.Hour), st.IssueCursor)
	assert.Equal(t, h.clock(), st.LastSuccessAt)
	assert.Empty(t, st.LastError)
	assert.Equal(t, githubmirror.RepoEnabled, st.RepoStatus)
	require.NotNil(t, st.RepoMeta)
	assert.Equal(t, int64(42), st.RepoMeta.ID)

	for _, key := range []string{h.prKey(1), h.prKey(2), h.issueKey(3)} {
		item, ok := st.Items[key]
		require.True(t, ok, key)
		assert.Equal(t, githubmirror.ResultAccepted, item.Status)
		assert.NotEmpty(t, item.ChangeHash)
	}
	expired, ok := st.Items[h.prKey(4)]
	require.True(t, ok, "an expired item is remembered so it is not re-fetched every cycle")
	assert.Equal(t, githubMirrorReasonExpired, expired.Reason)

	req := h.relay.requests[0]
	assert.Equal(t, "acme/api", req.Repo.FullName)
}

// Failure prevented: every 15-minute cycle re-fetches comments, reviews and
// files for every item GitHub lists — a quota burn that scales with repo size
// instead of with activity.
func TestGitHubMirror_SecondRunWithNoChangesMakesNoDetailCallsOrRelays(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	seedColdStart(h)
	h.run()
	require.Equal(t, 1, h.relay.requestCount())

	detailBefore := h.fetcher.detailCalls()
	require.Positive(t, detailBefore)
	// GitHub re-lists everything (overlap window, eventual consistency)
	h.fetcher.returnAll = true
	h.advance(15 * time.Minute)

	h.run()

	assert.Equal(t, detailBefore, h.fetcher.detailCalls(), "no per-item calls for items whose updated_at is unchanged")
	assert.Equal(t, 1, h.relay.requestCount(), "nothing changed, nothing relayed")
	assert.Equal(t, mirrorTestNow.Add(-2*time.Hour).Add(-githubMirrorCursorOverlap), h.fetcher.since("ListPullRequests"), "next listing starts a little before the cursor")

	st := h.state()
	assert.Equal(t, h.clock(), st.LastSuccessAt, "a quiet cycle is still a success; doctor must not call it stale")
	assert.Len(t, st.Items, 4)
}

// Failure prevented: a mirrored discussion goes stale — a human reply on a PR
// never reaches the board — or, the opposite, one reply re-sends the repo.
func TestGitHubMirror_NewHumanCommentRelaysExactlyThatItem(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	seedColdStart(h)
	h.run()

	h.advance(15 * time.Minute)
	pr2 := mirrorPR(2, h.clock().Add(-time.Minute))
	pr2.CreatedAt = h.clock().Add(-5 * time.Hour)
	h.fetcher.setPR(pr2)
	h.fetcher.addComment(2, mirrorComment(900, mirrorMember, "please also cover the empty case", h.clock().Add(-time.Minute)))
	detailBefore := h.fetcher.detailCalls()
	relaysBefore := h.relay.requestCount()

	h.run()

	assert.Equal(t, relaysBefore+1, h.relay.requestCount())
	last := h.relay.requests[len(h.relay.requests)-1]
	require.Len(t, last.Items, 1)
	assert.Equal(t, 2, last.Items[0].Number)
	require.Len(t, last.Items[0].Comments, 1)
	assert.Equal(t, 4, h.fetcher.detailCalls()-detailBefore, "four detail calls for the one changed PR, none for the rest")
	assert.Equal(t, h.clock().Add(-time.Minute), h.state().PullRequestCursor)
}

// Failure prevented: a bot commenting on a PR (coverage, dependabot, CI)
// re-publishes the post every time. Bot noise must move GitHub's updated_at
// without moving anything we relay.
func TestGitHubMirror_BotOnlyChangeRelaysNothing(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	seedColdStart(h)
	h.run()

	h.advance(15 * time.Minute)
	pr1 := mirrorPR(1, h.clock().Add(-time.Minute))
	pr1.CreatedAt = mirrorTestNow.Add(-3 * time.Hour)
	h.fetcher.setPR(pr1)
	h.fetcher.addComment(1, mirrorComment(901, mirrorBot, "coverage is 91%", h.clock().Add(-time.Minute)))
	relaysBefore := h.relay.requestCount()
	detailBefore := h.fetcher.detailCalls()

	h.run()

	assert.Equal(t, relaysBefore, h.relay.requestCount(), "a bot comment must not republish the item")
	assert.Equal(t, 4, h.fetcher.detailCalls()-detailBefore, "the changed updated_at costs one look at the item")
	assert.Equal(t, pr1.UpdatedAt, h.state().Items[h.prKey(1)].UpdatedAt, "the new updated_at is remembered, so the item is not looked at again")

	h.fetcher.returnAll = true
	h.advance(15 * time.Minute)
	detailBefore = h.fetcher.detailCalls()
	h.run()
	assert.Equal(t, detailBefore, h.fetcher.detailCalls())
}

// Failure prevented: the mirror used the git remote's spelling for identity,
// so a remote written as Acme/API and GitHub's acme-corp/api forked source
// keys — duplicate posts on the board.
func TestGitHubMirror_UsesGitHubsCanonicalRepoForKeysAndRelay(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	h.owner, h.repo = "Acme", "API"
	h.fetcher.repo = githubmirror.Repo{Owner: "acme-corp", Name: "api", FullName: "acme-corp/api", ID: 7, Private: true}
	h.fetcher.prs = []githubmirror.SourcePR{mirrorPR(1, h.clock().Add(-time.Hour))}

	h.run()

	st := h.state()
	canonical := githubmirror.SourceKey("acme-corp", "api", githubmirror.KindPullRequest, 1)
	assert.Contains(t, st.Items, canonical)
	assert.NotContains(t, st.Items, githubmirror.SourceKey("Acme", "API", githubmirror.KindPullRequest, 1))
	require.Len(t, h.relay.requests, 1)
	assert.Equal(t, "acme-corp", h.relay.requests[0].Repo.Owner)
	assert.True(t, h.relay.requests[0].Repo.Private, "the private flag is relayed for the server to enforce")
	assert.Equal(t, "Acme/API", st.Repo, "state identity is the remote, so a renamed GitHub repo does not reset it each cycle")
	for _, owner := range h.fetcher.owners {
		assert.Equal(t, "Acme", owner, "the fetcher is called with the remote's spelling")
	}
}

// --- budget --------------------------------------------------------------

// Failure prevented: a large repo's first pass either blows the GitHub quota
// in one cycle or loses the items it did not get to.
func TestGitHubMirror_DetailBudgetExhaustionKeepsCursorAndNextRunFinishes(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t, withMirrorBudget(2))
	seedPRs(h, 5)

	h.run()
	st := h.state()
	assert.Equal(t, []int{1, 2}, h.relay.relayedNumbers(), "newest items first")
	assert.True(t, st.PullRequestCursor.IsZero(), "cursor holds while items are left behind")
	assert.False(t, st.ColdStartDone)
	assert.Equal(t, 8, h.fetcher.detailCalls(), "two PRs at four calls each")

	h.advance(15 * time.Minute)
	h.run()
	assert.Equal(t, []int{1, 2, 3, 4}, h.relay.relayedNumbers())
	assert.True(t, h.state().PullRequestCursor.IsZero())

	h.advance(15 * time.Minute)
	h.run()
	st = h.state()
	assert.Equal(t, []int{1, 2, 3, 4, 5}, h.relay.relayedNumbers(), "each item relayed exactly once across cycles")
	assert.Equal(t, 20, h.fetcher.detailCalls(), "no item was fetched twice")
	assert.True(t, st.ColdStartDone)
	assert.Equal(t, mirrorTestNow.Add(-time.Hour), st.PullRequestCursor)
}

// Failure prevented: PRs a bot touched recently but nobody has discussed in
// 90 days (stale bots do this) are re-fetched every cycle, eat the whole
// budget, and a cold start never finishes. Expired items must be remembered.
func TestGitHubMirror_ExpiredItemsDoNotStarveTheBudget(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t, withMirrorBudget(2))
	now := h.clock()
	for i := 1; i <= 3; i++ { // the newest three are expired
		stale := mirrorPR(i, now.Add(-time.Duration(i)*time.Hour))
		stale.CreatedAt = now.Add(-200 * 24 * time.Hour)
		h.fetcher.prs = append(h.fetcher.prs, stale)
	}
	for i := 4; i <= 5; i++ {
		h.fetcher.prs = append(h.fetcher.prs, mirrorPR(i, now.Add(-time.Duration(i)*time.Hour)))
	}

	for cycle := 1; cycle <= 3 && !h.state().ColdStartDone; cycle++ {
		h.run()
		h.advance(15 * time.Minute)
	}

	assert.True(t, h.state().ColdStartDone, "three cycles of budget 2 cover five items when nothing is re-fetched")
	assert.ElementsMatch(t, []int{4, 5}, h.relay.relayedNumbers())
}

// --- gates ---------------------------------------------------------------

// Failure prevented: a team that has not been enrolled (flag off) pays for
// GitHub calls and mirror API calls it cannot use, or a state file appears for
// a feature that never ran.
func TestGitHubMirror_FlagOffMakesNoCallsOfAnyKind(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	seedColdStart(h)
	h.setEnabled(false)

	h.run()

	assert.Zero(t, h.totalCalls())
	_, err := os.Stat(githubmirror.StatePath(h.ledgerPath))
	assert.ErrorIs(t, err, os.ErrNotExist, "a feature that never ran leaves no state behind")
	assert.Nil(t, h.relayer.Stats(), "off and never run: nothing to show in ox status")

	h.setEnabled(true)
	h.run()
	assert.Equal(t, []int{1, 2, 3}, sortedCopy(h.relay.relayedNumbers()), "turning the flag on starts the mirror")
}

// relayedKeys lists the items the state file says were sent to the server,
// leaving out the markers it keeps for expired items.
func relayedKeys(st *githubmirror.State) []string {
	var keys []string
	for key, item := range st.Items {
		if item.Reason != githubMirrorReasonExpired {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

func sortedCopy(in []int) []int {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}

// Failure prevented: the mirror calls out before the pieces it needs exist — a
// Team Context checkout the server's posts will land in, a team, a SageOx
// token.
func TestGitHubMirror_GatesMakeNoNetworkCalls(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(h *mirrorHarness)
	}{
		{"no team", func(h *mirrorHarness) { h.teamRef = "" }},
		{"team context checkout missing", func(h *mirrorHarness) {
			h.teamPathFunc = func() string { return filepath.Join(h.teamPath, "not-cloned-yet") }
		}},
		{"no team context path", func(h *mirrorHarness) { h.teamPathFunc = func() string { return "" } }},
		{"no sageox token", func(h *mirrorHarness) { h.authToken = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newMirrorHarness(t)
			seedColdStart(h)
			tt.setup(h)

			h.run()

			assert.Zero(t, h.totalCalls())
			_, err := os.Stat(githubmirror.StatePath(h.ledgerPath))
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

// Failure prevented: a user who turned issue sync off in project config still
// has their issues published to the team board.
func TestGitHubMirror_HonorsPerKindSyncToggles(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	seedColdStart(h)
	h.noIssues = true

	h.run()

	assert.Equal(t, 1, h.fetcher.count("ListPullRequests"))
	assert.Zero(t, h.fetcher.count("ListIssues"))
	assert.NotContains(t, h.relay.relayedNumbers(), 3)
}

// --- backoff -------------------------------------------------------------

// Failure prevented: a refusal that only a human or a deploy can fix (mirror
// not enabled, wrong team, old server) is retried every 15 minutes forever; or
// a transient one (busy, rate limited) waits a day and the board goes stale.
func TestGitHubMirror_BackoffByCause(t *testing.T) {
	t.Parallel()
	githubDown := func(err error) func(m string, n int) error {
		return func(m string, _ int) error {
			if m == "ListPullRequests" {
				return fmt.Errorf("list pull requests: %w: status 403", err)
			}
			return nil
		}
	}
	relayFails := func(err error) func(*mirrorHarness) {
		return func(h *mirrorHarness) {
			h.relay.fail = func(githubmirror.RelayRequest) error { return err }
		}
	}
	tests := []struct {
		name        string
		cause       func(h *mirrorHarness)
		recover     func(h *mirrorHarness)
		wantBackoff time.Duration
		wantStatus  string
	}{
		{name: "mirror not enabled for the account", cause: relayFails(api.ErrGitHubMirrorNotEnabled), wantBackoff: 24 * time.Hour},
		{name: "not a member of the team", cause: relayFails(api.ErrGitHubMirrorNotAMember), wantBackoff: 24 * time.Hour},
		{name: "server without a mirror route", cause: relayFails(api.ErrGitHubMirrorUnsupported), wantBackoff: 24 * time.Hour},
		{name: "cli version refused", cause: relayFails(api.ErrVersionUnsupported), wantBackoff: 24 * time.Hour},
		{name: "not signed in", cause: relayFails(api.ErrUnauthorized), wantBackoff: time.Hour},
		{name: "busy with retry-after", cause: relayFails(&api.GitHubMirrorBusyError{Status: 429, RetryAfter: 7 * time.Minute}), wantBackoff: 7 * time.Minute},
		{name: "busy without retry-after", cause: relayFails(&api.GitHubMirrorBusyError{Status: 503}), wantBackoff: 15 * time.Minute},
		{name: "retry-after is capped at a day", cause: relayFails(&api.GitHubMirrorBusyError{Status: 429, RetryAfter: 72 * time.Hour}), wantBackoff: 24 * time.Hour},
		{
			name:        "repo not opted in",
			cause:       func(h *mirrorHarness) { h.relay.repoStatus = githubmirror.RepoNotOptedIn },
			recover:     func(h *mirrorHarness) { h.relay.repoStatus = "" },
			wantBackoff: 24 * time.Hour,
			wantStatus:  githubmirror.RepoNotOptedIn,
		},
		{
			name:        "repo not linked",
			cause:       func(h *mirrorHarness) { h.relay.repoStatus = githubmirror.RepoNotLinked },
			recover:     func(h *mirrorHarness) { h.relay.repoStatus = "" },
			wantBackoff: 24 * time.Hour,
			wantStatus:  githubmirror.RepoNotLinked,
		},
		{
			name:        "github auth",
			cause:       func(h *mirrorHarness) { h.fetcher.fail = githubDown(gh.ErrGitHubAuth) },
			recover:     func(h *mirrorHarness) { h.fetcher.fail = nil },
			wantBackoff: time.Hour,
		},
		{
			name:        "github rate limited",
			cause:       func(h *mirrorHarness) { h.fetcher.fail = githubDown(gh.ErrGitHubRateLimited) },
			recover:     func(h *mirrorHarness) { h.fetcher.fail = nil },
			wantBackoff: 30 * time.Minute,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newMirrorHarness(t)
			seedColdStart(h)
			tt.cause(h)
			if tt.recover == nil {
				tt.recover = func(h *mirrorHarness) { h.relay.fail = nil }
			}

			h.run()

			st := h.state()
			assert.NotEmpty(t, st.LastError)
			assert.Equal(t, h.clock(), st.LastErrorAt)
			assert.Equal(t, h.clock().Add(tt.wantBackoff), st.NextAllowedAt)
			assert.Equal(t, tt.wantStatus, st.RepoStatus)
			assert.True(t, st.LastSuccessAt.IsZero())
			assert.True(t, st.PullRequestCursor.IsZero() && st.IssueCursor.IsZero(), "a failed cycle must not move the cursors")
			assert.Empty(t, relayedKeys(st), "nothing is remembered as relayed when the cycle failed")
			stats := h.relayer.Stats()
			require.NotNil(t, stats)
			assert.Equal(t, st.LastError, stats.LastError)
			assert.Equal(t, st.NextAllowedAt, stats.NextAllowed)

			// quiet for the whole backoff, then trying again
			tt.recover(h)
			callsAtFailure := h.totalCalls()
			h.advance(tt.wantBackoff - time.Second)
			h.run()
			assert.Equal(t, callsAtFailure, h.totalCalls(), "no calls of any kind inside the backoff window")

			h.advance(time.Second)
			h.run()
			assert.Greater(t, h.totalCalls(), callsAtFailure, "the mirror resumes when the backoff ends")
			st = h.state()
			assert.Empty(t, st.LastError, "success clears the last error")
			assert.Equal(t, h.clock(), st.LastSuccessAt)
			assert.ElementsMatch(t, []string{h.prKey(1), h.prKey(2), h.issueKey(3)}, relayedKeys(st))
		})
	}
}

// Failure prevented: an unclassified failure (a network blip) is recorded but
// must not silence the mirror — the next cycle tries again.
func TestGitHubMirror_UnclassifiedErrorIsRetriedNextCycle(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	seedColdStart(h)
	failing := true
	h.relay.fail = func(githubmirror.RelayRequest) error {
		if failing {
			return errors.New("network error: connection reset")
		}
		return nil
	}

	h.run()
	st := h.state()
	assert.Contains(t, st.LastError, "connection reset")
	assert.True(t, st.NextAllowedAt.IsZero())

	failing = false
	h.advance(time.Minute)
	h.run()
	st = h.state()
	assert.Empty(t, st.LastError)
	assert.Len(t, relayedKeys(st), 3)
}

// Failure prevented: after the server declines a repo, its items are marked
// relayed, and when the team opts in nothing ever flows.
func TestGitHubMirror_RepoNotOptedInRemembersNoItems(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	seedColdStart(h)
	h.relay.repoStatus = githubmirror.RepoNotOptedIn

	h.run()
	assert.Empty(t, relayedKeys(h.state()))
	assert.Contains(t, h.state().LastError, githubmirror.RepoNotOptedIn)

	// the team opts the repo in; once the day's backoff ends everything flows
	h.relay.repoStatus = ""
	h.advance(24 * time.Hour)
	h.run()
	st := h.state()
	assert.Equal(t, githubmirror.RepoEnabled, st.RepoStatus)
	assert.Len(t, relayedKeys(st), 3)
}

// Failure prevented: GitHub rate-limits us halfway through a cold start and
// the work already paid for is thrown away, or the cursor jumps past items
// that were never processed.
func TestGitHubMirror_RateLimitMidwayRelaysWhatWasBuiltAndHoldsTheCursor(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	seedPRs(h, 4)
	h.fetcher.fail = func(method string, number int) error {
		if method == "ListReviews" && number == 3 {
			return fmt.Errorf("list reviews: %w", gh.ErrGitHubRateLimited)
		}
		return nil
	}

	h.run()

	st := h.state()
	assert.Equal(t, []int{1, 2}, h.relay.relayedNumbers(), "items built before the limit are still relayed")
	assert.Contains(t, st.Items, h.prKey(1))
	assert.NotContains(t, st.Items, h.prKey(3))
	assert.True(t, st.PullRequestCursor.IsZero())
	assert.Equal(t, h.clock().Add(30*time.Minute), st.NextAllowedAt)
}

// --- relay batching ------------------------------------------------------

// Failure prevented: a batch the server finds too large fails the whole run
// every cycle forever, instead of shrinking until it fits.
func TestGitHubMirror_TooLargeHalvesTheBatch(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	seedPRs(h, 5)
	h.relay.fail = func(req githubmirror.RelayRequest) error {
		if len(req.Items) > 2 {
			return api.ErrGitHubMirrorTooLarge
		}
		return nil
	}

	h.run()

	assert.Equal(t, []int{5, 2, 2, 1}, h.relay.batchSizes(), "5 is refused, halved to 2, and the remainder follows")
	st := h.state()
	assert.Empty(t, st.LastError)
	assert.True(t, st.ColdStartDone)
	for i := 1; i <= 5; i++ {
		assert.Equal(t, githubmirror.ResultAccepted, st.Items[h.prKey(i)].Status)
	}
}

// Failure prevented: relays are not split into the server's 50-item cap.
func TestGitHubMirror_RelaysInBatchesOfAtMostFifty(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t, withMirrorBudget(200))
	seedPRs(h, 120)

	h.run()

	assert.Equal(t, []int{50, 50, 20}, h.relay.batchSizes())
}

// Failure prevented: one item the server cannot take (too large on its own,
// or invalid) wedges every item batched with it, every cycle.
func TestGitHubMirror_OneBadItemIsIsolatedAndRejected(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		err        error
		wantReason string
	}{
		{"too large alone", api.ErrGitHubMirrorTooLarge, "too large"},
		{"validation", &api.GitHubMirrorValidationError{Message: "title is required"}, "title is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newMirrorHarness(t)
			seedPRs(h, 5)
			h.relay.fail = func(req githubmirror.RelayRequest) error {
				for _, it := range req.Items {
					if it.Number == 3 {
						return tt.err
					}
				}
				return nil
			}

			h.run()

			st := h.state()
			assert.Empty(t, st.LastError, "an item the server refuses is an outcome, not a failed cycle")
			assert.True(t, st.ColdStartDone)
			bad := st.Items[h.prKey(3)]
			assert.Equal(t, githubmirror.ResultRejected, bad.Status)
			assert.Contains(t, bad.Reason, tt.wantReason)
			for _, n := range []int{1, 2, 4, 5} {
				assert.Equal(t, githubmirror.ResultAccepted, st.Items[h.prKey(n)].Status, "PR %d", n)
			}

			// same bytes are not sent again
			h.fetcher.returnAll = true
			h.advance(15 * time.Minute)
			requestsBefore := h.relay.requestCount()
			detailBefore := h.fetcher.detailCalls()
			h.run()
			assert.Equal(t, requestsBefore, h.relay.requestCount())
			assert.Equal(t, detailBefore, h.fetcher.detailCalls())
		})
	}
}

// Failure prevented: an item the server rejected is retried on every cycle
// (hammering the server with bytes it will refuse again), or never again even
// after the author fixes it.
func TestGitHubMirror_RejectedItemWaitsUntilItChanges(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	seedPRs(h, 2)
	h.relay.verdict = func(it githubmirror.Item) (string, string) {
		if it.Number == 2 && len(it.Comments) == 0 {
			return githubmirror.ResultRejected, "body fails validation"
		}
		return githubmirror.ResultAccepted, ""
	}
	h.run()
	st := h.state()
	assert.Equal(t, githubmirror.ResultRejected, st.Items[h.prKey(2)].Status)
	assert.Equal(t, "body fails validation", st.Items[h.prKey(2)].Reason)
	assert.NotEmpty(t, st.Items[h.prKey(2)].ChangeHash)

	// a bot bumps updated_at: still the same bytes, still not sent
	h.advance(15 * time.Minute)
	pr2 := mirrorPR(2, h.clock().Add(-time.Minute))
	pr2.CreatedAt = mirrorTestNow.Add(-3 * time.Hour)
	h.fetcher.setPR(pr2)
	h.fetcher.addComment(2, mirrorComment(950, mirrorBot, "ci passed", h.clock()))
	requestsBefore := h.relay.requestCount()
	h.run()
	assert.Equal(t, requestsBefore, h.relay.requestCount())

	// the author edits it with a human comment: new bytes, new attempt
	h.advance(15 * time.Minute)
	pr2.UpdatedAt = h.clock().Add(-time.Minute)
	h.fetcher.setPR(pr2)
	h.fetcher.addComment(2, mirrorComment(951, mirrorMember, "fixed the description", h.clock().Add(-time.Minute)))
	h.run()
	assert.Equal(t, requestsBefore+1, h.relay.requestCount())
	assert.Equal(t, githubmirror.ResultAccepted, h.state().Items[h.prKey(2)].Status)
}

// Failure prevented: a server answer this client cannot interpret (an unknown
// status, a result for a key we never sent, a missing result) is recorded as
// "done", so the item is never relayed again.
func TestGitHubMirror_UnintelligibleResultsAreRetriedNotRemembered(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	seedPRs(h, 3)
	garble := true
	h.relay.respond = func(req githubmirror.RelayRequest) *githubmirror.RelayResponse {
		resp := &githubmirror.RelayResponse{RepoStatus: githubmirror.RepoEnabled}
		for _, it := range req.Items {
			key := githubmirror.SourceKey(req.Repo.Owner, req.Repo.Name, it.Kind, it.Number)
			switch {
			case !garble || it.Number == 1:
				resp.Results = append(resp.Results, githubmirror.ItemResult{SourceKey: key, Status: githubmirror.ResultAccepted})
			case it.Number == 2:
				resp.Results = append(resp.Results, githubmirror.ItemResult{SourceKey: key, Status: "queued-for-review"})
			}
			// item 3: no result at all
		}
		if garble {
			resp.Results = append(resp.Results, githubmirror.ItemResult{SourceKey: "github.com/other/repo/pull/9", Status: githubmirror.ResultAccepted})
		}
		return resp
	}

	h.run()

	st := h.state()
	assert.Contains(t, st.Items, h.prKey(1))
	assert.NotContains(t, st.Items, h.prKey(2), "unknown status is not done")
	assert.NotContains(t, st.Items, h.prKey(3), "a missing result is not done")
	assert.True(t, st.PullRequestCursor.IsZero(), "the cursor holds so the next cycle revisits them")
	assert.False(t, st.ColdStartDone)
	assert.Empty(t, st.LastError)
	assert.Equal(t, 1, strings.Count(h.logs.String(), "not fully understood"), "logged once at Warn")

	garble = false
	h.fetcher.returnAll = true
	h.advance(15 * time.Minute)
	h.run()
	st = h.state()
	assert.Contains(t, st.Items, h.prKey(2))
	assert.Contains(t, st.Items, h.prKey(3))
	assert.True(t, st.ColdStartDone)
	assert.Equal(t, []int{1, 2, 3, 2, 3}, h.relay.relayedNumbers(), "only the unfinished items were sent again")
}

// --- resilience ----------------------------------------------------------

// Failure prevented: a truncated or hand-edited state file wedges the mirror
// for good.
func TestGitHubMirror_CorruptStateFileStillRuns(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	seedColdStart(h)
	statePath := githubmirror.StatePath(h.ledgerPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(statePath), 0o755))
	require.NoError(t, os.WriteFile(statePath, []byte(`{"version": 1, "items": {"truncated`), 0o600))

	h.run()

	assert.ElementsMatch(t, []int{1, 2, 3}, h.relay.relayedNumbers())
	assert.Contains(t, h.logs.String(), "state unreadable")
	st := h.state() // loads without error: the corrupt file was replaced
	assert.True(t, st.ColdStartDone)
}

// Failure prevented: a state file from another repo (ledger re-pointed) makes
// the mirror skip items it has never relayed for this repo.
func TestGitHubMirror_StateForAnotherRepoIsDiscarded(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	seedPRs(h, 1)
	other := &githubmirror.State{
		Version:           githubmirror.StateVersion,
		Repo:              "someone/else",
		PullRequestCursor: mirrorTestNow,
		IssueCursor:       mirrorTestNow,
		Items: map[string]githubmirror.ItemState{
			h.prKey(1): {UpdatedAt: mirrorTestNow.Add(-time.Hour), Status: githubmirror.ResultAccepted, RelayedAt: mirrorTestNow},
		},
	}
	require.NoError(t, githubmirror.SaveState(h.ledgerPath, other))

	h.run()

	assert.Equal(t, []int{1}, h.relay.relayedNumbers())
	assert.Equal(t, "acme/api", h.state().Repo)
	assert.Equal(t, h.clock().Add(-githubmirror.Window), h.fetcher.since("ListPullRequests"), "another repo's cursor is not inherited")
}

// Failure prevented: an item deleted or transferred between the listing and
// the detail call (a GitHub 404) fails the whole cycle.
func TestGitHubMirror_ItemNotFoundOnGitHubIsSkipped(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	seedPRs(h, 3)
	h.fetcher.fail = func(method string, number int) error {
		if method == "ListIssueComments" && number == 2 {
			return fmt.Errorf("github api status 404: %w", ledger.ErrGitHubNotFound)
		}
		return nil
	}

	h.run()

	st := h.state()
	assert.Empty(t, st.LastError)
	assert.ElementsMatch(t, []int{1, 3}, h.relay.relayedNumbers())
	assert.NotContains(t, st.Items, h.prKey(2))
	assert.True(t, st.ColdStartDone, "a vanished item does not hold the cursor back")
}

// Failure prevented: a stale cursor (daemon off for months) lists history
// older than any post could live.
func TestGitHubMirror_ListingNeverReachesBeyondTheWindow(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	seedPRs(h, 1)
	stale := mirrorTestNow.Add(-200 * 24 * time.Hour)
	require.NoError(t, githubmirror.SaveState(h.ledgerPath, &githubmirror.State{
		Version: githubmirror.StateVersion, Repo: "acme/api", PullRequestCursor: stale, IssueCursor: stale,
	}))

	h.run()

	assert.Equal(t, mirrorTestNow.Add(-githubmirror.Window), h.fetcher.since("ListPullRequests"))
	assert.Equal(t, mirrorTestNow.Add(-githubmirror.Window), h.fetcher.since("ListIssues"))
}

// Failure prevented: private PR text leaking into daemon logs, which are
// shared in bug reports.
func TestGitHubMirror_LogsNeverContainItemText(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	pr := mirrorPR(1, h.clock().Add(-time.Hour))
	pr.Title = "TITLE-SECRET"
	pr.Body = "BODY-SECRET"
	h.fetcher.prs = []githubmirror.SourcePR{pr}
	h.fetcher.addComment(1, mirrorComment(1, mirrorMember, "COMMENT-SECRET", h.clock().Add(-time.Hour)))
	h.relay.fail = func(githubmirror.RelayRequest) error { return errors.New("relay refused") }

	h.run()
	h.relay.fail = nil
	h.advance(time.Minute)
	h.run()

	logs := h.logs.String()
	for _, secret := range []string{"TITLE-SECRET", "BODY-SECRET", "COMMENT-SECRET"} {
		assert.NotContains(t, logs, secret)
	}
	assert.Contains(t, logs, "github mirror cycle")
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		assert.Contains(t, line, "level=", "every record is one line")
	}
}

// --- status --------------------------------------------------------------

func TestGitHubMirror_Stats(t *testing.T) {
	t.Parallel()
	var nilRelayer *GitHubMirrorRelayer
	assert.Nil(t, nilRelayer.Stats())

	h := newMirrorHarness(t)
	seedColdStart(h)
	h.run()

	stats := h.relayer.Stats()
	require.NotNil(t, stats)
	assert.True(t, stats.Enabled)
	assert.Equal(t, h.clock(), stats.LastAttempt)
	assert.Equal(t, h.clock(), stats.LastSuccess)
	assert.Equal(t, githubmirror.RepoEnabled, stats.RepoStatus)
	assert.Equal(t, 4, stats.ItemsRemembered)
	assert.Empty(t, stats.LastError)

	h.setEnabled(false)
	stats = h.relayer.Stats()
	require.NotNil(t, stats, "a mirror that ran and was then turned off still reports")
	assert.False(t, stats.Enabled)
}

func TestGitHubMirrorFeatureOn(t *testing.T) {
	t.Parallel()
	yes, no := true, false
	tests := []struct {
		name     string
		settings func() *flags.CLISettingsResponse
		want     bool
	}{
		{"no settings source", nil, false},
		{"settings not fetched yet", func() *flags.CLISettingsResponse { return nil }, false},
		{"server sends no opinion", func() *flags.CLISettingsResponse { return &flags.CLISettingsResponse{} }, false},
		{"server says off", func() *flags.CLISettingsResponse {
			return &flags.CLISettingsResponse{FetchedAt: time.Now(), Features: flags.CLIFeatures{GitHubMirror: &no}}
		}, false},
		{"server says on", func() *flags.CLISettingsResponse {
			return &flags.CLISettingsResponse{FetchedAt: time.Now(), Features: flags.CLIFeatures{GitHubMirror: &yes}}
		}, true},
		{"server said on a while ago but the refresh is still within twice its interval", func() *flags.CLISettingsResponse {
			return &flags.CLISettingsResponse{FetchedAt: time.Now().Add(-flags.CLISettingsMaxAge - time.Minute), Features: flags.CLIFeatures{GitHubMirror: &yes}}
		}, true},
		{"server said on but the cache is older than twice the refresh interval", func() *flags.CLISettingsResponse {
			return &flags.CLISettingsResponse{FetchedAt: time.Now().Add(-2*flags.CLISettingsMaxAge - time.Minute), Features: flags.CLIFeatures{GitHubMirror: &yes}}
		}, false},
		{"server said on but the fetch time was never recorded", func() *flags.CLISettingsResponse {
			return &flags.CLISettingsResponse{Features: flags.CLIFeatures{GitHubMirror: &yes}}
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, githubMirrorFeatureOn(tt.settings))
		})
	}
}

func TestResolveMirrorAuthToken_PrefersHeartbeatToken(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "heartbeat-token", resolveMirrorAuthToken(func() string { return "heartbeat-token" }, "https://unused.example"))
}

func TestMirrorBackoff_NilAndUnclassified(t *testing.T) {
	t.Parallel()
	assert.Zero(t, mirrorBackoff(nil))
	assert.Zero(t, mirrorBackoff(errors.New("boom")))
}

func TestMirrorText_FlattensAndBounds(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "a b c", mirrorText(errors.New("a\n  b\tc")))
	long := mirrorText(strings.Repeat("é", 1000))
	assert.LessOrEqual(t, len([]rune(long)), githubMirrorTextMax+3)
	assert.True(t, strings.HasSuffix(long, "..."))
}

// --- independence from the Ledger sync -----------------------------------

// ledgerFetcherFake is GitHub as the Ledger sync sees it: empty, or failing.
type ledgerFetcherFake struct {
	mu      sync.Mutex
	prErr   error
	prCalls int
}

func (f *ledgerFetcherFake) ListPullRequests(context.Context, string, string, ledger.ListPRsOptions) ([]ledger.FetchedPR, *ledger.FetchRateLimit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prCalls++
	return nil, nil, f.prErr
}

func (f *ledgerFetcherFake) ListIssues(context.Context, string, string, ledger.ListIssuesOptions) ([]ledger.FetchedIssue, *ledger.FetchRateLimit, error) {
	return nil, nil, nil
}

func (f *ledgerFetcherFake) ListPRComments(context.Context, string, string, int) ([]ledger.FetchedComment, error) {
	return nil, nil
}

func (f *ledgerFetcherFake) ListIssueComments(context.Context, string, string, int) ([]ledger.FetchedComment, error) {
	return nil, nil
}

func (f *ledgerFetcherFake) ListPRCommits(context.Context, string, string, int) ([]ledger.FetchedPRCommit, error) {
	return nil, nil
}

func (f *ledgerFetcherFake) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prCalls
}

type syncWithMirror struct {
	*mirrorHarness
	manager *GitHubSyncManager
	ledger  *ledgerFetcherFake
	issues  *IssueTracker
}

// newSyncWithMirror builds a manager whose Ledger half runs against an empty
// fake GitHub and whose mirror half is the harness's relayer. Environment is
// pinned so a developer's own OX_GITHUB_SYNC* settings cannot change the test,
// which is why callers cannot run in parallel.
func newSyncWithMirror(t *testing.T, opts ...mirrorHarnessOption) *syncWithMirror {
	t.Helper()
	t.Setenv("GITHUB_TOKEN", "github-token")
	t.Setenv(config.EnvGitHubSync, "enabled")
	t.Setenv(config.EnvGitHubSyncPRs, "enabled")
	t.Setenv(config.EnvGitHubSyncIssues, "enabled")

	h := newMirrorHarness(t, opts...)
	seedColdStart(h)
	fakeLedger := &ledgerFetcherFake{}
	issues := NewIssueTracker()

	logger := slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	m := NewGitHubSyncManager(t.TempDir(), &sync.Mutex{}, logger)
	m.owner, m.repo = "acme", "api" // skip git remote detection
	m.ledgerFetcher = func(string) ledger.GitHubFetcher { return fakeLedger }
	m.SetIssueTracker(issues)
	m.SetMirrorRelayer(h.relayer)
	return &syncWithMirror{mirrorHarness: h, manager: m, ledger: fakeLedger, issues: issues}
}

func (s *syncWithMirror) ledgerAccounting() (failCount int, backoffUntil, lastSync time.Time, lastErr error) {
	s.manager.mu.Lock()
	defer s.manager.mu.Unlock()
	return s.manager.failCount, s.manager.backoffUntil, s.manager.lastSync, s.manager.lastErr
}

// Failure prevented: a flaky or refused mirror suspends the Ledger sync (its
// failCount trips the five-strike suspension) or raises its GitHub-auth issue.
func TestGitHubSync_MirrorFailureNeverTouchesLedgerSyncAccounting(t *testing.T) {
	causes := map[string]func(s *syncWithMirror){
		"mirror not enabled": func(s *syncWithMirror) {
			s.relay.fail = func(githubmirror.RelayRequest) error { return api.ErrGitHubMirrorNotEnabled }
		},
		"network error": func(s *syncWithMirror) {
			s.relay.fail = func(githubmirror.RelayRequest) error { return errors.New("network error") }
		},
		"server busy": func(s *syncWithMirror) {
			s.relay.fail = func(githubmirror.RelayRequest) error { return &api.GitHubMirrorBusyError{Status: 503} }
		},
		// the mirror's own GitHub auth failure must not raise the Ledger
		// sync's GitHub-auth issue
		"github auth in the mirror": func(s *syncWithMirror) {
			s.fetcher.fail = func(method string, _ int) error {
				if method == "ListPullRequests" {
					return fmt.Errorf("list pull requests: %w", gh.ErrGitHubAuth)
				}
				return nil
			}
		},
	}
	for name, cause := range causes {
		t.Run(name, func(t *testing.T) {
			s := newSyncWithMirror(t)
			cause(s)

			for i := 0; i < 8; i++ {
				s.advance(2 * time.Hour) // clear any mirror backoff between cycles
				s.manager.doSync(context.Background(), s.ledgerPath)
			}

			failCount, backoffUntil, lastSync, lastErr := s.ledgerAccounting()
			assert.Zero(t, failCount, "mirror failures are not Ledger sync failures")
			assert.True(t, backoffUntil.IsZero())
			assert.NoError(t, lastErr)
			assert.False(t, lastSync.IsZero(), "the Ledger sync completed every cycle")
			assert.Zero(t, s.issues.Count(), "the mirror must not raise the Ledger sync's issues")
			assert.Empty(t, s.manager.Status().LastError)
			assert.GreaterOrEqual(t, s.ledger.calls(), 8, "the Ledger sync ran every cycle")

			mirror := s.manager.Status().Mirror
			require.NotNil(t, mirror)
			assert.NotEmpty(t, mirror.LastError, "the mirror failure is visible, under its own heading")
		})
	}
}

// Failure prevented: a Ledger sync failure (GitHub auth, a push error) takes
// the mirror down with it for the cycle.
func TestGitHubSync_LedgerFailureDoesNotPreventTheMirror(t *testing.T) {
	s := newSyncWithMirror(t)
	s.ledger.prErr = fmt.Errorf("list PRs: %w: status 403", gh.ErrGitHubAuth)

	s.manager.doSync(context.Background(), s.ledgerPath)

	failCount, backoffUntil, _, lastErr := s.ledgerAccounting()
	assert.Equal(t, 1, failCount, "the Ledger sync did fail")
	assert.True(t, backoffUntil.After(time.Now()))
	require.Error(t, lastErr)
	assert.Equal(t, 1, s.issues.Count())

	assert.ElementsMatch(t, []int{1, 2, 3}, s.relay.relayedNumbers(), "the mirror ran anyway")
	assert.Empty(t, s.state().LastError)
}

// Failure prevented: a Ledger sync parked in backoff or suspended (it gives up
// after five failures) silences the mirror too, because the whole cycle was
// skipped at the gate.
func TestGitHubSync_LedgerBackoffDoesNotSilenceTheMirror(t *testing.T) {
	tests := []struct {
		name  string
		block func(m *GitHubSyncManager)
	}{
		{"backoff", func(m *GitHubSyncManager) { m.backoffUntil = time.Now().Add(time.Hour) }},
		{"suspended after repeated failures", func(m *GitHubSyncManager) { m.failCount = 6 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSyncWithMirror(t)
			tt.block(s.manager)
			failCountBefore, backoffBefore, _, _ := s.ledgerAccounting()

			s.manager.CheckAndSync(context.Background(), s.ledgerPath)

			require.Eventually(t, func() bool {
				return s.relay.requestCount() > 0 && !s.manager.Status().Syncing
			}, 5*time.Second, 10*time.Millisecond, "the mirror runs even though the Ledger sync is blocked")
			assert.Zero(t, s.ledger.calls(), "the blocked Ledger sync stays blocked")
			failCountAfter, backoffAfter, _, _ := s.ledgerAccounting()
			assert.Equal(t, failCountBefore, failCountAfter)
			assert.Equal(t, backoffBefore, backoffAfter)
		})
	}
}

// Pre-existing behavior, kept: with no mirror configured a blocked Ledger sync
// does nothing at all.
func TestGitHubSync_BlockedLedgerSyncWithoutMirrorIsANoOp(t *testing.T) {
	s := newSyncWithMirror(t)
	s.manager.SetMirrorRelayer(nil)
	s.manager.backoffUntil = time.Now().Add(time.Hour)

	s.manager.CheckAndSync(context.Background(), s.ledgerPath)

	assert.False(t, s.manager.Status().Syncing)
	assert.Zero(t, s.ledger.calls())
	assert.Zero(t, s.totalCalls())
}

// Failure prevented: a bug in the mirror (it handles server and GitHub data)
// crashes the whole daemon.
func TestGitHubSync_MirrorPanicIsContained(t *testing.T) {
	s := newSyncWithMirror(t)
	panicky := NewGitHubMirrorRelayer(GitHubMirrorDeps{
		Enabled:    func() bool { return true },
		AuthToken:  func() string { return "t" },
		Team:       func() (string, string) { return "team_1", s.teamPath },
		NewFetcher: func(string) githubmirror.Fetcher { panic("boom") },
		NewRelay:   func(string) MirrorRelayClient { return s.relay },
		Logger:     slog.New(slog.NewTextHandler(s.logs, nil)),
	})
	s.manager.SetMirrorRelayer(panicky)

	assert.NotPanics(t, func() { s.manager.doSync(context.Background(), s.ledgerPath) })

	failCount, _, lastSync, lastErr := s.ledgerAccounting()
	assert.Zero(t, failCount)
	assert.NoError(t, lastErr)
	assert.False(t, lastSync.IsZero())
	assert.Contains(t, s.logs.String(), "github mirror cycle panicked")
}

// Failure prevented: the mirror runs for a user who switched GitHub sync off.
func TestGitHubSync_MirrorHonorsTheLedgerSyncMasterSwitch(t *testing.T) {
	s := newSyncWithMirror(t)
	t.Setenv(config.EnvGitHubSync, "disabled")

	s.manager.doSync(context.Background(), s.ledgerPath)

	assert.Zero(t, s.ledger.calls())
	assert.Zero(t, s.totalCalls())
}

// Existing status JSON keeps its shape; the mirror block appears only once
// there is something to say.
func TestGitHubSyncStats_MirrorBlockIsOmittedWhenAbsent(t *testing.T) {
	t.Parallel()
	bare, err := json.Marshal(GitHubSyncStats{Owner: "acme", Repo: "api"})
	require.NoError(t, err)
	assert.NotContains(t, string(bare), "mirror")
	assert.Contains(t, string(bare), `"owner":"acme"`)

	withMirror, err := json.Marshal(GitHubSyncStats{Mirror: &GitHubMirrorStats{Enabled: true, RepoStatus: "enabled", ItemsRemembered: 3}})
	require.NoError(t, err)
	assert.Contains(t, string(withMirror), `"mirror":{"enabled":true`)
	assert.Contains(t, string(withMirror), `"items_remembered":3`)
}

// Failure prevented: a repo is switched to private on GitHub, and the daemon
// keeps relaying it as public from cached metadata — publishing private issues
// team-wide before any admin opted the repo in. Visibility is re-read every
// cycle, and a failed read relays nothing.
func TestGitHubMirror_RepoTurnedPrivateIsRelayedAsPrivateNextCycle(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	seedColdStart(h)
	h.run()
	require.Equal(t, 1, h.relay.requestCount())
	require.False(t, h.relay.requests[0].Repo.Private)

	h.fetcher.mu.Lock()
	h.fetcher.repo.Private = true
	h.fetcher.mu.Unlock()
	h.advance(15 * time.Minute)
	pr2 := mirrorPR(2, h.clock().Add(-time.Minute))
	pr2.CreatedAt = h.clock().Add(-5 * time.Hour)
	h.fetcher.setPR(pr2)
	h.fetcher.addComment(2, mirrorComment(910, mirrorMember, "follow-up", h.clock().Add(-time.Minute)))

	h.run()

	require.Equal(t, 2, h.relay.requestCount())
	assert.True(t, h.relay.requests[1].Repo.Private, "the very next relay must carry the new visibility")
}

func TestGitHubMirror_RepoVisibilityUnknownRelaysNothing(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	seedColdStart(h)
	h.run()
	relaysBefore := h.relay.requestCount()

	h.fetcher.mu.Lock()
	h.fetcher.fail = func(method string, _ int) error {
		if method == "Repo" {
			return errors.New("github unavailable")
		}
		return nil
	}
	h.fetcher.mu.Unlock()
	h.advance(15 * time.Minute)
	pr2 := mirrorPR(2, h.clock().Add(-time.Minute))
	pr2.CreatedAt = h.clock().Add(-5 * time.Hour)
	h.fetcher.setPR(pr2)
	h.fetcher.addComment(2, mirrorComment(911, mirrorMember, "follow-up", h.clock().Add(-time.Minute)))

	h.run()

	assert.Equal(t, relaysBefore, h.relay.requestCount(), "no relay on stale visibility")
}

// --- expiry refresh when the hash does not change ------------------------

func mirrorApproval(id int64, at time.Time) githubmirror.SourceReview {
	return githubmirror.SourceReview{ID: id, Author: mirrorReviewer, State: "APPROVED", SubmittedAt: at}
}

// Failure prevented: a reviewer approves a PR again (or the PR is closed and
// reopened) after the first relay. The change hash covers who approved, not
// when, so it does not move; the daemon dropped the item as "unchanged" and the
// post expired 90 days after the first approval even though people kept
// working on the PR.
func TestGitHubMirror_ReApprovalIsRelayedAgainSoTheExpiryMoves(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	h.noIssues = true
	first := h.clock().Add(-time.Hour)
	pr := mirrorPR(1, first)
	pr.CreatedAt = first.Add(-time.Hour)
	h.fetcher.setPR(pr)
	h.fetcher.reviews[1] = []githubmirror.SourceReview{mirrorApproval(1, first)}

	h.run()
	require.Equal(t, []int{1}, h.relay.relayedNumbers())
	require.True(t, h.state().Items[h.prKey(1)].LastMaterialChangeAt.Equal(first))

	// a month later the same reviewer approves again
	h.advance(30 * 24 * time.Hour)
	again := h.clock().Add(-time.Minute)
	pr.UpdatedAt = again
	h.fetcher.setPR(pr)
	h.fetcher.reviews[1] = append(h.fetcher.reviews[1], mirrorApproval(2, again))

	h.run()

	assert.Equal(t, []int{1, 1}, h.relay.relayedNumbers(), "the re-approval must reach the server, or the post expires on the first approval's clock")
	reqs := h.relay.requests
	require.Len(t, reqs, 2)
	assert.Equal(t, reqs[0].Items[0].ChangeHash, reqs[1].Items[0].ChangeHash, "precondition: the hash really did not move")
	assert.True(t, reqs[1].Items[0].LastMaterialChangeAt.Equal(again))
	assert.True(t, h.state().Items[h.prKey(1)].LastMaterialChangeAt.Equal(again), "the remembered activity time moves with it")

	// control: the same GitHub state again is still skipped for free
	h.fetcher.returnAll = true
	h.advance(15 * time.Minute)
	detailBefore := h.fetcher.detailCalls()
	h.run()
	assert.Equal(t, []int{1, 1}, h.relay.relayedNumbers())
	assert.Equal(t, detailBefore, h.fetcher.detailCalls())
}

// Failure prevented: a PR whose only activity is old was remembered as expired;
// when its reviewer approves again it is live, but the "same hash" shortcut
// kept it off the board for good.
func TestGitHubMirror_FreshApprovalRevivesAnItemMarkedExpired(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	h.noIssues = true
	now := h.clock()
	old := now.Add(-100 * 24 * time.Hour)
	pr := mirrorPR(1, now.Add(-24*time.Hour)) // a bot touched it yesterday
	pr.CreatedAt = old.Add(-time.Hour)
	h.fetcher.setPR(pr)
	h.fetcher.reviews[1] = []githubmirror.SourceReview{mirrorApproval(1, old)}

	h.run()
	require.Empty(t, h.relay.relayedNumbers(), "precondition: nothing alive to publish")
	require.Equal(t, githubMirrorReasonExpired, h.state().Items[h.prKey(1)].Reason)

	// control: another bot bump changes updated_at only; it stays off the board
	h.advance(time.Hour)
	pr.UpdatedAt = h.clock().Add(-time.Minute)
	h.fetcher.setPR(pr)
	h.run()
	assert.Empty(t, h.relay.relayedNumbers(), "bot noise must not revive an expired item")
	assert.Equal(t, githubMirrorReasonExpired, h.state().Items[h.prKey(1)].Reason)

	// the reviewer approves again: same hash, activity is now fresh
	h.advance(time.Hour)
	again := h.clock().Add(-time.Minute)
	pr.UpdatedAt = again
	h.fetcher.setPR(pr)
	h.fetcher.reviews[1] = append(h.fetcher.reviews[1], mirrorApproval(2, again))

	h.run()

	assert.Equal(t, []int{1}, h.relay.relayedNumbers(), "fresh human activity puts the item back on the board")
	item := h.state().Items[h.prKey(1)]
	assert.Equal(t, githubmirror.ResultAccepted, item.Status)
	assert.Empty(t, item.Reason)
	assert.True(t, item.LastMaterialChangeAt.Equal(again))
}

// --- relay history belongs to one team -----------------------------------

// Failure prevented: re-running ox init points the repo at another team while
// the Ledger (and so the saved relay history) stays. The new team's board has
// none of the posts, but the daemon believes everything was already relayed and
// sends nothing.
func TestGitHubMirror_ChangingTeamStartsOverSoTheNewBoardIsPopulated(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	seedColdStart(h)
	h.run()
	require.Equal(t, []int{1, 2, 3}, sortedCopy(h.relay.relayedTo("team_1")))

	h.setTeam("team_2")
	h.advance(15 * time.Minute)
	h.fetcher.returnAll = true
	h.run()

	assert.Equal(t, []int{1, 2, 3}, sortedCopy(h.relay.relayedTo("team_2")), "the new team's board gets the existing posts")
	assert.Equal(t, "team_2", h.state().Team)
	assert.Contains(t, h.logs.String(), "belongs to another team")

	// control: the same team again relays nothing more
	requestsBefore := h.relay.requestCount()
	h.advance(15 * time.Minute)
	h.run()
	assert.Equal(t, requestsBefore, h.relay.requestCount())
}

// Failure prevented: a state file written before teams were recorded is thrown
// away on upgrade, re-crawling a repo's whole window for nothing.
func TestGitHubMirror_StateWithoutATeamIsAdoptedNotDiscarded(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	seedPRs(h, 1)
	h.noIssues = true
	h.run()
	st := h.state()
	st.Team = ""
	require.NoError(t, githubmirror.SaveState(h.ledgerPath, st))
	relaysBefore := h.relay.requestCount()

	h.fetcher.returnAll = true
	h.advance(15 * time.Minute)
	h.run()

	assert.Equal(t, relaysBefore, h.relay.requestCount(), "history without a team still counts")
	assert.Equal(t, "team_1", h.state().Team, "and is claimed by the team that adopts it")
}

// --- one cursor per kind -------------------------------------------------

// Failure prevented: PR-only cycles advance a shared cursor to today; when
// issue sync is switched on later, issues are listed from just before that
// cursor and every still-live issue updated earlier in the 90-day window never
// reaches the board.
func TestGitHubMirror_EnablingIssuesLaterListsTheirWholeBacklog(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	h.noIssues = true
	now := h.clock()
	h.fetcher.prs = []githubmirror.SourcePR{mirrorPR(1, now.Add(-time.Hour))}
	h.fetcher.issues = []githubmirror.SourceIssue{mirrorIssue(3, now.Add(-30*24*time.Hour))}

	h.run()
	require.Equal(t, []int{1}, h.relay.relayedNumbers())

	h.noIssues = false
	h.advance(15 * time.Minute)
	h.run()

	assert.Equal(t, []int{1, 3}, h.relay.relayedNumbers(), "an issue from 30 days ago is still live and must be relayed")
	assert.Equal(t, h.clock().Add(-githubmirror.Window), h.fetcher.since("ListIssues"), "a kind that never ran lists its whole window")

	st := h.state()
	assert.True(t, st.PullRequestCursor.Equal(now.Add(-time.Hour)), "the PR cursor is the newest PR")
	assert.True(t, st.IssueCursor.Equal(now.Add(-30*24*time.Hour)), "the issue cursor is the newest issue, not the PR's")
}

// Failure prevented: a kind that is switched off still has its cursor moved by
// the other kind's progress.
func TestGitHubMirror_DisabledKindKeepsItsCursorStill(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t)
	seedColdStart(h)
	h.noPRs = true

	h.run()

	st := h.state()
	assert.True(t, st.PullRequestCursor.IsZero(), "pull requests were never listed")
	assert.True(t, st.IssueCursor.Equal(h.clock().Add(-3*time.Hour)))
	assert.Zero(t, h.fetcher.count("ListPullRequests"))

	// and each kind lists from its own cursor
	h.noPRs = false
	h.advance(15 * time.Minute)
	h.run()
	assert.Equal(t, h.clock().Add(-githubmirror.Window), h.fetcher.since("ListPullRequests"))
	assert.Equal(t, mirrorTestNow.Add(-3*time.Hour).Add(-githubMirrorCursorOverlap), h.fetcher.since("ListIssues"))
}

// --- cycle time budget ---------------------------------------------------

// Failure prevented: a GitHub that stops answering keeps one relay cycle alive
// past the sync interval; the sync manager stays "syncing" and the Ledger's own
// GitHub sync is skipped. Running out of time is not a failure either: no
// backoff, no error, and the next cycle finishes the job.
func TestGitHubMirror_CycleBudgetEndsASlowCycleWithoutFailingIt(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t, withMirrorCycleTimeout(200*time.Millisecond))
	h.noIssues = true
	seedPRs(h, 3)
	var stuck atomic.Bool
	stuck.Store(true)
	h.fetcher.hang = func(method string, number int) bool {
		return stuck.Load() && method == "ListIssueComments" && number == 3
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.run()
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the cycle outlived its time budget")
	}

	st := h.state()
	assert.Empty(t, st.LastError, "running out of time is not a failure")
	assert.True(t, st.LastErrorAt.IsZero())
	assert.True(t, st.NextAllowedAt.IsZero(), "no backoff: the next cycle should simply continue")
	assert.Equal(t, []int{1, 2}, h.relay.relayedNumbers(), "items built before the budget ended are still relayed")
	assert.Contains(t, st.Items, h.prKey(1))
	assert.Contains(t, st.Items, h.prKey(2))
	assert.NotContains(t, st.Items, h.prKey(3))
	assert.True(t, st.PullRequestCursor.IsZero(), "the cursor holds so the unfinished item is revisited")
	assert.Contains(t, h.logs.String(), "ran out of time")
	require.NotNil(t, h.relayer.Stats())
	assert.Empty(t, h.relayer.Stats().LastError)

	stuck.Store(false)
	h.advance(15 * time.Minute)
	h.run()

	assert.Equal(t, []int{1, 2, 3}, h.relay.relayedNumbers(), "the next cycle relays only what was left")
	st = h.state()
	assert.True(t, st.ColdStartDone)
	assert.True(t, st.PullRequestCursor.Equal(mirrorTestNow.Add(-time.Hour)))
}

// Failure prevented: on a repo busy enough that listing alone outlasts the
// cycle budget, the pages already listed were thrown away and every cycle
// restarted the same crawl, so nothing ever reached the board. The newest
// listed items are relayed instead, and the cursor holds until a listing
// finishes, so the older ones follow once GitHub keeps up.
func TestGitHubMirror_SlowListingStillRelaysTheNewestItems(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t, withMirrorCycleTimeout(200*time.Millisecond))
	h.noIssues = true
	seedPRs(h, 5)
	var stuck atomic.Bool
	stuck.Store(true)
	h.fetcher.listPartial = 2 // PRs 1 and 2 are the newest
	h.fetcher.hang = func(method string, _ int) bool {
		return stuck.Load() && method == "ListPullRequests"
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.run()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the cycle outlived its time budget")
	}

	assert.Equal(t, []int{1, 2}, h.relay.relayedNumbers(), "the newest listed items reach the board even though the listing did not finish")
	st := h.state()
	assert.Empty(t, st.LastError, "running out of time is not a failure")
	assert.True(t, st.NextAllowedAt.IsZero())
	assert.True(t, st.PullRequestCursor.IsZero(), "an unfinished listing must not move the cursor past items it never saw")
	assert.False(t, st.ColdStartDone)

	stuck.Store(false)
	h.advance(15 * time.Minute)
	h.run()

	assert.Equal(t, []int{1, 2, 3, 4, 5}, h.relay.relayedNumbers(), "once a listing finishes, only the rest is relayed")
	st = h.state()
	assert.True(t, st.ColdStartDone)
	assert.False(t, st.PullRequestCursor.IsZero())
}

// Failure prevented: the time budget only bounds the happy path. A real
// failure (a refused relay) that lands while the budget is still open must keep
// its backoff, and a parent cancellation (daemon shutdown) must stay
// "interrupted", not "partial".
func TestGitHubMirror_BudgetDoesNotMaskRealFailures(t *testing.T) {
	t.Parallel()
	h := newMirrorHarness(t, withMirrorCycleTimeout(time.Minute))
	seedColdStart(h)
	h.relay.fail = func(githubmirror.RelayRequest) error { return api.ErrGitHubMirrorNotEnabled }

	h.run()

	st := h.state()
	assert.NotEmpty(t, st.LastError)
	assert.Equal(t, h.clock().Add(24*time.Hour), st.NextAllowedAt)

	h2 := newMirrorHarness(t, withMirrorCycleTimeout(time.Minute))
	seedColdStart(h2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h2.relayer.Run(ctx, h2.target())
	assert.Empty(t, h2.state().LastError, "shutting down records nothing")
	assert.NotContains(t, h2.logs.String(), "ran out of time")
}

// Failure prevented: a slow mirror keeps the sync manager "syncing" past the
// next interval, so the Ledger's own GitHub sync is skipped until it ends.
func TestGitHubSync_SlowMirrorDoesNotHoldBackTheNextLedgerSync(t *testing.T) {
	s := newSyncWithMirror(t, withMirrorCycleTimeout(200*time.Millisecond))
	s.fetcher.hang = func(method string, _ int) bool { return method == "ListIssueComments" }

	s.manager.CheckAndSync(context.Background(), s.ledgerPath)
	require.Eventually(t, func() bool { return s.ledger.calls() >= 1 }, 5*time.Second, 10*time.Millisecond, "the Ledger half ran")
	require.Eventually(t, func() bool { return !s.manager.Status().Syncing }, 5*time.Second, 10*time.Millisecond,
		"the sync manager must be free again once the mirror's budget is spent")

	s.manager.CheckAndSync(context.Background(), s.ledgerPath)
	require.Eventually(t, func() bool { return s.ledger.calls() >= 2 }, 5*time.Second, 10*time.Millisecond, "the next Ledger cycle runs")
	require.Eventually(t, func() bool { return !s.manager.Status().Syncing }, 5*time.Second, 10*time.Millisecond)
	assert.Empty(t, s.manager.Status().LastError)
	assert.Zero(t, s.issues.Count())
}

// --- stale cached settings -----------------------------------------------

// Failure prevented: the server turns the mirror off (revoked, incident) but
// the daemon's settings refresh keeps failing; the last cached "on" is trusted
// forever and the daemon keeps publishing to the team board.
func TestGitHubMirror_StaleCachedSettingsMakeNoCalls(t *testing.T) {
	t.Parallel()
	yes := true
	var mu sync.Mutex
	settings := &flags.CLISettingsResponse{
		FetchedAt: time.Now().Add(-3 * flags.CLISettingsMaxAge),
		Features:  flags.CLIFeatures{GitHubMirror: &yes},
	}
	current := func() *flags.CLISettingsResponse {
		mu.Lock()
		defer mu.Unlock()
		cp := *settings
		return &cp
	}
	h := newMirrorHarness(t, withMirrorEnabledFunc(func() bool { return githubMirrorFeatureOn(current) }))
	seedColdStart(h)

	h.run()

	assert.Zero(t, h.totalCalls(), "settings older than twice the refresh interval are no opinion: the mirror stays off")
	_, err := os.Stat(githubmirror.StatePath(h.ledgerPath))
	assert.ErrorIs(t, err, os.ErrNotExist)

	// a successful refresh turns it back on
	mu.Lock()
	settings.FetchedAt = time.Now()
	mu.Unlock()
	h.run()
	assert.Equal(t, []int{1, 2, 3}, sortedCopy(h.relay.relayedNumbers()))
}
