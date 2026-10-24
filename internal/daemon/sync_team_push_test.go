package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
	}{
		{name: "remote unchanged"},
		{name: "remote moved underneath", remoteMoved: true},
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
			_, err := s.pullTeamContext(context.Background(), clone)
			require.NoError(t, err)

			assert.Equal(t, "add team skill: deploy", gitCmd(t, bare, "log", "-1", "--format=%s", "main"),
				"the publish commit must be on the remote after one team sync")
			assert.Empty(t, gitCmd(t, clone, "log", "--format=%s", "@{upstream}..HEAD"),
				"the clone must no longer be ahead")
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
