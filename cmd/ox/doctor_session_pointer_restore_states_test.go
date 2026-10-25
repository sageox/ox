package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRestoreUnpushedSessionPointers_WorkingCopyStates covers working trees that disagree with the
// committed content. Without the checks, the repair would overwrite a newer local edit or mint a pointer
// the working file contradicts.
func TestRestoreUnpushedSessionPointers_WorkingCopyStates(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name         string
		mutate       func(t *testing.T, ledger string)
		wantRestored []string
		wantBlocked  string // reason fragment for the teammate artifact; empty when it is restored
	}{
		{"uncommitted local edit is never overwritten", func(t *testing.T, ledger string) {
			writeLedgerFile(t, ledger, teammateRaw, teammateContent+"{\"edited\":true}\n")
		}, []string{ownRaw}, "uncommitted local edit"},
		{"working file already the matching pointer", func(t *testing.T, ledger string) {
			ref := lfs.NewFileRef([]byte(teammateContent))
			writeLedgerFile(t, ledger, teammateRaw, lfs.FormatPointer(ref.OID, ref.Size))
		}, []string{teammateRaw, ownRaw}, ""},
		{"working file is a different pointer", func(t *testing.T, ledger string) {
			other := lfs.NewFileRef([]byte("another object\n"))
			writeLedgerFile(t, ledger, teammateRaw, lfs.FormatPointer(other.OID, other.Size))
		}, []string{ownRaw}, "different pointer"},
		{"working file missing", func(t *testing.T, ledger string) {
			require.NoError(t, os.Remove(filepath.Join(ledger, filepath.FromSlash(teammateRaw))))
		}, []string{ownRaw}, "unreadable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ledger := newWedgedLedger(t, false)
			tt.mutate(t, ledger)

			report, err := restoreUnpushedSessionPointers(ctx, ledger, true, nil)

			require.NoError(t, err)
			assert.ElementsMatch(t, tt.wantRestored, report.Restored)
			if tt.wantBlocked == "" {
				assert.Empty(t, report.Unrepairable)
				return
			}
			require.Len(t, report.Unrepairable, 1)
			assert.Equal(t, teammateRaw, report.Unrepairable[0].Path)
			assert.Contains(t, report.Unrepairable[0].Reason, tt.wantBlocked)
		})
	}
}

// TestRestoreUnpushedSessionPointers_StorageGitIsNotRaw covers artifacts the manifest declares Storage=git.
// Raw content is correct there; flagging it would have the repair fight the validator.
func TestRestoreUnpushedSessionPointers_StorageGitIsNotRaw(t *testing.T) {
	ledger := newWedgedLedger(t, false)
	gitPath := "sessions/2026-10-06T14-00-gus-OxGGGG/summary.md"
	writeLedgerFile(t, ledger, gitPath, "hello\n")
	writeLedgerFile(t, ledger, "sessions/2026-10-06T14-00-gus-OxGGGG/meta.json",
		`{"title":"git","files":{"summary.md":{"storage":"git","size":6}}}`+"\n")
	mustRunGit(t, ledger, "add", "-A")
	mustRunGit(t, ledger, "commit", "-m", "git stored summary")

	report, err := restoreUnpushedSessionPointers(context.Background(), ledger, false, nil)

	require.NoError(t, err)
	assert.NotContains(t, report.Raw, gitPath)
	assert.ElementsMatch(t, []string{teammateRaw, ownRaw}, report.Raw)
}

// TestRestoreUnpushedSessionPointers_SecondRunIsStable covers rerunning the fix. The repaired files must
// not be touched again and a leftover unknown file must keep being reported, not silently dropped.
func TestRestoreUnpushedSessionPointers_SecondRunIsStable(t *testing.T) {
	ctx := context.Background()
	ledger := newWedgedLedger(t, true)
	_, err := restoreUnpushedSessionPointers(ctx, ledger, true, nil)
	require.NoError(t, err)
	head, _ := runIsolatedGit(t, ledger, "rev-parse", "HEAD")

	report, err := restoreUnpushedSessionPointers(ctx, ledger, true, nil)

	require.NoError(t, err)
	assert.Equal(t, []string{unknownRaw}, report.Raw)
	assert.Len(t, report.Unrepairable, 1)
	assert.False(t, report.Committed)
	after, _ := runIsolatedGit(t, ledger, "rev-parse", "HEAD")
	assert.Equal(t, head, after, "a run with nothing repairable adds no commit")
}

// TestRunSessionPointerRestore_Results covers what the coworker reads from the doctor check for each state.
func TestRunSessionPointerRestore_Results(t *testing.T) {
	fixCmd := "ox doctor --fix-slug=" + CheckSlugSessionPointerRestore
	tests := []struct {
		name        string
		setup       func(t *testing.T) string
		fix         bool
		wantPassed  bool
		wantSkipped bool
		wantMessage string
		wantDetail  []string
	}{
		{"no upstream is skipped", func(t *testing.T) string {
			dir := t.TempDir()
			mustRunGit(t, dir, "init", "--initial-branch=main")
			return dir
		}, true, false, true, "no upstream", nil},
		{"clean ledger passes", func(t *testing.T) string {
			ledger := newWedgedLedger(t, false)
			mustRunGit(t, ledger, "reset", "--hard", "origin/main")
			return ledger
		}, false, true, false, "no raw session content", nil},
		{"report only names the fix command", func(t *testing.T) string { return newWedgedLedger(t, true) },
			false, false, false, "3 session artifact(s)", []string{fixCmd}},
		{"unknown file is named with its path and reason", func(t *testing.T) string { return newWedgedLedger(t, true) },
			true, false, false, "no known LFS object", []string{unknownRaw, "restored 2", fixCmd}},
		{"fully repairable ledger passes", func(t *testing.T) string { return newWedgedLedger(t, false) },
			true, true, false, "restored LFS pointers for 2", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := runSessionPointerRestore(tt.setup(t), tt.fix, nil)

			assert.Equal(t, tt.wantPassed, result.passed, "%s / %s", result.message, result.detail)
			assert.Equal(t, tt.wantSkipped, result.skipped)
			assert.Contains(t, result.message, tt.wantMessage)
			for _, fragment := range tt.wantDetail {
				assert.Contains(t, result.detail, fragment)
			}
		})
	}
}

func TestSessionPointerRestoreCheck_IsRegistered(t *testing.T) {
	check, ok := DoctorCheckRegistry[CheckSlugSessionPointerRestore]

	require.True(t, ok, "--fix-slug needs the check registered")
	assert.Equal(t, FixLevelSuggested, check.FixLevel)
}
