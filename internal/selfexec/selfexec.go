// Package selfexec resolves the path of the running ox binary for the handful
// of code paths that re-run ox as a subprocess, and refuses to hand that path
// out from inside a test binary.
package selfexec

import (
	"errors"
	"os"
	"testing"
)

// ErrUnderTest is returned by Path when the running binary is a Go test binary.
// Callers treat it like any other "cannot locate the executable" error: skip
// the subprocess and continue.
var ErrUnderTest = errors.New("selfexec: refusing to re-exec the test binary")

// Path returns the absolute path of the running ox executable, for callers that
// re-run ox as a subprocess (`ox daemon start`, `ox agent prime`, ...).
//
// Under `go test`, os.Executable() resolves to the compiled TEST binary
// (.../b001/ox.test), not the ox CLI. Handing that path to exec.Command with a
// subcommand does not run the subcommand: Go's flag package stops parsing at
// the first non-flag argument, so "daemon"/"start" are consumed as positional
// args and the binary silently re-runs the ENTIRE test suite. That child is
// exec'd directly rather than by `go test`, so -test.timeout defaults to 0 and
// it never times out — and every generation spawns the next. Observed in the
// wild as dozens of `ox.test daemon start` processes reparented to PID 1,
// surviving 30+ minutes past the run that created them.
//
// Returning ErrUnderTest makes that recursion impossible by construction,
// rather than relying on each new call site remembering to guard itself.
func Path() (string, error) {
	return resolve(testing.Testing())
}

// resolve is Path with the under-test decision injected. Path's own branches
// are otherwise untestable: testing.Testing() is true by definition inside
// every test, so the real-binary path could never be exercised.
func resolve(underTest bool) (string, error) {
	if underTest {
		return "", ErrUnderTest
	}
	return os.Executable()
}
