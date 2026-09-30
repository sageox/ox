package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon"
	"github.com/stretchr/testify/require"
)

func TestClassifyLedgerSync(t *testing.T) {
	t.Parallel()

	backoff := daemon.DaemonIssue{
		Type:    daemon.IssueTypeSyncBackoff,
		Repo:    "ledger",
		Summary: "Sync suspended after 4 consecutive failures (retrying at 3:04PM)",
	}

	tests := []struct {
		name  string
		facts ledgerSyncFacts
		want  []string
	}{
		{
			name:  "clean ledger is synced",
			facts: ledgerSyncFacts{UpstreamKnown: true},
			want:  nil,
		},
		{
			name:  "ahead only is still synced (sync is a pull)",
			facts: ledgerSyncFacts{UpstreamKnown: true, Ahead: 3},
			want:  nil,
		},
		{
			name:  "unknown upstream is not evidence of a problem",
			facts: ledgerSyncFacts{Behind: 9},
			want:  nil,
		},
		{
			name:  "backoff issue on the ledger",
			facts: ledgerSyncFacts{Issues: []daemon.DaemonIssue{backoff}},
			want:  []string{backoff.Summary},
		},
		{
			name: "issues for other repos and non-blocking types are ignored",
			facts: ledgerSyncFacts{Issues: []daemon.DaemonIssue{
				{Type: daemon.IssueTypeSyncBackoff, Repo: "team_abc", Summary: "team backoff"},
				{Type: daemon.IssueTypeAuthExpiring, Repo: "ledger", Summary: "token expiring"},
			}},
			want: nil,
		},
		{
			name: "duplicate summaries are reported once; empty summary falls back to type",
			facts: ledgerSyncFacts{Issues: []daemon.DaemonIssue{
				backoff, backoff,
				{Type: daemon.IssueTypeDiverged, Repo: "ledger"},
			}},
			want: []string{backoff.Summary, daemon.IssueTypeDiverged},
		},
		{
			name: "the observed incident: backoff, diverged, wedged rebase, stale lock",
			facts: ledgerSyncFacts{
				Issues:           []daemon.DaemonIssue{backoff},
				RebaseInProgress: true,
				StaleLocks:       []string{"index.lock"},
				UpstreamKnown:    true,
				Ahead:            19,
				Behind:           3561,
			},
			want: []string{
				backoff.Summary,
				"wedged rebase in progress",
				"stale git lock: index.lock",
				"ahead 19 / behind 3561",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, classifyLedgerSync(tt.facts))
		})
	}
}

func TestLedgerNotSyncedError_NamesTheFix(t *testing.T) {
	t.Parallel()
	got := ledgerNotSyncedError([]string{"Sync suspended after 4 consecutive failures", "ahead 19 / behind 3561"})
	require.Equal(t, "Sync suspended after 4 consecutive failures; ahead 19 / behind 3561; run `ox doctor`", got)
}

func TestWriteLedgerSyncLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		ledger SyncLedgerResult
		want   string
	}{
		{name: "synced", ledger: SyncLedgerResult{Status: "synced"}, want: "  Ledger: synced\n"},
		{
			name:   "not synced carries the reason on the same line",
			ledger: SyncLedgerResult{Status: ledgerSyncStatusNotSynced, Error: "ahead 1 / behind 2; run `ox doctor`"},
			want:   "  Ledger: NOT synced — ahead 1 / behind 2; run `ox doctor`\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			require.NoError(t, writeLedgerSyncLine(&buf, tt.ledger))
			require.Equal(t, tt.want, buf.String())
		})
	}
}

func TestStaleLedgerLocks_OnlyReportsOldLocks(t *testing.T) {
	t.Parallel()

	ledger := t.TempDir()
	gitDir := filepath.Join(ledger, ".git")
	require.NoError(t, os.MkdirAll(gitDir, 0o755))

	now := time.Now()
	old := filepath.Join(gitDir, "index.lock")
	require.NoError(t, os.WriteFile(old, nil, 0o644))
	require.NoError(t, os.Chtimes(old, now.Add(-2*ledgerSyncLockAge), now.Add(-2*ledgerSyncLockAge)))
	require.NoError(t, os.WriteFile(filepath.Join(gitDir, "HEAD.lock"), nil, 0o644))

	require.Equal(t, []string{"index.lock"}, staleLedgerLocks(ledger, now))
}

func TestGatherLedgerSyncFacts_NoStatusOrPathIsEmpty(t *testing.T) {
	t.Parallel()
	require.Empty(t, classifyLedgerSync(gatherLedgerSyncFacts(nil)))
	require.Empty(t, classifyLedgerSync(gatherLedgerSyncFacts(&daemon.StatusData{LedgerPath: filepath.Join(t.TempDir(), "missing")})))
}
