package gitutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWaitForLockFiles(t *testing.T) {
	t.Parallel()

	t.Run("clear dir returns immediately", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, WaitForLockFiles(context.Background(), t.TempDir(), time.Second))
	})

	t.Run("lock released within budget", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		lock := filepath.Join(dir, "index.lock")
		require.NoError(t, os.WriteFile(lock, nil, 0o644))
		go func() {
			time.Sleep(150 * time.Millisecond)
			_ = os.Remove(lock)
		}()
		start := time.Now()
		assert.Empty(t, WaitForLockFiles(context.Background(), dir, 5*time.Second))
		// proves the waiter blocked on the lock rather than scanning after removal
		assert.GreaterOrEqual(t, time.Since(start), 100*time.Millisecond)
	})

	t.Run("budget elapses with lock still held", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "index.lock"), nil, 0o644))
		assert.Equal(t, []string{"index.lock"}, WaitForLockFiles(context.Background(), dir, 100*time.Millisecond))
		assert.FileExists(t, filepath.Join(dir, "index.lock"), "a waiter never removes a lock")
	})

	t.Run("context ends the wait", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "index.lock"), nil, 0o644))
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		assert.Equal(t, []string{"index.lock"}, WaitForLockFiles(ctx, dir, time.Minute))
	})
}

func TestIsIndexLockContention(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		out  string
		want bool
	}{
		{"index lock", "fatal: Unable to create '/r/.git/index.lock': File exists.", true},
		{"other lock", "error: Unable to create '/r/.git/HEAD.lock': File exists.", true},
		{"conflict", "CONFLICT (content): Merge conflict in f.md", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsIndexLockContention(tt.out))
		})
	}
}
