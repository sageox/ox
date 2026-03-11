package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestListPullRequests(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	prs := []PullRequest{
		{Number: 10, Title: "PR ten", UpdatedAt: now},
		{Number: 9, Title: "PR nine", UpdatedAt: now.Add(-time.Hour)},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("unexpected Accept header: %s", r.Header.Get("Accept"))
		}

		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Reset", "1700000000")

		json.NewEncoder(w).Encode(prs)
	}))
	defer srv.Close()

	client := NewClient("test-token")
	client.baseURL = srv.URL

	got, rl, err := client.ListPullRequests(context.Background(), "owner", "repo", ListPRsOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d PRs, want 2", len(got))
	}
	if got[0].Number != 10 {
		t.Errorf("first PR number = %d, want 10", got[0].Number)
	}
	if rl == nil {
		t.Fatal("expected rate limit, got nil")
	}
	if rl.Remaining != 4999 {
		t.Errorf("remaining = %d, want 4999", rl.Remaining)
	}
	if rl.Limit != 5000 {
		t.Errorf("limit = %d, want 5000", rl.Limit)
	}
}

func TestListPullRequestsSinceCutoff(t *testing.T) {
	cutoff := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)

	// page 1: two PRs, one before cutoff
	page1 := []PullRequest{
		{Number: 5, Title: "recent", UpdatedAt: cutoff.Add(time.Hour)},
		{Number: 4, Title: "old", UpdatedAt: cutoff.Add(-time.Hour)},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(page1)
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL

	got, _, err := client.ListPullRequests(context.Background(), "o", "r", ListPRsOptions{
		Since: cutoff,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// should only include PR #5 (updated after cutoff)
	if len(got) != 1 {
		t.Fatalf("got %d PRs, want 1", len(got))
	}
	if got[0].Number != 5 {
		t.Errorf("PR number = %d, want 5", got[0].Number)
	}
}

func TestListPRComments(t *testing.T) {
	comments := []Comment{
		{ID: 1, Body: "looks good", Path: "main.go", User: GitHubUser{Login: "reviewer"}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(comments)
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL

	got, err := client.ListPRComments(context.Background(), "o", "r", 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d comments, want 1", len(got))
	}
	if got[0].Path != "main.go" {
		t.Errorf("path = %q, want %q", got[0].Path, "main.go")
	}
}

func TestListIssueComments(t *testing.T) {
	comments := []Comment{
		{ID: 100, Body: "discussion comment", User: GitHubUser{Login: "author"}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(comments)
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL

	got, err := client.ListIssueComments(context.Background(), "o", "r", 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d comments, want 1", len(got))
	}
	if got[0].ID != 100 {
		t.Errorf("id = %d, want 100", got[0].ID)
	}
}

func TestDoRequestHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"forbidden"}`))
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL

	var result []PullRequest
	_, err := client.doRequest(context.Background(), "GET", "/repos/o/r/pulls", &result)
	if err == nil {
		t.Fatal("expected error for 403 response")
	}
}

func TestDoRequestContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	var result []PullRequest
	_, err := client.doRequest(ctx, "GET", "/repos/o/r/pulls", &result)
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}

func TestApplyDefaults(t *testing.T) {
	opts := applyDefaults(ListPRsOptions{})
	if opts.State != "all" {
		t.Errorf("state = %q, want %q", opts.State, "all")
	}
	if opts.Sort != "updated" {
		t.Errorf("sort = %q, want %q", opts.Sort, "updated")
	}
	if opts.Direction != "desc" {
		t.Errorf("direction = %q, want %q", opts.Direction, "desc")
	}
	if opts.PerPage != 100 {
		t.Errorf("per_page = %d, want 100", opts.PerPage)
	}
	if opts.Page != 1 {
		t.Errorf("page = %d, want 1", opts.Page)
	}

	// explicit values preserved
	custom := applyDefaults(ListPRsOptions{State: "open", PerPage: 30, Page: 2})
	if custom.State != "open" {
		t.Errorf("state = %q, want %q", custom.State, "open")
	}
	if custom.PerPage != 30 {
		t.Errorf("per_page = %d, want 30", custom.PerPage)
	}

	// PerPage clamped to 100
	clamped := applyDefaults(ListPRsOptions{PerPage: 200})
	if clamped.PerPage != 100 {
		t.Errorf("per_page = %d, want 100 (clamped)", clamped.PerPage)
	}
}

func TestParseRateLimit(t *testing.T) {
	h := http.Header{}
	h.Set("X-RateLimit-Remaining", "42")
	h.Set("X-RateLimit-Limit", "5000")
	h.Set("X-RateLimit-Reset", "1700000000")

	rl := parseRateLimit(h)
	if rl == nil {
		t.Fatal("expected rate limit")
	}
	if rl.Remaining != 42 {
		t.Errorf("remaining = %d, want 42", rl.Remaining)
	}
	if rl.Limit != 5000 {
		t.Errorf("limit = %d, want 5000", rl.Limit)
	}
	expected := time.Unix(1700000000, 0)
	if !rl.Reset.Equal(expected) {
		t.Errorf("reset = %v, want %v", rl.Reset, expected)
	}

	// no headers => nil
	empty := parseRateLimit(http.Header{})
	if empty != nil {
		t.Errorf("expected nil for empty headers, got %+v", empty)
	}
}
