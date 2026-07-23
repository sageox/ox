package recap

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- parseSessionTrailer ---
//
// Failure prevented: a valid SageOx-Session trailer silently fails to join
// back to its session because the URL shape (current vs. legacy) wasn't
// parsed, so a shipped commit's receipt goes missing from "your work".

func TestParseSessionTrailer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		value    string
		wantID   string
		wantName string
	}{
		{
			name:   "current session-id form",
			value:  "https://sageox.ai/c/ses_0199abcd",
			wantID: "ses_0199abcd",
		},
		{
			name:     "legacy repo/sessions/name/view form",
			value:    "https://sageox.ai/repo/repo_01example/sessions/2026-01-15T10-00-alice-ox1234/view",
			wantName: "2026-01-15T10-00-alice-ox1234",
		},
		{
			name:  "unresolvable value",
			value: "https://sageox.ai/some/other/path",
		},
		{
			name:  "empty value",
			value: "",
		},
		{
			name:   "multiple values joined by record separator, first resolvable wins",
			value:  "garbage\x1ehttps://sageox.ai/c/ses_second",
			wantID: "ses_second",
		},
		{
			name:   "trailing slash trimmed",
			value:  "https://sageox.ai/c/ses_trailing/",
			wantID: "ses_trailing",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotID, gotName := parseSessionTrailer(tt.value)
			assert.Equal(t, tt.wantID, gotID)
			assert.Equal(t, tt.wantName, gotName)
		})
	}
}

// --- trailerCommit.short ---

func TestTrailerCommitShort(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "abc1234", trailerCommit{sha: "abc1234567890"}.short())
	assert.Equal(t, "abc", trailerCommit{sha: "abc"}.short(), "a SHA shorter than 7 chars is returned as-is")
}

// --- gatherWork ---
//
// Failure prevented: a shipped commit either attaches to the wrong session
// (or no session), or the git-mining step blows up the whole report when
// run outside a git repo.

func TestGatherWork_JoinByID(t *testing.T) {
	if testing.Short() {
		t.Skip("short: shells git")
	}
	t.Parallel()
	f := newFixture(t)
	f.GitInit()

	f.WriteSession("s1", WithSessionID("ses_join01"), WithCreatedAt(fixtureNow), WithTitle("Fix the bug"))
	sha := f.GitCommit("Fix the bug\n\nSageOx-Session: https://sageox.ai/c/ses_join01")

	sessions := []SessionFacts{{Name: "s1", SessionID: "ses_join01", Title: "Fix the bug", CreatedAt: fixtureNow, Mine: true}}
	items := gatherWork(context.Background(), f.Project, fixtureNow.Add(-24*time.Hour), sessions)

	require.Len(t, items, 1)
	require.Len(t, items[0].Commits, 1)
	assert.Contains(t, items[0].Commits[0], sha[:7])
	assert.Contains(t, items[0].Commits[0], "Fix the bug")
}

func TestGatherWork_JoinByLegacyNameForm(t *testing.T) {
	if testing.Short() {
		t.Skip("short: shells git")
	}
	t.Parallel()
	f := newFixture(t)
	f.GitInit()

	sessionName := "2026-01-15T10-00-alice-ox1234"
	f.WriteSession(sessionName, WithSessionID(""), WithCreatedAt(fixtureNow), WithTitle("Legacy join"))
	f.GitCommit("Legacy join\n\nSageOx-Session: https://sageox.ai/repo/repo_01example/sessions/" + sessionName + "/view")

	sessions := []SessionFacts{{Name: sessionName, SessionID: "", Title: "Legacy join", CreatedAt: fixtureNow, Mine: true}}
	items := gatherWork(context.Background(), f.Project, fixtureNow.Add(-24*time.Hour), sessions)

	require.Len(t, items, 1)
	require.Len(t, items[0].Commits, 1, "a commit whose trailer uses the legacy repo/sessions/<name>/view form must join by session name")
}

