package agentwork

import (
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/lfs"
)

type uploadedCall struct {
	projectRoot string
	sessionName string
	sessionID   string
}

// gitBackedNotifyHandler is a handler that pushes to a real local ledger and
// records each upload-confirmed signal instead of sending it.
func gitBackedNotifyHandler(t *testing.T, clonePath string) (*SessionFinalizeHandler, *[]uploadedCall) {
	t.Helper()
	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipLFS = true
	handler.skipGit = false
	handler.ledgerMu = &sync.Mutex{}
	enableLocalFinalizeLFS(t, handler, clonePath)
	var calls []uploadedCall
	handler.notifyUploaded = func(projectRoot string, meta *lfs.SessionMeta, sessionName string) ([]api.PRLinkMiss, bool) {
		calls = append(calls, uploadedCall{projectRoot, sessionName, meta.EffectiveSessionID()})
		return nil, true
	}
	return handler, &calls
}

func cacheSession(t *testing.T, clonePath, sessionName string, finalized bool) string {
	t.Helper()
	cacheDir := filepath.Join(clonePath, ".sageox", "cache", "sessions", sessionName)
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "raw.jsonl"), []byte(testRawContent), 0o644))
	if finalized {
		for _, name := range requiredArtifacts {
			require.NoError(t, os.WriteFile(filepath.Join(cacheDir, name), []byte("artifact content"), 0o644))
		}
		meta := `{"session_name":"` + sessionName + `","session_id":"` + draftTestSessionID + `"}`
		require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "meta.json"), []byte(meta), 0o644))
	}
	return cacheDir
}

// A session the daemon pushes must tell the server it is uploaded, as the
// CLI's stop and recover paths do. Without the signal the lifecycle row stays
// "recording" and /c/<session_id> reads "in progress" indefinitely (ox#1826).
// Claude Code sessions always finalize in the daemon, so this is their path.
func TestProcessResult_PushedSessionSendsUploadedSignal(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	llmOutput := `{"title":"Test Session","summary":"A test session.","quality_score":0.8,"score_reason":"fine","outcome":"success","key_actions":["did something"]}`

	for _, tc := range []struct {
		name       string
		uploadOnly bool
	}{
		{"summarized then pushed", false},
		{"upload-only", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, clonePath := setupBareAndCloneLedger(t)
			sessionName := "2026-11-08T10-00-testuser-OxNOTIFY"
			cacheDir := cacheSession(t, clonePath, sessionName, tc.uploadOnly)
			handler, calls := gitBackedNotifyHandler(t, clonePath)

			payload := &SessionFinalizePayload{
				SessionDir: cacheDir,
				RawPath:    filepath.Join(cacheDir, "raw.jsonl"),
				LedgerPath: clonePath,
				UploadOnly: tc.uploadOnly,
			}
			result := &RunResult{}
			if !tc.uploadOnly {
				payload.Missing = requiredArtifacts
				result.Output = llmOutput
			}
			require.NoError(t, handler.ProcessResult(&WorkItem{ID: "notify", Type: sessionFinalizeType, Payload: payload}, result))

			require.Len(t, *calls, 1, "the pushed session must send exactly one uploaded signal")
			got := (*calls)[0]
			assert.Equal(t, handler.projectRoot, got.projectRoot)
			assert.Equal(t, sessionName, got.sessionName)
			published, err := lfs.ReadSessionMeta(filepath.Join(clonePath, "sessions", sessionName))
			require.NoError(t, err)
			assert.NotEmpty(t, got.sessionID)
			assert.Equal(t, published.EffectiveSessionID(), got.sessionID, "the signal names the session the Ledger now holds")
			if tc.uploadOnly {
				assert.Equal(t, draftTestSessionID, got.sessionID)
			}
		})
	}
}

// A session whose push failed is not on the Ledger's origin. Announcing it
// would show an uploaded session whose content nobody can fetch.
func TestProcessResult_FailedPushSendsNoUploadedSignal(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	_, clonePath := setupBareAndCloneLedger(t)
	runGitCmd(t, clonePath, "remote", "set-url", "origin", "/nonexistent/broken/remote.git")
	cacheDir := cacheSession(t, clonePath, "2026-11-08T10-05-testuser-OxNOPUSH", true)
	handler, calls := gitBackedNotifyHandler(t, clonePath)

	err := handler.ProcessResult(&WorkItem{ID: "no-notify", Type: sessionFinalizeType, Payload: &SessionFinalizePayload{
		SessionDir: cacheDir, RawPath: filepath.Join(cacheDir, "raw.jsonl"), LedgerPath: clonePath, UploadOnly: true,
	}}, &RunResult{})

	require.Error(t, err)
	assert.Empty(t, *calls)
}
