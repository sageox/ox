package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/sageox/ox/internal/githubmirror"
	"github.com/sageox/ox/internal/githubmirror/mirrortest"
	"github.com/sageox/ox/internal/ledger"
)

// Fixtures below are raw GitHub-shaped JSON on purpose. Encoding this package's
// own structs would let a wrong json tag pass: the same typo would sit on both
// sides of the round trip.

func TestMirrorFetcher_RepoReportsPrivateFlag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want githubmirror.Repo
	}{
		{
			name: "private repo",
			body: `{"id":98765,"name":"api","full_name":"acme/api","private":true,"owner":{"login":"acme","id":42,"type":"Organization"}}`,
			want: githubmirror.Repo{Owner: "acme", Name: "api", FullName: "acme/api", ID: 98765, Private: true},
		},
		{
			name: "public repo",
			body: `{"id":98766,"name":"docs","full_name":"acme/docs","private":false,"owner":{"login":"acme","id":42,"type":"Organization"}}`,
			want: githubmirror.Repo{Owner: "acme", Name: "docs", FullName: "acme/docs", ID: 98766, Private: false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := NewMirrorFetcher(newMirrorTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			})))

			got, err := f.Repo(context.Background(), "acme", "api")
			if err != nil {
				t.Fatalf("Repo: %v", err)
			}
			if got != tt.want {
				t.Errorf("Repo = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// Failure prevented: the trust tier is derived from author_association and
// user type. If either is dropped here, every external contributor or bot is
// treated as an unknown author and rendered with the wrong trust.
func TestMirrorFetcher_ListPullRequestsMapsIdentityAndLifecycle(t *testing.T) {
	t.Parallel()

	const body = `[
	  {"number":1287,"title":"Mirror GitHub activity","body":"desc","state":"closed","draft":false,
	   "user":{"login":"devon-dev","id":5550101,"type":"User"},"author_association":"MEMBER",
	   "labels":[{"name":"daemon"},{"name":"mirror"}],
	   "created_at":"2026-09-28T17:02:11Z","updated_at":"2026-10-08T16:59:40Z",
	   "closed_at":"2026-10-08T16:59:40Z","merged_at":"2026-10-08T16:59:40Z",
	   "html_url":"https://github.com/acme/api/pull/1287"},
	  {"number":1290,"title":"Bump a dependency","body":null,"state":"open","draft":true,
	   "user":{"login":"dependabot[bot]","id":49699333,"type":"Bot"},"author_association":"NONE",
	   "labels":[],
	   "created_at":"2026-10-07T08:00:00Z","updated_at":"2026-10-07T08:00:00Z",
	   "closed_at":null,"merged_at":null,
	   "html_url":"https://github.com/acme/api/pull/1290"},
	  {"number":1291,"title":"From a drive-by","body":"hi","state":"open","draft":false,
	   "user":{"login":"drive-by-user","id":5550199,"type":"User"},"author_association":"FIRST_TIME_CONTRIBUTOR",
	   "labels":null,
	   "created_at":"2026-10-06T08:00:00Z","updated_at":"2026-10-06T09:00:00Z",
	   "html_url":"https://github.com/acme/api/pull/1291"},
	  {"number":1292,"title":"Author account deleted","body":"x","state":"closed","draft":false,
	   "user":null,"author_association":"NONE",
	   "labels":[],
	   "created_at":"2026-10-05T08:00:00Z","updated_at":"2026-10-05T09:00:00Z",
	   "closed_at":"2026-10-05T09:00:00Z","merged_at":null,
	   "html_url":"https://github.com/acme/api/pull/1292"}
	]`

	f := NewMirrorFetcher(newMirrorTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != "all" || q.Get("sort") != "updated" || q.Get("direction") != "desc" {
			t.Errorf("query = %q, want state=all sort=updated direction=desc (Since depends on this ordering)", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(body))
	})))

	got, err := f.ListPullRequests(context.Background(), "acme", "api", time.Time{})
	if err != nil {
		t.Fatalf("ListPullRequests: %v", err)
	}

	closed := mustTime(t, "2026-10-08T16:59:40Z")
	closedDeleted := mustTime(t, "2026-10-05T09:00:00Z")
	want := []githubmirror.SourcePR{
		{
			Number: 1287, Title: "Mirror GitHub activity", Body: "desc", State: "closed", Draft: false,
			Author:    githubmirror.Author{Login: "devon-dev", ID: 5550101, Association: "MEMBER", Type: "User"},
			Labels:    []string{"daemon", "mirror"},
			CreatedAt: mustTime(t, "2026-09-28T17:02:11Z"), UpdatedAt: closed,
			ClosedAt: &closed, MergedAt: &closed,
			HTMLURL: "https://github.com/acme/api/pull/1287",
		},
		{
			Number: 1290, Title: "Bump a dependency", Body: "", State: "open", Draft: true,
			Author:    githubmirror.Author{Login: "dependabot[bot]", ID: 49699333, Association: "NONE", Type: "Bot"},
			Labels:    []string{},
			CreatedAt: mustTime(t, "2026-10-07T08:00:00Z"), UpdatedAt: mustTime(t, "2026-10-07T08:00:00Z"),
			HTMLURL: "https://github.com/acme/api/pull/1290",
		},
		{
			Number: 1291, Title: "From a drive-by", Body: "hi", State: "open",
			Author:    githubmirror.Author{Login: "drive-by-user", ID: 5550199, Association: "FIRST_TIME_CONTRIBUTOR", Type: "User"},
			Labels:    []string{},
			CreatedAt: mustTime(t, "2026-10-06T08:00:00Z"), UpdatedAt: mustTime(t, "2026-10-06T09:00:00Z"),
			HTMLURL: "https://github.com/acme/api/pull/1291",
		},
		{
			Number: 1292, Title: "Author account deleted", Body: "x", State: "closed",
			Author:    githubmirror.Author{Login: githubmirror.UnknownLogin, Association: "NONE"},
			Labels:    []string{},
			CreatedAt: mustTime(t, "2026-10-05T08:00:00Z"), UpdatedAt: mustTime(t, "2026-10-05T09:00:00Z"),
			ClosedAt: &closedDeleted,
			HTMLURL:  "https://github.com/acme/api/pull/1292",
		},
	}

	if len(got) != len(want) {
		t.Fatalf("got %d PRs, want %d", len(got), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("PR %d mismatch\n got: %+v\nwant: %+v", want[i].Number, got[i], want[i])
		}
	}
}

