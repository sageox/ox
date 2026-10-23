package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGCSessionPublicationGuard(t *testing.T) {
	for _, secret := range []bool{false, true} {
		name := "ordinary recovery"
		if secret {
			name = "credential-bearing recovery"
		}
		t.Run(name, func(t *testing.T) {
			isolateCredentials(t)
			scheduler := gcTestScheduler(t)
			bare, clone := gcInitBareAndClone(t, t.TempDir())
			head := func(path string) string {
				out, err := exec.Command("git", "-C", path, "rev-parse", "HEAD").Output()
				require.NoError(t, err)
				return strings.TrimSpace(string(out))
			}
			before := head(bare)
			rel := "ordinary.txt"
			content := "ordinary recovery notes"
			if secret {
				rel = "sessions/synthetic/raw.jsonl"
				content = `{"content":"AKIAIOSFODNN7EXAMPLE"}`
			}
			abs := filepath.Join(clone, rel)
			require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0700))
			require.NoError(t, os.WriteFile(abs, []byte(content), 0600))
			require.NoError(t, exec.Command("git", "-C", clone, "add", "--", rel).Run())
			require.NoError(t, exec.Command("git", "-C", clone, "commit", "-m", "synthetic recovery").Run())
			err := scheduler.gcPushUnpushedCommits(context.Background(), WorkspaceState{Path: clone})
			if secret {
				require.ErrorContains(t, err, "committed session content refused")
				require.Equal(t, before, head(bare), "GC must not publish refused history before recloning")
				_, err := os.Stat(abs)
				require.NoError(t, err, "refused recovery stays available locally")
			} else {
				require.NoError(t, err)
				require.Equal(t, head(clone), head(bare), "ordinary GC recovery must still publish")
			}
		})
	}
}
