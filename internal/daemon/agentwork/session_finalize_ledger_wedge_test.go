package agentwork

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// wedgeLedger leaves the ledger index with an unmerged meta.json (UU state), the
// condition a failed conflicting merge or rebase leaves behind for a human.
func wedgeLedger(t *testing.T, ledgerPath string) {
	t.Helper()
	metaPath := filepath.Join(ledgerPath, "sessions", "other", "meta.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(metaPath), 0o755))
	require.NoError(t, os.WriteFile(metaPath, []byte("base\n"), 0o644))
	runGitCmd(t, ledgerPath, "add", "--sparse", "sessions/other/meta.json")
	runGitCmd(t, ledgerPath, "commit", "--no-verify", "-m", "base")
	runGitCmd(t, ledgerPath, "checkout", "-b", "side")
	require.NoError(t, os.WriteFile(metaPath, []byte("side\n"), 0o644))
	runGitCmd(t, ledgerPath, "commit", "--no-verify", "-am", "side")
	runGitCmd(t, ledgerPath, "checkout", "-")
	require.NoError(t, os.WriteFile(metaPath, []byte("main\n"), 0o644))
	runGitCmd(t, ledgerPath, "commit", "--no-verify", "-am", "main")

	cmd := exec.Command("git", "merge", "side")
	cmd.Dir = ledgerPath
	require.Error(t, cmd.Run(), "merge must conflict to produce UU state")
	require.Contains(t, gitOutput(t, ledgerPath, "ls-files", "--unmerged"), "sessions/other/meta.json")
}

func uploadOnlyItem(t *testing.T, ledgerPath string) *WorkItem {
	t.Helper()
	name := "2026-01-15T10-00-testuser-OxWEDGE"
	cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", name)
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "raw.jsonl"), []byte(testRawContent), 0o644))
	for _, artifact := range requiredArtifacts {
		require.NoError(t, os.WriteFile(filepath.Join(cacheDir, artifact), []byte("artifact content"), 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "meta.json"), []byte(`{"session_name":"`+name+`"}`), 0o644))
	return &WorkItem{
		ID:   "wedge-" + name,
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: cacheDir,
			RawPath:    filepath.Join(cacheDir, "raw.jsonl"),
			LedgerPath: ledgerPath,
			UploadOnly: true,
		},
	}
}

// Failure prevented: a ledger wedged on unmerged files made the daemon re-upload
// the same session blobs to LFS every scan (1,083 times in 29 hours) before the
// commit transaction rejected them.
func TestProcessResult_UnmergedLedgerSkipsLFSUpload(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	tests := []struct {
		name         string
		wedge        bool
		wantErr      error
		wantUploaded bool
	}{
		{name: "unmerged ledger refuses before any LFS traffic", wedge: true, wantErr: ErrLedgerUnresolved, wantUploaded: false},
		// negative control: the same flow on a healthy ledger does reach LFS
		{name: "healthy ledger uploads", wedge: false, wantUploaded: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, clonePath := setupBareAndCloneLedger(t)
			handler := NewSessionFinalizeHandler(slog.Default())
			handler.ledgerMu = &sync.Mutex{}
			lfsService := enableLocalFinalizeLFS(t, handler, clonePath)
			item := uploadOnlyItem(t, clonePath)
			if tt.wedge {
				wedgeLedger(t, clonePath)
			}

			err := handler.ProcessResult(item, &RunResult{})

			if tt.wantErr != nil {
				require.True(t, errors.Is(err, tt.wantErr), "got %v", err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.wantUploaded, lfsService.batchCalls.Load() > 0)
		})
	}
}

// Failure prevented: 158 sessions with corrupt recording state re-logged the same
// warning every 5-minute scan (113k lines in 29 hours).
func TestLogStaleRecoveryDeferred_WarnsOncePerSession(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	handler := NewSessionFinalizeHandler(logger)
	cause := errors.New("parse recording state: invalid character")

	for i := 0; i < 5; i++ {
		handler.logStaleRecoveryDeferred("s1", cause)
	}
	handler.logStaleRecoveryDeferred("s2", cause)

	require.Equal(t, 1, strings.Count(buf.String(), "session=s1"), "repeat scans of one session stay quiet")
	require.Equal(t, 1, strings.Count(buf.String(), "session=s2"), "a different session still warns")
}
