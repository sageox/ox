package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseQueryArgs_PositionalQuery(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"architecture decisions"})
	require.NoError(t, err)
	assert.Equal(t, "architecture decisions", qa.query)
	assert.Equal(t, "hybrid", qa.mode)
	assert.Equal(t, 5, qa.limit)
}

func TestParseQueryArgs_ModeSeparate(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"--mode", "knn", "search"})
	require.NoError(t, err)
	assert.Equal(t, "knn", qa.mode)
	assert.Equal(t, "search", qa.query)
}

func TestParseQueryArgs_ModeEquals(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"--mode=bm25", "search"})
	require.NoError(t, err)
	assert.Equal(t, "bm25", qa.mode)
}

func TestParseQueryArgs_LimitSeparate(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"--limit", "3", "search"})
	require.NoError(t, err)
	assert.Equal(t, 3, qa.limit)
}

func TestParseQueryArgs_LimitEquals(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"--limit=10", "search"})
	require.NoError(t, err)
	assert.Equal(t, 10, qa.limit)
}

func TestParseQueryArgs_TeamAndRepo(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"--team", "t1", "--repo", "r1", "q"})
	require.NoError(t, err)
	assert.Equal(t, "t1", qa.teamID)
	assert.Equal(t, "r1", qa.repoID)
	assert.Equal(t, "q", qa.query)
}

func TestParseQueryArgs_TeamAndRepoEquals(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"--team=t1", "--repo=r1", "q"})
	require.NoError(t, err)
	assert.Equal(t, "t1", qa.teamID)
	assert.Equal(t, "r1", qa.repoID)
}

func TestParseQueryArgs_AllFlags(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"--mode", "bm25", "--limit", "20", "--team", "team-abc", "--repo", "repo-xyz", "how do we deploy"})
	require.NoError(t, err)
	assert.Equal(t, "how do we deploy", qa.query)
	assert.Equal(t, "bm25", qa.mode)
	assert.Equal(t, 20, qa.limit)
	assert.Equal(t, "team-abc", qa.teamID)
	assert.Equal(t, "repo-xyz", qa.repoID)
}

func TestParseQueryArgs_InvalidLimitSeparate(t *testing.T) {
	t.Parallel()
	_, err := parseQueryArgs([]string{"--limit", "abc", "search"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --limit")
	assert.Contains(t, err.Error(), "abc")
}

func TestParseQueryArgs_InvalidLimitEquals(t *testing.T) {
	t.Parallel()
	_, err := parseQueryArgs([]string{"--limit=xyz", "search"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --limit")
	assert.Contains(t, err.Error(), "xyz")
}

func TestParseQueryArgs_InvalidMode(t *testing.T) {
	t.Parallel()
	_, err := parseQueryArgs([]string{"--mode", "semantic", "search"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid mode")
	assert.Contains(t, err.Error(), "semantic")
}

func TestParseQueryArgs_MissingQuery(t *testing.T) {
	t.Parallel()
	_, err := parseQueryArgs([]string{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query text is required")
}

func TestParseQueryArgs_OnlyFlags(t *testing.T) {
	t.Parallel()
	_, err := parseQueryArgs([]string{"--mode", "knn", "--limit", "3"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query text is required")
}

func TestParseQueryArgs_FlagsAfterQuery(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"search text", "--limit", "7", "--mode=knn"})
	require.NoError(t, err)
	assert.Equal(t, "search text", qa.query)
	assert.Equal(t, 7, qa.limit)
	assert.Equal(t, "knn", qa.mode)
}

// --k is a hidden friction alias for --limit
func TestParseQueryArgs_KAliasSeparate(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"--k", "3", "search"})
	require.NoError(t, err)
	assert.Equal(t, 3, qa.limit)
}

func TestParseQueryArgs_KAliasEquals(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"--k=10", "search"})
	require.NoError(t, err)
	assert.Equal(t, 10, qa.limit)
}

func TestParseQueryArgs_KAliasInvalidValue(t *testing.T) {
	t.Parallel()
	_, err := parseQueryArgs([]string{"--k", "abc", "search"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --limit")
}