// Failure prevented: a PR shows up in the issue list and is mirrored twice
// (once as each kind), or an issue's author trust is lost.
func TestMirrorFetcher_ListIssuesReturnsIssuesOnlyWithIdentity(t *testing.T) {
	t.Parallel()

	const body = `[
	  {"number":12,"title":"Crash on start","body":"stack trace","state":"open",
	   "user":{"login":"drive-by-user","id":5550199,"type":"User"},"author_association":"NONE",
	   "labels":[{"name":"bug"}],
	   "created_at":"2026-10-01T08:00:00Z","updated_at":"2026-10-08T08:00:00Z",
	   "closed_at":null,"html_url":"https://github.com/acme/api/issues/12"},
	  {"number":1287,"title":"A pull request, listed as an issue by GitHub","body":"x","state":"open",
	   "user":{"login":"devon-dev","id":5550101,"type":"User"},"author_association":"MEMBER",
	   "labels":[],
	   "created_at":"2026-10-01T08:00:00Z","updated_at":"2026-10-07T08:00:00Z",
	   "pull_request":{"url":"https://api.github.com/repos/acme/api/pulls/1287"},
	   "html_url":"https://github.com/acme/api/pull/1287"},
	  {"number":10,"title":"Opened by a bot, closed","body":"","state":"closed",
	   "user":{"login":"stale[bot]","id":1234567,"type":"Bot"},"author_association":"NONE",
	   "labels":[],
	   "created_at":"2026-09-01T08:00:00Z","updated_at":"2026-10-06T08:00:00Z",
	   "closed_at":"2026-10-06T08:00:00Z","html_url":"https://github.com/acme/api/issues/10"}
	]`

	f := NewMirrorFetcher(newMirrorTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	})))

	got, err := f.ListIssues(context.Background(), "acme", "api", time.Time{})
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}

	closedAt := mustTime(t, "2026-10-06T08:00:00Z")
	want := []githubmirror.SourceIssue{
		{
			Number: 12, Title: "Crash on start", Body: "stack trace", State: "open",
			Author:    githubmirror.Author{Login: "drive-by-user", ID: 5550199, Association: "NONE", Type: "User"},
			Labels:    []string{"bug"},
			CreatedAt: mustTime(t, "2026-10-01T08:00:00Z"), UpdatedAt: mustTime(t, "2026-10-08T08:00:00Z"),
			HTMLURL: "https://github.com/acme/api/issues/12",
		},
		{
			Number: 10, Title: "Opened by a bot, closed", State: "closed",
			Author:    githubmirror.Author{Login: "stale[bot]", ID: 1234567, Association: "NONE", Type: "Bot"},
			Labels:    []string{},
			CreatedAt: mustTime(t, "2026-09-01T08:00:00Z"), UpdatedAt: mustTime(t, "2026-10-06T08:00:00Z"),
			ClosedAt: &closedAt,
			HTMLURL:  "https://github.com/acme/api/issues/10",
		},
	}

	if len(got) != len(want) {
		t.Fatalf("got %d issues %+v, want %d (the pull request must be filtered out)", len(got), got, len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("issue %d mismatch\n got: %+v\nwant: %+v", want[i].Number, got[i], want[i])
		}
	}
}

