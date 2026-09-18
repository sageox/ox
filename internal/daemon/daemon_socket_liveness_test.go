package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSocketCleanupRequiresConfirmedOwnerExit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		remove bool
	}{
		{"running", nil, false},
		{"sandbox EPERM", syscall.EPERM, false},
		{"wrapped EPERM", fmt.Errorf("signal: %w", syscall.EPERM), false},
		{"unknown failure", errors.New("probe unavailable"), false},
		{"confirmed ESRCH", syscall.ESRCH, true},
		{"wrapped ESRCH", fmt.Errorf("signal: %w", syscall.ESRCH), true},
		{"reaped process", os.ErrProcessDone, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "daemon.sock")
			require.NoError(t, os.WriteFile(path, []byte("socket fixture"), 0600))
			require.Equal(t, tc.remove, removeSocketForExitedOwner(path, tc.err))
			if tc.remove {
				require.NoFileExists(t, path)
			} else {
				require.FileExists(t, path)
			}
		})
	}
}
