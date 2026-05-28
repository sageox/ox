package gitutil

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureLogger returns a slog.Logger that writes JSON records into buf,
// so tests can assert on individual fields without depending on text format.
func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// parseLogRecords splits the buffer into one JSON object per line and
// returns them as parsed maps for field-by-field assertions.
func parseLogRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &rec), "line: %s", line)
		records = append(records, rec)
	}
	return records
}

// findRecord returns the first record whose msg matches.
func findRecord(records []map[string]any, msg string) map[string]any {
	for _, rec := range records {
		if m, _ := rec["msg"].(string); m == msg {
			return rec
		}
	}
	return nil
}

func TestAuditAndAbort_RebaseConflictHappyPath(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git rebase operations")
	}

	_, repo := setupDivergentRepos(t, "conflict.txt", "local-side", "remote-side")
	require.True(t, IsRebaseInProgress(repo), "precondition: rebase must be in progress")

	var buf bytes.Buffer
	logger := captureLogger(&buf)

	err := AuditAndAbort(context.Background(), repo, AuditOpRebase, "test reason", logger)
	require.NoError(t, err)

	// rebase should be cleared
	assert.False(t, IsRebaseInProgress(repo), "rebase should have been aborted")

	records := parseLogRecords(t, &buf)
	require.GreaterOrEqual(t, len(records), 2, "expected at least pre + post records, got %d", len(records))

	pre := findRecord(records, "git_abort_pre")
	require.NotNil(t, pre, "expected git_abort_pre record")
	assert.Equal(t, "rebase_abort_pre", pre["op"])
	assert.Equal(t, repo, pre["repo"])
	assert.Equal(t, "test reason", pre["reason"])
	headSHA, _ := pre["head_sha"].(string)
	assert.NotEmpty(t, headSHA)
	assert.NotEqual(t, "unknown", headSHA, "head_sha should be a real SHA, not 'unknown'")
	// rebase-in-progress on a one-file conflict → unmerged_count must be > 0
	unmergedCount, _ := pre["unmerged_count"].(float64)
	assert.Greater(t, unmergedCount, float64(0), "unmerged_count should be > 0 during conflict")
	sample, _ := pre["unmerged_sample"].(string)
	assert.Contains(t, sample, "conflict.txt", "unmerged_sample should list the conflicted file")

	post := findRecord(records, "git_abort_post")
	require.NotNil(t, post, "expected git_abort_post record")
	assert.Equal(t, "rebase_abort_post", post["op"])
	assert.Equal(t, true, post["success"])
	assert.NotEmpty(t, post["head_sha_after"])
}

func TestAuditAndAbort_AbortFailurePropagates(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}

	// Build a clean repo with no rebase in progress, then synthesize a
	// corrupted rebase dir so `git rebase --abort` actually fails. Git
	// refuses to abort a rebase whose state files are unreadable.
	repo := t.TempDir()
	gitInRepo(t, t.TempDir(), "init", "--initial-branch=main", repo)
	gitInRepo(t, repo, "config", "user.name", "test")
	gitInRepo(t, repo, "config", "user.email", "test@test.com")

	require.NoError(t, os.WriteFile(filepath.Join(repo, "f.txt"), []byte("seed"), 0o644))
	gitInRepo(t, repo, "add", "f.txt")
	gitInRepo(t, repo, "commit", "-m", "seed")

	// no rebase in progress → `git rebase --abort` exits non-zero with
	// "fatal: no rebase in progress?". That's the failure path we want
	// to exercise: AuditAndAbort must return error AND emit a *_abort_failed
	// log record.
	require.False(t, IsRebaseInProgress(repo), "precondition: no rebase in progress")

	var buf bytes.Buffer
	logger := captureLogger(&buf)

	err := AuditAndAbort(context.Background(), repo, AuditOpRebase, "synthesized failure", logger)
	require.Error(t, err, "abort should fail when no rebase is in progress")
	assert.Contains(t, err.Error(), "rebase --abort")

	records := parseLogRecords(t, &buf)
	failed := findRecord(records, "git_abort_failed")
	require.NotNil(t, failed, "expected git_abort_failed record, got: %v", records)
	assert.Equal(t, "rebase_abort_failed", failed["op"])
	assert.Equal(t, repo, failed["repo"])
}

func TestAuditAndAbort_NilLoggerDoesNotPanic(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	repo := t.TempDir()
	gitInRepo(t, t.TempDir(), "init", "--initial-branch=main", repo)

	// nil logger: must use a defensive fallback rather than panic.
	// We expect an error (no rebase in progress) but no crash.
	assert.NotPanics(t, func() {
		_ = AuditAndAbort(context.Background(), repo, AuditOpRebase, "nil-logger-test", nil)
	})
}

func TestSampleJoin(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		n     int
		want  string
	}{
		{"empty", nil, 3, ""},
		{"one", []string{"a"}, 3, "a"},
		{"under limit", []string{"a", "b"}, 3, "a, b"},
		{"at limit", []string{"a", "b", "c"}, 3, "a, b, c"},
		{"over limit", []string{"a", "b", "c", "d"}, 3, "a, b, c"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, sampleJoin(tc.paths, tc.n))
		})
	}
}
