package main

import (
	"os"
	"sync"
	"testing"
	"time"

	"github.com/sageox/agentx"
	"github.com/stretchr/testify/require"
)

// TestClaimSessionStart_DuplicateHookJoinsTheWinner verifies that of two
// SessionStart hooks for one session firing together, only one primes.
// Failure prevented (GH #1844): ox's hooks wired twice (a repo's tracked
// settings plus --settings) made two recordings for one Claude Code session;
// the empty one kept `ox session wait` waiting until its timeout.
func TestClaimSessionStart_DuplicateHookJoinsTheWinner(t *testing.T) {
	isolateSessionMarkerDir(t)
	const sid = "claude-session-dup"

	waiting := make(chan struct{})
	prevWaiting := sessionStartWaiting
	t.Cleanup(func() { sessionStartWaiting = prevWaiting })
	sessionStartWaiting = func() { close(waiting) }

	release, handled := claimSessionStart(sid, nil)
	require.False(t, handled, "the first hook must prime")

	var wg sync.WaitGroup
	var loserHandled bool
	wg.Add(1)
	go func() {
		defer wg.Done()
		loserRelease, h := claimSessionStart(sid, nil)
		defer loserRelease()
		loserHandled = h
	}()

	select {
	case <-waiting:
	case <-time.After(10 * time.Second):
		t.Fatal("the duplicate hook never waited on the claim")
	}
	require.NoError(t, WriteSessionMarker(&SessionMarker{AgentID: "OxAAAA", AgentSessionID: sid, PrimedAt: time.Now()}))
	release()
	wg.Wait()

	require.True(t, loserHandled, "the duplicate hook must join the recording the first one started")
}

// TestClaimSessionStart_DuplicateCompactHookJoinsTheWinner verifies the same
// for a re-prime after /compact or /clear, where both hooks saw the old marker.
func TestClaimSessionStart_DuplicateCompactHookJoinsTheWinner(t *testing.T) {
	isolateSessionMarkerDir(t)
	const sid = "claude-session-compact"
	old := &SessionMarker{AgentID: "OxAAAA", AgentSessionID: sid, PrimedAt: time.Now().Add(-time.Hour)}
	require.NoError(t, WriteSessionMarker(old))
	seen, err := ReadSessionMarker(sid)
	require.NoError(t, err)

	release, handled := claimSessionStart(sid, seen)
	require.False(t, handled, "the first hook must re-prime")
	require.NoError(t, WriteSessionMarker(&SessionMarker{AgentID: "OxAAAA", AgentSessionID: sid, PrimedAt: time.Now()}))
	release()

	release, handled = claimSessionStart(sid, seen)
	release()
	require.True(t, handled, "a duplicate that saw the pre-compact marker must not re-prime again")
}

// TestClaimSessionStart_LaterEventStillPrimes verifies a later SessionStart
// that saw the current marker is not mistaken for a duplicate: a real /clear
// must still start a fresh recording.
func TestClaimSessionStart_LaterEventStillPrimes(t *testing.T) {
	isolateSessionMarkerDir(t)
	const sid = "claude-session-later"
	require.NoError(t, WriteSessionMarker(&SessionMarker{AgentID: "OxAAAA", AgentSessionID: sid, PrimedAt: time.Now()}))
	seen, err := ReadSessionMarker(sid)
	require.NoError(t, err)

	release, handled := claimSessionStart(sid, seen)
	release()
	require.False(t, handled)
}

// TestClaimSessionStart_StuckClaimDoesNotWedgeTheHook verifies a hook whose
// claim is held past sessionStartWait primes on its own instead of hanging.
func TestClaimSessionStart_StuckClaimDoesNotWedgeTheHook(t *testing.T) {
	isolateSessionMarkerDir(t)
	prev := sessionStartWait
	sessionStartWait = 200 * time.Millisecond
	t.Cleanup(func() { sessionStartWait = prev })
	const sid = "claude-session-stuck"

	held, _ := claimSessionStart(sid, nil)
	defer held()

	start := time.Now()
	release, handled := claimSessionStart(sid, nil)
	release()
	require.False(t, handled, "a hook that could not claim must prime as before")
	require.Less(t, time.Since(start), 2*time.Second, "a stuck claim must not hold the hook")
}

// TestHandleStart_DuplicateHookJoinsWithoutPriming verifies the SessionStart
// hook itself takes the claim: a hook that finds the session already primed
// by its duplicate joins that marker and does not prime again (GH #1844).
func TestHandleStart_DuplicateHookJoinsWithoutPriming(t *testing.T) {
	isolateSessionMarkerDir(t)
	const sid = "claude-session-hook"
	primedAt := time.Now().Add(-time.Minute).Truncate(time.Second)
	require.NoError(t, WriteSessionMarker(&SessionMarker{AgentID: "OxAAAA", AgentSessionID: sid, PrimedAt: primedAt}))

	ctx := &HookContext{AgentType: "claude-code", ProjectRoot: t.TempDir(), Input: &agentx.HookInput{SessionID: sid, Source: "startup"}}
	require.NoError(t, handleStart(ctx))

	require.NotNil(t, ctx.Marker, "the hook must join the duplicate's marker")
	require.Equal(t, "OxAAAA", ctx.Marker.AgentID)
	require.True(t, primedAt.Equal(ctx.Marker.PrimedAt), "the hook must not prime the session a second time")
}

// TestClaimSessionStart_NoClaimWithoutASession verifies a hook with no native
// session ID, or no marker directory, primes as before instead of failing.
func TestClaimSessionStart_NoClaimWithoutASession(t *testing.T) {
	isolateSessionMarkerDir(t)
	release, handled := claimSessionStart("", nil)
	release()
	require.False(t, handled)

	require.NoError(t, os.RemoveAll(SessionMarkerDir()))
	require.NoError(t, os.WriteFile(SessionMarkerDir(), nil, 0o600))
	t.Cleanup(func() { _ = os.Remove(SessionMarkerDir()) })
	release, handled = claimSessionStart("claude-session-nodir", nil)
	release()
	require.False(t, handled)
}
