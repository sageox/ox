package testguard

import (
	"fmt"
	"os"
	"testing"
)

// RunWithDataSandbox runs the package's tests with XDG_DATA_HOME pointed at a
// fresh temp dir and legacy mode (OX_XDG_DISABLE) off, removes the dir
// afterwards, and returns m.Run's exit code. Use it in TestMain:
// os.Exit(testguard.RunWithDataSandbox(m)).
//
// Without it, every paths.DataDir write (the started-recordings and
// live-conversations registries, team and ledger checkouts) lands in the
// developer's real ~/.local/share/sageox, and paths.DataDir panics under go
// test to say so. TestMain is the place because setting HOME per test does
// not move the data dir (paths caches the home on first use); XDG_DATA_HOME is
// read on every resolution, so tests that need their own still t.Setenv it.
func RunWithDataSandbox(m *testing.M) int {
	sandbox, err := os.MkdirTemp("", "ox-test-data-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "testguard: cannot create a data sandbox: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(sandbox) }()
	// OX_XDG_DISABLE is the one switch that moves DataDir to the legacy
	// ~/.sageox/data, past XDG_DATA_HOME; inherited, it would bypass the sandbox.
	if err := os.Unsetenv("OX_XDG_DISABLE"); err != nil {
		fmt.Fprintf(os.Stderr, "testguard: cannot clear OX_XDG_DISABLE: %v\n", err)
		return 1
	}
	if err := os.Setenv("XDG_DATA_HOME", sandbox); err != nil {
		fmt.Fprintf(os.Stderr, "testguard: cannot set XDG_DATA_HOME: %v\n", err)
		return 1
	}
	return m.Run()
}
