package paths

import (
	"fmt"
	"os"
	"testing"
)

// TestMain moves HOME, not XDG_DATA_HOME: most tests here clear the XDG
// variables (clearXDGEnv) to exercise the HOME-derived defaults, so only a
// HOME set before getHomeDir's first call keeps those defaults, and legacy
// mode's ~/.sageox, out of the developer's real home.
func TestMain(m *testing.M) {
	os.Exit(runWithHomeSandbox(m))
}

func runWithHomeSandbox(m *testing.M) int {
	sandbox, err := os.MkdirTemp("", "ox-paths-tests-home-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "internal/paths tests: cannot create a home sandbox: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(sandbox) }()
	for _, key := range []string{"HOME", "USERPROFILE"} {
		if err := os.Setenv(key, sandbox); err != nil {
			fmt.Fprintf(os.Stderr, "internal/paths tests: cannot set %s: %v\n", key, err)
			return 1
		}
	}
	return m.Run()
}
