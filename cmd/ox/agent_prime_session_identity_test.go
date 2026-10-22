//go:build !short

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// primeWithHook runs one real in-process prime. A non-empty sessionID delivers
// a hook payload on stdin the way Claude Code does; an empty one is a bare
// `ox agent prime` typed by the coworker or run by the AI coworker itself.
func primeWithHook(t *testing.T, sessionID, source string) string {
	t.Helper()
	if sessionID != "" {
		origStdin := os.Stdin
		r, w, err := os.Pipe()
		require.NoError(t, err)
		_, err = io.WriteString(w, `{"session_id":"`+sessionID+`","hook_event_name":"SessionStart","source":"`+source+`"}`)
		require.NoError(t, err)
		require.NoError(t, w.Close())
		os.Stdin = r
		t.Cleanup(func() { os.Stdin = origStdin; _ = r.Close() })
	}
	var buf bytes.Buffer
	cmd := agentPrimeCmd
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	require.NoError(t, cmd.Flags().Set("agent", "claude-code"))
	t.Cleanup(func() {
		_ = cmd.Flags().Set("agent", "")
		cmd.SetOut(nil)
		cmd.SetErr(nil)
	})
	require.NoError(t, runAgentPrime(cmd, nil))
	return buf.String()
}

// uniqueSessionID returns a native session ID no earlier run can have left a
// marker for: session markers live in a shared temp dir the e2e fixture does
// not isolate, so a fixed ID would inherit a stale marker and prove nothing.
func uniqueSessionID(t *testing.T, label string) string {
	t.Helper()
	id := fmt.Sprintf("%s-%d", label, time.Now().UnixNano())
	t.Cleanup(func() { _ = DeleteSessionMarker(id) })
	return id
}

func isCompactPrime(out string) bool {
	return strings.Contains(out, `<ox-prime mode="compact"`)
}

// Failure prevented: a coworker's second Claude Code session got the short
// "already primed" prime, which omits team knowledge, because ox matched it to
// the first session's AI coworker identity (same parent process / inherited
// SAGEOX_AGENT_ID) even though this native session had never been primed.
func TestRunAgentPrime_NewNativeSessionNeverGetsCompactPrime(t *testing.T) {
	initializedE2E(t)
	one, two := uniqueSessionID(t, "native-one"), uniqueSessionID(t, "native-two")

	first := primeWithHook(t, one, "startup")
	require.False(t, isCompactPrime(first), "a session's first prime must be full")

	marker, err := ReadSessionMarker(one)
	require.NoError(t, err)
	require.NotNil(t, marker)
	// the identity a second session under the same parent process inherits
	t.Setenv("SAGEOX_AGENT_ID", marker.AgentID)

	second := primeWithHook(t, two, "startup")
	assert.False(t, isCompactPrime(second),
		"a native session that was never primed got the compact re-prime and lost team context")
}

// Failure prevented: the over-correction. The CLAUDE.md "prime at session
// start" instruction runs a bare prime after the SessionStart hook already
// delivered the full prime into this same session; that repeat is the redundant
// case the compact tier exists for and must stay compact.
func TestRunAgentPrime_RepeatInsideHookPrimedSessionStaysCompact(t *testing.T) {
	initializedE2E(t)
	hooked := uniqueSessionID(t, "native-hooked")
	t.Setenv("CLAUDE_CODE_SESSION_ID", hooked)

	first := primeWithHook(t, hooked, "startup")
	require.False(t, isCompactPrime(first), "a session's first prime must be full")

	repeat := primeWithHook(t, "", "")
	assert.True(t, isCompactPrime(repeat), "a repeat inside a session the hook already primed should be compact")
}

// Failure prevented: an AI coworker ran `ox agent prime | head -100` (or any
// bare prime whose output may never have reached its context), then ran it
// again and was told "you were already primed earlier", so the team context was
// never read. Only a hook-delivered full prime licenses a later compact one.
func TestRunAgentPrime_BareRepeatsNeverLicenseCompact(t *testing.T) {
	initializedE2E(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", uniqueSessionID(t, "native-bare"))

	for i := 1; i <= 3; i++ {
		out := primeWithHook(t, "", "")
		assert.False(t, isCompactPrime(out), "bare prime #%d was compact though no full prime was ever hook-delivered", i)
	}
}
