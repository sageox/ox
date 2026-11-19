package testguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	os.Exit(RunWithDataSandbox(m))
}

// Failure prevented: an inherited OX_XDG_DISABLE=1 sent DataDir to the legacy
// ~/.sageox/data past the sandboxed XDG_DATA_HOME, so a package that called
// RunWithDataSandbox still resolved the real home.
func TestRunWithDataSandbox_SandboxesXDGAndClearsLegacyMode(t *testing.T) {
	if v := os.Getenv("OX_XDG_DISABLE"); v != "" {
		t.Fatalf("OX_XDG_DISABLE=%q survived the sandbox; DataDir would use the legacy home", v)
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	tempDir, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(dataHome)
	if err != nil {
		t.Fatalf("XDG_DATA_HOME=%q is not an existing sandbox: %v", dataHome, err)
	}
	if !strings.HasPrefix(resolved, tempDir+string(filepath.Separator)) {
		t.Fatalf("XDG_DATA_HOME=%q is not inside the temp dir %q", resolved, tempDir)
	}
}
