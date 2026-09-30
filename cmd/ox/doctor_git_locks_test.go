package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGitLockFilesResult(t *testing.T) {
	t.Parallel()

	now := time.Now()
	tests := []struct {
		name        string
		locks       map[string]time.Duration // lock name -> age
		wantPassed  bool
		wantWarning bool
	}{
		{name: "no locks passes", wantPassed: true},
		{name: "fresh lock warns", locks: map[string]time.Duration{"index.lock": time.Minute}, wantPassed: true, wantWarning: true},
		{name: "hour-old lock fails", locks: map[string]time.Duration{"index.lock": 2 * time.Hour}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gitDir := t.TempDir()
			for lock, age := range tt.locks {
				p := filepath.Join(gitDir, lock)
				require.NoError(t, os.WriteFile(p, nil, 0o644))
				require.NoError(t, os.Chtimes(p, now.Add(-age), now.Add(-age)))
			}
			got := gitLockFilesResult("ledger git locks", gitDir, now)
			require.Equal(t, tt.wantPassed, got.passed)
			require.Equal(t, tt.wantWarning, got.warning)
			for lock := range tt.locks {
				// exact path, never a one-element brace expansion the shell won't expand
				require.Contains(t, got.detail, "rm "+filepath.Join(gitDir, lock))
			}
		})
	}
}
