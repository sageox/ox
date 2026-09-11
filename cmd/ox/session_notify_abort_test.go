package main

import (
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/require"
)

// TestNotifySessionAbortedAsync_GatesOnPriorRegistration covers the narrow but
// real seam left by manual-mode suppression.
//
// Manual publishing suppresses start-registration, so a purely manual session is
// unknown to the server. If aborting it still POSTed, that abort would be the
// FIRST thing the server ever heard about the session — a leak of exactly the
// kind manual mode promises to prevent, and an especially perverse one, since
// the user reached it by trying to throw the session away.
//
// The converse matters just as much and pulls the other way: a session that DID
// register (started under auto, flipped to manual later) has already published.
// Skipping its abort would leave a stale "in progress" /c/ page up forever for a
// session the user explicitly discarded — a worse privacy outcome than the two
// opaque ids the abort call carries. So the gate is "was this ever registered",
// not "is publishing manual".
func TestNotifySessionAbortedAsync_GatesOnPriorRegistration(t *testing.T) {
	t.Run("never registered: stays silent", func(t *testing.T) {
		projectRoot, requestCount := newNotifyManualFixture(t, config.SessionPublishingManual)

		notifySessionAbortedAsync(projectRoot, "ses_01920000-0000-7000-8000-00000000ab01", false)

		require.Zero(t, atomic.LoadInt32(requestCount),
			"a session the server never knew about must not be announced by its own abort")
	})

	t.Run("previously registered: tombstones anyway", func(t *testing.T) {
		// Manual publishing here too, to pin the rule: the gate keys on prior
		// registration, NOT on the current publishing mode. A session that
		// registered under auto and was flipped to manual must still be
		// tombstoned, or its in-progress page outlives the session forever.
		projectRoot, requestCount := newNotifyManualFixture(t, config.SessionPublishingManual)

		notifySessionAbortedAsync(projectRoot, "ses_01920000-0000-7000-8000-00000000ab02", true)

		require.Equal(t, int32(1), atomic.LoadInt32(requestCount),
			"an already-registered session must be tombstoned even under manual publishing")
	})

	t.Run("empty session id: nothing to tombstone", func(t *testing.T) {
		projectRoot, requestCount := newNotifyManualFixture(t, config.SessionPublishingAuto)

		notifySessionAbortedAsync(projectRoot, "", true)

		require.Zero(t, atomic.LoadInt32(requestCount), "no id means no request")
	})
}