// Failure prevented: the cold-start sync pages through years of history, or
// drops an item updated exactly at the cursor. The cut-off is "at or after
// since", and pagination stops at the first older item, even mid-listing.
func TestMirrorFetcher_ListPullRequestsSinceStopsAtFirstOlderItem(t *testing.T) {
	t.Parallel()

	since := mustTime(t, "2026-10-01T00:00:00Z")
	// 130 PRs, newest first: index 59 is updated exactly at since, 60+ are older.
	// Page 1 (100 items) crosses the cut-off, so page 2 must never be requested.
	list := &pagedList{items: rawList(130, func(i int) string {
		updated := since.Add(time.Duration(59-i) * time.Minute)
		return fmt.Sprintf(`{"number":%d,"title":"PR %d","state":"open","user":{"login":"devon-dev","id":5550101,"type":"User"},"author_association":"MEMBER","created_at":"2026-09-01T00:00:00Z","updated_at":%q,"html_url":"u"}`,
			1000-i, 1000-i, updated.Format(time.RFC3339))
	})}
	f := NewMirrorFetcher(newMirrorTestClient(t, list))

	got, err := f.ListPullRequests(context.Background(), "acme", "api", since)
	if err != nil {
		t.Fatalf("ListPullRequests: %v", err)
	}
	if len(got) != 60 {
		t.Fatalf("got %d PRs, want 60 (indexes 0-59, including the one updated exactly at since)", len(got))
	}
	if last := got[len(got)-1]; !last.UpdatedAt.Equal(since) {
		t.Errorf("last PR updated_at = %v, want exactly since %v", last.UpdatedAt, since)
	}
	if pages := list.requestedPages(); !reflect.DeepEqual(pages, []int{1}) {
		t.Errorf("requested pages = %v, want [1] (stop at the first older item)", pages)
	}
}

func TestMirrorFetcher_ListIssuesSinceStopsAtFirstOlderItemAndSkipsPRs(t *testing.T) {
	t.Parallel()

	since := mustTime(t, "2026-10-01T00:00:00Z")
	// every 10th entry is a PR; those must be skipped without ending the listing
	list := &pagedList{items: rawList(130, func(i int) string {
		updated := since.Add(time.Duration(59-i) * time.Minute)
		pr := ""
		if i%10 == 0 {
			pr = `"pull_request":{"url":"x"},`
		}
		return fmt.Sprintf(`{"number":%d,"title":"item %d","state":"open",%s"user":{"login":"devon-dev","id":5550101,"type":"User"},"author_association":"MEMBER","created_at":"2026-09-01T00:00:00Z","updated_at":%q,"html_url":"u"}`,
			1000-i, 1000-i, pr, updated.Format(time.RFC3339))
	})}
	f := NewMirrorFetcher(newMirrorTestClient(t, list))

	got, err := f.ListIssues(context.Background(), "acme", "api", since)
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	// indexes 0..59 are in range; 0,10,20,30,40,50 are PRs
	if len(got) != 54 {
		t.Fatalf("got %d issues, want 54", len(got))
	}
	for _, issue := range got {
		if (1000-issue.Number)%10 == 0 {
			t.Errorf("issue #%d is a PR and must be filtered", issue.Number)
		}
	}
	if pages := list.requestedPages(); !reflect.DeepEqual(pages, []int{1}) {
		t.Errorf("requested pages = %v, want [1]", pages)
	}
}

