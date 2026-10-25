package github

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/ledger"
)

// TestSyncPRs_KeepsDraftAndClosedAt drives a PR listing from a fake GitHub API
// through the real Fetcher and ledger.SyncPRs, and reads the snapshot back.
//
// Failure prevented: the fetcher dropped `draft` and `closed_at`, so the Ledger
// recorded a draft PR as ready for review and a closed-unmerged PR with no
// close time — the reader and plans saw the wrong PR state.
func TestSyncPRs_KeepsDraftAndClosedAt(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	created := now.Add(-72 * time.Hour)
	closed := now.Add(-2 * time.Hour)
	prs := []PullRequest{
		{Number: 10, Title: "WIP: new parser", State: "open", Draft: true, User: GitHubUser{Login: "eve"}, CreatedAt: created, UpdatedAt: now},
		{Number: 11, Title: "Abandoned approach", State: "closed", ClosedAt: &closed, User: GitHubUser{Login: "eve"}, CreatedAt: created, UpdatedAt: closed},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/pulls") {
			_ = json.NewEncoder(w).Encode(prs)
			return
		}
		_, _ = w.Write([]byte("[]")) // comments, commits
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL
	ledgerPath := t.TempDir()

	if _, err := ledger.SyncPRs(context.Background(), NewFetcher(client), ledgerPath, "o", "r", 30, slog.Default()); err != nil {
		t.Fatal(err)
	}

	draft, err := ledger.ReadGitHubPR(ledgerPath, 10, created)
	if err != nil {
		t.Fatal(err)
	}
	if !draft.Draft {
		t.Errorf("PR 10 is a draft on GitHub but the Ledger snapshot has draft=false")
	}

	abandoned, err := ledger.ReadGitHubPR(ledgerPath, 11, created)
	if err != nil {
		t.Fatal(err)
	}
	if abandoned.State != "closed" {
		t.Errorf("PR 11 state = %q, want closed", abandoned.State)
	}
	if abandoned.ClosedAt == nil || !abandoned.ClosedAt.Equal(closed) {
		t.Errorf("PR 11 closed_at = %v, want %v", abandoned.ClosedAt, closed)
	}
}
