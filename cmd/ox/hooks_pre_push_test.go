package main

import (
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hooks_pre_push_test.go covers the pure parsing + extraction logic of the
// pre-push linkage hook. Branch/PR resolution and the recording-state write
// are integration-tested via the shared trailerRewriteEnv harness; the
// stdin parser and issue-ref extractor are unit-tested here because they
// carry the regex edge cases that silently mislink or drop issue refs.

func TestParsePrePushStdin(t *testing.T) {
	in := strings.NewReader(
		"refs/heads/feature aaaa1111 refs/heads/feature 00000000\n" +
			"refs/heads/main bbbb2222 refs/heads/main cccc3333\n" +
			"refs/heads/deleted 0000000000000000000000000000000000000000 refs/heads/deleted dddd4444\n" +
			"malformed line\n" +
			"\n")
	refs := parsePrePushStdin(in)
	// deletion (local-sha all zeros) dropped; malformed dropped; blank dropped
	assert.Len(t, refs, 2)
	assert.Equal(t, "feature", strings.TrimPrefix(refs[0].localRef, "refs/heads/"))
	assert.Equal(t, "00000000", refs[0].remoteSHA) // new branch (short zero in fixture)
	assert.Equal(t, "cccc3333", refs[1].remoteSHA) // existing branch
}

// TestParseIssueRefs verifies closing-keyword extraction and that bare
// mentions are NOT captured.
// Failure prevented: linking every "#12" mention (e.g. "see #12") would
// pollute LinkedIssues; missing "Fixes #N" would drop real linkage.
func TestParseIssueRefs(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"fixes", "feat: thing\n\nFixes #42", []string{"#42"}},
		{"closes colon", "Closes: #7", []string{"#7"}},
		{"resolves lower", "resolves #100", []string{"#100"}},
		{"gh dash form", "Done\n\nGH-9", []string{"#9"}},
		{"multiple unique", "Fixes #1\nCloses #2\nfixes #1", []string{"#1", "#2"}},
		{"bare mention not captured", "see #12 for context", nil},
		{"fixed past tense", "Fixed #5", []string{"#5"}},
		{"no refs", "just a normal commit", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseIssueRefs(c.body)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestMergeUnique(t *testing.T) {
	assert.Equal(t,
		[]string{"a", "b", "c"},
		mergeUnique([]string{"a", "b"}, []string{"b", "c", "", "a"}),
		"merge skips dupes and empties, preserves order")
	assert.Equal(t,
		[]string{"x"},
		mergeUnique(nil, []string{"x", "x"}))
}

func TestMapKeys(t *testing.T) {
	m := map[string]struct{}{"#1": {}, "#2": {}, "#3": {}}
	got := mapKeys(m)
	sort.Strings(got)
	assert.Equal(t, []string{"#1", "#2", "#3"}, got)
}

// TestPrePush_RecordsIssueRefsOnActiveRecording exercises the full handler
// against a real git repo with an active recording: commits carrying
// "Fixes #N" footers, fed through runHooksPrePush via the pre-push stdin
// stream, must land in the recording's LinkedIssues.
// Failure prevented: the parse→range→state-write pipeline silently drops
// issue linkage even when commits clearly reference issues.
func TestPrePush_RecordsIssueRefsOnActiveRecording(t *testing.T) {
	projectRoot, sessionName, _ := trailerRewriteEnv(t)

	// two commits, one referencing an issue
	first := commitWithTrailer(t, projectRoot, "feat: base")
	commitWithIssueRef(t, projectRoot, "fix: thing\n\nFixes #77")
	head := strings.TrimSpace(captureGitTrailer(t, projectRoot, "rev-parse", "HEAD"))

	// simulate a push of first..head onto an existing remote branch by
	// feeding the pre-push ref line on stdin. remoteSHA = first (already
	// pushed), localSHA = head.
	stdin := "refs/heads/main " + head + " refs/heads/main " + first + "\n"
	withStdin(t, stdin, func() {
		hooksPrePushRemote, hooksPrePushURL = "origin", "https://example.com/repo.git"
		require.NoError(t, runHooksPrePush(nil, nil))
	})

	state := loadRecordingForTest(t, projectRoot, sessionName)
	assert.Contains(t, state.LinkedIssues, "#77",
		"pre-push must record issue refs from the pushed commit range")
}

// commitWithIssueRef commits with an explicit message (bypasses trailer
// injection — we only care about the message body for issue parsing).
func commitWithIssueRef(t *testing.T, projectRoot, message string) {
	t.Helper()
	makeContentChange(t, projectRoot, message)
	requireGitTrailer(t, projectRoot, "add", "-A")
	requireGitTrailer(t, projectRoot, "commit", "-m", message)
}

// withStdin temporarily replaces os.Stdin with a pipe carrying data for the
// duration of fn. Restores the original afterward.
func withStdin(t *testing.T, data string, fn func()) {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig })
	go func() {
		_, _ = w.WriteString(data)
		_ = w.Close()
	}()
	fn()
	_ = r.Close()
}
