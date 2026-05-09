package daemon

// KB sync backoff tests — the kb pipeline's failure-recovery posture
// is intentionally simpler than the ledger's exponential backoff
// machinery: the list-call's "retry interval" IS the team-context
// tick (15s), and per-bubble failures (clone error, pull error) are
// contained without escalating to a workspace-registry backoff.
//
// What we verify:
//   - Transient list errors don't *prevent* the next pass from succeeding
//     (no sticky in-memory backoff state).
//   - 429 / 503 / generic errors all behave the same way (logged, retry
//     next pass) — there's no special "rate-limit" branch that could
//     exfiltrate the wrong sentinel.
//   - Clone failures don't poison subsequent good bubbles (already
//     covered by sync_bubbles_test.go's PerBubbleFailureIsolation; here
//     we add the temporal axis: a previously-failed bubble that
//     succeeds on a later pass actually gets cloned).
//   - 401 surfaces ErrUnauthorized (handled separately at the API layer
//     — included here because heartbeat token-rotation interacts with
//     this case).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSyncBubbles_Backoff_NoStickyState verifies that a transient list
// failure on pass N does NOT prevent pass N+1 from running normally.
// The kb pipeline must not maintain in-memory "this lister is broken"
// state across passes — the next ticker is the retry mechanism.
//
// Failure prevented: a future change adding an in-memory mutex /
// circuit-breaker that would lock the lister out after the first
// transient error, breaking recovery once the API came back.
func TestSyncBubbles_Backoff_NoStickyState(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	s, _ := kbTestScheduler(t)
	bareDir := makeBareRepo(t, "recover", "r.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_recover",
		KBType:  api.KBTypeTeam,
		Slug:    "recover",
		RepoURL: "file://" + bareDir,
	}

	// pass 1: lister fails with a transient error.
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{err: errors.New("transient 503")}
	})
	s.syncBubbles(context.Background())

	target := paths.KBDir(bubble.KBID)
	if _, err := os.Stat(filepath.Join(target, ".git")); err == nil {
		t.Fatalf("bubble was unexpectedly cloned despite list error")
	}

	// pass 2: lister recovers — bubble must clone now.
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})
	s.syncBubbles(context.Background())

	assert.DirExists(t, filepath.Join(target, ".git"),
		"bubble must clone on the recovery pass — no sticky failure state")
}

// TestSyncBubbles_Backoff_RateLimitedThenRecovers verifies that the
// kb pipeline treats a real HTTP 429 (Too Many Requests) and 503
// (Service Unavailable) the same as any other generic error —
// non-fatal, retried on the next pass. We use a real HTTP test server
// with the daemon's actual api.KBClient (rather than a fake lister)
// so this exercises the ListBubbles → HTTP → parse code path that
// production hits.
//
// Failure prevented: a regression that adds a special branch for
// 429/503 returning ErrKBAPIUnavailable (which is supposed to mean
// "feature flag off" — completely different semantics) and silently
// suppressing the warning.
func TestSyncBubbles_Backoff_RateLimitedThenRecovers(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git + HTTP test server")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	bareDir := makeBareRepo(t, "rate", "r.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_rate",
		KBType:  api.KBTypeTeam,
		Slug:    "rate",
		Name:    "rate-limited",
		RepoURL: "file://" + bareDir,
	}

	// rotating handler: first call 429, second 503, third returns the
	// real bubble.
	var callIdx atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/kb") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		switch callIdx.Add(1) {
		case 1:
			http.Error(w, "rate limited", http.StatusTooManyRequests)
		case 2:
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		default:
			w.Header().Set("Content-Type", "application/json")
			body := fmt.Sprintf(`{"bubbles":[{"kb_id":%q,"kb_type":"team","slug":"rate","name":"rate-limited","repo_url":%q}]}`,
				bubble.KBID, bubble.RepoURL)
			_, _ = w.Write([]byte(body))
		}
	}))
	t.Cleanup(srv.Close)

	s, _ := kbTestScheduler(t)
	// real client — exercises the HTTP error path, not just a fake error.
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return api.NewKBClientWithEndpoint(srv.URL).WithAuthToken("test-token")
	})

	// passes 1 & 2: HTTP 429 and 503. Both must be non-fatal — no
	// panic, no clone yet.
	for range 2 {
		assert.NotPanics(t, func() {
			s.syncBubbles(context.Background())
		})
	}
	target := paths.KBDir(bubble.KBID)
	if _, err := os.Stat(filepath.Join(target, ".git")); err == nil {
		t.Fatal("bubble cloned despite back-to-back rate-limit responses")
	}

	// pass 3: API recovers, bubble clones.
	s.syncBubbles(context.Background())
	assert.DirExists(t, filepath.Join(target, ".git"),
		"bubble must clone on first successful list response after rate-limit")

	assert.GreaterOrEqual(t, callIdx.Load(), int32(3),
		"the daemon must have retried the kb list endpoint each pass (no client-side backoff)")
}

