package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDoRequestErrorCodes(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		headers    map[string]string
		wantErr    error // sentinel to check with errors.Is
		wantErrMsg string
	}{
		{
			name:    "401 returns ErrGitHubAuth",
			status:  http.StatusUnauthorized,
			body:    `{"message":"Bad credentials"}`,
			wantErr: ErrGitHubAuth,
		},
		{
			name:    "429 returns ErrGitHubRateLimited",
			status:  http.StatusTooManyRequests,
			body:    `{"message":"rate limit exceeded"}`,
			wantErr: ErrGitHubRateLimited,
		},
		{
			name:   "403 with remaining=0 is rate limit, not auth error",
			status: http.StatusForbidden,
			body:   `{"message":"API rate limit exceeded"}`,
			headers: map[string]string{
				"X-RateLimit-Remaining": "0",
				"X-RateLimit-Limit":     "5000",
				"X-RateLimit-Reset":     "1700000000",
			},
			wantErr: ErrGitHubRateLimited,
		},
		{
			name:   "403 with Retry-After header is rate limit",
			status: http.StatusForbidden,
			body:   `{"message":"secondary rate limit"}`,
			headers: map[string]string{
				"Retry-After": "60",
			},
			wantErr: ErrGitHubRateLimited,
		},
		{
			name:    "403 without rate limit indicators is auth error",
			status:  http.StatusForbidden,
			body:    `{"message":"forbidden"}`,
			wantErr: ErrGitHubAuth,
		},
		{
			name:       "500 returns generic error with status and body",
			status:     http.StatusInternalServerError,
			body:       `{"message":"internal error"}`,
			wantErrMsg: "github api status 500",
		},
		{
			name:       "404 returns generic error",
			status:     http.StatusNotFound,
			body:       `{"message":"Not Found"}`,
			wantErrMsg: "github api status 404",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tt.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tt.status)
				w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			client := NewClient("tok")
			client.baseURL = srv.URL

			var result []PullRequest
			_, err := client.doRequest(context.Background(), "GET", "/test", &result)
			if err == nil {
				t.Fatal("expected error")
			}

			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("errors.Is(%v, %v) = false", err, tt.wantErr)
			}
			if tt.wantErrMsg != "" {
				if got := err.Error(); !contains(got, tt.wantErrMsg) {
					t.Errorf("error = %q, want substring %q", got, tt.wantErrMsg)
				}
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestDoRequestInvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not json at all`))
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL

	var result []PullRequest
	_, err := client.doRequest(context.Background(), "GET", "/test", &result)
	if err == nil {
		t.Fatal("expected decode error for invalid JSON")
	}
	if !contains(err.Error(), "decode github response") {
		t.Errorf("error = %q, want 'decode github response' substring", err.Error())
	}
}

func TestDoRequestRateLimitReturnedOnError(t *testing.T) {
	// 429 should still return the parsed rate limit info
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Reset", "1700000000")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL

	var result []PullRequest
	rl, err := client.doRequest(context.Background(), "GET", "/test", &result)
	if err == nil {
		t.Fatal("expected error")
	}
	if rl == nil {
		t.Fatal("rate limit should be returned even on 429")
	}
	if rl.Remaining != 0 {
		t.Errorf("remaining = %d, want 0", rl.Remaining)
	}
}

func TestParseRateLimitPartialHeaders(t *testing.T) {
	tests := []struct {
		name          string
		headers       map[string]string
		wantNil       bool
		wantRemaining int
		wantLimit     int
	}{
		{
			name:    "no headers at all",
			headers: map[string]string{},
			wantNil: true,
		},
		{
			name: "only remaining",
			headers: map[string]string{
				"X-RateLimit-Remaining": "42",
			},
			wantRemaining: 42,
			wantLimit:     0,
		},
		{
			name: "only limit",
			headers: map[string]string{
				"X-RateLimit-Limit": "5000",
			},
			wantRemaining: 0,
			wantLimit:     5000,
		},
		{
			name: "malformed remaining defaults to 0",
			headers: map[string]string{
				"X-RateLimit-Remaining": "not-a-number",
				"X-RateLimit-Limit":     "5000",
			},
			wantRemaining: 0,
			wantLimit:     5000,
		},
		{
			name: "malformed reset parsed without error",
			headers: map[string]string{
				"X-RateLimit-Remaining": "100",
				"X-RateLimit-Reset":     "invalid",
			},
			wantRemaining: 100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tt.headers {
				h.Set(k, v)
			}

			rl := parseRateLimit(h)
			if tt.wantNil {
				if rl != nil {
					t.Fatalf("expected nil, got %+v", rl)
				}
				return
			}
			if rl == nil {
				t.Fatal("expected non-nil rate limit")
			}
			if rl.Remaining != tt.wantRemaining {
				t.Errorf("remaining = %d, want %d", rl.Remaining, tt.wantRemaining)
			}
			if rl.Limit != tt.wantLimit {
				t.Errorf("limit = %d, want %d", rl.Limit, tt.wantLimit)
			}
		})
	}
}

func TestApplyIssueDefaults(t *testing.T) {
	tests := []struct {
		name string
		in   ListIssuesOptions
		want ListIssuesOptions
	}{
		{
			name: "all zeroes get defaults",
			in:   ListIssuesOptions{},
			want: ListIssuesOptions{
				State: "all", Sort: "updated", Direction: "desc",
				PerPage: 100, Page: 1,
			},
		},
		{
			name: "explicit values preserved",
			in:   ListIssuesOptions{State: "open", Sort: "created", Direction: "asc", PerPage: 50, Page: 3},
			want: ListIssuesOptions{State: "open", Sort: "created", Direction: "asc", PerPage: 50, Page: 3},
		},
		{
			name: "PerPage over 100 clamped",
			in:   ListIssuesOptions{PerPage: 200},
			want: ListIssuesOptions{
				State: "all", Sort: "updated", Direction: "desc",
				PerPage: 100, Page: 1,
			},
		},
		{
			name: "negative PerPage gets default",
			in:   ListIssuesOptions{PerPage: -1},
			want: ListIssuesOptions{
				State: "all", Sort: "updated", Direction: "desc",
				PerPage: 100, Page: 1,
			},
		},
		{
			name: "negative page gets default",
			in:   ListIssuesOptions{Page: -5},
			want: ListIssuesOptions{
				State: "all", Sort: "updated", Direction: "desc",
				PerPage: 100, Page: 1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applyIssueDefaults(tt.in)
			// compare relevant fields (Since is time.Time zero in both)
			if got.State != tt.want.State {
				t.Errorf("state = %q, want %q", got.State, tt.want.State)
			}
			if got.Sort != tt.want.Sort {
				t.Errorf("sort = %q, want %q", got.Sort, tt.want.Sort)
			}
			if got.Direction != tt.want.Direction {
				t.Errorf("direction = %q, want %q", got.Direction, tt.want.Direction)
			}
			if got.PerPage != tt.want.PerPage {
				t.Errorf("per_page = %d, want %d", got.PerPage, tt.want.PerPage)
			}
			if got.Page != tt.want.Page {
				t.Errorf("page = %d, want %d", got.Page, tt.want.Page)
			}
		})
	}
}

func TestListIssuesFiltersPRs(t *testing.T) {
	prMarker := &struct{}{}
	issues := []Issue{
		{Number: 1, Title: "real issue", UpdatedAt: time.Now()},
		{Number: 2, Title: "actually a PR", UpdatedAt: time.Now(), PullRequest: prMarker},
		{Number: 3, Title: "another issue", UpdatedAt: time.Now()},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(issues)
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL

	got, _, err := client.ListIssues(context.Background(), "o", "r", ListIssuesOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d issues, want 2 (PR should be filtered)", len(got))
	}
	for _, issue := range got {
		if issue.PullRequest != nil {
			t.Errorf("issue #%d should not be a PR", issue.Number)
		}
	}
}

func TestListIssuesSinceCutoff(t *testing.T) {
	cutoff := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)

	issues := []Issue{
		{Number: 10, Title: "recent", UpdatedAt: cutoff.Add(2 * time.Hour)},
		{Number: 9, Title: "also recent", UpdatedAt: cutoff.Add(time.Hour)},
		{Number: 8, Title: "old", UpdatedAt: cutoff.Add(-time.Hour)},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(issues)
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL

	got, _, err := client.ListIssues(context.Background(), "o", "r", ListIssuesOptions{
		Since: cutoff,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// #10 and #9 are after cutoff, #8 is before -> stops at #8
	if len(got) != 2 {
		t.Fatalf("got %d issues, want 2", len(got))
	}
	if got[0].Number != 10 || got[1].Number != 9 {
		t.Errorf("got issues %d, %d; want 10, 9", got[0].Number, got[1].Number)
	}
}

func TestListIssuesSinceCutoffFiltersPRs(t *testing.T) {
	// PRs should be filtered even when using Since
	cutoff := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	prMarker := &struct{}{}

	issues := []Issue{
		{Number: 10, Title: "issue", UpdatedAt: cutoff.Add(2 * time.Hour)},
		{Number: 9, Title: "a PR", UpdatedAt: cutoff.Add(time.Hour), PullRequest: prMarker},
		{Number: 8, Title: "old", UpdatedAt: cutoff.Add(-time.Hour)},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(issues)
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL

	got, _, err := client.ListIssues(context.Background(), "o", "r", ListIssuesOptions{Since: cutoff})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d issues, want 1 (PR filtered + old cutoff)", len(got))
	}
	if got[0].Number != 10 {
		t.Errorf("got issue #%d, want #10", got[0].Number)
	}
}

func TestListPRCommitsAuthorExtraction(t *testing.T) {
	// when author.login is present, use it over commit.author.name
	commits := []prCommitJSON{
		{
			SHA: "abc123",
			Commit: struct {
				Message string `json:"message"`
				Author  struct {
					Name string    `json:"name"`
					Date time.Time `json:"date"`
				} `json:"author"`
			}{
				Message: "feat: something",
				Author: struct {
					Name string    `json:"name"`
					Date time.Time `json:"date"`
				}{
					Name: "Full Name",
					Date: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
				},
			},
			Author: &struct {
				Login string `json:"login"`
			}{Login: "username"},
		},
		{
			// no GitHub author (e.g., unlinked committer) - falls back to commit author name
			SHA: "def456",
			Commit: struct {
				Message string `json:"message"`
				Author  struct {
					Name string    `json:"name"`
					Date time.Time `json:"date"`
				} `json:"author"`
			}{
				Message: "fix: other thing",
				Author: struct {
					Name string    `json:"name"`
					Date time.Time `json:"date"`
				}{
					Name: "Committer Name",
					Date: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
				},
			},
			Author: nil,
		},
		{
			// GitHub author present but login is empty - falls back to commit name
			SHA: "ghi789",
			Commit: struct {
				Message string `json:"message"`
				Author  struct {
					Name string    `json:"name"`
					Date time.Time `json:"date"`
				} `json:"author"`
			}{
				Message: "chore: cleanup",
				Author: struct {
					Name string    `json:"name"`
					Date time.Time `json:"date"`
				}{
					Name: "Another Name",
					Date: time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC),
				},
			},
			Author: &struct {
				Login string `json:"login"`
			}{Login: ""},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(commits)
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL

	got, err := client.ListPRCommits(context.Background(), "o", "r", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d commits, want 3", len(got))
	}

	wantAuthors := []string{"username", "Committer Name", "Another Name"}
	for i, want := range wantAuthors {
		if got[i].Author != want {
			t.Errorf("commit[%d].Author = %q, want %q", i, got[i].Author, want)
		}
	}

	if got[0].SHA != "abc123" {
		t.Errorf("commit[0].SHA = %q, want %q", got[0].SHA, "abc123")
	}
	if got[0].Msg != "feat: something" {
		t.Errorf("commit[0].Msg = %q, want %q", got[0].Msg, "feat: something")
	}
}

func TestListPRCommitsErrorPropagation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL

	_, err := client.ListPRCommits(context.Background(), "o", "r", 999)
	if err == nil {
		t.Fatal("expected error for 404")
	}
}

func TestListPullRequestsPagination(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	callCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch callCount {
		case 1:
			// return exactly PerPage items to trigger next page fetch
			prs := make([]PullRequest, 2)
			for i := range prs {
				prs[i] = PullRequest{Number: 100 - i, Title: "pr", UpdatedAt: now}
			}
			json.NewEncoder(w).Encode(prs)
		case 2:
			// second page: fewer results = last page
			json.NewEncoder(w).Encode([]PullRequest{
				{Number: 98, Title: "last", UpdatedAt: now},
			})
		default:
			t.Error("unexpected third page request")
			json.NewEncoder(w).Encode([]PullRequest{})
		}
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL

	got, _, err := client.ListPullRequests(context.Background(), "o", "r", ListPRsOptions{
		PerPage: 2, // small page size to test pagination
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d PRs, want 3 (2 from page 1, 1 from page 2)", len(got))
	}
	if callCount != 2 {
		t.Errorf("server called %d times, want 2", callCount)
	}
}

func TestListPullRequestsEmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]PullRequest{})
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL

	got, _, err := client.ListPullRequests(context.Background(), "o", "r", ListPRsOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d PRs, want 0", len(got))
	}
}

func TestListIssuesEmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]Issue{})
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL

	got, _, err := client.ListIssues(context.Background(), "o", "r", ListIssuesOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d issues, want 0", len(got))
	}
}

func TestListIssuesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"message":"server error"}`))
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL

	_, _, err := client.ListIssues(context.Background(), "o", "r", ListIssuesOptions{})
	if err == nil {
		t.Fatal("expected error for 500")
	}
}

func TestNewClient(t *testing.T) {
	c := NewClient("my-token")
	if c.token != "my-token" {
		t.Errorf("token = %q, want %q", c.token, "my-token")
	}
	if c.baseURL != "https://api.github.com" {
		t.Errorf("baseURL = %q, want %q", c.baseURL, "https://api.github.com")
	}
	if c.httpClient == nil {
		t.Error("httpClient should not be nil")
	}
	if c.httpClient.Timeout != 15*time.Second {
		t.Errorf("timeout = %v, want 15s", c.httpClient.Timeout)
	}
}
