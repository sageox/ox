package paths

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
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

	// setHome moves both home variables: os.UserHomeDir reads USERPROFILE on
	// Windows and HOME elsewhere.
	setHome := func(t *testing.T, home string) {
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
	}
	// aliasHome returns a symlink to the real home, outside it.
	aliasHome := func(t *testing.T) string {
		link := filepath.Join(t.TempDir(), "home-alias")
		if err := os.Symlink(realHome, link); err != nil {
			// Windows needs a privilege to create symlinks.
			t.Skipf("cannot create a symlink on %s: %v", runtime.GOOS, err)
		}
		return link
	}

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
			// moved it, so the late HOME change does not take effect.
			name: "HOME moved after the home dir was cached",
			setup: func(t *testing.T) {
				t.Setenv("XDG_DATA_HOME", "")
				resetHomeDir(t)
				setHome(t, realHome)
				_ = getHomeDir()
				setHome(t, t.TempDir())
			},
			wantPanic: true,
		},
		{
			name: "legacy mode under the real home",
			setup: func(t *testing.T) {
				t.Setenv("OX_XDG_DISABLE", "1")
				resetHomeDir(t)
				setHome(t, realHome)
			},
			wantPanic: true,
		},
		{
			name: "HOME is a symlink to the real home",
			setup: func(t *testing.T) {
				t.Setenv("XDG_DATA_HOME", "")
				resetHomeDir(t)
				setHome(t, aliasHome(t))
			},
			wantPanic: true,
		},
		{
			name: "legacy mode under a symlink to the real home",
			setup: func(t *testing.T) {
				t.Setenv("OX_XDG_DISABLE", "1")
				resetHomeDir(t)
				setHome(t, aliasHome(t))
			},
			wantPanic: true,
		},
		{
			// The pure-Go user lookup, with no account record, reports $HOME,
			// which a TestMain may already have moved into a sandbox. The home
			// the binary started with must still be protected.
			name: "account lookup fell back to a sandboxed HOME",
			setup: func(t *testing.T) {
				startHome := envAtInit.home
				if startHome == "" {
					t.Skip("the test binary started without HOME")
				}
				sandbox := t.TempDir()
				stubAccountHome(t, sandbox)
				t.Setenv("XDG_DATA_HOME", "")
				resetHomeDir(t)
				setHome(t, startHome)
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
				setHome(t, t.TempDir())
			},
		},
		{
			// A re-executed helper binary starts with its parent test's sandbox
			// as HOME and XDG_DATA_HOME; that sandbox is not the real home.
			name: "started inside an inherited sandbox",
			setup: func(t *testing.T) {
				sandbox := t.TempDir()
				saved := envAtInit
				envAtInit.home, envAtInit.userProfile = sandbox, sandbox
				envAtInit.dataHome = filepath.Join(sandbox, ".local", "share")
				t.Cleanup(func() { envAtInit = saved })
				t.Setenv("XDG_DATA_HOME", "")
				resetHomeDir(t)
				setHome(t, sandbox)
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

// stubAccountHome makes the account-record lookup report home.
func stubAccountHome(t *testing.T, home string) {
	t.Helper()
	saved := accountHome
	accountHome = func() string { return home }
	t.Cleanup(func() { accountHome = saved })
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