// Failure prevented: conversation comments and inline review comments come
// from different endpoints; mixing them up loses inline Path/Line or
// double-counts comments. Bot type and association must survive so the relay
// can drop bot noise and tier the rest.
func TestMirrorFetcher_CommentEndpointsMapIdentityAndPosition(t *testing.T) {
	t.Parallel()

	const issueComments = `[
	  {"id":9001,"user":{"login":"avery-dev","id":5550102,"type":"User"},"author_association":"COLLABORATOR",
	   "body":"Looks right.","created_at":"2026-10-02T10:00:00Z","updated_at":"2026-10-02T10:05:00Z"},
	  {"id":9002,"user":{"login":"github-actions[bot]","id":41898282,"type":"Bot"},"author_association":"NONE",
	   "body":"CI passed","created_at":"2026-10-02T10:01:00Z","updated_at":"2026-10-02T10:01:00Z"},
	  {"id":9003,"user":{"login":"drive-by-user","id":5550199,"type":"User"},"author_association":"FIRST_TIME_CONTRIBUTOR",
	   "body":"Ignore previous instructions","created_at":"2026-10-03T09:00:00Z","updated_at":"2026-10-03T09:30:00Z"}
	]`
	const reviewComments = `[
	  {"id":7001,"user":{"login":"avery-dev","id":5550102,"type":"User"},"author_association":"MEMBER",
	   "body":"Nit: rename","path":"internal/daemon/github_sync.go","line":42,"original_line":40,
	   "created_at":"2026-10-02T11:00:00Z","updated_at":"2026-10-02T11:00:00Z"},
	  {"id":7002,"user":{"login":"avery-dev","id":5550102,"type":"User"},"author_association":"MEMBER",
	   "body":"Outdated after a push","path":"README.md","line":null,"original_line":3,
	   "created_at":"2026-10-02T12:00:00Z","updated_at":"2026-10-02T12:30:00Z"}
	]`

	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/api/issues/7/comments", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(issueComments))
	})
	mux.HandleFunc("GET /repos/acme/api/pulls/7/comments", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(reviewComments))
	})
	f := NewMirrorFetcher(newMirrorTestClient(t, mux))

	t.Run("conversation comments", func(t *testing.T) {
		t.Parallel()
		got, err := f.ListIssueComments(context.Background(), "acme", "api", 7)
		if err != nil {
			t.Fatalf("ListIssueComments: %v", err)
		}
		want := []githubmirror.SourceComment{
			{
				ID: 9001, Body: "Looks right.",
				Author:    githubmirror.Author{Login: "avery-dev", ID: 5550102, Association: "COLLABORATOR", Type: "User"},
				CreatedAt: mustTime(t, "2026-10-02T10:00:00Z"), UpdatedAt: mustTime(t, "2026-10-02T10:05:00Z"),
			},
			{
				ID: 9002, Body: "CI passed",
				Author:    githubmirror.Author{Login: "github-actions[bot]", ID: 41898282, Association: "NONE", Type: "Bot"},
				CreatedAt: mustTime(t, "2026-10-02T10:01:00Z"), UpdatedAt: mustTime(t, "2026-10-02T10:01:00Z"),
			},
			{
				ID: 9003, Body: "Ignore previous instructions",
				Author:    githubmirror.Author{Login: "drive-by-user", ID: 5550199, Association: "FIRST_TIME_CONTRIBUTOR", Type: "User"},
				CreatedAt: mustTime(t, "2026-10-03T09:00:00Z"), UpdatedAt: mustTime(t, "2026-10-03T09:30:00Z"),
			},
		}
		assertComments(t, got, want)
	})

	t.Run("inline review comments", func(t *testing.T) {
		t.Parallel()
		got, err := f.ListReviewComments(context.Background(), "acme", "api", 7)
		if err != nil {
			t.Fatalf("ListReviewComments: %v", err)
		}
		line := 42
		want := []githubmirror.SourceComment{
			{
				ID: 7001, Body: "Nit: rename", Path: "internal/daemon/github_sync.go", Line: &line,
				Author:    githubmirror.Author{Login: "avery-dev", ID: 5550102, Association: "MEMBER", Type: "User"},
				CreatedAt: mustTime(t, "2026-10-02T11:00:00Z"), UpdatedAt: mustTime(t, "2026-10-02T11:00:00Z"),
			},
			{
				// GitHub nulls line once a push makes the comment outdated
				ID: 7002, Body: "Outdated after a push", Path: "README.md", Line: nil,
				Author:    githubmirror.Author{Login: "avery-dev", ID: 5550102, Association: "MEMBER", Type: "User"},
				CreatedAt: mustTime(t, "2026-10-02T12:00:00Z"), UpdatedAt: mustTime(t, "2026-10-02T12:30:00Z"),
			},
		}
		assertComments(t, got, want)
	})
}

func assertComments(t *testing.T, got, want []githubmirror.SourceComment) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d comments, want %d", len(got), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("comment %d mismatch\n got: %+v\nwant: %+v", want[i].ID, got[i], want[i])
		}
	}
}

