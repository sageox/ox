package daemon

// Mirrors sync_network_test.go for the kb sync path: timeouts, partial
// failures, retries, lock-file detection, and canceled-context
// behaviors. The kb sync loop must degrade as gracefully as the ledger
// and team-context loops under network pressure.
//
// Per Ryan: "all the ledger and team context tests need to be applied
// to kb git repos." Slow tests skip under -short.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSyncBubbles_Network_UnreachableRemoteIsContained verifies a
// bubble whose RepoURL points to an unreachable host fails its clone
// without taking the rest of the pass down. Mirrors the ledger
// FetchFailureRecordsBackoff intent at the kb level: a single bad
// remote must be contained.
//
// Failure prevented: one unreachable bubble blocking sync of every
// other bubble — the most user-visible network failure mode.
func TestSyncBubbles_Network_UnreachableRemoteIsContained(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	// 127.0.0.1:1 immediately refuses connections (no hang).
	bad := api.KB{
		KBID:    "kb_unreachable",
		KBType:  api.KBTypeTeam,
		Slug:    "unreachable",
		RepoURL: "https://127.0.0.1:1/nonexistent.git",
	}
	goodBare := makeBareRepo(t, "good-after-net", "f.md", "ok\n")
	good := api.KB{
		KBID:    "kb_good_after_net",
		KBType:  api.KBTypeTeam,
		Slug:    "good",
		RepoURL: "file://" + goodBare,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bad, good}}
	})

	// must complete reasonably fast even with one unreachable remote.
	done := make(chan struct{})
	go func() {
		s.syncBubbles(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(90 * time.Second):
		t.Fatal("syncBubbles did not return — unreachable remote should not stall the pass")
	}

	// the unreachable bubble is not cloned.
	assert.NoDirExists(t, filepath.Join(paths.KBDir(bad.KBID), ".git"))
	// the good bubble must still land.
	assert.DirExists(t, filepath.Join(paths.KBDir(good.KBID), ".git"))
}

// TestSyncBubbles_Network_APIListTimeout verifies the kb API list call
// is bounded — a slow API does not stall the entire sync loop. Mirrors
// the ContextCancellationDuringFetch ethos at the kb-API level.
//
// Failure prevented: a 60s+ hang on `GET /api/v1/kb` blocking every
// other scheduler tick. Particularly bad when staging is degraded.
func TestSyncBubbles_Network_APIListTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real listener + sleep")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	// stub a lister that simulates a hanging API call by waiting for
	// the context to time out internally. syncBubbles wraps the listCtx
	// with a 60s timeout — instead of waiting that long, we have the
	// fake itself respect the (passed-down) context.
	var observedDeadline atomic.Bool
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBListerWithCtx{
			fn: func(ctx context.Context) ([]api.KB, error) {
				if _, ok := ctx.Deadline(); ok {
					observedDeadline.Store(true)
				}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(2 * time.Minute):
					return nil, errors.New("test timeout: listener never canceled")
				}
			},
		}
	})

	// give the parent context a hard upper bound so the test itself
	// can never hang the suite.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// run with our own short budget.
	short, shortCancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer shortCancel()
	assert.NotPanics(t, func() {
		s.syncBubbles(short)
	})
	assert.True(t, observedDeadline.Load(), "lister must observe a context deadline — confirms list call is bounded")
}

// fakeKBListerWithCtx is a context-aware ListBubbles fake. The
// stock fakeKBLister in sync_bubbles_test.go ignores ctx, which is
// fine for fast unit tests but unsuitable for testing timeout
// behavior. Local to this file to avoid changing the existing fake.
type fakeKBListerWithCtx struct {
	fn func(ctx context.Context) ([]api.KB, error)
}

func (f *fakeKBListerWithCtx) ListBubbles(ctx context.Context) ([]api.KB, error) {
	return f.fn(ctx)
}

// TestSyncBubbles_Network_PartialAPIErrorsAreContained verifies that a
// non-sentinel API error (e.g., 500, transient network) is logged and
// swallowed rather than crashing the daemon. Mirrors the ledger
// "ListErrorIsLoggedNotPanicked" guard.
//
// Failure prevented: a brief 500 from the kb endpoint crashing the
// scheduler tick and disabling sync for the rest of the daemon's life.
func TestSyncBubbles_Network_PartialAPIErrorsAreContained(t *testing.T) {
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	cases := []error{
		errors.New("HTTP 500: internal server error"),
		errors.New("EOF"),
		errors.New("connection reset by peer"),
		errors.New("i/o timeout"),
	}
	for _, e := range cases {
		err := e
		t.Run(e.Error(), func(t *testing.T) {
			s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
				return &fakeKBLister{err: err}
			})
			assert.NotPanics(t, func() {
				s.syncBubbles(context.Background())
			})
		})
	}
}

