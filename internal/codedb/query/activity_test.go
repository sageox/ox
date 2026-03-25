package query

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Step 2: PR Query Tests ───────────────────────────────────────────────────

func TestQueryPRs_WithinWindowByUpdatedAt(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)

	updatedAt := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC).Unix()
	createdAt := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC).Unix()
	insertPR(t, s, 42, "In window PR", "body", "alice", "open", int64Ptr(createdAt), int64Ptr(updatedAt), nil, "https://github.com/org/repo/pull/42")

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	assert.Len(t, result.PRClusters, 1)
	assert.Equal(t, 42, result.PRClusters[0].Number)
}

func TestQueryPRs_WithinWindowByMergedAtOnly(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)

	// created and updated BEFORE window, but merged WITHIN window
	createdAt := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC).Unix()
	updatedAt := time.Date(2026, 2, 5, 0, 0, 0, 0, time.UTC).Unix()
	mergedAt := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	insertPR(t, s, 55, "Merged in window", "body", "bob", "merged", int64Ptr(createdAt), int64Ptr(updatedAt), int64Ptr(mergedAt), "")

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	assert.Len(t, result.PRClusters, 1)
	assert.Equal(t, 55, result.PRClusters[0].Number)
}

func TestQueryPRs_WithinWindowByCreatedAtOnly(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)

	createdAt := time.Date(2026, 3, 12, 0, 0, 0, 0, time.UTC).Unix()
	insertPR(t, s, 60, "Created in window", "body", "carol", "open", int64Ptr(createdAt), nil, nil, "")

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	assert.Len(t, result.PRClusters, 1)
}

func TestQueryPRs_OutsideWindow(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)

	oldTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	insertPR(t, s, 1, "Old PR", "body", "alice", "merged", int64Ptr(oldTime), int64Ptr(oldTime), int64Ptr(oldTime), "")

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	assert.Empty(t, result.PRClusters)
}

func TestQueryPRs_BoundaryTimestamps(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)

	sinceUnix := since.Unix()
	untilUnix := until.Unix()

	// Exactly at since boundary
	insertPR(t, s, 1, "At since", "", "a", "open", int64Ptr(sinceUnix), int64Ptr(sinceUnix), nil, "")
	// Exactly at until boundary
	insertPR(t, s, 2, "At until", "", "b", "open", int64Ptr(untilUnix), int64Ptr(untilUnix), nil, "")

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	assert.Len(t, result.PRClusters, 2)
}

func TestQueryPRs_EmptyDatabase(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	assert.Empty(t, result.PRClusters)
	assert.Empty(t, result.StandaloneIssues)
	assert.Empty(t, result.StandaloneCommits)
	assert.Equal(t, 0, result.Metadata.PRCount)
}

func TestQueryPRs_Ordering(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	ts1 := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC).Unix()
	ts2 := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	ts3 := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC).Unix()

	insertPR(t, s, 1, "First", "", "a", "open", int64Ptr(ts1), int64Ptr(ts1), nil, "")
	insertPR(t, s, 2, "Second", "", "b", "merged", int64Ptr(ts1), int64Ptr(ts1), int64Ptr(ts2), "")
	insertPR(t, s, 3, "Third", "", "c", "open", int64Ptr(ts3), int64Ptr(ts3), nil, "")

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	require.Len(t, result.PRClusters, 3)
	// ordered by COALESCE(merged_at, updated_at, created_at) DESC
	assert.Equal(t, 2, result.PRClusters[0].Number) // merged Mar 15
	assert.Equal(t, 3, result.PRClusters[1].Number) // updated Mar 10
	assert.Equal(t, 1, result.PRClusters[2].Number) // updated Mar 5
}

