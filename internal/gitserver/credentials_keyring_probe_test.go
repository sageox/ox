package gitserver

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- probeKeyringCached: avoid re-probing the OS keychain on every call ---
//
// isKeyringAvailable is called from the daemon's heartbeat and sync loops
// (roughly once a minute) plus most CLI commands touching git credentials.
// An uncached live probe on every call means a Set+Delete round-trip
// through OS Keychain Services every time — on macOS this can reprompt the
// user for access on every call, and always reprompts after a locally
// rebuilt dev binary changes identity. Failure prevented: a popup roughly
// every minute (worse after every rebuild) instead of at most once per TTL
// window.

// resetKeyringProbeState restores the probe function, clock, and cache to
// clean defaults after a test, so later tests never observe a stubbed
// probe or a stale cached result.
func resetKeyringProbeState(t *testing.T) {
	t.Helper()
	prevFn := TestSetKeyringProbeFunc(liveKeyringProbe)
	prevNow := TestSetKeyringProbeNow(time.Now)
	TestResetKeyringProbeCache()
	t.Cleanup(func() {
		TestSetKeyringProbeFunc(prevFn)
		TestSetKeyringProbeNow(prevNow)
		TestResetKeyringProbeCache()
	})
}

// TestProbeKeyringCached_SecondCallWithinTTLDoesNotReprobe proves back-to-
// back calls reuse the cached result instead of hitting the OS keychain
// again. Failure prevented: a probe on every isKeyringAvailable call —
// exactly the bug that produced a keychain popup roughly once a minute.
func TestProbeKeyringCached_SecondCallWithinTTLDoesNotReprobe(t *testing.T) {
	resetKeyringProbeState(t)

	var calls int32
	TestSetKeyringProbeFunc(func() bool {
		atomic.AddInt32(&calls, 1)
		return true
	})

	first := probeKeyringCached()
	second := probeKeyringCached()

	assert.True(t, first)
	assert.True(t, second)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls),
		"the live probe must fire once, not once per call")
}

// TestProbeKeyringCached_ReprobesAfterTTLExpires proves the cache isn't
// permanent — once the TTL window has passed, the next call re-probes
// live instead of trusting a result from before the keychain state (e.g.
// locked/unlocked, or a rebuilt binary's identity) may have changed.
// Uses an injectable clock, not time.Sleep, so this is deterministic and
// fast per this repo's testing discipline.
func TestProbeKeyringCached_ReprobesAfterTTLExpires(t *testing.T) {
	resetKeyringProbeState(t)

	var calls int32
	TestSetKeyringProbeFunc(func() bool {
		atomic.AddInt32(&calls, 1)
		return true
	})

	now := time.Now()
	TestSetKeyringProbeNow(func() time.Time { return now })
	probeKeyringCached()
	require.Equal(t, int32(1), atomic.LoadInt32(&calls))

	// still within TTL: no re-probe
	TestSetKeyringProbeNow(func() time.Time { return now.Add(keyringProbeCacheTTL - time.Second) })
	probeKeyringCached()
	require.Equal(t, int32(1), atomic.LoadInt32(&calls), "must not reprobe before the TTL elapses")

	// past TTL: must re-probe
	TestSetKeyringProbeNow(func() time.Time { return now.Add(keyringProbeCacheTTL + time.Second) })
	probeKeyringCached()
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls), "must reprobe once the TTL has elapsed")
}

// TestProbeKeyringCached_CachesFailureNotJustSuccess proves a failed probe
// (keychain locked, unavailable, etc.) is cached too — not just successes.
// Failure prevented: a broken keychain still gets hammered with a live
// probe on every call because only the success path was memoized.
func TestProbeKeyringCached_CachesFailureNotJustSuccess(t *testing.T) {
	resetKeyringProbeState(t)

	var calls int32
	TestSetKeyringProbeFunc(func() bool {
		atomic.AddInt32(&calls, 1)
		return false
	})

	first := probeKeyringCached()
	second := probeKeyringCached()

	assert.False(t, first)
	assert.False(t, second)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls),
		"a failed probe must also be cached, not retried on every call")
}

// TestIsKeyringAvailable_ForceFileStorageNeverProbes proves the
// forceFileStorage override still short-circuits before touching the
// probe cache at all — the caching change must not alter this existing,
// test-relied-upon contract (setupTestDir forces file storage in nearly
// every other test in this package).
func TestIsKeyringAvailable_ForceFileStorageNeverProbes(t *testing.T) {
	resetKeyringProbeState(t)
	setupTestDir(t) // sets forceFileStorage = true, restores on cleanup

	TestSetKeyringProbeFunc(func() bool {
		t.Fatal("live probe must never run when forceFileStorage is set")
		return false
	})

	assert.False(t, isKeyringAvailable())
}
