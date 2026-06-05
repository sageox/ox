package main

import (
	"strings"
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hooks_post_rewrite_test.go covers the SHA-rewrite path. The handler
// reads a git old→new mapping and rewrites entries in the active
// recording's ProducedCommits index. Failure here means rebases and
// amends silently leave the index pointing at SHAs no longer in history.

func TestParsePostRewriteStdin_Mapping(t *testing.T) {
	in := strings.NewReader("aaa1111 bbb2222\n" +
		"ccc3333 ddd4444 extra-rebase-column\n" +
		"\n" +
		"   eee5555    fff6666\n" +
		"not-a-sha lol\n")
	m, err := parsePostRewriteStdin(in)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"aaa1111": "bbb2222",
		"ccc3333": "ddd4444",
		"eee5555": "fff6666",
	}, m)
}

func TestRewriteSHAs_AmendSingleEntry(t *testing.T) {
	s := &session.RecordingState{ProducedCommits: []string{"old1234"}}
	rewriteSHAs(s, map[string]string{"old1234": "new5678"})
	assert.Equal(t, []string{"new5678"}, s.ProducedCommits)
}

func TestRewriteSHAs_RebaseMultiple(t *testing.T) {
	s := &session.RecordingState{ProducedCommits: []string{"a1", "b2", "c3"}}
	rewriteSHAs(s, map[string]string{
		"a1": "A1",
		"b2": "B2",
		"c3": "C3",
	})
	assert.Equal(t, []string{"A1", "B2", "C3"}, s.ProducedCommits)
}

// TestRewriteSHAs_SquashCollapses verifies that when N commits collapse
// into one (all entries map to the same new SHA), the list collapses
// rather than carrying N duplicate entries.
// Failure prevented: post-squash `ox session view` showing 3 lines that
// all resolve to the same commit.
func TestRewriteSHAs_SquashCollapses(t *testing.T) {
	s := &session.RecordingState{ProducedCommits: []string{"a1", "b2", "c3"}}
	rewriteSHAs(s, map[string]string{
		"a1": "SQUASHED",
		"b2": "SQUASHED",
		"c3": "SQUASHED",
	})
	assert.Equal(t, []string{"SQUASHED"}, s.ProducedCommits,
		"three commits squashed into one should leave one entry")
}

// TestRewriteSHAs_UnrelatedSHAsUntouched verifies that entries NOT in
// the rewrite mapping pass through unchanged. Without this, a partial
// rebase mid-list would zero out trailing entries.
func TestRewriteSHAs_UnrelatedSHAsUntouched(t *testing.T) {
	s := &session.RecordingState{ProducedCommits: []string{"a1", "b2", "c3"}}
	rewriteSHAs(s, map[string]string{"b2": "B2_NEW"})
	assert.Equal(t, []string{"a1", "B2_NEW", "c3"}, s.ProducedCommits)
}

// TestRewriteSHAs_EmptyMapping is a no-op safety check.
func TestRewriteSHAs_EmptyMapping(t *testing.T) {
	s := &session.RecordingState{ProducedCommits: []string{"a1", "b2"}}
	rewriteSHAs(s, map[string]string{})
	assert.Equal(t, []string{"a1", "b2"}, s.ProducedCommits)
}

// TestLooksLikeSHA covers the shape guard. Defensive: we accept any
// hex of plausible length so abbreviated SHAs from `rebase -i` reword
// (which sometimes emits short hashes) work.
func TestLooksLikeSHA(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"abc1234", true}, // 7-char short
		{"abcdef1234567890abcdef1234567890abcdef12", true},                         // 40-char SHA-1
		{"abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890", true}, // 64-char SHA-256
		{"", false},                      // empty
		{"abc12", false},                 // too short
		{strings.Repeat("a", 65), false}, // too long
		{"abcdefg", false},               // bad hex
		{"ABC1234", true},                // uppercase ok
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, looksLikeSHA(c.s), "looksLikeSHA(%q)", c.s)
	}
}

// TestCollapseAdjacentDuplicates covers the helper directly.
func TestCollapseAdjacentDuplicates(t *testing.T) {
	assert.Equal(t, []string{"a", "b", "a"},
		collapseAdjacentDuplicates([]string{"a", "a", "b", "a", "a"}),
		"collapse only adjacent runs; non-adjacent repeats stay")
}

// TestPostRewrite_NoopWithoutRecording verifies the cobra entry point
// exits cleanly when no recording is active.
func TestPostRewrite_NoopWithoutRecording(t *testing.T) {
	projectRoot, _, agentID := trailerRewriteEnv(t)
	require.NoError(t, session.ClearRecordingStateForAgent(projectRoot, agentID))
	t.Setenv("SAGEOX_AGENT_ID", "")
	assert.NoError(t, runHooksPostRewrite(nil, nil))
}