func TestQueryPRs_FieldMapping(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	ts := time.Date(2026, 3, 15, 14, 30, 0, 0, time.UTC).Unix()
	insertPR(t, s, 42, "Test PR", "PR body text", "alice", "merged", int64Ptr(ts), int64Ptr(ts), int64Ptr(ts),
		"https://github.com/org/repo/pull/42")

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	require.Len(t, result.PRClusters, 1)

	pr := result.PRClusters[0]
	assert.Equal(t, "PR body text", pr.Description) // body → Description
	assert.Equal(t, "merged", pr.Status)            // state → Status
	assert.NotNil(t, pr.MergedAt)
	assert.Equal(t, "2026-03-15T14:30:00Z", *pr.MergedAt)

	// Verify JSON serialization uses correct field names
	data, err := json.Marshal(pr)
	require.NoError(t, err)
	raw := string(data)
	assert.Contains(t, raw, `"description"`)
	assert.Contains(t, raw, `"status"`)
	assert.NotContains(t, raw, `"body"`) // not the issue-style body key
}

// ─── Step 3: Comment Splitting Tests ──────────────────────────────────────────

func TestCommentSplitting_ReviewVsDiscussion(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	ts := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	prID := insertPR(t, s, 42, "Test", "", "alice", "open", int64Ptr(ts), int64Ptr(ts), nil, "")

	// 2 review comments (with path), 2 discussion comments (no path)
	insertComment(t, s, prID, "bob", "review 1", strPtr("api/handler.go"), intPtr(10), ts)
	insertComment(t, s, prID, "bob", "review 2", strPtr("api/handler.go"), intPtr(20), ts+1)
	insertComment(t, s, prID, "alice", "discussion 1", nil, nil, ts+2)
	insertComment(t, s, prID, "carol", "discussion 2", nil, nil, ts+3)

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	require.Len(t, result.PRClusters, 1)

	pr := result.PRClusters[0]
	assert.Len(t, pr.Reviews, 1) // 1 reviewer
	assert.Len(t, pr.Reviews[0].Comments, 2)
	assert.Len(t, pr.DiscussionComments, 2)
}

func TestCommentSplitting_GroupByAuthor(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	ts := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	prID := insertPR(t, s, 42, "Test", "", "alice", "open", int64Ptr(ts), int64Ptr(ts), nil, "")

	// 3 from alice, 2 from bob — all review comments
	insertComment(t, s, prID, "alice", "r1", strPtr("a.go"), nil, ts)
	insertComment(t, s, prID, "alice", "r2", strPtr("b.go"), nil, ts+1)
	insertComment(t, s, prID, "alice", "r3", strPtr("c.go"), nil, ts+2)
	insertComment(t, s, prID, "bob", "r4", strPtr("d.go"), nil, ts+3)
	insertComment(t, s, prID, "bob", "r5", strPtr("e.go"), nil, ts+4)

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	require.Len(t, result.PRClusters, 1)

	pr := result.PRClusters[0]
	require.Len(t, pr.Reviews, 2) // 2 reviewers
	assert.Equal(t, "alice", pr.Reviews[0].Reviewer)
	assert.Len(t, pr.Reviews[0].Comments, 3)
	assert.Equal(t, "bob", pr.Reviews[1].Reviewer)
	assert.Len(t, pr.Reviews[1].Comments, 2)
}

func TestCommentSplitting_SingleReviewerMultipleComments(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	ts := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	prID := insertPR(t, s, 42, "Test", "", "alice", "open", int64Ptr(ts), int64Ptr(ts), nil, "")

	for i := range 4 {
		insertComment(t, s, prID, "alice", "comment", strPtr("file.go"), nil, ts+int64(i))
	}

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	require.Len(t, result.PRClusters[0].Reviews, 1)
	assert.Len(t, result.PRClusters[0].Reviews[0].Comments, 4)
}

func TestCommentSplitting_CommentOrdering(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	ts := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	prID := insertPR(t, s, 42, "Test", "", "alice", "open", int64Ptr(ts), int64Ptr(ts), nil, "")

	// Insert out of order
	insertComment(t, s, prID, "bob", "third", strPtr("a.go"), nil, ts+3)
	insertComment(t, s, prID, "bob", "first", strPtr("a.go"), nil, ts+1)
	insertComment(t, s, prID, "bob", "second", strPtr("a.go"), nil, ts+2)

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	comments := result.PRClusters[0].Reviews[0].Comments
	assert.Equal(t, "first", comments[0].Body)
	assert.Equal(t, "second", comments[1].Body)
	assert.Equal(t, "third", comments[2].Body)
}

