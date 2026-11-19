package session

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// StartRecording writes the started-recordings registry to the ox data
	// directory. A test's HOME does not move it: paths resolves the home once
	// per process, so without this every test's entry lands in the
	// developer's real registry (or wherever an exported XDG_DATA_HOME points).
	// Tests that need their own data directory still t.Setenv it.
	sandbox, err := os.MkdirTemp("", "ox-session-tests-data-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "internal/session tests: cannot create a data sandbox: %v\n", err)
		os.Exit(1)
	}
	_ = os.Setenv("XDG_DATA_HOME", sandbox)
	code := m.Run()
	_ = os.RemoveAll(sandbox)
	os.Exit(code)
}