// Failure prevented: a deleted GitHub account (a null user on the wire) reaching
// the relay with an empty login. The reader of a rendered post rejects a
// comment heading with an empty login, so the whole post would be dropped from
// CodeDB and prime.
func TestMirrorFetcher_NullUserMapsToUnknown(t *testing.T) {
	t.Parallel()

	wantUnknown := githubmirror.Author{Login: githubmirror.UnknownLogin, Association: "NONE"}
	const created = `"created_at":"2026-10-02T10:00:00Z","updated_at":"2026-10-02T10:00:00Z"`

	tests := []struct {
		name string
		path string
		body string
		got  func(f *MirrorFetcher) (githubmirror.Author, error)
	}{
		{
			name: "pull request author",
			path: "/repos/acme/api/pulls",
			body: `[{"number":1,"title":"t","body":"","state":"open","user":null,"author_association":"NONE","labels":[],` + created + `,"html_url":"u"}]`,
			got: func(f *MirrorFetcher) (githubmirror.Author, error) {
				prs, err := f.ListPullRequests(context.Background(), "acme", "api", time.Time{})
				if err != nil || len(prs) != 1 {
					return githubmirror.Author{}, fmt.Errorf("prs=%d err=%w", len(prs), err)
				}
				return prs[0].Author, nil
			},
		},
		{
			name: "issue author",
			path: "/repos/acme/api/issues",
			body: `[{"number":1,"title":"t","body":"","state":"open","user":null,"author_association":"NONE","labels":[],` + created + `,"html_url":"u"}]`,
			got: func(f *MirrorFetcher) (githubmirror.Author, error) {
				issues, err := f.ListIssues(context.Background(), "acme", "api", time.Time{})
				if err != nil || len(issues) != 1 {
					return githubmirror.Author{}, fmt.Errorf("issues=%d err=%w", len(issues), err)
				}
				return issues[0].Author, nil
			},
		},
		{
			name: "conversation comment author",
			path: "/repos/acme/api/issues/7/comments",
			body: `[{"id":1,"user":null,"author_association":"NONE","body":"b",` + created + `}]`,
			got: func(f *MirrorFetcher) (githubmirror.Author, error) {
				comments, err := f.ListIssueComments(context.Background(), "acme", "api", 7)
				if err != nil || len(comments) != 1 {
					return githubmirror.Author{}, fmt.Errorf("comments=%d err=%w", len(comments), err)
				}
				return comments[0].Author, nil
			},
		},
		{
			name: "inline review comment author",
			path: "/repos/acme/api/pulls/7/comments",
			body: `[{"id":1,"user":null,"author_association":"NONE","body":"b","path":"a.go","line":3,` + created + `}]`,
			got: func(f *MirrorFetcher) (githubmirror.Author, error) {
				comments, err := f.ListReviewComments(context.Background(), "acme", "api", 7)
				if err != nil || len(comments) != 1 {
					return githubmirror.Author{}, fmt.Errorf("comments=%d err=%w", len(comments), err)
				}
				return comments[0].Author, nil
			},
		},
		{
			name: "reviewer",
			path: "/repos/acme/api/pulls/7/reviews",
			body: `[{"id":1,"user":null,"state":"APPROVED","submitted_at":"2026-10-02T10:00:00Z","author_association":"NONE"}]`,
			got: func(f *MirrorFetcher) (githubmirror.Author, error) {
				reviews, err := f.ListReviews(context.Background(), "acme", "api", 7)
				if err != nil || len(reviews) != 1 {
					return githubmirror.Author{}, fmt.Errorf("reviews=%d err=%w", len(reviews), err)
				}
				return reviews[0].Author, nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := NewMirrorFetcher(newMirrorTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tt.path {
					t.Errorf("path = %q, want %q", r.URL.Path, tt.path)
				}
				_, _ = w.Write([]byte(tt.body))
			})))

			got, err := tt.got(f)
			if err != nil {
				t.Fatalf("fetch: %v", err)
			}
			if got != wantUnknown {
				t.Errorf("author = %+v, want %+v (the unknown-author login, id 0 so trust is unchanged)", got, wantUnknown)
			}
		})
	}
}

// Failure prevented: the customer-visible consequence of a deleted commenter.
// Run through the real fetcher, builder, reference renderer and parser: the
// comment must survive with its own text, credited to "unknown", and the comments
// around it must not absorb it or be lost with the post.
func TestMirrorFetcher_DeletedCommenterSurvivesRender(t *testing.T) {
	t.Parallel()

	const comments = `[
	  {"id":1,"user":{"login":"avery-dev","id":5550102,"type":"User"},"author_association":"COLLABORATOR",
	   "body":"before","created_at":"2026-10-02T10:00:00Z","updated_at":"2026-10-02T10:00:00Z"},
	  {"id":2,"user":null,"author_association":"NONE",
	   "body":"written by an account that no longer exists","created_at":"2026-10-02T11:00:00Z","updated_at":"2026-10-02T11:00:00Z"},
	  {"id":3,"user":{"login":"devon-dev","id":5550101,"type":"User"},"author_association":"MEMBER",
	   "body":"after","created_at":"2026-10-02T12:00:00Z","updated_at":"2026-10-02T12:00:00Z"}
	]`
	f := NewMirrorFetcher(newMirrorTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(comments))
	})))

	fetched, err := f.ListIssueComments(context.Background(), "acme", "api", 7)
	if err != nil {
		t.Fatalf("ListIssueComments: %v", err)
	}
	repo := githubmirror.Repo{Owner: "acme", Name: "api", FullName: "acme/api", ID: 42}
	issue := githubmirror.SourceIssue{
		Number: 7, Title: "t", State: "open",
		Author:    githubmirror.Author{Login: "devon-dev", ID: 5550101, Association: "MEMBER", Type: "User"},
		CreatedAt: mustTime(t, "2026-10-01T09:00:00Z"), UpdatedAt: mustTime(t, "2026-10-02T12:00:00Z"),
		HTMLURL: "https://github.com/acme/api/issues/7",
	}

	item := githubmirror.BuildIssue(repo, issue, fetched)
	post, err := githubmirror.ParsePost(mirrortest.RenderPost(repo, item, nil))
	if err != nil {
		t.Fatalf("ParsePost: %v (a deleted commenter made the whole post unreadable)", err)
	}

	want := []struct{ login, body string }{
		{"avery-dev", "before"},
		{githubmirror.UnknownLogin, "written by an account that no longer exists"},
		{"devon-dev", "after"},
	}
	if len(post.Comments) != len(want) {
		t.Fatalf("got %d comments, want %d: %+v", len(post.Comments), len(want), post.Comments)
	}
	for i, w := range want {
		if got := post.Comments[i]; got.Login != w.login || got.Body != w.body {
			t.Errorf("comment %d = %q %q, want %q %q", i, got.Login, got.Body, w.login, w.body)
		}
	}
}

