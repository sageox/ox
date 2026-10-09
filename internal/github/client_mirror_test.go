package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- shared fixtures for the mirror client and adapter tests ---

// pagedList serves items the way GitHub's list endpoints do: it honors page and
// per_page (default 30, like GitHub) and records every page requested. A
// client that forgets per_page=100 therefore sees short pages and stops early,
// which the length assertions catch.
type pagedList struct {
	items []json.RawMessage

	mu    sync.Mutex
	pages []int
}

func (p *pagedList) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	page := queryInt(r, "page", 1)
	perPage := queryInt(r, "per_page", 30)

	p.mu.Lock()
	p.pages = append(p.pages, page)
	p.mu.Unlock()

	start := min((page-1)*perPage, len(p.items))
	end := min(start+perPage, len(p.items))
	// copy so an empty page encodes as [] rather than null
	out := append([]json.RawMessage{}, p.items[start:end]...)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (p *pagedList) requestedPages() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int(nil), p.pages...)
}

func queryInt(r *http.Request, key string, fallback int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get(key)); err == nil && v > 0 {
		return v
	}
	return fallback
}

func rawList(n int, item func(i int) string) []json.RawMessage {
	items := make([]json.RawMessage, n)
	for i := range items {
		items[i] = json.RawMessage(item(i))
	}
	return items
}

func fileName(i int) string { return fmt.Sprintf("pkg/file-%03d.go", i) }

func fileItems(n int) []json.RawMessage {
	return rawList(n, func(i int) string {
		return fmt.Sprintf(`{"filename":%q,"status":"modified","additions":1}`, fileName(i))
	})
}

func fileNames(n int) []string {
	names := make([]string, n)
	for i := range names {
		names[i] = fileName(i)
	}
	return names
}

// pathSpan summarizes a path list for failure messages without dumping hundreds of entries.
func pathSpan(paths []string) string {
	if len(paths) == 0 {
		return "empty"
	}
	return paths[0] + " .. " + paths[len(paths)-1]
}

func newMirrorTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewClient("test-token")
	c.baseURL = srv.URL
	return c
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return ts
}

// --- GetRepo ---

// Failure prevented: a private repo is relayed with private=false and the
// server's private-repo opt-in is never consulted.
func TestGetRepo_ReportsPrivateFlag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want RepoInfo
	}{
		{
			name: "private repo",
			body: `{"id":98765,"name":"api","full_name":"Acme/api","private":true,"owner":{"login":"Acme","id":42,"type":"Organization"}}`,
			want: RepoInfo{ID: 98765, Name: "api", FullName: "Acme/api", Private: true, Owner: GitHubUser{Login: "Acme", ID: 42, Type: "Organization"}},
		},
		{
			name: "public repo",
			body: `{"id":98766,"name":"docs","full_name":"Acme/docs","private":false,"owner":{"login":"Acme","id":42,"type":"Organization"}}`,
			want: RepoInfo{ID: 98766, Name: "docs", FullName: "Acme/docs", Private: false, Owner: GitHubUser{Login: "Acme", ID: 42, Type: "Organization"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var gotPath atomic.Value
			client := newMirrorTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath.Store(r.URL.Path)
				_, _ = w.Write([]byte(tt.body))
			}))

			got, err := client.GetRepo(context.Background(), "Acme", "api")
			if err != nil {
				t.Fatalf("GetRepo: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetRepo = %+v, want %+v", got, tt.want)
			}
			if p, _ := gotPath.Load().(string); p != "/repos/Acme/api" {
				t.Errorf("request path = %q, want /repos/Acme/api", p)
			}
		})
	}
}

// --- ListPRReviews ---

// Failure prevented: a PR with more than one page of reviews silently loses the
// later reviews, so a recorded approval or change request never reaches the post.
func TestListPRReviews_Paginates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		total     int
		wantPages []int
	}{
		{"no reviews", 0, []int{1}},
		{"one short page", 3, []int{1}},
		{"two pages", 103, []int{1, 2}},
		{"exactly one full page needs a second request to confirm the end", 100, []int{1, 2}},
		{"three pages", 250, []int{1, 2, 3}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			list := &pagedList{items: rawList(tt.total, func(i int) string {
				return fmt.Sprintf(`{"id":%d,"user":{"login":"reviewer","id":7,"type":"User"},"state":"APPROVED","submitted_at":"2026-10-01T10:00:00Z","author_association":"MEMBER"}`, i+1)
			})}
			client := newMirrorTestClient(t, list)

			got, err := client.ListPRReviews(context.Background(), "o", "r", 5)
			if err != nil {
				t.Fatalf("ListPRReviews: %v", err)
			}
			if len(got) != tt.total {
				t.Fatalf("got %d reviews, want %d", len(got), tt.total)
			}
			for i, r := range got {
				if r.ID != int64(i+1) {
					t.Fatalf("review %d has id %d, want %d (order must be preserved)", i, r.ID, i+1)
				}
			}
			if pages := list.requestedPages(); !reflect.DeepEqual(pages, tt.wantPages) {
				t.Errorf("requested pages = %v, want %v", pages, tt.wantPages)
			}
		})
	}
}

