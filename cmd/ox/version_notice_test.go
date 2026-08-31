package main

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/updatenotice"
	"github.com/sageox/ox/internal/version"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// atTerminal puts a human at stderr and clears machine-output mode, so a test
// exercises the cadence rather than the audience gate. Neither is true under
// `go test`, where stderr is a pipe.
func atTerminal(t *testing.T) {
	t.Helper()
	old := updatenotice.StderrIsTTY
	updatenotice.StderrIsTTY = func() bool { return true }
	updatenotice.SetMachineOutput(false)
	t.Cleanup(func() { updatenotice.StderrIsTTY = old })
}

// The calm "update available" line is the surface a coworker sees dozens of
// times a day. First sight speaks; everything inside the cap is silent.
func TestCalmUpdateNoticeDue_SpeaksOnceThenStaysQuiet(t *testing.T) {
	useTestCacheDir(t)
	atTerminal(t)
	writeTestVersionCache(t, &versionCacheData{
		LatestVersion: "v99.0.0",
		CheckedAt:     time.Now(),
	})

	now := time.Now()
	line, due := calmUpdateNoticeDue(now)
	require.True(t, due, "first sight of a release line must speak")
	assert.Equal(t, "99.0", line)
	updatenotice.RecordNotified(line, now)

	_, due = calmUpdateNoticeDue(now.Add(1 * time.Hour))
	assert.False(t, due, "an hour later, same line — stay quiet")
	_, due = calmUpdateNoticeDue(now.Add(updatenotice.NotifyInterval - time.Minute))
	assert.False(t, due, "still inside the cap — stay quiet")

	_, due = calmUpdateNoticeDue(now.Add(updatenotice.NotifyInterval + time.Minute))
	assert.True(t, due, "past the cap — speak again")
}

// A newly published release line is news; news is never capped.
func TestCalmUpdateNoticeDue_NewReleaseLineResetsTheCap(t *testing.T) {
	useTestCacheDir(t)
	atTerminal(t)
	now := time.Now()

	writeTestVersionCache(t, &versionCacheData{LatestVersion: "v98.0.0", CheckedAt: now})
	line, due := calmUpdateNoticeDue(now)
	require.True(t, due)
	require.Equal(t, "98.0", line)
	updatenotice.RecordNotified(line, now)

	// 99.0 ships one minute later — the cap must not swallow the announcement
	writeTestVersionCache(t, &versionCacheData{
		LatestVersion:  "v99.0.0",
		CheckedAt:      now,
		LastNaggedLine: "98.0",
		LastNaggedAt:   now,
	})
	line, due = calmUpdateNoticeDue(now.Add(time.Minute))
	assert.True(t, due, "a new release line must speak immediately")
	assert.Equal(t, "99.0", line)

	// a patch on the SAME line is not news
	writeTestVersionCache(t, &versionCacheData{
		LatestVersion:  "v99.0.1",
		CheckedAt:      now,
		LastNaggedLine: "99.0",
		LastNaggedAt:   now,
	})
	_, due = calmUpdateNoticeDue(now.Add(time.Minute))
	assert.False(t, due, "a patch release on a line we already announced is not news")
}

// The calm tier must never reach an agent transcript or a machine-output
// stream — and a suppressed notice must not consume the day's budget, or the
// human who runs ox at a terminal later gets told nothing.
func TestCalmUpdateNoticeDue_SilentForAgentsAndMachineOutput(t *testing.T) {
	useTestCacheDir(t)
	writeTestVersionCache(t, &versionCacheData{LatestVersion: "v99.0.0", CheckedAt: time.Now()})
	now := time.Now()

	oldTTY := updatenotice.StderrIsTTY
	t.Cleanup(func() {
		updatenotice.StderrIsTTY = oldTTY
		updatenotice.SetMachineOutput(false)
	})

	// stderr captured by a coding agent
	updatenotice.StderrIsTTY = func() bool { return false }
	updatenotice.SetMachineOutput(false)
	_, due := calmUpdateNoticeDue(now)
	assert.False(t, due, "an agent-captured stderr must see no calm notice")

	// --json / --text
	updatenotice.StderrIsTTY = func() bool { return true }
	updatenotice.SetMachineOutput(true)
	_, due = calmUpdateNoticeDue(now)
	assert.False(t, due, "machine-output mode must see no calm notice")

	// nothing was stamped, so a human at a terminal still gets told
	assert.True(t, readVersionCache().LastNaggedAt.IsZero(), "suppression must not stamp the ledger")
	assert.Empty(t, readVersionCache().LastNaggedLine)
	updatenotice.SetMachineOutput(false)
	_, due = calmUpdateNoticeDue(now)
	assert.True(t, due, "suppressed notices must not consume the ledger")
}

