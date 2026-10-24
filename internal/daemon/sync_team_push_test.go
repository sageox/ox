package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The push decision is the whole safety property of the daemon's Team Context
// push leg: `git push` moves the branch, so one foreign commit underneath a
// publish must stop it.
func TestShouldPushTeamPublishes(t *testing.T) {
	tests := []struct {
		name        string
		subjects    string
		wantOK      bool
		wantBlocker string
	}{
		{name: "nothing unpushed", subjects: "", wantOK: false},
		{name: "one publish", subjects: "add team skill: deploy\n", wantOK: true},
		{name: "several publishes", subjects: "add team skill: a, b\nadd team skill: c\n", wantOK: true},
		{name: "a coworker's own commit underneath", subjects: "add team skill: deploy\nfix the onboarding doc\n",
			wantBlocker: "fix the onboarding doc"},
		// ox#1286: a daemon housekeeping commit must never ride a publish out.
		{name: "daemon housekeeping commit", subjects: "chore: add .sageox/.gitignore to exclude daemon cache files\nadd team skill: deploy",
			wantBlocker: "chore: add .sageox/.gitignore to exclude daemon cache files"},
		{name: "prefix must be the subject start", subjects: "revert add team skill: deploy", wantBlocker: "revert add team skill: deploy"},
		{name: "blank lines between subjects are not commits", subjects: "add team skill: a\n\n   \nadd team skill: b\n", wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			blocker, ok := ShouldPushTeamPublishes(tt.subjects)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantBlocker, blocker)
		})
	}
}

// commitLocal writes one file and commits it in the clone with subject.
func commitLocal(t *testing.T, clone, rel, subject string) {
	t.Helper()
	path := filepath.Join(clone, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("content of "+rel+"\n"), 0o644))
	gitCmd(t, clone, "add", rel)
	gitCmd(t, clone, "commit", "-m", subject)
}

// A Team Context one commit ahead with a skill publish must reach the remote on
// the daemon's next team pull — including when the remote did not move.
// Failure prevented (ox#1477): `ox skills publish` committed locally and printed
// "distribution is automatic", but the daemon only ever pulled team contexts, so
// the commit sat `[ahead 1]` and no coworker ever received the skill.
func TestPullTeamContext_PushesALocalPublishCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	for _, tc := range []struct {
		name        string
		remoteMoved bool
		endpoint    string
	}{
		{name: "remote unchanged"},
		{name: "remote moved underneath", remoteMoved: true},
		// With an endpoint the push refreshes credentials first; a remote that
		// is not on that endpoint's server must be pushed as-is, never rewritten.
		{name: "endpoint configured", endpoint: "https://api.test.sageox.ai"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clone := filepath.Join(t.TempDir(), "team")
			require.NoError(t, os.MkdirAll(clone, 0o755))
			setupGitRepo(t, clone)
			bare := bareRepoPath(clone)
			commitLocal(t, clone, "agents/skills/deploy/SKILL.md", "add team skill: deploy")
			if tc.remoteMoved {
				pushFromSeparateClone(t, bare, "remote-doc.md", "teammate's doc")
			}

			s := newPullTestScheduler(t, clone)
			if tc.endpoint != "" {
				s.workspaceRegistry.mu.Lock()
				s.workspaceRegistry.endpoint = tc.endpoint
				s.workspaceRegistry.mu.Unlock()
			}
			_, err := s.pullTeamContext(context.Background(), clone)
			require.NoError(t, err)

			assert.Equal(t, "add team skill: deploy", gitCmd(t, bare, "log", "-1", "--format=%s", "main"),
				"the publish commit must be on the remote after one team sync")
			assert.Empty(t, gitCmd(t, clone, "log", "--format=%s", "@{upstream}..HEAD"),
				"the clone must no longer be ahead")
			assert.Equal(t, bare, gitCmd(t, clone, "remote", "get-url", "origin"),
				"the credential refresh rewrote a remote that is not on the endpoint's server")
		})
	}
}

// A commit ox did not make keeps the whole branch local, publish included.
func TestPullTeamContext_LeavesAForeignUnpushedCommitAlone(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	clone := filepath.Join(t.TempDir(), "team")
	require.NoError(t, os.MkdirAll(clone, 0o755))
	setupGitRepo(t, clone)
	bare := bareRepoPath(clone)
	before := gitCmd(t, bare, "rev-parse", "main")
	commitLocal(t, clone, "docs/draft.md", "my private draft")
	commitLocal(t, clone, "agents/skills/deploy/SKILL.md", "add team skill: deploy")

	s := newPullTestScheduler(t, clone)
	_, err := s.pullTeamContext(context.Background(), clone)
	require.NoError(t, err)

	assert.Equal(t, before, gitCmd(t, bare, "rev-parse", "main"), "the daemon published a commit that was not ox's")
}

