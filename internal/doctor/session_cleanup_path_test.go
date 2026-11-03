package doctor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionOrphanedCheckDoesNotDeleteRepositoryControlledPaths(t *testing.T) {
	for _, attack := range []string{"absolute", "traversal", "symlink"} {
		t.Run(attack, func(t *testing.T) {
			project := t.TempDir()
			gitDir := filepath.Join(project, ".git")
			require.NoError(t, os.MkdirAll(gitDir, 0755))
			head := filepath.Join(gitDir, "HEAD")
			require.NoError(t, os.WriteFile(head, []byte("ref: refs/heads/main\n"), 0600))
			sessionDir := filepath.Join(project, "sessions", "ghost")
			require.NoError(t, os.MkdirAll(sessionDir, 0755))
			target := gitDir
			switch attack {
			case "traversal":
				target = sessionDir + "/../../.git"
			case "symlink":
				link := filepath.Join(project, "redirect")
				require.NoError(t, os.Symlink(project, link))
				target = filepath.Join(link, ".git")
			}
			state := session.RecordingState{
				SessionPath: target,
				ParentPID:   0x7FFFFFFF,
				StartedAt:   time.Now().Add(-72 * time.Hour),
			}
			data, err := json.Marshal(state)
			require.NoError(t, err)
			marker := filepath.Join(sessionDir, ".recording.json")
			require.NoError(t, os.WriteFile(marker, data, 0600))

			check := NewSessionOrphanedCheck(project, false) // ordinary doctor, not --fix
			check.SetHealthStatus(&session.HealthStatus{})
			check.Run(context.Background(), false)

			content, err := os.ReadFile(head)
			require.NoError(t, err)
			assert.Equal(t, "ref: refs/heads/main\n", string(content))
			assert.FileExists(t, marker, "legacy repository sessions must not be automatically reaped")
		})
	}
}
