package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTestsRunOutsideAnyGitRepository is the tripwire for the guard in TestMain.
//
// Doctor checks resolve their target with findGitRoot(), which walks up from the
// process working directory. While `go test` ran inside cmd/ox that resolved to
// the DEVELOPER'S OWN CHECKOUT, and FixLevelAuto checks reconciled it: `go test
// ./cmd/ox/ -run TestDoctor` deleted nine tracked skills from the working tree
// and staged the lockfile.
//
// The defense is structural — start outside any repository, so a check that
// forgets to chdir into a fixture resolves nothing and skips. This test exists so
// that defense cannot be removed by accident: delete the chdir in TestMain and
// this fails immediately, naming the consequence, instead of the next developer
// discovering it as unexplained deletions in `git status`.
func TestTestsRunOutsideAnyGitRepository(t *testing.T) {
	if root := findGitRoot(); root != "" {
		t.Fatalf("cmd/ox tests are running INSIDE git repository %q.\n"+
			"Any FixLevelAuto doctor check that does not chdir into a fixture will "+
			"reconcile that repository — this previously deleted tracked skills from "+
			"the developer's checkout. Restore the sandbox chdir in TestMain.", root)
	}

	// And the sandbox must be a real, writable directory: tests that build fixtures
	// with relative paths depend on it.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	probe := filepath.Join(wd, "sandbox-writable-probe")
	if err := os.WriteFile(probe, []byte("x"), 0o644); err != nil {
		t.Fatalf("sandbox is not writable: %v", err)
	}
	_ = os.Remove(probe)
}

// TestRepoPathResolvesTheSourceTree covers the escape hatch: a handful of tests
// legitimately read checked-in files (a source file they lint, a docs fixture).
// They must go through repoPath, because the working directory is no longer the
// source tree.
func TestRepoPathResolvesTheSourceTree(t *testing.T) {
	if _, err := os.Stat(repoPath("main.go")); err != nil {
		t.Errorf("repoPath does not resolve the cmd/ox source directory: %v", err)
	}
	if _, err := os.Stat(repoPath("..", "..", "go.mod")); err != nil {
		t.Errorf("repoPath cannot reach the repository root: %v", err)
	}
}