func TestCommentSplitting_NoComments(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	ts := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	insertPR(t, s, 42, "Test", "", "alice", "open", int64Ptr(ts), int64Ptr(ts), nil, "")

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	pr := result.PRClusters[0]
	assert.NotNil(t, pr.Reviews)
	assert.NotNil(t, pr.DiscussionComments)
	assert.Empty(t, pr.Reviews)
	assert.Empty(t, pr.DiscussionComments)

	// Verify JSON serializes as [] not null
	data, err := result.MarshalEventClusters()
	require.NoError(t, err)
	raw := string(data)
	assert.Contains(t, raw, `"reviews":[]`)
	assert.Contains(t, raw, `"discussion_comments":[]`)
}

func TestCommentSplitting_ReviewCommentFields(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	ts := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	prID := insertPR(t, s, 42, "Test", "", "alice", "open", int64Ptr(ts), int64Ptr(ts), nil, "")
	insertComment(t, s, prID, "bob", "looks good", strPtr("api/handler.go"), intPtr(42), ts)

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)

	rc := result.PRClusters[0].Reviews[0].Comments[0]
	assert.Equal(t, "looks good", rc.Body)
	require.NotNil(t, rc.Path)
	assert.Equal(t, "api/handler.go", *rc.Path)
	require.NotNil(t, rc.Line)
	assert.Equal(t, 42, *rc.Line)
}

func TestCommentSplitting_NullableLine(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	ts := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	prID := insertPR(t, s, 42, "Test", "", "alice", "open", int64Ptr(ts), int64Ptr(ts), nil, "")
	insertComment(t, s, prID, "bob", "general comment", strPtr("api/handler.go"), nil, ts)

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)

	rc := result.PRClusters[0].Reviews[0].Comments[0]
	require.NotNil(t, rc.Path)
	assert.Nil(t, rc.Line)

	// Verify JSON serialization
	data, err := result.MarshalEventClusters()
	require.NoError(t, err)
	assert.Contains(t, string(data), `"line":null`)
}

// ─── Step 4: PR Commits via LEFT JOIN ─────────────────────────────────────────

func TestPRCommits_Normal(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	ts := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	prID := insertPR(t, s, 42, "Test", "", "alice", "merged", int64Ptr(ts), int64Ptr(ts), int64Ptr(ts), "")

	insertCommit(t, s, "aaa111", "sarah", "feat: add feature", ts)
	insertCommit(t, s, "bbb222", "sarah", "fix: typo", ts+1)
	insertPRCommit(t, s, prID, "aaa111")
	insertPRCommit(t, s, prID, "bbb222")

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)

	commits := result.PRClusters[0].Commits
	require.Len(t, commits, 2)
	assert.Equal(t, "aaa111", commits[0].SHA)
	require.NotNil(t, commits[0].Message)
	assert.Equal(t, "feat: add feature", *commits[0].Message)
	require.NotNil(t, commits[0].Author)
	assert.Equal(t, "sarah", *commits[0].Author)
}

func TestPRCommits_SquashDegradation(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	ts := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	prID := insertPR(t, s, 55, "Squash PR", "", "bob", "merged", int64Ptr(ts), int64Ptr(ts), int64Ptr(ts), "")

	// pr_commits exist but NO matching commits rows (squash)
	insertPRCommit(t, s, prID, "squash1")
	insertPRCommit(t, s, prID, "squash2")

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)

	commits := result.PRClusters[0].Commits
	require.Len(t, commits, 2)
	assert.Equal(t, "squash1", commits[0].SHA)
	assert.Nil(t, commits[0].Message)
	assert.Nil(t, commits[0].Author)
	assert.Nil(t, commits[0].Timestamp)
}

func TestPRCommits_Mixed(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	ts := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	prID := insertPR(t, s, 42, "Mixed", "", "alice", "merged", int64Ptr(ts), int64Ptr(ts), int64Ptr(ts), "")

	// 2 with full metadata, 1 degraded
	insertCommit(t, s, "aaa111", "sarah", "feat: first", ts)
	insertCommit(t, s, "bbb222", "sarah", "feat: second", ts+1)
	insertPRCommit(t, s, prID, "aaa111")
	insertPRCommit(t, s, prID, "bbb222")
	insertPRCommit(t, s, prID, "ccc333") // no matching commits row

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)

	commits := result.PRClusters[0].Commits
	require.Len(t, commits, 3)

	// Count full vs degraded
	var full, degraded int
	for _, c := range commits {
		if c.Message != nil {
			full++
		} else {
			degraded++
		}
	}
	assert.Equal(t, 2, full)
	assert.Equal(t, 1, degraded)
}

