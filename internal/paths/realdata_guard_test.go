package paths

import (
	"fmt"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// TestDataDir_RefusesRealDataDirUnderTest proves the guard against the leak it
// exists for: a test that never sandboxed the data dir resolving the
// developer's real one. DataDir only builds a path, so the real-home cases
// touch nothing on disk.
func TestDataDir_RefusesRealDataDirUnderTest(t *testing.T) {
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		t.Skip("no home directory on record for the current user")
	}
	realHome := u.HomeDir

	tests := []struct {
		name      string
		setup     func(t *testing.T)
		wantPanic bool
	}{
		{
			name: "XDG_DATA_HOME is the real default",
			setup: func(t *testing.T) {
				t.Setenv("XDG_DATA_HOME", filepath.Join(realHome, ".local", "share"))
			},
			wantPanic: true,
		},
		{
			// The bead's leak: HOME was resolved (and cached) before the test
			// moved it, so the late t.Setenv("HOME") does not take effect.
			name: "HOME moved after the home dir was cached",
			setup: func(t *testing.T) {
				t.Setenv("XDG_DATA_HOME", "")
				resetHomeDir(t)
				t.Setenv("HOME", realHome)
				_ = getHomeDir()
				t.Setenv("HOME", t.TempDir())
			},
			wantPanic: true,
		},
		{
			name: "legacy mode under the real home",
			setup: func(t *testing.T) {
				t.Setenv("OX_XDG_DISABLE", "1")
				resetHomeDir(t)
				t.Setenv("HOME", realHome)
			},
			wantPanic: true,
		},
		{
			name: "XDG_DATA_HOME sandboxed",
			setup: func(t *testing.T) {
				t.Setenv("XDG_DATA_HOME", t.TempDir())
			},
		},
		{
			name: "HOME sandboxed before the first resolution",
			setup: func(t *testing.T) {
				t.Setenv("XDG_DATA_HOME", "")
				resetHomeDir(t)
				t.Setenv("HOME", t.TempDir())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// t.Setenv and resetHomeDir mutate process state: no t.Parallel.
			t.Setenv("OX_XDG_DISABLE", "")
			t.Setenv("OX_XDG_ENABLE", "")
			tt.setup(t)

			got := dataDirPanic(t)
			if !tt.wantPanic {
				if got != "" {
					t.Fatalf("DataDir panicked on a sandboxed data dir: %s", got)
				}
				return
			}
			if got == "" {
				t.Fatal("DataDir resolved the real user data dir under go test without panicking")
			}
			if !strings.Contains(got, "testguard.RunWithDataSandbox") {
				t.Fatalf("panic does not tell the author how to sandbox: %s", got)
			}
		})
	}
}

// dataDirPanic calls DataDir and returns its panic message, or "" if it
// returned normally.
func dataDirPanic(t *testing.T) (msg string) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			msg = fmt.Sprint(r)
		}
	}()
	_ = DataDir()
	return ""
}
