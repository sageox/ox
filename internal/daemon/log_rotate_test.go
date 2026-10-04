package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeSparseFile creates a file of the given logical size without writing its
// bytes, so size-threshold tests do not spend 10 MB of disk and time.
func writeSparseFile(t *testing.T, path string, size int64) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(size))
	require.NoError(t, f.Close())
}

// TestRotateDaemonLog pins the startup rotation rule.
//
// Failure prevented: nothing ever rotated the daemon log, so one coworker's
// log reached 96 MB / 528k lines in a week. The daemon opens it O_APPEND and
// inherits it as stdout/stderr, so the only safe place to rotate is before that
// open, and a rotation problem must never stop the daemon from starting.
func TestRotateDaemonLog(t *testing.T) {
	tests := []struct {
		name        string
		size        int64 // -1: no log file
		priorRotted bool  // a previous ".1" exists and must be replaced
		blockRotted bool  // ".1" is a non-empty directory, so the rename fails
		wantRotated bool
	}{
		{name: "over the limit is rotated", size: maxDaemonLogBytes + 1, wantRotated: true},
		{name: "over the limit replaces an older rotated log", size: maxDaemonLogBytes + 1, priorRotted: true, wantRotated: true},
		{name: "exactly at the limit is left alone", size: maxDaemonLogBytes},
		{name: "small log is left alone", size: 1024},
		{name: "empty log is left alone", size: 0},
		{name: "missing log is a no-op", size: -1},
		{name: "rename failure leaves the log in place and does not error", size: maxDaemonLogBytes + 1, blockRotted: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "daemon.log")
			rotatedPath := logPath + ".1"

			if tt.size >= 0 {
				writeSparseFile(t, logPath, tt.size)
			}
			if tt.priorRotted {
				require.NoError(t, os.WriteFile(rotatedPath, []byte("older generation"), 0o600))
			}
			if tt.blockRotted {
				require.NoError(t, os.MkdirAll(filepath.Join(rotatedPath, "child"), 0o700))
			}

			got := RotateDaemonLog(logPath)

			assert.Equal(t, tt.wantRotated, got, "return value")
			if tt.wantRotated {
				_, err := os.Stat(logPath)
				assert.True(t, os.IsNotExist(err), "original path must be free for a fresh log")
				info, err := os.Stat(rotatedPath)
				require.NoError(t, err)
				assert.Equal(t, tt.size, info.Size(), ".1 must hold the rotated content")
				return
			}
			if tt.size >= 0 {
				info, err := os.Stat(logPath)
				require.NoError(t, err, "log must still exist when not rotated")
				assert.Equal(t, tt.size, info.Size(), "log must be untouched when not rotated")
			}
		})
	}
}