func TestPRCommits_EmptyCommits(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	ts := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	insertPR(t, s, 60, "No commits", "", "carol", "open", int64Ptr(ts), int64Ptr(ts), nil, "")

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)

	commits := result.PRClusters[0].Commits
	assert.NotNil(t, commits)
	assert.Empty(t, commits)
}

// ─── Step 5: Standalone Issues ────────────────────────────────────────────────

func TestStandaloneIssues_WithinWindow(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)

	ts := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	insertIssue(t, s, 30, "Bug report", "Something broke", "dave", "open", int64Ptr(ts), int64Ptr(ts), "https://github.com/org/repo/issues/30")

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	require.Len(t, result.StandaloneIssues, 1)
	assert.Equal(t, 30, result.StandaloneIssues[0].Number)
	assert.Equal(t, "Bug report", result.StandaloneIssues[0].Title)
	assert.Equal(t, "open", result.StandaloneIssues[0].State)
}

func TestStandaloneIssues_OutsideWindow(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)

	oldTs := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	insertIssue(t, s, 1, "Old issue", "old", "alice", "open", int64Ptr(oldTs), int64Ptr(oldTs), "")

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	assert.Empty(t, result.StandaloneIssues)
}

func TestStandaloneIssues_WithComments(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	ts := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	issueID := insertIssue(t, s, 30, "Bug", "desc", "dave", "open", int64Ptr(ts), int64Ptr(ts), "")
	insertIssueComment(t, s, issueID, "eve", "comment 1", ts+1)
	insertIssueComment(t, s, issueID, "frank", "comment 2", ts+2)
	insertIssueComment(t, s, issueID, "dave", "comment 3", ts+3)

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	require.Len(t, result.StandaloneIssues, 1)
	assert.Len(t, result.StandaloneIssues[0].Comments, 3)
	assert.Equal(t, "comment 1", result.StandaloneIssues[0].Comments[0].Body)
}

func TestStandaloneIssues_NoComments(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	ts := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	insertIssue(t, s, 55, "Empty issue", "desc", "eve", "open", int64Ptr(ts), int64Ptr(ts), "")

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	assert.NotNil(t, result.StandaloneIssues[0].Comments)
	assert.Empty(t, result.StandaloneIssues[0].Comments)
}

func TestStandaloneIssues_Ordering(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	ts1 := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC).Unix()
	ts2 := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	ts3 := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC).Unix()

	insertIssue(t, s, 1, "First", "", "a", "open", int64Ptr(ts1), int64Ptr(ts1), "")
	insertIssue(t, s, 2, "Second", "", "b", "open", int64Ptr(ts2), int64Ptr(ts2), "")
	insertIssue(t, s, 3, "Third", "", "c", "open", int64Ptr(ts3), int64Ptr(ts3), "")

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	require.Len(t, result.StandaloneIssues, 3)
	// Ordered by COALESCE(updated_at, created_at) DESC
	assert.Equal(t, 2, result.StandaloneIssues[0].Number)
	assert.Equal(t, 3, result.StandaloneIssues[1].Number)
	assert.Equal(t, 1, result.StandaloneIssues[2].Number)
}

func TestStandaloneIssues_OldIssueUpdatedRecently(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 13, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)

	// created 60 days ago, updated 1 day ago
	createdAt := time.Date(2026, 1, 19, 0, 0, 0, 0, time.UTC).Unix()
	updatedAt := time.Date(2026, 3, 19, 0, 0, 0, 0, time.UTC).Unix()
	insertIssue(t, s, 99, "Old issue with recent update", "body", "alice", "open",
		int64Ptr(createdAt), int64Ptr(updatedAt), "")

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	require.Len(t, result.StandaloneIssues, 1, "old issue with recent updated_at should appear in window")
	assert.Equal(t, 99, result.StandaloneIssues[0].Number)
}

