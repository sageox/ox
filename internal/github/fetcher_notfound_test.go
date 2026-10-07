package github

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/ledger"
)

// Failure prevented: a merged PR whose head branch was deleted answers 404 on
// the commits endpoint, and the backfill pass asked again every sync cycle
// (37 identical warnings in one day).
func TestBackfillPRCommits_DoesNotRefetchPRThatReturned404(t *testing.T) {
	ledger.ResetPRCommitsNotFound()
	t.Cleanup(ledger.ResetPRCommitsNotFound)

	var deadRequests, liveRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/700/commits"):
			deadRequests.Add(1)
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/pulls/701/commits"):
			liveRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"sha":"abc123","commit":{"message":"m","author":{"name":"a","date":"2026-01-01T00:00:00Z"}}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := NewClient("test-token")
	client.baseURL = srv.URL
	fetcher := NewFetcher(client)

	ledgerPath := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	for _, number := range []int{700, 701} {
		pr := &ledger.PRFile{Number: number, Title: "merged", State: "merged", Author: "a", CreatedAt: now, UpdatedAt: now, MergedAt: &now}
		if err := ledger.WriteGitHubPR(ledgerPath, pr); err != nil {
			t.Fatalf("write PR %d: %v", number, err)
		}
	}

	for cycle := 1; cycle <= 3; cycle++ {
		if _, err := ledger.BackfillPRCommits(context.Background(), fetcher, ledgerPath, "notfound-org", "notfound-repo", slog.Default()); err != nil {
			t.Fatalf("cycle %d: %v", cycle, err)
		}
	}

	if got := deadRequests.Load(); got != 1 {
		t.Errorf("404 PR requested %d times across 3 cycles, want 1", got)
	}
	// negative control: a PR that succeeds is enriched on the first cycle and
	// never asked again, so the skip is not simply "stop calling the API".
	if got := liveRequests.Load(); got != 1 {
		t.Errorf("healthy PR requested %d times across 3 cycles, want 1", got)
	}
}
