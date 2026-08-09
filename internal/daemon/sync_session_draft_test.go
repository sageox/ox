package daemon

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pushSessionDraftCommits is the highest-blast-radius function in the draft
// feature, and the reason is what it does NOT run.
//
// The CLI's pushLedger runs a pre-push secret gate and an LFS reconcile before
// every push. This path runs neither — and `git push` moves the whole branch.
// So the ONLY thing standing between the daemon and pushing a commit the CLI
// deliberately refused (a credential the secret scanner rejected) or a finalize
// commit whose LFS blobs are not uploaded yet, is the rule that every unpushed
// commit must be a draft commit.
//
// Delete that rule and nothing else in the codebase notices.

func newDraftPushScheduler(t *testing.T, mock *mockGitRunner) (*SyncScheduler, string) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := DefaultConfig()
	cfg.ProjectRoot = tmpDir
	cfg.LedgerPath = tmpDir
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewSyncScheduler(cfg, logger, WithGitRunner(mock)), tmpDir
}

// TestShouldPushSessionDrafts is the security property, tested on the pure
// decision rather than through the mock — the push goes through
// gitutil.PushWithRetry, which is not injectable, so an integration-only test
// could observe "one git call happened" for both outcomes and distinguish
// nothing.
//
// Every ok=false row is a way an ungated commit would otherwise reach the
// shared remote.
func TestShouldPushSessionDrafts(t *testing.T) {
	tests := []struct {
		name        string
		subjects    string // newline-separated commit subjects, newest first
		wantOK      bool
		wantBlocker string
		why         string
	}{
		{
			name:     "all draft commits",
			subjects: "session-draft: 2026-01-01T00-00-u-OxA\nsession-draft: supersede 2026-01-01T00-00-u-OxB",
			wantOK:   true,
			why:      "the only case where pushing without the secret gate is acceptable",
		},
		{
			name:        "a session finalize commit is unpushed underneath",
			subjects:    "session-draft: 2026-01-01T00-00-u-OxA\nsession: 2026-01-01T00-00-u-OxB",
			wantBlocker: "session: 2026-01-01T00-00-u-OxB",
			why: "pushing a finalize before its LFS blobs are uploaded is rejected by the " +
				"ledger's pre-receive hook, and this path has no ReconcileLFS to recover",
		},
		{
			name:        "an arbitrary CLI commit is unpushed underneath",
			subjects:    "session-draft: 2026-01-01T00-00-u-OxA\nsummary: 2026-01-01T00-00-u-OxB",
			wantBlocker: "summary: 2026-01-01T00-00-u-OxB",
			why:         "it may be a commit the CLI's pre-push secret gate refused",
		},
		{
			name:        "the blocking commit is NEWEST, not oldest",
			subjects:    "chore: something\nsession-draft: 2026-01-01T00-00-u-OxA",
			wantBlocker: "chore: something",
			why:         "order must not matter — any non-draft blocks",
		},
		{
			name:     "nothing unpushed",
			subjects: "",
			why:      "no work to do, and no blocker to report",
		},
		{
			name:     "only whitespace",
			subjects: "   \n  \n",
			why:      "an empty result must not be parsed into a phantom draft commit",
		},
		{
			name:        "a subject that merely CONTAINS the marker",
			subjects:    "fix: handle session-draft: prefixes correctly",
			wantBlocker: "fix: handle session-draft: prefixes correctly",
			why:         "the marker must anchor at the START, or any commit mentioning it slips through",
		},
		{
			name:     "a subject with leading whitespace before the marker",
			subjects: "  session-draft: 2026-01-01T00-00-u-OxA",
			wantOK:   true,
			why:      "git may pad output; a real draft commit must not be misread as a blocker",
		},
		{
			name:     "single draft commit",
			subjects: "session-draft: discard 2026-01-01T00-00-u-OxA",
			wantOK:   true,
			why:      "abort/retract commits are draft commits and must still be pushed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			blocker, ok := shouldPushSessionDrafts(tc.subjects)
			assert.Equal(t, tc.wantOK, ok, tc.why)
			assert.Equal(t, tc.wantBlocker, blocker,
				"the blocking subject is what the operator sees in the log")
		})
	}
}

// TestPushSessionDraftCommits_AlwaysChecksBeforeActing wires the pure decision
// to the real call path, so a refactor cannot leave the guard defined but
// unreferenced.
func TestPushSessionDraftCommits_AlwaysChecksBeforeActing(t *testing.T) {
	mock := &mockGitRunner{output: "session: not-a-draft"}
	s, ledgerPath := newDraftPushScheduler(t, mock)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.pushSessionDraftCommits(ctx, ledgerPath)

	require.Equal(t, int64(1), mock.calls.Load(),
		"a blocked push must read the subjects and then stop, not proceed to git operations")
	assert.Contains(t, mock.lastArgs, "log")
}

// TestPushSessionDraftCommits_ChecksSubjectsNotPathspec.
//
// The detection deliberately reads commit SUBJECTS rather than filtering by a
// pathspec on sessions/. A pathspec would also match session FINALIZE commits,
// which touch the same directory — and pushing one of those before its LFS
// blobs exist is exactly what the ledger rejects. If someone "simplifies" this
// to a pathspec, the filter silently starts approving the commits it exists to
// block.
func TestPushSessionDraftCommits_ChecksSubjectsNotPathspec(t *testing.T) {
	mock := &mockGitRunner{output: ""}
	s, ledgerPath := newDraftPushScheduler(t, mock)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.pushSessionDraftCommits(ctx, ledgerPath)

	args := strings.Join(mock.lastArgs, " ")
	assert.Contains(t, args, "--format=%s", "must read subjects")
	assert.NotContains(t, args, "sessions/",
		"must NOT filter by pathspec — that would match finalize commits too")
}

// TestPushSessionDraftCommits_GitErrorIsNonFatal — the sync cycle must survive a
// ledger that is mid-rebase or otherwise unreadable. A panic or a hang here
// stalls every other sync task behind it.
func TestPushSessionDraftCommits_GitErrorIsNonFatal(t *testing.T) {
	mock := &mockGitRunner{output: "", err: assert.AnError}
	s, ledgerPath := newDraftPushScheduler(t, mock)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	assert.NotPanics(t, func() { s.pushSessionDraftCommits(ctx, ledgerPath) })
}

// TestSessionDraftCommitPrefix_MatchesEveryWriter.
//
// The daemon's filter and the CLI's commit messages are coupled by a string.
// The CLI produces four subjects — publish/refresh, supersede, retract, discard
// — and every one of them must match, or drafts silently stop being pushed for
// that operation with no error anywhere.
//
// This pins the contract from the daemon side; the message formats live in
// cmd/ox/session_draft.go and are asserted against real commits there.
func TestSessionDraftCommitPrefix_MatchesEveryWriter(t *testing.T) {
	const name = "2026-01-01T00-00-testuser-OxPfx1"
	for _, subject := range []string{
		"session-draft: " + name,           // publish / refresh
		"session-draft: supersede " + name, // finalize takeover
		"session-draft: retract " + name,   // zero-entry stop
		"session-draft: discard " + name,   // abort
	} {
		assert.True(t, strings.HasPrefix(subject, sessionDraftCommitPrefix),
			"subject %q must match the daemon's push filter", subject)
	}

	for _, subject := range []string{
		"session: " + name,
		"summary: " + name,
		"lfs: pointerize " + name,
		"recover uncommitted sessions",
	} {
		assert.False(t, strings.HasPrefix(subject, sessionDraftCommitPrefix),
			"subject %q must NOT be treated as a draft commit", subject)
	}
}