// `ox agent prime` is the designated once-carrier: its structured output
// deliberately bypasses the TTY gate (prime IS machine output) but still obeys
// the cadence, so an agent hears about an upgrade once a day, not every prime.
func TestUpdateNoticeDue_PrimeIgnoresTTYButObeysTheCap(t *testing.T) {
	useTestCacheDir(t)
	writeTestVersionCache(t, &versionCacheData{LatestVersion: "v99.0.0", CheckedAt: time.Now()})

	oldTTY := updatenotice.StderrIsTTY
	updatenotice.StderrIsTTY = func() bool { return false }
	updatenotice.SetMachineOutput(true)
	t.Cleanup(func() {
		updatenotice.StderrIsTTY = oldTTY
		updatenotice.SetMachineOutput(false)
	})

	now := time.Now()
	line, due := updateNoticeDue(now)
	require.True(t, due, "prime carries the fact even with no TTY and machine output on")
	updatenotice.RecordNotified(line, now)

	_, due = updateNoticeDue(now.Add(time.Hour))
	assert.False(t, due, "the second prime an hour later must stay quiet")
}

// A routine version refresh must not erase the ledger — if it did, every
// `ox status`/`ox doctor` GitHub check would restore the per-command nag.
func TestWriteVersionCacheFromDoctor_PreservesLedger(t *testing.T) {
	useTestCacheDir(t)
	stamped := time.Now().Add(-time.Hour)
	writeTestVersionCache(t, &versionCacheData{
		LatestVersion:  "v0.15.0",
		CheckedAt:      stamped,
		ETag:           `"preserve-me"`,
		LastNaggedLine: "0.15",
		LastNaggedAt:   stamped,
	})

	writeVersionCacheFromDoctor("v0.15.1")

	cached := readVersionCache()
	require.NotNil(t, cached)
	assert.Equal(t, "v0.15.1", cached.LatestVersion)
	assert.Equal(t, `"preserve-me"`, cached.ETag)
	assert.Equal(t, "0.15", cached.LastNaggedLine, "a version refresh must not erase the ledger")
	assert.WithinDuration(t, stamped, cached.LastNaggedAt, time.Second)
}

// After a successful `ox upgrade` the ledger must be gone, so the FIRST notice
// about the NEXT release line is not swallowed by a stale cap.
func TestClearVersionCacheAfterUpgrade_ClearsLedger(t *testing.T) {
	useTestCacheDir(t)
	atTerminal(t)
	now := time.Now()
	writeTestVersionCache(t, &versionCacheData{
		LatestVersion:  "v99.0.0",
		CheckedAt:      now,
		LastNaggedLine: "99.0",
		LastNaggedAt:   now,
	})
	_, due := calmUpdateNoticeDue(now)
	require.False(t, due, "precondition: capped before the upgrade")

	clearVersionCacheAfterUpgrade()

	assert.Nil(t, readVersionCache(), "upgrade must drop the cache, ledger included")
	line, due := calmUpdateNoticeDue(now)
	assert.True(t, due, "the next release line must be announceable after an upgrade")
	assert.Equal(t, updatenotice.ReleaseLine(version.Version), line,
		"with no cache, the ledger keys on the running binary's own line")
}

// The calm tier's copy is a client-owned contract; the urgent tier's is the
// server's header value, printed verbatim.
func TestFormatCalmUpdateNotice(t *testing.T) {
	t.Parallel()
	assert.Equal(t,
		"→ ox 0.16.0 available (you're on 0.14.3) — run 'ox upgrade'",
		formatCalmUpdateNotice("0.16.0", "0.14.3"))
}