// TestSyncBubbles_Backoff_FailedBubbleSucceedsOnLaterPass verifies
// the *temporal* failure-isolation invariant: a bubble whose clone
// fails on pass N (e.g., temporary file:// path didn't exist yet)
// must succeed when its preconditions are met on pass N+1, without
// the daemon stamping it as "permanently broken".
//
// Failure prevented: a per-bubble in-memory "tried this kb_id once
// and it failed, never trying again" cache that would prevent
// recovery from any transient clone-side error.
func TestSyncBubbles_Backoff_FailedBubbleSucceedsOnLaterPass(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	s, _ := kbTestScheduler(t)

	// the bubble's RepoURL points at a path that doesn't exist yet.
	// pass 1 will fail to clone.
	tmp := t.TempDir()
	bareLater := filepath.Join(tmp, "later.bare")
	bubble := api.KB{
		KBID:    "kb_later",
		KBType:  api.KBTypeTeam,
		Slug:    "later",
		RepoURL: "file://" + bareLater, // doesn't exist on pass 1
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	// pass 1: clone fails.
	s.syncBubbles(context.Background())
	target := paths.KBDir(bubble.KBID)
	if _, err := os.Stat(filepath.Join(target, ".git")); err == nil {
		t.Fatal("expected clone to fail when the bare repo does not exist")
	}

	// now create the bare repo so pass 2 can succeed.
	require.NoError(t, exec.Command("git", "init", "--bare", "-b", "main", bareLater).Run())
	// seed it with a commit via a throwaway clone.
	seed := filepath.Join(tmp, "seed")
	require.NoError(t, exec.Command("git", "clone", bareLater, seed).Run())
	gitConfig(t, seed)
	require.NoError(t, os.WriteFile(filepath.Join(seed, "now.md"), []byte("alive\n"), 0o644))
	require.NoError(t, exec.Command("git", "-C", seed, "add", "now.md").Run())
	require.NoError(t, exec.Command("git", "-C", seed, "commit", "-m", "seed").Run())
	require.NoError(t, exec.Command("git", "-C", seed, "push", "origin", "HEAD:main").Run())

	// pass 2: clone must succeed.
	s.syncBubbles(context.Background())
	assert.DirExists(t, filepath.Join(target, ".git"),
		"bubble must clone on the next pass once the underlying remote is reachable")
}

// TestSyncBubbles_Backoff_UnauthorizedSurfacedDistinctly verifies that
// HTTP 401 (auth expired / token rotated) reaches the daemon as a
// distinct, log-worthy error, NOT as ErrKBAPIUnavailable. The kb API
// client treats 401 as ErrUnauthorized so the daemon can trigger a
// credential refresh on the next heartbeat.
//
// Failure prevented: silently masking 401 as "feature flag off" and
// blocking the user from ever discovering their token expired.
func TestSyncBubbles_Backoff_UnauthorizedSurfacedDistinctly(t *testing.T) {
	if testing.Short() {
		t.Skip("short: HTTP test server")
	}
	kbTestEnv(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "auth required", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	s, _ := kbTestScheduler(t)
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return api.NewKBClientWithEndpoint(srv.URL).WithAuthToken("expired-token")
	})

	// must not panic, must not crash the loop.
	assert.NotPanics(t, func() {
		s.syncBubbles(context.Background())
	})

	// no kb dirs should have been created — the list call failed
	// before the per-bubble loop ran.
	root := paths.KBDir("")
	if entries, err := os.ReadDir(root); err == nil {
		assert.Empty(t, entries, "401 must not produce any on-disk side effects")
	}

	// confirm at the api layer that 401 routes to ErrUnauthorized
	// rather than the kb-availability sentinel — proves the daemon is
	// not silently swallowing an auth-failure as "flag off".
	client := api.NewKBClientWithEndpoint(srv.URL).WithAuthToken("expired-token")
	_, err := client.ListBubbles(context.Background())
	require.Error(t, err)
	assert.False(t, errors.Is(err, api.ErrKBAPIUnavailable),
		"401 must NOT collapse to ErrKBAPIUnavailable — that's reserved for 403/404")
}