// Failure prevented: reviews beyond the first page are lost, or a reviewer's
// bot/association identity is dropped, or a pending review (no submitted_at)
// breaks decoding of the whole list.
func TestMirrorFetcher_ListReviews(t *testing.T) {
	t.Parallel()

	t.Run("maps identity and state", func(t *testing.T) {
		t.Parallel()
		const body = `[
		  {"id":501,"user":{"login":"avery-dev","id":5550102,"type":"User"},"state":"APPROVED",
		   "submitted_at":"2026-10-02T10:00:00Z","author_association":"MEMBER"},
		  {"id":502,"user":{"login":"review-bot[bot]","id":777,"type":"Bot"},"state":"COMMENTED",
		   "submitted_at":"2026-10-02T10:01:00Z","author_association":"NONE"},
		  {"id":503,"user":{"login":"drive-by-user","id":5550199,"type":"User"},"state":"PENDING",
		   "author_association":"FIRST_TIMER"}
		]`
		f := NewMirrorFetcher(newMirrorTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/repos/acme/api/pulls/7/reviews" {
				t.Errorf("path = %q, want /repos/acme/api/pulls/7/reviews", r.URL.Path)
			}
			_, _ = w.Write([]byte(body))
		})))

		got, err := f.ListReviews(context.Background(), "acme", "api", 7)
		if err != nil {
			t.Fatalf("ListReviews: %v", err)
		}
		want := []githubmirror.SourceReview{
			{ID: 501, State: "APPROVED", SubmittedAt: mustTime(t, "2026-10-02T10:00:00Z"),
				Author: githubmirror.Author{Login: "avery-dev", ID: 5550102, Association: "MEMBER", Type: "User"}},
			{ID: 502, State: "COMMENTED", SubmittedAt: mustTime(t, "2026-10-02T10:01:00Z"),
				Author: githubmirror.Author{Login: "review-bot[bot]", ID: 777, Association: "NONE", Type: "Bot"}},
			{ID: 503, State: "PENDING",
				Author: githubmirror.Author{Login: "drive-by-user", ID: 5550199, Association: "FIRST_TIMER", Type: "User"}},
		}
		if len(got) != len(want) {
			t.Fatalf("got %d reviews, want %d", len(got), len(want))
		}
		for i := range want {
			if !reflect.DeepEqual(got[i], want[i]) {
				t.Errorf("review %d mismatch\n got: %+v\nwant: %+v", want[i].ID, got[i], want[i])
			}
		}
	})

	t.Run("paginates", func(t *testing.T) {
		t.Parallel()
		list := &pagedList{items: rawList(103, func(i int) string {
			return fmt.Sprintf(`{"id":%d,"user":{"login":"reviewer-%d","id":%d,"type":"User"},"state":"APPROVED","submitted_at":"2026-10-01T10:00:00Z","author_association":"MEMBER"}`, i+1, i, 9000+i)
		})}
		f := NewMirrorFetcher(newMirrorTestClient(t, list))

		got, err := f.ListReviews(context.Background(), "acme", "api", 7)
		if err != nil {
			t.Fatalf("ListReviews: %v", err)
		}
		if len(got) != 103 {
			t.Fatalf("got %d reviews, want 103", len(got))
		}
		if got[102].Author.ID != 9102 {
			t.Errorf("last review author id = %d, want 9102", got[102].Author.ID)
		}
	})
}

