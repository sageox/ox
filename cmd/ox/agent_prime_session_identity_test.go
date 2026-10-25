//go:build !short

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	return primeWithHookAs(t, "claude-code", sessionID, source)
}

// primeWithHookAs is primeWithHook for a specific host. Claude Code caps hook
// output, so its hook prime is trimmed; other hosts are not known to cap.
func primeWithHookAs(t *testing.T, agent, sessionID, source string) string {
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
	require.NoError(t, cmd.Flags().Set("agent", agent))
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

// requireFullPrime asserts the output is the full prime, not merely "not
// compact": an empty or truncated prime is not compact either, and would let
// these regression tests pass while the model received no team context.
func requireFullPrime(t *testing.T, out, msg string) {
	t.Helper()
	require.NotEmpty(t, strings.TrimSpace(out), "%s: prime emitted nothing", msg)
	assert.False(t, isCompactPrime(out), msg)
	assert.Contains(t, out, "<ox-prime>", "%s: missing the full prime root element", msg)
	assert.Contains(t, out, "<consult-first>", "%s: missing the team-context guidance only the full prime carries", msg)
}

// Failure prevented: a coworker's second Claude Code session got the short
// "already primed" prime, which omits team knowledge, because ox matched it to
// the first session's AI coworker identity (same parent process / inherited
// SAGEOX_AGENT_ID) even though this native session had never been primed.
func TestRunAgentPrime_NewNativeSessionNeverGetsCompactPrime(t *testing.T) {
	initializedE2E(t)
	one, two := uniqueSessionID(t, "native-one"), uniqueSessionID(t, "native-two")

	first := primeWithHook(t, one, "startup")
	requireFullPrime(t, first, "a session's first prime must be full")

	marker, err := ReadSessionMarker(one)
	require.NoError(t, err)
	require.NotNil(t, marker)
	// the identity a second session under the same parent process inherits
	t.Setenv("SAGEOX_AGENT_ID", marker.AgentID)

	second := primeWithHook(t, two, "startup")
	requireFullPrime(t, second,
		"a native session that was never primed got the compact re-prime and lost team context")
}

// Failure prevented: the over-correction. The CLAUDE.md "prime at session
// start" instruction runs a bare prime after the SessionStart hook already
// delivered the full prime into this same session; that repeat is the redundant
// case the compact tier exists for and must stay compact. Uses a host without a
// hook-output cap: a Claude Code hook prime is trimmed and so never licenses
// compact (see TestRunAgentPrime_CappedHookPrimeDoesNotLicenseCompact).
func TestRunAgentPrime_RepeatInsideHookPrimedSessionStaysCompact(t *testing.T) {
	initializedE2E(t)
	hooked := uniqueSessionID(t, "native-hooked")
	t.Setenv("CLAUDE_CODE_SESSION_ID", hooked)

	// the hook prime runs as an uncapped host; the repeat resolves the same
	// native session id through the environment, as the BLOCKING instruction does
	first := primeWithHookAs(t, "codex", hooked, "startup")
	requireFullPrime(t, first, "a session's first prime must be full")
	require.NotContains(t, first, hookCapDeferredOpen, "harness: an uncapped host must receive the untrimmed prime")

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
		requireFullPrime(t, out, fmt.Sprintf("bare prime #%d was compact though no full prime was ever hook-delivered", i))
	}
}

// useOversizedTeamContext points the e2e repo at a team context large enough
// that a hook-driven prime exceeds Claude Code's hook cap and is trimmed.
func useOversizedTeamContext(t *testing.T, env *oxE2E) {
	t.Helper()
	teamDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(teamDir, "agents", "rules"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(teamDir, "docs"), 0o755))
	rule := "---\nname: retry-policy\ndescription: how clients retry\nvisibility: always\n---\n\n# Retry policy\n\n" + strings.Repeat("Retry with capped exponential backoff, max 3 attempts, never on 4xx.\n", 12)
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "agents", "rules", "retry-policy.md"), []byte(rule), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "MEMORY.md"), []byte("# memory\n\n"+strings.Repeat("- a durable team fact worth keeping\n", 15)), 0o644))
	for i := 0; i < 24; i++ {
		doc := fmt.Sprintf("---\ntitle: Doc %02d\ndescription: reference doc %02d\nvisibility: indexed\nwhen: when task %02d comes up in the upload service\n---\n\n# Doc %02d\n\nbody\n", i, i, i, i)
		require.NoError(t, os.WriteFile(filepath.Join(teamDir, "docs", fmt.Sprintf("doc-%02d.md", i)), []byte(doc), 0o644))
	}
	localCfg := fmt.Sprintf("\n[[team_contexts]]\nteam_id = %q\nteam_name = %q\nslug = %q\npath = %q\nlast_sync = 0001-01-01T00:00:00Z\n",
		env.TeamID, "E2E Team", "e2e-team", teamDir)
	require.NoError(t, os.WriteFile(filepath.Join(env.Root, ".sageox", "config.local.toml"), []byte(localCfg), 0o600))
}

// Failure prevented: a SessionStart hook prime that exceeded Claude Code's hook
// cap reached the model as a trimmed preview (sections deferred to a file), yet
// was recorded as a delivered full prime. A later bare prime then came back
// compact and the model never saw the deferred team context.
func TestRunAgentPrime_CappedHookPrimeDoesNotLicenseCompact(t *testing.T) {
	env := initializedE2E(t)
	useOversizedTeamContext(t, env)
	capped := uniqueSessionID(t, "native-capped")
	t.Setenv("CLAUDE_CODE_SESSION_ID", capped)

	first := primeWithHook(t, capped, "startup")
	require.Contains(t, first, `<deferred path="`, "harness: the hook prime must exceed the cap and be trimmed")

	marker, err := ReadSessionMarker(capped)
	require.NoError(t, err)
	require.NotNil(t, marker)
	assert.False(t, marker.FullPrimeViaHook, "a trimmed hook prime was recorded as a delivered full prime")

	repeat := primeWithHook(t, "", "")
	requireFullPrime(t, repeat, "a bare prime after a capped hook prime must be full so the deferred context reaches the model")
}