// rejectPushes installs a pre-receive hook on the bare remote that refuses every
// push with message, as a server-side rejection reads to git.
func rejectPushes(t *testing.T, bare, message string) {
	t.Helper()
	hook := filepath.Join(bare, "hooks", "pre-receive")
	require.NoError(t, os.MkdirAll(filepath.Dir(hook), 0o755))
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\necho \""+message+"\" >&2\nexit 1\n"), 0o755))
}

// A publish push the remote refuses is a WARN naming the clone, and the commit
// stays local for the next sync's retry — never dropped, never reported pushed.
func TestPushTeamPublishCommits_ARejectedPushKeepsTheCommitAndWarns(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	clone := filepath.Join(t.TempDir(), "team")
	require.NoError(t, os.MkdirAll(clone, 0o755))
	setupGitRepo(t, clone)
	bare := bareRepoPath(clone)
	before := gitCmd(t, bare, "rev-parse", "main")
	commitLocal(t, clone, "agents/skills/deploy/SKILL.md", "add team skill: deploy")
	// "Permission denied" is a permanent rejection, so the push fails on its
	// first attempt instead of spending the retry backoff.
	rejectPushes(t, bare, "Permission denied (test remote)")

	s := newPullTestScheduler(t, clone)
	h := &recordingHandler{}
	s.logger = slog.New(h)
	s.pushTeamPublishCommits(context.Background(), clone)

	assert.Equal(t, before, gitCmd(t, bare, "rev-parse", "main"))
	assert.Equal(t, "add team skill: deploy", gitCmd(t, clone, "log", "--format=%s", "@{upstream}..HEAD"),
		"the publish commit must survive a rejected push for the next sync to retry")
	warned := h.byMessage("team publish push failed (will retry next sync)")
	require.Len(t, warned, 1)
	assert.Equal(t, slog.LevelWarn, warned[0].Level)
	attrs := attrsOf(warned[0])
	assert.Equal(t, clone, attrs["path"])
	assert.Contains(t, attrs["error"], "Permission denied")
}

// A Team Context whose pushes are suspended (an LFS rejection no repair could
// fix) is skipped before git runs: re-pushing it only repeats the rejection.
func TestPushTeamPublishCommits_SkipsAWedgedTeamContext(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	clone := filepath.Join(t.TempDir(), "team")
	require.NoError(t, os.MkdirAll(clone, 0o755))
	setupGitRepo(t, clone)
	bare := bareRepoPath(clone)
	before := gitCmd(t, bare, "rev-parse", "main")
	commitLocal(t, clone, "agents/skills/deploy/SKILL.md", "add team skill: deploy")

	// Trip the process-wide breaker the way a ledger push does. It is keyed by
	// this test's own temp path, so no other test sees it.
	rejectPushes(t, bare, "LFS objects are missing")
	err := gitutil.PushWithRetry(context.Background(), clone, gitutil.PushOpts{
		MaxRetries:        1,
		OpTimeout:         20 * time.Second,
		Logger:            slog.New(slog.DiscardHandler),
		SuspendWhenWedged: true,
		ReconcileLFS:      func(string) (bool, error) { return false, nil },
	})
	require.ErrorIs(t, err, gitutil.ErrPushWedged)
	_, wedged := gitutil.PushWedgedUntil(clone)
	require.True(t, wedged)
	// The remote would accept the push now; only the breaker stands in the way.
	require.NoError(t, os.Remove(filepath.Join(bare, "hooks", "pre-receive")))

	s := newPullTestScheduler(t, clone)
	h := &recordingHandler{}
	s.logger = slog.New(h)
	s.pushTeamPublishCommits(context.Background(), clone)

	assert.Equal(t, before, gitCmd(t, bare, "rev-parse", "main"), "a wedged Team Context was pushed")
	skipped := h.byMessage("skipping team publish push: push wedged")
	require.Len(t, skipped, 1)
	assert.Equal(t, clone, attrsOf(skipped[0])["path"])
	assert.Empty(t, h.byMessage("team publish push failed (will retry next sync)"))
}
