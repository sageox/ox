package main

import (
	"fmt"
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
// collision at process scope instead of test scope.
//
// The process-unique component must come from the PARENT of t.TempDir(), not
// its basename. Go builds the per-test directory as
// os.MkdirTemp("", "TestName") + "/%03d": the random component lives in the
// parent, and the basename is only a sequence counter — "001" in every
// process, for every run. An earlier version of this helper used the basename
// and asserted in this very comment that it was "unique per test AND per
// process". It was not, and the collision it was written to prevent came back
// exactly as described: two concurrent `go test ./cmd/ox/` runs derived the
// same directory and deleted each other's markers, making
// TestRunAgentDispatcher_ResolvesNativeSessionMarkerWithoutEnv fail 8 times
// out of 8 while passing alone. The PID is belt and braces.
func isolateSessionMarkerDir(t *testing.T) {
	t.Helper()
	// filepath.Dir strips the "%03d" counter, leaving MkdirTemp's random name.
	processUnique := filepath.Base(filepath.Dir(t.TempDir()))
	unique := fmt.Sprintf("oxtest-%d-%s-%s", os.Getpid(), processUnique,
		strings.ReplaceAll(t.Name(), "/", "_"))
	t.Setenv("USER", unique)
	t.Setenv("USERNAME", unique)
	require.NoError(t, os.MkdirAll(SessionMarkerDir(), 0o700))
	t.Cleanup(func() { _ = os.RemoveAll(SessionMarkerDir()) })
}

// isolateAgentDetection makes agent detection deterministic for a test that
// wants to be treated as one specific coding agent.
//
// agentx resolves "which agent am I running inside" in two phases: an agent's
// RuntimeDetector (env vars the agent itself sets) wins over an AGENT_ENV
// override, deliberately, so a stray `AGENT_ENV=pi` in a CLAUDE.md cannot
// convince ox it is Pi while it is plainly running inside Claude Code (#527).
//
// That priority is correct in production and hostile to tests. A test that
// sets a foreign agent's runtime signal — say CODEX_THREAD_ID — while the
// developer runs `go test` INSIDE Claude Code produces TWO agents with a live
// runtime signal at once. Nothing in agentx breaks that tie, so the winner is
// whichever the registry yields first, which is Go map order: random per call.
//
// Failure prevented: exactly that, observed as
// TestRunAgentDispatcher_ResolvesNativeSessionMarkerWithoutEnv failing 4-5
// times out of 6 on a developer machine and passing every time in CI. When
// Claude won the tie, agent.SessionID() returned the developer's REAL Claude
// Code session id, the marker lookup used that as its key, found nothing, and
// the dispatcher reported "no agent ID" — an error with no connection to the
// behavior under test.
//
// Clearing the competing runtime signals leaves exactly one, so detection is
// deterministic wherever the suite runs.
func isolateAgentDetection(t *testing.T) {
	t.Helper()
	for _, v := range []string{
		// Claude Code's runtime signals (agents/claudecode.go DetectRuntime)
		"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SESSION_ID",
		// other agents' native session ids, so a test naming one agent is
		// never silently competing with another
		"CODEX_THREAD_ID", "AMP_THREAD_URL", "CURSOR_SESSION_ID",
	} {
		t.Setenv(v, "")
	}
}
