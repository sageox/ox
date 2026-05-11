package ledger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRedactPR_NoRedactorIsNoOp verifies that without SetFieldRedactor having
// been called, content passes through unchanged. This guards the "fail safe"
// behavior: if init wiring is ever removed, tests fail loudly rather than the
// cache silently storing plaintext credentials in tests too.
func TestRedactPR_NoRedactorIsNoOp(t *testing.T) {
	SetFieldRedactor(nil) // explicit reset
	defer SetFieldRedactor(nil)

	pr := &PRFile{
		Number: 1,
		Title:  "AKIAIOSFODNN7EXAMPLE in title",
		Body:   "Authorization: Bearer ya29.tokenvalue1234567890",
	}
	out := redactPR(pr)
	assert.Equal(t, pr.Title, out.Title)
	assert.Equal(t, pr.Body, out.Body)
}

// TestRedactPR_WithRedactorScrubsAllFields verifies user-authored text fields
// are passed through the installed FieldRedactor. The forensic scan on
// 2026-05-10 found 16 Authorization: Bearer headers in PR cache files; this
// test would have caught that leak.
func TestRedactPR_WithRedactorScrubsAllFields(t *testing.T) {
	calls := 0
	SetFieldRedactor(func(s string) string {
		calls++
		return strings.ReplaceAll(s, "SECRET_VALUE", "[REDACTED]")
	})
	defer SetFieldRedactor(nil)

	pr := &PRFile{
		Number: 42,
		Title:  "fix: scrub SECRET_VALUE",
		Body:   "test plan:\n- run with SECRET_VALUE\n- assert success",
		Comments: []PRComment{
			{Author: "alice", Body: "patch includes SECRET_VALUE", CreatedAt: time.Now()},
			{Author: "bob", Body: "lgtm", CreatedAt: time.Now()},
		},
		Commits: []PRCommit{
			{SHA: "abc123", Author: "alice", Date: time.Now(), Msg: "wip: remove SECRET_VALUE"},
		},
		CreatedAt: time.Now(),
		URL:       "https://github.com/foo/bar/pull/42",
	}

	out := redactPR(pr)

	// originals untouched (caller-mutation safety)
	assert.Contains(t, pr.Title, "SECRET_VALUE")
	assert.Contains(t, pr.Body, "SECRET_VALUE")
	assert.Contains(t, pr.Comments[0].Body, "SECRET_VALUE")
	assert.Contains(t, pr.Commits[0].Msg, "SECRET_VALUE")

	// output scrubbed in every textual field
	assert.NotContains(t, out.Title, "SECRET_VALUE")
	assert.NotContains(t, out.Body, "SECRET_VALUE")
	assert.NotContains(t, out.Comments[0].Body, "SECRET_VALUE")
	assert.NotContains(t, out.Commits[0].Msg, "SECRET_VALUE")
	assert.Contains(t, out.Title, "[REDACTED]")

	// non-textual fields preserved
	assert.Equal(t, pr.Number, out.Number)
	assert.Equal(t, pr.URL, out.URL)
	assert.Equal(t, pr.Comments[1].Body, out.Comments[1].Body) // "lgtm" — no secret to scrub

	// redactor was called for every text field
	expected := 1 /* title */ + 1 /* body */ + 2 /* comments */ + 1 /* commit msg */
	assert.Equal(t, expected, calls)
}

// TestRedactIssue_WithRedactorScrubsAllFields is the IssueFile analog.
func TestRedactIssue_WithRedactorScrubsAllFields(t *testing.T) {
	SetFieldRedactor(func(s string) string {
		return strings.ReplaceAll(s, "TOKEN_VALUE", "[REDACTED]")
	})
	defer SetFieldRedactor(nil)

	issue := &IssueFile{
		Number: 100,
		Title:  "bug: TOKEN_VALUE in logs",
		Body:   "log line: TOKEN_VALUE",
		Comments: []IssueComment{
			{Author: "alice", Body: "we saw TOKEN_VALUE in prod"},
		},
		CreatedAt: time.Now(),
	}

	out := redactIssue(issue)
	assert.NotContains(t, out.Title, "TOKEN_VALUE")
	assert.NotContains(t, out.Body, "TOKEN_VALUE")
	assert.NotContains(t, out.Comments[0].Body, "TOKEN_VALUE")
	assert.Contains(t, out.Title, "[REDACTED]")
}

// TestWriteGitHubPR_RedactsBeforeDisk is the integration test that proves the
// on-disk file never contains the secret. This is the load-bearing assertion.
func TestWriteGitHubPR_RedactsBeforeDisk(t *testing.T) {
	SetFieldRedactor(func(s string) string {
		return strings.ReplaceAll(s, "SECRET_TOKEN_xyz", "[REDACTED]")
	})
	defer SetFieldRedactor(nil)

	ledgerDir := t.TempDir()
	pr := &PRFile{
		Number:    7,
		Title:     "test pr SECRET_TOKEN_xyz",
		Body:      "body with SECRET_TOKEN_xyz embedded",
		Author:    "test",
		State:     "open",
		CreatedAt: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Now(),
		URL:       "https://github.com/foo/bar/pull/7",
	}

	require.NoError(t, WriteGitHubPR(ledgerDir, pr))

	// Find the written file (filename has a content hash).
	dir := filepath.Join(ledgerDir, "data", "github", "2026", "05", "01", "pr")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "expected exactly one PR file")

	raw, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	require.NoError(t, err)
	contents := string(raw)

	// Load-bearing: the secret must not appear on disk.
	assert.NotContains(t, contents, "SECRET_TOKEN_xyz",
		"secret reached on-disk cache:\n%s", contents)
	assert.Contains(t, contents, "[REDACTED]")

	// Sanity-check the JSON is still well-formed.
	var roundTrip PRFile
	require.NoError(t, json.Unmarshal(raw, &roundTrip))
	assert.Equal(t, 7, roundTrip.Number)
}

// TestWriteGitHubIssue_RedactsBeforeDisk is the IssueFile analog.
func TestWriteGitHubIssue_RedactsBeforeDisk(t *testing.T) {
	SetFieldRedactor(func(s string) string {
		return strings.ReplaceAll(s, "CRED_xyz", "[REDACTED]")
	})
	defer SetFieldRedactor(nil)

	ledgerDir := t.TempDir()
	issue := &IssueFile{
		Number:    33,
		Title:     "CRED_xyz appears in title",
		Body:      "body says CRED_xyz too",
		Author:    "test",
		State:     "open",
		CreatedAt: time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Now(),
		URL:       "https://github.com/foo/bar/issues/33",
	}
	require.NoError(t, WriteGitHubIssue(ledgerDir, issue))

	dir := filepath.Join(ledgerDir, "data", "github", "2026", "05", "02", "issue")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	raw, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	require.NoError(t, err)
	contents := string(raw)
	assert.NotContains(t, contents, "CRED_xyz", "credential reached disk: %s", contents)
}
