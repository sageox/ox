package agentwork

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionFinalizeCleanupIgnoresSerializedSessionPath(t *testing.T) {
	for _, location := range []string{"cache", "tracked"} {
		t.Run(location, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
			ledger := t.TempDir()
			sessionsDir := filepath.Join(ledger, "sessions")
			if location == "cache" {
				sessionsDir = filepath.Join(ledger, ".sageox", "cache", "sessions")
			}
			sessionDir := filepath.Join(sessionsDir, "ghost")
			require.NoError(t, os.MkdirAll(sessionDir, 0755))
			gitDir := filepath.Join(ledger, ".git")
			require.NoError(t, os.MkdirAll(gitDir, 0755))
			head := filepath.Join(gitDir, "HEAD")
			require.NoError(t, os.WriteFile(head, []byte("ref: refs/heads/main\n"), 0600))
			state := session.RecordingState{
				SessionPath: gitDir,
				ParentPID:   0x7FFFFFFF,
				StartedAt:   time.Now().Add(-72 * time.Hour),
			}
			writeRecordingState(t, filepath.Join(sessionDir, recordingMarker), state)

			NewSessionFinalizeHandlerForTest(nil).Cleanup(ledger)

			content, err := os.ReadFile(head)
			require.NoError(t, err)
			assert.Equal(t, "ref: refs/heads/main\n", string(content))
			assert.NoDirExists(t, sessionDir, "only the scanned ghost should be removed")
		})
	}
}