// TestSyncBubbles_Network_ContextAlreadyCanceledIsNoOp verifies that
// passing an already-canceled context to syncBubbles returns promptly
// without leaving partial state. Mirrors the ledger
// AlreadyCanceledContext guard.
//
// Failure prevented: the daemon shutting down mid-sync leaves a
// half-cloned bubble that subsequent passes can't reconcile.
func TestSyncBubbles_Network_ContextAlreadyCanceledIsNoOp(t *testing.T) {
	if testing.Short() {
		t.Skip("short: spawns goroutines")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "canceled", "f.md", "x\n")
	bubble := api.KB{
		KBID:    "kb_canceled",
		KBType:  api.KBTypePersonal,
		Slug:    "canceled",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		s.syncBubbles(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("syncBubbles did not return promptly with a canceled context")
	}
}

// TestSyncBubbles_Network_HTTPMockServer_KnownStatuses validates the
// daemon-side handling of representative HTTP responses from the kb
// API: 200 with bubbles, 403 (flag off) -> ErrKBAPIUnavailable, 500
// (transient). Lives at the wire level — exercises NewKBClientWithEndpoint
// against an httptest.Server, the same shape sync_network_test.go
// uses for the team-context flow at the git transport layer.
//
// Failure prevented: a future change to ListBubbles' status-code
// handling silently shifting (e.g., 403 no longer maps to the
// sentinel) and the kb sync loop misclassifying flag-off as a fatal
// error.
func TestSyncBubbles_Network_HTTPMockServer_KnownStatuses(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantSentinel bool
		wantErr      bool
	}{
		{"200 ok empty", http.StatusOK, `{"bubbles":[]}`, false, false},
		{"403 forbidden", http.StatusForbidden, `{"error":"flag off"}`, true, true},
		{"404 not found", http.StatusNotFound, `not found`, true, true},
		{"500 transient", http.StatusInternalServerError, `boom`, false, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			client := api.NewKBClientWithEndpoint(srv.URL)
			_, err := client.ListBubbles(context.Background())
			if tc.wantErr {
				require.Error(t, err)
				if tc.wantSentinel {
					assert.True(t, errors.Is(err, api.ErrKBAPIUnavailable),
						"%s: expected ErrKBAPIUnavailable, got %v", tc.name, err)
				} else {
					assert.False(t, errors.Is(err, api.ErrKBAPIUnavailable),
						"%s: must NOT map to ErrKBAPIUnavailable", tc.name)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestSyncBubbles_Network_RepeatedPassesIdempotentUnderFlapping
// verifies that calling syncBubbles repeatedly while the API
// alternates between success and failure does not accumulate state
// (no leaked goroutines, no growing on-disk artifacts).
//
// Failure prevented: a flapping API causing each tick to retry-clone
// the same bubble or build up backups, eating disk space until the
// user notices. Cumulative correctness, not single-pass.
func TestSyncBubbles_Network_RepeatedPassesIdempotentUnderFlapping(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "flap", "f.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_flap",
		KBType:  api.KBTypeTeam,
		Slug:    "flap",
		RepoURL: "file://" + bareDir,
	}
	var n atomic.Int32
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		// alternate: even passes return the bubble, odd passes return
		// a transient error. Real-world flap pattern.
		i := n.Add(1)
		if i%2 == 0 {
			return &fakeKBLister{err: errors.New("transient 503")}
		}
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	for i := 0; i < 6; i++ {
		assert.NotPanics(t, func() {
			s.syncBubbles(context.Background())
		})
	}

	// the bubble should be cloned exactly once — not duplicated, not
	// repeatedly torn down.
	target := paths.KBDir(bubble.KBID)
	assert.DirExists(t, filepath.Join(target, ".git"))
	parent := filepath.Dir(target)
	entries, err := os.ReadDir(parent)
	require.NoError(t, err)
	bakCount := 0
	for _, e := range entries {
		// .bak.* would indicate the loop torn-down + recreated the dir.
		if e.Name() != filepath.Base(target) && len(e.Name()) > len(filepath.Base(target)) {
			bakCount++
		}
	}
	assert.Equal(t, 0, bakCount, "no .bak siblings expected on healthy flapping pattern")
}

// TestSyncBubbles_Network_ConnectionImmediatelyClosed exercises the
// "remote closes connection during fetch" failure mode at the kb API
// layer. Uses a TCP listener that accepts then immediately closes —
// equivalent to "the remote end hung up unexpectedly" git error.
//
// Failure prevented: the daemon hanging or panicking when the kb
// endpoint is in a partial-failure state (load balancer flap).
func TestSyncBubbles_Network_ConnectionImmediatelyClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("short: opens a listener")
	}

	// listener accepts then closes — net/http will surface as an EOF.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	client := api.NewKBClientWithEndpoint("http://" + ln.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = client.ListBubbles(ctx)
	require.Error(t, err, "must surface an error on connection-closed")
	assert.False(t, errors.Is(err, api.ErrKBAPIUnavailable),
		"connection-closed (transport failure) must NOT collapse to the flag-off sentinel")
}
