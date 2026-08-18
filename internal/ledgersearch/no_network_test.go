package ledgersearch

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestSearch_NoNetworkCalls is the integration guarantee documented in ox-m01h:
// --source=local must perform zero network operations.
//
// Approach: install a counting dialer on http.DefaultTransport for the lifetime
// of the test. If anything reachable from Search() — directly or transitively —
// dials out via the default HTTP transport, the dialer increments a counter
// and fails the test. ledgersearch is pure-fs today; this guards against a
// future change that quietly introduces an HTTP dep without anyone noticing.
//
// Limitations: this hook does NOT catch raw net.Dial calls that bypass
// http.DefaultTransport (e.g., direct gRPC dialers). If a future change adds
// such a code path, augment this test rather than weakening the assertion.
func TestSearch_NoNetworkCalls(t *testing.T) {
	var attempts atomic.Int32

	origTransport := http.DefaultTransport
	cloned := origTransport.(*http.Transport).Clone()
	cloned.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		attempts.Add(1)
		t.Errorf("unexpected network dial during Search: %s %s", network, addr)
		return nil, errBlockedNetwork
	}
	http.DefaultTransport = cloned
	t.Cleanup(func() {
		http.DefaultTransport = origTransport
	})

	// Build a realistic ledger and ensure results come back via the pure-fs
	// path. If Search ever introduces network I/O, the dialer hook above
	// fires and fails the test.
	dir := t.TempDir()
	fixtureTime := time.Date(2026, 5, 20, 10, 15, 0, 0, time.UTC)
	sessionPath := filepath.Join(dir, "sessions", fixtureTime.Format("2006-01-02T15-04")+"-ryan-OxTest")
	if err := os.MkdirAll(sessionPath, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionPath, "summary.md"), []byte("the term widget matches"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Keep the clock near the fixed fixture timestamp. This test covers the
	// zero-network path, not the 90-day session-retention boundary.
	results, err := Search(Options{
		LedgerPath: dir,
		Query:      "widget",
		Now:        fixtureTime.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Search returned err (should be fail-open): %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected hit")
	}
	if n := attempts.Load(); n > 0 {
		t.Fatalf("Search attempted %d network dials via http.DefaultTransport; must be zero", n)
	}
}

var errBlockedNetwork = errors.New("dial blocked by no-network test")
