//go:build darwin || linux || freebsd

package proc

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
)

func TestProcessMayBeAlivePermissionAndUnknownErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		alive bool
	}{
		{"running", nil, true},
		{"sandbox denied", syscall.EPERM, true},
		{"wrapped permission", fmt.Errorf("probe: %w", syscall.EPERM), true},
		{"access denied", syscall.EACCES, true},
		{"unknown", errors.New("probe unavailable"), true},
		{"no process", syscall.ESRCH, false},
		{"wrapped exit", fmt.Errorf("probe: %w", syscall.ESRCH), false},
		{"reaped", os.ErrProcessDone, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := processMayBeAlive(tc.err); got != tc.alive {
				t.Fatalf("alive=%v, want %v", got, tc.alive)
			}
		})
	}
}
