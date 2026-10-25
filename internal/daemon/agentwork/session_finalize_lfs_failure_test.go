package agentwork

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An unavailable LFS service must leave a retryable local session, never a
// committed fallback declaring ordinary session bytes as Git storage.
func TestSessionFinalize_LFSFailurePreservesCacheUntilRetry(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	for _, door := range []string{"regular", "upload-only", "upload-only-draft"} {
		for _, failure := range []string{"client", "batch", "blob"} {
			t.Run(door+"/"+failure, func(t *testing.T) {
				barePath, ledgerPath := setupBareAndCloneLedger(t)
				sessionName := "2026-01-15T15-00-testuser-OxLFSFAIL"
				cacheDir := writeProductionShapedSession(t, ledgerPath, sessionName)
				if door == "regular" {
					for _, artifact := range requiredArtifacts {
						require.NoError(t, os.Remove(filepath.Join(cacheDir, artifact)))
					}
				}
				if door == "upload-only-draft" {
					// Legacy raw has no ID; the committed draft is its only carrier.
					require.NoFileExists(t, filepath.Join(cacheDir, "meta.json"))
					writeDraftMeta(t, filepath.Join(ledgerPath, "sessions", sessionName))
					runGitCmd(t, ledgerPath, "add", "sessions")
					runGitCmd(t, ledgerPath, "commit", "-m", "publish session draft")
					runGitCmd(t, ledgerPath, "push", "origin", "HEAD")
				}
				handler := newGitBackedHandler()
				service := enableLocalFinalizeLFS(t, handler, ledgerPath)
				var credentialDir string
				switch failure {
				case "client":
					credentialDir = gitserver.TestSetConfigDirOverride(t.TempDir())
					t.Cleanup(func() { gitserver.TestSetConfigDirOverride(credentialDir) })
				case "batch":
					service.batchUnavailable.Store(true)
				case "blob":
					service.blobUnavailable.Store(true)
				}
				before := gitOutput(t, ledgerPath, "rev-parse", "HEAD")
				payload := &SessionFinalizePayload{
					SessionDir: cacheDir,
					RawPath:    filepath.Join(cacheDir, "raw.jsonl"),
					LedgerPath: ledgerPath,
				}
				if door == "regular" {
					payload.Missing = requiredArtifacts
					item := &WorkItem{ID: "lfs-outage", Type: sessionFinalizeType, Payload: payload}
					// Regular finalization preserves the cache and defers publication;
					// its caller observes the pending session on the next Detect pass.
					_ = handler.ProcessResult(item, &RunResult{Output: `{"title":"Recovered Session","summary":"A useful session with completed implementation and validation.","quality_score":0.8,"score_reason":"fine","outcome":"success","key_actions":["did something"]}`})
				} else {
					require.Error(t, handler.processUploadOnly(payload))
				}
				assert.Equal(t, before, gitOutput(t, ledgerPath, "rev-parse", "HEAD"), "LFS failure must not create a fallback commit")
				assert.Equal(t, before, gitOutput(t, barePath, "rev-parse", "HEAD"), "LFS failure must not publish content")
				raw, err := os.ReadFile(filepath.Join(cacheDir, "raw.jsonl"))
				require.NoError(t, err, "the original cache is required for retry")
				assert.Equal(t, testRawContent, string(raw))
				if door == "upload-only-draft" {
					meta, err := lfs.ReadSessionMeta(cacheDir)
					require.NoError(t, err)
					require.Equal(t, draftTestSessionID, meta.SessionID, "draft identity must survive outside the discarded payload")
				}

				if failure == "client" {
					gitserver.TestSetConfigDirOverride(credentialDir)
				}
				service.batchUnavailable.Store(false)
				service.blobUnavailable.Store(false)
				pending, err := handler.Detect(ledgerPath)
				require.NoError(t, err)
				require.Len(t, pending, 1, "LFS failure must remain discoverable for retry")
				require.NoError(t, handler.ProcessResult(pending[0], &RunResult{}))
				assert.NoDirExists(t, cacheDir, "cache may be pruned only after successful upload and push")
				assert.Equal(t, gitOutput(t, ledgerPath, "rev-parse", "HEAD"), gitOutput(t, barePath, "rev-parse", "HEAD"))
				committedRaw := gitOutput(t, barePath, "show", "HEAD:sessions/"+sessionName+"/raw.jsonl")
				oid, size, err := lfs.ParsePointer(committedRaw)
				require.NoError(t, err, "the recovered session must publish an LFS pointer")
				assert.Equal(t, "sha256:"+lfs.ComputeOID(raw), oid)
				assert.Equal(t, int64(len(raw)), size)
				if door == "upload-only-draft" {
					meta, err := lfs.ReadSessionMeta(filepath.Join(ledgerPath, "sessions", sessionName))
					require.NoError(t, err)
					assert.Equal(t, draftTestSessionID, meta.SessionID, "Detect retry must retain the published session link")
				}
				settled, err := handler.Detect(ledgerPath)
				require.NoError(t, err)
				assert.Empty(t, settled, "the retry must converge once LFS recovers")
			})
		}
	}
}
