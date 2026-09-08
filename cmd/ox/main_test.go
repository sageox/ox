package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/testutil/slogquiet"
)

// packageDir is the directory `go test` starts in — the cmd/ox source directory,
// inside the real ox repository. Captured before TestMain moves away from it, for
// the few tests that legitimately need to read fixtures from the source tree.
var packageDir string

// repoPath resolves a path relative to the ox source tree, for tests that read
// checked-in fixtures. Use it instead of a relative literal: the process working
// directory is deliberately NOT the source tree while tests run.
func repoPath(parts ...string) string {
	return filepath.Join(append([]string{packageDir}, parts...)...)
}

// TestMain moves the process OUT of the ox repository before any test runs.
//
// Doctor checks resolve their target repository with findGitRoot(), which walks
// up from the process working directory. `go test` starts inside cmd/ox, so a
// check invoked without chdir'ing into a fixture resolved to the DEVELOPER'S OWN
// CHECKOUT — and FixLevelAuto checks then reconciled it. `go test ./cmd/ox/ -run
// TestDoctor` deleted nine tracked skills from the working tree and staged the
// lockfile. The checks were always cwd-resolved; the ox-cli-* rename only made
// the consequence visible, because reconcile finally had old names to retire
// rather than identical content to rewrite.
//
// Starting in a directory that is not a git repository at all makes the failure
// mode structural rather than a rule every future test author has to remember: a
// check that does not chdir into a fixture now resolves NO repository and skips,
// instead of silently operating on the wrong one. Tests that need a repository
// still build one and chdir into it exactly as before.
func TestMain(m *testing.M) {
	slogquiet.Silence()

	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cmd/ox tests: cannot resolve working directory: %v\n", err)
		os.Exit(1)
	}
	packageDir = wd

	sandbox, err := os.MkdirTemp("", "ox-cmd-tests-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "cmd/ox tests: cannot create sandbox: %v\n", err)
		os.Exit(1)
	}
	if err := os.Chdir(sandbox); err != nil {
		fmt.Fprintf(os.Stderr, "cmd/ox tests: cannot enter sandbox: %v\n", err)
		os.Exit(1)
	}

	// Verify the sandbox is not itself inside a repository, BEFORE any test runs.
	//
	// os.MkdirTemp uses os.TempDir(), which honors $TMPDIR (or %TMP%/%TEMP%). If
	// that points under a checkout — a developer with TMPDIR set to a scratch dir
	// in a repo, or a CI image that does — findGitRoot() still resolves a
	// repository after the chdir, and a FixLevelAuto check reached before the
	// tripwire test would reconcile it. The tripwire alone is not enough: it runs
	// in test order, and TestRunDoctorChecks_WithFixFlag may run first.
	if root := findGitRoot(); root != "" {
		fmt.Fprintf(os.Stderr,
			"cmd/ox tests: sandbox %s is inside git repository %s.\n"+
				"A FixLevelAuto doctor check would reconcile that repository. "+
				"Set TMPDIR to a directory outside any checkout and re-run.\n",
			sandbox, root)
		_ = os.Chdir(packageDir)
		_ = os.RemoveAll(sandbox)
		os.Exit(1)
	}

	code := m.Run()

	// Leave the sandbox before removing it; some platforms refuse to unlink the
	// working directory out from under a live process.
	_ = os.Chdir(packageDir)
	_ = os.RemoveAll(sandbox)
	os.Exit(code)
}