// --- ListPRFiles ---

// Failure prevented: a large PR costs dozens of calls per sync cycle, or the
// post claims "files truncated" for a PR that fit exactly (or hides that it did
// not fit).
func TestListPRFiles_CapAndTruncation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		totalFiles int
		limit      int
		wantLen    int
		wantTrunc  bool
		wantPages  []int
	}{
		{name: "no files", totalFiles: 0, limit: 50, wantLen: 0, wantTrunc: false, wantPages: []int{1}},
		{name: "fewer files than the limit", totalFiles: 3, limit: 50, wantLen: 3, wantTrunc: false, wantPages: []int{1}},
		{name: "exactly at the limit is not truncated", totalFiles: 50, limit: 50, wantLen: 50, wantTrunc: false, wantPages: []int{1}},
		{name: "one over the limit is truncated", totalFiles: 51, limit: 50, wantLen: 50, wantTrunc: true, wantPages: []int{1}},
		{name: "far over the limit stops after the first page", totalFiles: 250, limit: 50, wantLen: 50, wantTrunc: true, wantPages: []int{1}},
		{name: "exactly at the limit on a full page checks the next page", totalFiles: 100, limit: 100, wantLen: 100, wantTrunc: false, wantPages: []int{1, 2}},
		{name: "one past a full page", totalFiles: 101, limit: 100, wantLen: 100, wantTrunc: true, wantPages: []int{1, 2}},
		{name: "limit spans pages and stops once exceeded", totalFiles: 250, limit: 150, wantLen: 150, wantTrunc: true, wantPages: []int{1, 2}},
		{name: "limit of one", totalFiles: 5, limit: 1, wantLen: 1, wantTrunc: true, wantPages: []int{1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			list := &pagedList{items: fileItems(tt.totalFiles)}
			client := newMirrorTestClient(t, list)

			got, truncated, err := client.ListPRFiles(context.Background(), "o", "r", 9, tt.limit)
			if err != nil {
				t.Fatalf("ListPRFiles: %v", err)
			}
			if truncated != tt.wantTrunc {
				t.Errorf("truncated = %v, want %v", truncated, tt.wantTrunc)
			}
			if want := fileNames(tt.wantLen); !reflect.DeepEqual(got, want) {
				t.Errorf("got %d paths (%s), want the first %d in order", len(got), pathSpan(got), tt.wantLen)
			}
			if pages := list.requestedPages(); !reflect.DeepEqual(pages, tt.wantPages) {
				t.Errorf("requested pages = %v, want %v", pages, tt.wantPages)
			}
		})
	}
}

// Failure prevented: a nonsense limit silently returns an empty, "complete"
// file list instead of failing loudly.
func TestListPRFiles_RejectsNonPositiveLimit(t *testing.T) {
	t.Parallel()

	for _, limit := range []int{0, -1} {
		t.Run(fmt.Sprintf("limit %d", limit), func(t *testing.T) {
			t.Parallel()
			var requests atomic.Int32
			client := newMirrorTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				_, _ = w.Write([]byte(`[]`))
			}))

			paths, truncated, err := client.ListPRFiles(context.Background(), "o", "r", 9, limit)
			if err == nil {
				t.Fatalf("ListPRFiles(limit=%d) succeeded with %v, truncated=%v; want an error", limit, paths, truncated)
			}
			if n := requests.Load(); n != 0 {
				t.Errorf("made %d requests for an invalid limit, want 0", n)
			}
		})
	}
}

// Failure prevented: a failure on a later page reads as "that PR has N files,
// none truncated" and the relay publishes a short file list as if complete.
func TestListPRFiles_LaterPageFailureIsAnError(t *testing.T) {
	t.Parallel()

	list := &pagedList{items: fileItems(150)}
	client := newMirrorTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if queryInt(r, "page", 1) >= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"boom"}`))
			return
		}
		list.ServeHTTP(w, r)
	}))

	_, truncated, err := client.ListPRFiles(context.Background(), "o", "r", 9, 150)
	if err == nil {
		t.Fatal("ListPRFiles succeeded despite a failing second page")
	}
	if truncated {
		t.Error("truncated = true on an error; want false so callers cannot mistake it for a cap")
	}
	if errors.Is(err, ErrGitHubAuth) || errors.Is(err, ErrGitHubRateLimited) {
		t.Errorf("a 500 must not map to auth or rate-limit sentinels: %v", err)
	}
}
