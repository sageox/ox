package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// isolateSessionMarkerDir gives the calling test a private SessionMarkerDir.
//
// SessionMarkerDir() is paths.TempDir()/sessions — ONE global directory
// shared by the whole package (and with the developer's real ox install).
// FindSessionMarkerByPID scans it and returns the FIRST marker matching the
// queried PID, in os.ReadDir order. Every test that writes a marker with
// ParentPID: os.Getpid() therefore competes for the same key, and which one
// a PID query resolves to comes down to filename ordering — several test
// files do exactly that (agent_hook_test.go, session_force_stop_test.go,
// agent_prime_id_reuse_test.go).
//
// Failure prevented: a PID-query test intermittently asserting against
// another test's marker. Reproduced deterministically by planting a
// same-PID marker whose session ID sorts earlier — the query then returns
// the competitor rather than the marker under test.
//
// paths.TempDir() derives the directory from $USER (then $USERNAME on
// Windows), so overriding those per test isolates the namespace without
// touching production code. t.Setenv restores them automatically and marks
// the test non-parallel, which is required here anyway. Any future test
// that queries markers by PID should call this rather than hope it wins the
// ordering race.
// t.Name() alone is NOT enough: two concurrent `go test` processes run the
// same test name, would derive the same directory, and the Cleanup below
// would delete the other process's markers — reintroducing the shared-key
// collision at process scope instead of test scope. t.TempDir()'s basename
// is unique per test AND per process, so it closes that last gap.
func isolateSessionMarkerDir(t *testing.T) {
	t.Helper()
	unique := "oxtest-" + filepath.Base(t.TempDir()) + "-" + strings.ReplaceAll(t.Name(), "/", "_")
	t.Setenv("USER", unique)
	t.Setenv("USERNAME", unique)
	require.NoError(t, os.MkdirAll(SessionMarkerDir(), 0o700))
	t.Cleanup(func() { _ = os.RemoveAll(SessionMarkerDir()) })
}