// ─── Step 6: Standalone Commits ───────────────────────────────────────────────

func TestStandaloneCommits_NotLinkedToPR(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	ts := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	insertCommit(t, s, "abc123", "sarah", "standalone commit", ts)

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	require.Len(t, result.StandaloneCommits, 1)
	assert.Equal(t, "abc123", result.StandaloneCommits[0].SHA)
	assert.Equal(t, "standalone commit", result.StandaloneCommits[0].Message)
}

func TestStandaloneCommits_PRLinkedExcluded(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	ts := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	prID := insertPR(t, s, 42, "PR", "", "alice", "merged", int64Ptr(ts), int64Ptr(ts), int64Ptr(ts), "")
	insertCommit(t, s, "linked", "sarah", "linked commit", ts)
	insertPRCommit(t, s, prID, "linked")

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	assert.Empty(t, result.StandaloneCommits)
}

func TestStandaloneCommits_CrossWindowExclusion(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)

	// PR is OUTSIDE the time window
	oldTs := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	prID := insertPR(t, s, 99, "Old PR", "", "alice", "merged", int64Ptr(oldTs), int64Ptr(oldTs), int64Ptr(oldTs), "")

	// Commit IS in window but linked to the out-of-window PR
	inWindowTs := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	insertCommit(t, s, "cross", "sarah", "cross window commit", inWindowTs)
	insertPRCommit(t, s, prID, "cross")

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	// The commit should still be excluded from standalone even though its PR is outside window
	assert.Empty(t, result.StandaloneCommits)
}

func TestStandaloneCommits_OutsideWindow(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)

	oldTs := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	insertCommit(t, s, "old", "alice", "old commit", oldTs)

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	assert.Empty(t, result.StandaloneCommits)
}

func TestStandaloneCommits_Ordering(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	ts1 := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC).Unix()
	ts2 := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	ts3 := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC).Unix()

	insertCommit(t, s, "aaa", "a", "first", ts1)
	insertCommit(t, s, "bbb", "b", "second", ts2)
	insertCommit(t, s, "ccc", "c", "third", ts3)

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	require.Len(t, result.StandaloneCommits, 3)
	// Ordered by timestamp DESC
	assert.Equal(t, "bbb", result.StandaloneCommits[0].SHA)
	assert.Equal(t, "ccc", result.StandaloneCommits[1].SHA)
	assert.Equal(t, "aaa", result.StandaloneCommits[2].SHA)
}

// ─── Step 7: Metadata, Full Integration, Volume ──────────────────────────────

func TestMetadata_Counts(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	ts := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	insertPR(t, s, 1, "PR1", "", "a", "open", int64Ptr(ts), int64Ptr(ts), nil, "")
	insertPR(t, s, 2, "PR2", "", "b", "merged", int64Ptr(ts), int64Ptr(ts), int64Ptr(ts), "")
	insertIssue(t, s, 10, "Issue", "", "c", "open", int64Ptr(ts), int64Ptr(ts), "")
	insertCommit(t, s, "abc", "d", "commit", ts)

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	assert.Equal(t, 2, result.Metadata.PRCount)
	assert.Equal(t, 1, result.Metadata.IssueCount)
	assert.Equal(t, 1, result.Metadata.CommitCount)
	assert.Equal(t, since, result.Metadata.Since)
	assert.Equal(t, until, result.Metadata.Until)
}

func TestMetadata_EmptyWindow(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	assert.Equal(t, 0, result.Metadata.PRCount)
	assert.Equal(t, 0, result.Metadata.IssueCount)
	assert.Equal(t, 0, result.Metadata.CommitCount)

	data, err := result.MarshalEventClusters()
	require.NoError(t, err)
	assert.Equal(t, "[]", string(data))
}

