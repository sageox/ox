package fileutil

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Exercise the real Windows sharing violation, releasing the handle only after
// observing a failed rename. No timing assumption decides whether the retry ran.
func TestWindowsReplacementRetriesHeldDestination(t *testing.T) {
	for _, release := range []bool{true, false} {
		name := "released"
		if !release {
			name = "still-held"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			src, dst := filepath.Join(dir, "new"), filepath.Join(dir, "state")
			require.NoError(t, os.WriteFile(src, []byte("new state"), 0600))
			require.NoError(t, os.WriteFile(dst, []byte("old state"), 0600))
			held, err := os.Open(dst)
			require.NoError(t, err)
			t.Cleanup(func() { _ = held.Close() })
			attempts := 0
			err = retryWindowsRename(func() error {
				attempts++
				err := os.Rename(src, dst)
				if attempts == 1 {
					require.Error(t, err, "fixture must actually deny replacement")
					if release {
						require.NoError(t, held.Close())
					}
				}
				return err
			}, func(time.Duration) {})
			want := "new state"
			if release {
				require.NoError(t, err)
				require.Equal(t, 2, attempts)
			} else {
				require.Error(t, err)
				require.Equal(t, 20, attempts)
				want = "old state"
			}
			data, err := os.ReadFile(dst)
			require.NoError(t, err)
			require.Equal(t, want, string(data))
		})
	}
}

func TestWindowsReplacementDoesNotRetryOtherFailures(t *testing.T) {
	err := retryWindowsRename(func() error { return os.ErrNotExist }, func(time.Duration) {
		t.Fatal("non-sharing errors must not be retried")
	})
	require.ErrorIs(t, err, os.ErrNotExist)
}
