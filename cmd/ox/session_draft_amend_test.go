package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCommitDraftLocally_AmendsUnpushedTipDraft pins the rule that keeps the
// unpushed ledger backlog bounded: a refresh of session X amends X's draft
// commit only while that commit is the branch tip AND not yet on the remote.
// Anything already pushed is append-only history and must never be rewritten.
func TestCommitDraftLocally_AmendsUnpushedTipDraft(t *testing.T) {
	const (
		sessionX = "2026-01-01T00-00-testuser-OxAmendX"
		sessionY = "2026-01-01T00-00-testuser-OxAmendY"
	)

	tests := []struct {
		name         string
		setup        func(t *testing.T, f *draftLedgerFixture)
		wantNewCount int // commits added by the second draft write of X
		wantSubject  string
	}{
		{
			name:         "first draft for session creates a commit",
			setup:        func(t *testing.T, f *draftLedgerFixture) {},
			wantNewCount: 1,
			wantSubject:  "session-draft: " + sessionX,
		},
		{
			name: "unpushed tip draft for same session is amended",
			setup: func(t *testing.T, f *draftLedgerFixture) {
				f.publish(t, sessionX, 1)
			},
			wantNewCount: 0,
			wantSubject:  "session-draft: " + sessionX,
		},
		{
			name: "tip draft already on remote is never amended",
			setup: func(t *testing.T, f *draftLedgerFixture) {
				f.publish(t, sessionX, 1)
				runGit(t, f.ledgerPath, "push", "origin", "HEAD")
			},
			wantNewCount: 1,
			wantSubject:  "session-draft: " + sessionX,
		},
		{
			name: "tip draft for a different session is not amended",
			setup: func(t *testing.T, f *draftLedgerFixture) {
				f.publish(t, sessionY, 1)
			},
			wantNewCount: 1,
			wantSubject:  "session-draft: " + sessionX,
		},
		{
			name: "draft buried under a non-draft tip is not amended",
			setup: func(t *testing.T, f *draftLedgerFixture) {
				f.publish(t, sessionX, 1)
				require.NoError(t, os.WriteFile(filepath.Join(f.ledgerPath, "other.txt"), []byte("x"), 0o644))
				runGit(t, f.ledgerPath, "add", "other.txt")
				runGit(t, f.ledgerPath, "commit", "--no-verify", "-m", "murmur: unrelated")
			},
			wantNewCount: 1,
			wantSubject:  "session-draft: " + sessionX,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newDraftLedgerFixture(t)
			tt.setup(t, f)

			pushedBefore := runGit(t, f.ledgerPath, "rev-parse", "@{u}")
			countBefore := commitCount(t, f.ledgerPath)

			f.publish(t, sessionX, 7)

			assert.Equal(t, countBefore+tt.wantNewCount, commitCount(t, f.ledgerPath))
			assert.Equal(t, tt.wantSubject, runGit(t, f.ledgerPath, "log", "-1", "--format=%s"))
			assert.Contains(t, runGit(t, f.ledgerPath, "show", "HEAD:sessions/"+sessionX+"/meta.json"), "7",
				"tip must carry the refreshed draft content")

			// nothing the remote already has may be rewritten
			runGit(t, f.ledgerPath, "merge-base", "--is-ancestor", pushedBefore, "HEAD")
		})
	}
}