func TestGatherWork_MultipleCommitsAttachWithoutDuplication(t *testing.T) {
	if testing.Short() {
		t.Skip("short: shells git")
	}
	t.Parallel()
	f := newFixture(t)
	f.GitInit()

	f.WriteSession("s1", WithSessionID("ses_multi01"), WithCreatedAt(fixtureNow), WithTitle("Multi-commit session"))
	sha1 := f.GitCommit("First commit\n\nSageOx-Session: https://sageox.ai/c/ses_multi01")
	sha2 := f.GitCommit("Second commit\n\nSageOx-Session: https://sageox.ai/c/ses_multi01")

	sessions := []SessionFacts{{Name: "s1", SessionID: "ses_multi01", Title: "Multi-commit session", CreatedAt: fixtureNow, Mine: true}}
	items := gatherWork(context.Background(), f.Project, fixtureNow.Add(-24*time.Hour), sessions)

	require.Len(t, items, 1)
	require.Len(t, items[0].Commits, 2, "each distinct commit must attach exactly once — no duplication, no drops")
	joined := items[0].Commits[0] + items[0].Commits[1]
	assert.Contains(t, joined, sha1[:7])
	assert.Contains(t, joined, sha2[:7])
	assert.NotEqual(t, items[0].Commits[0], items[0].Commits[1])
}

func TestGatherWork_CommitWithoutMatchingSessionIsIgnored(t *testing.T) {
	if testing.Short() {
		t.Skip("short: shells git")
	}
	t.Parallel()
	f := newFixture(t)
	f.GitInit()

	f.WriteSession("s1", WithSessionID("ses_known"), WithCreatedAt(fixtureNow), WithTitle("Known session"))
	f.GitCommit("Orphan commit\n\nSageOx-Session: https://sageox.ai/c/ses_unknown")

	sessions := []SessionFacts{{Name: "s1", SessionID: "ses_known", Title: "Known session", CreatedAt: fixtureNow, Mine: true}}
	items := gatherWork(context.Background(), f.Project, fixtureNow.Add(-24*time.Hour), sessions)

	require.Len(t, items, 1)
	assert.Empty(t, items[0].Commits, "a commit trailer referencing a session outside the mined set must not be force-attached anywhere")
}

func TestGatherWork_NonGitDirFailsOpen(t *testing.T) {
	t.Parallel()
	f := newFixture(t) // f.Project exists but was never git-initialized

	sessions := []SessionFacts{{Name: "s1", Title: "No git here", CreatedAt: fixtureNow, Mine: true}}
	var items []WorkItem
	assert.NotPanics(t, func() {
		items = gatherWork(context.Background(), f.Project, fixtureNow.Add(-24*time.Hour), sessions)
	})

	require.Len(t, items, 1, "sessions must still be listed even when git mining fails open")
	assert.Empty(t, items[0].Commits)
}

func TestGatherWork_EmptyProjectRoot(t *testing.T) {
	t.Parallel()
	sessions := []SessionFacts{{Name: "s1", Title: "No project root", CreatedAt: fixtureNow, Mine: true}}
	items := gatherWork(context.Background(), "", fixtureNow, sessions)
	require.Len(t, items, 1)
	assert.Empty(t, items[0].Commits)
}

func TestGatherWork_TeammateSessionsExcluded(t *testing.T) {
	t.Parallel()
	sessions := []SessionFacts{
		{Name: "mine", Title: "Mine", CreatedAt: fixtureNow, Mine: true},
		{Name: "theirs", Title: "Theirs", CreatedAt: fixtureNow, Mine: false},
	}
	items := gatherWork(context.Background(), "", fixtureNow, sessions)
	require.Len(t, items, 1, "gatherWork must only ever list the identity's own sessions")
	assert.Equal(t, "mine", items[0].Session)
}

func TestGatherWork_NewestFirstAndCapped(t *testing.T) {
	t.Parallel()
	var sessions []SessionFacts
	for i := 0; i < maxWorkItems+3; i++ {
		sessions = append(sessions, SessionFacts{
			Name:      "s" + string(rune('a'+i)),
			CreatedAt: fixtureNow.Add(time.Duration(i) * time.Hour),
			Mine:      true,
		})
	}
	items := gatherWork(context.Background(), "", fixtureNow, sessions)
	require.Len(t, items, maxWorkItems)
	// newest CreatedAt should lead
	for i := 1; i < len(items); i++ {
		assert.False(t, items[i].When.After(items[i-1].When), "work items must be ordered newest-first")
	}
}