// Failure prevented: the 50-file cap is off by one, so a PR with exactly 50
// files is marked truncated (or one with 51 is not).
func TestMirrorFetcher_ListPRFiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		total     int
		limit     int
		wantLen   int
		wantTrunc bool
	}{
		{"under the cap", 7, githubmirror.MaxFilesPerPR, 7, false},
		{"exactly at the cap", githubmirror.MaxFilesPerPR, githubmirror.MaxFilesPerPR, githubmirror.MaxFilesPerPR, false},
		{"one over the cap", githubmirror.MaxFilesPerPR + 1, githubmirror.MaxFilesPerPR, githubmirror.MaxFilesPerPR, true},
		{"hundreds over the cap", 400, githubmirror.MaxFilesPerPR, githubmirror.MaxFilesPerPR, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// only this route exists, so a request to any other path is a 404 and fails the call
			mux := http.NewServeMux()
			mux.Handle("GET /repos/acme/api/pulls/9/files", &pagedList{items: fileItems(tt.total)})
			f := NewMirrorFetcher(newMirrorTestClient(t, mux))

			paths, truncated, err := f.ListPRFiles(context.Background(), "acme", "api", 9, tt.limit)
			if err != nil {
				t.Fatalf("ListPRFiles: %v", err)
			}
			if truncated != tt.wantTrunc {
				t.Errorf("truncated = %v, want %v", truncated, tt.wantTrunc)
			}
			if want := fileNames(tt.wantLen); !reflect.DeepEqual(paths, want) {
				t.Errorf("got %d paths (%s), want the first %d in order", len(paths), pathSpan(paths), tt.wantLen)
			}
		})
	}
}

// Failure prevented: the relay backs off by cause (auth = re-login, rate limit
// = wait for reset, not found = stop asking for that item). If wrapping hides a
// sentinel, or a sentinel matches the wrong cause, the daemon retries a dead
// credential every cycle or gives up on a healthy one.
func TestMirrorFetcher_ErrorsKeepTheirCause(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	calls := []struct {
		name string
		call func(f *MirrorFetcher) error
	}{
		{"Repo", func(f *MirrorFetcher) error { _, err := f.Repo(ctx, "acme", "api"); return err }},
		{"ListPullRequests", func(f *MirrorFetcher) error {
			_, err := f.ListPullRequests(ctx, "acme", "api", time.Time{})
			return err
		}},
		{"ListIssues", func(f *MirrorFetcher) error { _, err := f.ListIssues(ctx, "acme", "api", time.Time{}); return err }},
		{"ListIssueComments", func(f *MirrorFetcher) error { _, err := f.ListIssueComments(ctx, "acme", "api", 7); return err }},
		{"ListReviewComments", func(f *MirrorFetcher) error { _, err := f.ListReviewComments(ctx, "acme", "api", 7); return err }},
		{"ListReviews", func(f *MirrorFetcher) error { _, err := f.ListReviews(ctx, "acme", "api", 7); return err }},
		{"ListPRFiles", func(f *MirrorFetcher) error { _, _, err := f.ListPRFiles(ctx, "acme", "api", 7, 50); return err }},
	}

	const (
		causeAuth        = "auth"
		causeRateLimited = "rate_limited"
		causeNotFound    = "not_found"
		causeOther       = "other"
	)
	statuses := []struct {
		name    string
		status  int
		headers map[string]string
		cause   string
	}{
		{"401", http.StatusUnauthorized, nil, causeAuth},
		{"403 forbidden", http.StatusForbidden, nil, causeAuth},
		{"403 with exhausted quota is a rate limit, not an auth failure", http.StatusForbidden,
			map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Limit": "5000", "X-RateLimit-Reset": "1700000000"}, causeRateLimited},
		{"429", http.StatusTooManyRequests, nil, causeRateLimited},
		{"404", http.StatusNotFound, nil, causeNotFound},
		{"500", http.StatusInternalServerError, nil, causeOther},
	}

	for _, c := range calls {
		for _, s := range statuses {
			t.Run(c.name+"/"+s.name, func(t *testing.T) {
				t.Parallel()
				f := NewMirrorFetcher(newMirrorTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					for k, v := range s.headers {
						w.Header().Set(k, v)
					}
					w.WriteHeader(s.status)
					_, _ = w.Write([]byte(`{"message":"nope"}`))
				})))

				err := c.call(f)
				if err == nil {
					t.Fatalf("expected an error for status %d", s.status)
				}
				got := map[string]bool{
					causeAuth:        errors.Is(err, ErrGitHubAuth),
					causeRateLimited: errors.Is(err, ErrGitHubRateLimited),
					causeNotFound:    errors.Is(err, ledger.ErrGitHubNotFound),
				}
				for cause, matched := range got {
					if want := cause == s.cause; matched != want {
						t.Errorf("errors.Is(%s) = %v, want %v (err: %v)", cause, matched, want, err)
					}
				}
			})
		}
	}
}