func TestGoldenFile_FullIntegration(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	ts := time.Date(2026, 3, 18, 14, 30, 0, 0, time.UTC).Unix()
	openTS := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC).Unix()
	closedTS := time.Date(2026, 3, 16, 8, 0, 0, 0, time.UTC).Unix()

	// PR #100: merged, review + discussion comments + commits (1 full, 1 degraded)
	pr100 := insertPR(t, s, 100, "Add rate limiting to API",
		"Implements token bucket at 100 req/min. Closes #50.",
		"sarah", "merged", int64Ptr(ts-86400), int64Ptr(ts), int64Ptr(ts),
		"https://github.com/org/repo/pull/100")
	insertComment(t, s, pr100, "jake", "Switch to token bucket?", strPtr("api/rate_limit.go"), intPtr(42), ts-3600)
	insertComment(t, s, pr100, "jake", "Approving.", nil, nil, ts-1800)
	insertComment(t, s, pr100, "sarah", "Good point.", nil, nil, ts-900)
	insertCommit(t, s, "aaa111", "sarah", "feat: add rate limiter", ts-7200)
	insertPRCommit(t, s, pr100, "aaa111")
	insertPRCommit(t, s, pr100, "bbb222") // no matching commits row → degraded

	// PR #101: open, 1 review comment, 0 commits
	pr101 := insertPR(t, s, 101, "WIP: Add caching", "Cache layer",
		"bob", "open", int64Ptr(openTS), int64Ptr(openTS), nil,
		"https://github.com/org/repo/pull/101")
	insertComment(t, s, pr101, "alice", "Needs tests", strPtr("cache/store.go"), intPtr(15), openTS+100)

	// PR #102: closed without merge, 2 discussion comments
	pr102 := insertPR(t, s, 102, "Experiment with Redis", "Trying Redis",
		"carol", "closed", int64Ptr(closedTS), int64Ptr(closedTS), nil,
		"https://github.com/org/repo/pull/102")
	insertComment(t, s, pr102, "dave", "Not sure about this approach", nil, nil, closedTS+100)
	insertComment(t, s, pr102, "carol", "Yeah, closing this", nil, nil, closedTS+200)

	// Issues
	issue50 := insertIssue(t, s, 50, "API rate limiting needed", "Users hitting limits", "dave", "open",
		int64Ptr(ts-172800), int64Ptr(ts-172800), "https://github.com/org/repo/issues/50")
	insertIssueComment(t, s, issue50, "eve", "Agreed, this is urgent", ts-86400)
	insertIssue(t, s, 55, "Docs update needed", "README is stale", "eve", "open",
		int64Ptr(ts-86400), int64Ptr(ts-86400), "https://github.com/org/repo/issues/55")

	// Standalone commits
	insertCommit(t, s, "standalone1", "frank", "chore: update deps", ts-43200)
	insertCommit(t, s, "standalone2", "grace", "docs: fix typo", ts-21600)
	insertCommit(t, s, "standalone3", "hank", "ci: update workflow", ts-10800)

	result, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)

	// Validate counts
	assert.Equal(t, 3, result.Metadata.PRCount)
	assert.Equal(t, 2, result.Metadata.IssueCount)
	assert.Equal(t, 3, result.Metadata.CommitCount)

	// Validate PR #100
	var pr100Result *PRCluster
	for i := range result.PRClusters {
		if result.PRClusters[i].Number == 100 {
			pr100Result = &result.PRClusters[i]
		}
	}
	require.NotNil(t, pr100Result)
	assert.Equal(t, "merged", pr100Result.Status)
	assert.Equal(t, "Implements token bucket at 100 req/min. Closes #50.", pr100Result.Description)
	assert.Len(t, pr100Result.Reviews, 1)
	assert.Equal(t, "jake", pr100Result.Reviews[0].Reviewer)
	assert.Len(t, pr100Result.Reviews[0].Comments, 1) // only the one with path
	assert.Len(t, pr100Result.DiscussionComments, 2)  // jake "Approving" + sarah "Good point"
	require.Len(t, pr100Result.Commits, 2)
	// aaa111 = full metadata, bbb222 = degraded
	var fullCount, degradedCount int
	for _, c := range pr100Result.Commits {
		if c.Message != nil {
			fullCount++
		} else {
			degradedCount++
		}
	}
	assert.Equal(t, 1, fullCount)
	assert.Equal(t, 1, degradedCount)

	// Validate PR #101
	var pr101Result *PRCluster
	for i := range result.PRClusters {
		if result.PRClusters[i].Number == 101 {
			pr101Result = &result.PRClusters[i]
		}
	}
	require.NotNil(t, pr101Result)
	assert.Equal(t, "open", pr101Result.Status)
	assert.Empty(t, pr101Result.Commits)
	assert.Len(t, pr101Result.Reviews, 1)

	// Validate PR #102
	var pr102Result *PRCluster
	for i := range result.PRClusters {
		if result.PRClusters[i].Number == 102 {
			pr102Result = &result.PRClusters[i]
		}
	}
	require.NotNil(t, pr102Result)
	assert.Equal(t, "closed", pr102Result.Status)
	assert.Nil(t, pr102Result.MergedAt)
	assert.Len(t, pr102Result.DiscussionComments, 2)

	// Validate MarshalEventClusters flat JSON
	data, err := result.MarshalEventClusters()
	require.NoError(t, err)

	var elements []map[string]any
	require.NoError(t, json.Unmarshal(data, &elements))

	// Count by type
	typeCounts := map[string]int{}
	for _, el := range elements {
		typeCounts[el["type"].(string)]++
	}
	assert.Equal(t, 3, typeCounts["pull_request"])
	assert.Equal(t, 2, typeCounts["issue"])
	assert.Equal(t, 3, typeCounts["commit"])

	// Verify absent fields
	raw := string(data)
	assert.NotContains(t, raw, `"related_issue"`)
	assert.NotContains(t, raw, `"files_changed"`)
}

