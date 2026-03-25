package query

import (
	"encoding/json"
	"sort"
	"time"
)

// ActivityResult contains assembled GitHub activity grouped by cluster type.
// Use MarshalEventClusters() to produce the flat JSON array for the extractor.
type ActivityResult struct {
	PRClusters        []PRCluster        `json:"-"`
	StandaloneIssues  []StandaloneIssue  `json:"-"`
	StandaloneCommits []StandaloneCommit `json:"-"`
	Metadata          ActivityMetadata   `json:"-"`
}

// ActivityMetadata records the query parameters and result counts.
type ActivityMetadata struct {
	Since       time.Time `json:"since"`
	Until       time.Time `json:"until"`
	PRCount     int       `json:"pr_count"`
	IssueCount  int       `json:"issue_count"`
	CommitCount int       `json:"commit_count"`
}

// PRCluster represents a pull request with its associated comments and commits.
type PRCluster struct {
	Number             int           `json:"number"`
	Title              string        `json:"title"`
	Description        string        `json:"description"`
	Author             string        `json:"author"`
	Status             string        `json:"status"`
	MergedAt           *string       `json:"merged_at,omitempty"`
	UpdatedAt          *string       `json:"-"` // internal: for per-day bucketing
	URL                string        `json:"url"`
	Labels             []string      `json:"labels,omitempty"`
	Commits            []CommitEntry `json:"commits"`
	Reviews            []ReviewGroup `json:"reviews"`
	DiscussionComments []Comment     `json:"discussion_comments"`
}

// ReviewGroup groups review comments by reviewer.
type ReviewGroup struct {
	Reviewer string          `json:"reviewer"`
	Comments []ReviewComment `json:"comments"`
}

// ReviewComment is a single code review comment with optional file location.
type ReviewComment struct {
	Body string  `json:"body"`
	Path *string `json:"path"`
	Line *int    `json:"line"`
}

// Comment is a discussion comment (PR or issue).
type Comment struct {
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

// CommitEntry is a commit linked to a PR or standalone.
// Fields may be nil when commit metadata is unavailable (squash/rebase).
type CommitEntry struct {
	SHA       string  `json:"sha"`
	Message   *string `json:"message"`
	Author    *string `json:"author"`
	Timestamp *string `json:"timestamp"`
}

// StandaloneIssue is an issue not referenced by any PR in the window.
type StandaloneIssue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Author    string    `json:"author"`
	State     string    `json:"state"`
	UpdatedAt *string   `json:"-"` // internal: for per-day bucketing
	URL       string    `json:"url"`
	Comments  []Comment `json:"comments"`
}

// StandaloneCommit is a commit not linked to any PR.
type StandaloneCommit struct {
	SHA       string `json:"sha"`
	Message   string `json:"message"`
	Author    string `json:"author"`
	Timestamp string `json:"timestamp"`
}

// Flat array wrapper types with type discriminator for MarshalEventClusters.

type prClusterWithType struct {
	Type string `json:"type"`
	PRCluster
}

type issueWithType struct {
	Type string `json:"type"`
	StandaloneIssue
}

type commitWithType struct {
	Type string `json:"type"`
	StandaloneCommit
}

// ByDay partitions the ActivityResult into per-day buckets keyed by YYYY-MM-DD.
// Primary timestamps: PRCluster uses MergedAt (if set) else UpdatedAt;
// StandaloneIssue uses UpdatedAt; StandaloneCommit uses Timestamp.
// Items with unparseable timestamps go into today's bucket.
func (r *ActivityResult) ByDay() map[string]*ActivityResult {
	today := time.Now().UTC().Format("2006-01-02")
	buckets := make(map[string]*ActivityResult)

	ensure := func(day string) *ActivityResult {
		if buckets[day] == nil {
			buckets[day] = &ActivityResult{}
		}
		return buckets[day]
	}

	for _, pr := range r.PRClusters {
		var day string
		if pr.MergedAt != nil {
			day = parseDayFromRFC3339(pr.MergedAt, today)
		} else {
			day = parseDayFromRFC3339(pr.UpdatedAt, today)
		}
		b := ensure(day)
		b.PRClusters = append(b.PRClusters, pr)
	}

	for _, issue := range r.StandaloneIssues {
		day := parseDayFromRFC3339(issue.UpdatedAt, today)
		b := ensure(day)
		b.StandaloneIssues = append(b.StandaloneIssues, issue)
	}

	for _, commit := range r.StandaloneCommits {
		day := parseDayFromTimestamp(commit.Timestamp, today)
		b := ensure(day)
		b.StandaloneCommits = append(b.StandaloneCommits, commit)
	}

	// populate metadata per bucket
	for _, bucket := range buckets {
		bucket.Metadata = ActivityMetadata{
			PRCount:     len(bucket.PRClusters),
			IssueCount:  len(bucket.StandaloneIssues),
			CommitCount: len(bucket.StandaloneCommits),
		}
	}

	return buckets
}

// SortedDays returns the day keys from ByDay in chronological order.
func SortedDays(byDay map[string]*ActivityResult) []string {
	days := make([]string, 0, len(byDay))
	for day := range byDay {
		days = append(days, day)
	}
	sort.Strings(days)
	return days
}

// parseDayFromRFC3339 extracts YYYY-MM-DD from an RFC3339 timestamp pointer.
// Returns fallback if the pointer is nil or unparseable.
func parseDayFromRFC3339(ts *string, fallback string) string {
	if ts == nil {
		return fallback
	}
	t, err := time.Parse(time.RFC3339, *ts)
	if err != nil {
		return fallback
	}
	return t.UTC().Format("2006-01-02")
}

// parseDayFromTimestamp extracts YYYY-MM-DD from an RFC3339 timestamp string.
func parseDayFromTimestamp(ts string, fallback string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return fallback
	}
	return t.UTC().Format("2006-01-02")
}

// MarshalEventClusters produces a flat JSON array of event clusters with
// type discriminators, matching the extractor input spec.
func (r *ActivityResult) MarshalEventClusters() ([]byte, error) {
	var elements []any

	for _, pr := range r.PRClusters {
		elements = append(elements, prClusterWithType{
			Type:      "pull_request",
			PRCluster: pr,
		})
	}

	for _, issue := range r.StandaloneIssues {
		elements = append(elements, issueWithType{
			Type:            "issue",
			StandaloneIssue: issue,
		})
	}

	for _, commit := range r.StandaloneCommits {
		elements = append(elements, commitWithType{
			Type:             "commit",
			StandaloneCommit: commit,
		})
	}

	if len(elements) == 0 {
		return []byte("[]"), nil
	}

	return json.Marshal(elements)
}