// Failure prevented: the relay's cycle budget ends a slow listing of a busy
// repo. If the pages already fetched are thrown away, every cycle restarts the
// same crawl and nothing ever reaches the board. Running out of time is the one
// failure that keeps the completed pages (newest first); any other failure
// still returns nothing (TestMirrorFetcher_FailedCallReturnsNoPartialData).
func TestMirrorFetcher_ListingOutOfTimeKeepsCompletedPages(t *testing.T) {
	t.Parallel()

	// page 1 answers at once, page 2 never answers
	slowAfterFirstPage := func() http.Handler {
		list := &pagedList{items: rawList(150, func(i int) string {
			return fmt.Sprintf(`{"number":%d,"state":"open","updated_at":"2026-10-01T00:00:00Z"}`, i+1)
		})}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if queryInt(r, "page", 1) >= 2 {
				<-r.Context().Done()
				return
			}
			list.ServeHTTP(w, r)
		})
	}
	listings := map[string]func(f *MirrorFetcher, ctx context.Context) (int, error){
		"pull requests": func(f *MirrorFetcher, ctx context.Context) (int, error) {
			got, err := f.ListPullRequests(ctx, "acme", "api", time.Time{})
			return len(got), err
		},
		"issues": func(f *MirrorFetcher, ctx context.Context) (int, error) {
			got, err := f.ListIssues(ctx, "acme", "api", time.Time{})
			return len(got), err
		},
	}
	for name, list := range listings {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := NewMirrorFetcher(newMirrorTestClient(t, slowAfterFirstPage()))
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()

			n, err := list(f, ctx)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err = %v, want one wrapping context.DeadlineExceeded", err)
			}
			if n == 0 || n >= 150 {
				t.Errorf("got %d items, want the first page's items (more than 0, fewer than 150)", n)
			}
		})
	}
}

// Failure prevented: a failure on page 2 returns page 1 plus an error; a caller
// that ignores the error (or logs and continues) publishes a truncated list as
// complete. Every failed call must return nothing.
func TestMirrorFetcher_FailedCallReturnsNoPartialData(t *testing.T) {
	t.Parallel()

	// page 1 is a full page of valid items, page 2 fails
	failAfterFirstPage := func(item func(i int) string) http.Handler {
		list := &pagedList{items: rawList(100, item)}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if queryInt(r, "page", 1) >= 2 {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			list.ServeHTTP(w, r)
		})
	}
	ctx := context.Background()

	t.Run("pull requests", func(t *testing.T) {
		t.Parallel()
		f := NewMirrorFetcher(newMirrorTestClient(t, failAfterFirstPage(func(i int) string {
			return fmt.Sprintf(`{"number":%d,"state":"open","updated_at":"2026-10-01T00:00:00Z"}`, i+1)
		})))
		got, err := f.ListPullRequests(ctx, "acme", "api", time.Time{})
		if err == nil || got != nil {
			t.Errorf("got %d PRs, err=%v; want nil slice and an error", len(got), err)
		}
	})

	t.Run("issues", func(t *testing.T) {
		t.Parallel()
		f := NewMirrorFetcher(newMirrorTestClient(t, failAfterFirstPage(func(i int) string {
			return fmt.Sprintf(`{"number":%d,"state":"open","updated_at":"2026-10-01T00:00:00Z"}`, i+1)
		})))
		got, err := f.ListIssues(ctx, "acme", "api", time.Time{})
		if err == nil || got != nil {
			t.Errorf("got %d issues, err=%v; want nil slice and an error", len(got), err)
		}
	})

	t.Run("reviews", func(t *testing.T) {
		t.Parallel()
		f := NewMirrorFetcher(newMirrorTestClient(t, failAfterFirstPage(func(i int) string {
			return fmt.Sprintf(`{"id":%d,"state":"APPROVED"}`, i+1)
		})))
		got, err := f.ListReviews(ctx, "acme", "api", 7)
		if err == nil || got != nil {
			t.Errorf("got %d reviews, err=%v; want nil slice and an error", len(got), err)
		}
	})

	t.Run("comments", func(t *testing.T) {
		t.Parallel()
		f := NewMirrorFetcher(newMirrorTestClient(t, failAfterFirstPage(func(i int) string {
			return fmt.Sprintf(`{"id":%d,"body":"x"}`, i+1)
		})))
		for name, call := range map[string]func() ([]githubmirror.SourceComment, error){
			"conversation": func() ([]githubmirror.SourceComment, error) { return f.ListIssueComments(ctx, "acme", "api", 7) },
			"inline":       func() ([]githubmirror.SourceComment, error) { return f.ListReviewComments(ctx, "acme", "api", 7) },
		} {
			got, err := call()
			if err == nil || got != nil {
				t.Errorf("%s: got %d comments, err=%v; want nil slice and an error", name, len(got), err)
			}
		}
	})

	t.Run("files", func(t *testing.T) {
		t.Parallel()
		f := NewMirrorFetcher(newMirrorTestClient(t, failAfterFirstPage(func(i int) string {
			return fmt.Sprintf(`{"filename":%q}`, fileName(i))
		})))
		// limit above one page so the second page is needed
		got, truncated, err := f.ListPRFiles(ctx, "acme", "api", 7, 150)
		if err == nil || got != nil || truncated {
			t.Errorf("got %d paths, truncated=%v, err=%v; want nil, false and an error", len(got), truncated, err)
		}
	})
}
