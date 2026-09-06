package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/sageox/agentx"
)

// TestEnsurePrimeBeforeSession_DoesNotRecurseUnderGoTest is the regression
// test for #846: the full `go test ./cmd/ox/...` run deterministically hung
// for ~10 minutes and then failed, because ensurePrimeBeforeSession used
// os.Executable() to find the ox binary to shell out to for inline priming.
// Under `go test`, os.Executable() resolves to the compiled test binary, not
// the real ox CLI. Exec'ing that binary with args "agent prime" doesn't match
// any -test.* flag, so the test binary just reran its entire suite from
// scratch — which reaches this same code path again, recursing without bound
// until the package's test deadline killed the whole run.
//
// Failure prevented: any test that exercises runAgentSessionStart on an
// agent session with no prime marker yet (matching real agent env vars, e.g.
// CLAUDE_CODE_SESSION_ID) triggers an unbounded recursive self-invocation of
// the whole test binary instead of returning.
func TestEnsurePrimeBeforeSession_DoesNotRecurseUnderGoTest(t *testing.T) {
	// A session ID agentx's ClaudeCodeAgent will report via SessionID(), with
	// no corresponding marker on disk — this is exactly the "prime hasn't run
	// yet" state that used to trigger the exec.Command call.
	agentSessionID := fmt.Sprintf("test-guard-%d", time.Now().UnixNano())
	t.Setenv("CLAUDE_CODE_SESSION_ID", agentSessionID)

	// Confirm the test actually reaches the branch it means to exercise.
	// agentx.CurrentAgent() returns whichever agent it detects first — if a
	// different signal in the test environment won the detection, or that
	// agent doesn't support sessions, or reports a different session ID,
	// ensurePrimeBeforeSession would return early via its "no session ID"
	// path instead of the testing.Testing() guard, and this test would pass
	// for the wrong reason.
	agent := agentx.CurrentAgent()
	if agent == nil {
		t.Fatal("agentx.CurrentAgent() returned nil — CLAUDE_CODE_SESSION_ID should have made ClaudeCodeAgent detectable")
	}
	if !agent.SupportsSession() {
		t.Fatalf("detected agent %q does not support sessions", agent.Name())
	}
	if got := agent.SessionID(agentx.NewSystemEnvironment()); got != agentSessionID {
		t.Fatalf("detected agent %q reports SessionID() = %q, want %q — some other env signal is interfering", agent.Name(), got, agentSessionID)
	}

	if marker, err := ReadSessionMarker(agentSessionID); err != nil || marker != nil {
		t.Fatalf("test session id %q unexpectedly already has a marker (marker=%+v err=%v)", agentSessionID, marker, err)
	}

	done := make(chan struct{})
	go func() {
		ensurePrimeBeforeSession("test-agent-id")
		close(done)
	}()

	select {
	case <-done:
		// good: returned without shelling out
	case <-time.After(5 * time.Second):
		t.Fatal("ensurePrimeBeforeSession did not return within 5s — it is " +
			"almost certainly shelling out via os.Executable(), which under " +
			"go test recursively re-invokes the whole test suite (see #846)")
	}

	// A real inline prime run would have written a session marker on success.
	// If one appeared, the exec branch fired for real — the guard failed to
	// stop it, and we got lucky it also happened to complete before the
	// timeout above rather than recursing.
	if marker, _ := ReadSessionMarker(agentSessionID); marker != nil {
		t.Fatalf("ensurePrimeBeforeSession wrote a session marker under go test — "+
			"it exec'd something instead of skipping: %+v", marker)
	}
}