func TestAssembleActivity_Idempotent(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	ts := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	prID := insertPR(t, s, 42, "Test PR", "body", "alice", "merged",
		int64Ptr(ts), int64Ptr(ts), int64Ptr(ts), "https://github.com/org/repo/pull/42")
	insertComment(t, s, prID, "bob", "review", strPtr("file.go"), intPtr(10), ts)
	insertComment(t, s, prID, "carol", "discussion", nil, nil, ts+1)
	insertCommit(t, s, "aaa111", "alice", "feat: add thing", ts)
	insertPRCommit(t, s, prID, "aaa111")
	insertIssue(t, s, 10, "Bug", "desc", "dave", "open", int64Ptr(ts), int64Ptr(ts), "")
	insertCommit(t, s, "standalone", "eve", "chore: cleanup", ts)

	r1, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)
	r2, err := AssembleActivity(context.Background(), s, since, until)
	require.NoError(t, err)

	data1, err := r1.MarshalEventClusters()
	require.NoError(t, err)
	data2, err := r2.MarshalEventClusters()
	require.NoError(t, err)

	assert.JSONEq(t, string(data1), string(data2), "two runs with identical inputs must produce identical JSON")
}

func TestContextCancellation(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := AssembleActivity(ctx, s, time.Now(), time.Now())
	assert.ErrorIs(t, err, context.Canceled)
}

func TestContextDeadline(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	// Insert enough PRs to make N+1 loop take time
	ts := since.Unix()
	for i := range 5 {
		prID := insertPR(t, s, i+1, "PR", "", "a", "open", int64Ptr(ts+int64(i)), int64Ptr(ts+int64(i)), nil, "")
		for j := range 3 {
			insertComment(t, s, prID, "b", "comment", nil, nil, ts+int64(j))
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond) // ensure deadline passes

	_, err := AssembleActivity(ctx, s, since, until)
	assert.Error(t, err)
}

func TestVolumeTest(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	ts := since.Unix()
	for i := range 55 {
		prID := insertPR(t, s, i+1, "PR", "body", "author", "open",
			int64Ptr(ts+int64(i*100)), int64Ptr(ts+int64(i*100)), nil, "")
		for j := range 3 {
			insertComment(t, s, prID, "reviewer", "comment", strPtr("file.go"), nil, ts+int64(j))
		}
	}

	start := time.Now()
	result, err := AssembleActivity(context.Background(), s, since, until)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Len(t, result.PRClusters, 55)
	assert.Equal(t, 55, result.Metadata.PRCount)
	assert.Less(t, elapsed, 5*time.Second, "volume test must complete within 5 seconds")

	// Ensure all have comments
	for _, pr := range result.PRClusters {
		assert.NotEmpty(t, pr.Reviews)
	}
}
