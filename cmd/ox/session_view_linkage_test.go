package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// session_view_linkage_test.go verifies renderSessionLinkage emits the
// Pull Requests / Issues / Commits markdown sections from a session's
// meta.json. Failure prevented: linkage data lands in meta.json but never
// surfaces in `ox session view`, so the durable-linkage feature is
// invisible to the human reviewing the session.

// writeLinkageSessionMeta writes a meta.json with the given linkage fields
// into a session dir and returns a StoredSession pointing at its raw.jsonl
// (which need not exist — renderSessionLinkage only reads meta.json).
func writeLinkageSessionMeta(t *testing.T, prs, issues, commits []string) *session.StoredSession {
	t.Helper()
	sessionDir := filepath.Join(t.TempDir(), "2026-05-28T12-00-tester-Ox1234")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	meta := lfs.NewSessionMeta("2026-05-28T12-00-tester-Ox1234", "tester", "Ox1234", "claude-code",
		time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)).
		RepoID("repo_xyz").
		LinkedPRs(prs).
		LinkedIssues(issues).
		ProducedCommits(commits).
		Build()
	require.NoError(t, lfs.WriteSessionMeta(sessionDir, meta))

	return &session.StoredSession{
		Info: session.SessionInfo{
			FilePath: filepath.Join(sessionDir, "raw.jsonl"),
		},
	}
}

func TestRenderSessionLinkage_AllSections(t *testing.T) {
	st := writeLinkageSessionMeta(t,
		[]string{"https://github.com/owner/repo/pull/42"},
		[]string{"#101", "#103"},
		nil, // commits resolved via git; omit to avoid needing a repo here
	)

	// projectRoot empty: commit resolution falls back to bare SHA, but we
	// passed no commits, so only PR + Issue sections render.
	out := renderSessionLinkage(st, "")

	assert.Contains(t, out, "## Pull Requests")
	assert.Contains(t, out, "https://github.com/owner/repo/pull/42")
	assert.Contains(t, out, "## Issues")
	assert.Contains(t, out, "#101")
	assert.Contains(t, out, "#103")
	assert.NotContains(t, out, "## Commits", "no commits provided → no Commits section")
}

func TestRenderSessionLinkage_EmptyOmitsAllSections(t *testing.T) {
	st := writeLinkageSessionMeta(t, nil, nil, nil)
	out := renderSessionLinkage(st, "")
	assert.Empty(t, out, "no linkage data → no sections, not even empty headers")
}

func TestRenderSessionLinkage_PRsOnly(t *testing.T) {
	st := writeLinkageSessionMeta(t,
		[]string{"https://github.com/owner/repo/pull/7"}, nil, nil)
	out := renderSessionLinkage(st, "")
	assert.Contains(t, out, "## Pull Requests")
	assert.NotContains(t, out, "## Issues")
	assert.NotContains(t, out, "## Commits")
}

func TestRenderSessionLinkage_NilSession(t *testing.T) {
	assert.Empty(t, renderSessionLinkage(nil, ""))
}
