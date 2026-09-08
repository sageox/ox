package api

import (
	"bytes"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/updatenotice"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newProcess resets the process-scoped state the real CLI resets by exiting:
// the sync.Once and the captured stderr. Every `ox` command is a fresh process,
// so this is what the second, third, and hundredth invocation actually look
// like — the case the sync.Once alone never covered.
func newProcess(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	oldOut := noticeOut
	noticeOut = &buf
	deprecationShown = sync.Once{}
	t.Cleanup(func() {
		noticeOut = oldOut
		deprecationShown = sync.Once{}
	})
	return &buf
}

// calmTestEnv points the ledger at a temp file and puts a human at the
// terminal, so these tests exercise the cadence rather than the TTY gate.
func calmTestEnv(t *testing.T) {
	t.Helper()
	oldPath, oldTTY := updatenotice.Path, updatenotice.StderrIsTTY
	updatenotice.Path = filepath.Join(t.TempDir(), "version-check.json")
	updatenotice.StderrIsTTY = func() bool { return true }
	updatenotice.SetMachineOutput(false)
	t.Cleanup(func() {
		updatenotice.Path, updatenotice.StderrIsTTY = oldPath, oldTTY
	})
}

func deprecatedResponse() *http.Response {
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}
	resp.Header.Set(HeaderDeprecated, "ox v0.5.0 no longer syncs telemetry — run 'ox upgrade' to restore it")
	return resp
}

// THE regression this ledger exists to close: before it, the deprecation
// warning reprinted on every single `ox` command, forever, because a sync.Once
// scoped to one process dedupes nothing for a CLI.
func TestDeprecationWarning_SecondInvocationStaysQuiet(t *testing.T) {
	calmTestEnv(t)

	first := newProcess(t)
	require.False(t, CheckVersionResponse(deprecatedResponse()))
	assert.Contains(t, first.String(), "Deprecation warning", "first sight of a release line must speak")

	second := newProcess(t)
	require.False(t, CheckVersionResponse(deprecatedResponse()))
	assert.Empty(t, second.String(), "a second invocation inside the cap must stay silent")
}

// The value the server sends is printed verbatim after the existing prefix —
// the server owns the stakes copy, the client owns only the cadence.
func TestDeprecationWarning_PrintsServerCopyVerbatim(t *testing.T) {
	calmTestEnv(t)

	buf := newProcess(t)
	CheckVersionResponse(deprecatedResponse())
	assert.Contains(t, buf.String(),
		"⚠ Deprecation warning: ox v0.5.0 no longer syncs telemetry — run 'ox upgrade' to restore it")
}

// Once a day, not once a command: the cap must lift after NotifyInterval.
func TestDeprecationWarning_SpeaksAgainAfterTheCap(t *testing.T) {
	calmTestEnv(t)

	first := newProcess(t)
	CheckVersionResponse(deprecatedResponse())
	require.NotEmpty(t, first.String())

	// backdate the stamp past the cap, as the passage of a day would
	d := updatenotice.Read()
	require.NotNil(t, d)
	d.LastNaggedAt = time.Now().Add(-updatenotice.NotifyInterval - time.Minute)
	require.NoError(t, updatenotice.Write(d))

	later := newProcess(t)
	CheckVersionResponse(deprecatedResponse())
	assert.Contains(t, later.String(), "Deprecation warning", "the cap must lift after a day")
}

// A newly published release line is news, and news is not capped.
func TestDeprecationWarning_NewReleaseLineSpeaksImmediately(t *testing.T) {
	calmTestEnv(t)

	require.NoError(t, updatenotice.Write(&updatenotice.Data{
		LatestVersion: "v0.16.0", CheckedAt: time.Now(),
	}))

	first := newProcess(t)
	CheckVersionResponse(deprecatedResponse())
	require.NotEmpty(t, first.String())
	require.Equal(t, "0.16", updatenotice.Read().LastNaggedLine)

	// pretend a 0.17 release just landed — one minute after the 0.16 notice
	d := updatenotice.Read()
	d.LatestVersion = "v0.17.0"
	d.LastNaggedAt = time.Now().Add(-time.Minute)
	require.NoError(t, updatenotice.Write(d))

	next := newProcess(t)
	CheckVersionResponse(deprecatedResponse())
	assert.Contains(t, next.String(), "Deprecation warning", "a new release line resets the cap")
	assert.Equal(t, "0.17", updatenotice.Read().LastNaggedLine)
}

// Agent transcripts stay clean: nothing is printed when nobody is watching
// stderr, and nothing is stamped either — the human who runs ox at a terminal
// tomorrow has not been told anything.
func TestDeprecationWarning_SilentWhenStderrIsNotATTY(t *testing.T) {
	calmTestEnv(t)
	updatenotice.StderrIsTTY = func() bool { return false }

	buf := newProcess(t)
	require.False(t, CheckVersionResponse(deprecatedResponse()))
	assert.Empty(t, buf.String(), "an agent-captured stderr must see no notice")
	assert.Nil(t, updatenotice.Read(), "suppressed notices must not consume the ledger")
}

// --json output is parsed by machines; a notice line in it is a parse error.
func TestDeprecationWarning_SilentInMachineOutputMode(t *testing.T) {
	calmTestEnv(t)
	updatenotice.SetMachineOutput(true)
	t.Cleanup(func() { updatenotice.SetMachineOutput(false) })

	buf := newProcess(t)
	require.False(t, CheckVersionResponse(deprecatedResponse()))
	assert.Empty(t, buf.String())
}

// The 426 hard-block path is server-driven and unconditional — it is a failure,
// not a nag, so the calm ledger must not gate it. (The server never sends 426
// today; this pins the behavior so a future ledger change cannot mute it.)
func TestUpgradeRequired_IgnoresTheCalmLedger(t *testing.T) {
	calmTestEnv(t)
	updatenotice.StderrIsTTY = func() bool { return false }
	updatenotice.RecordNotified("0.16", time.Now())

	buf := newProcess(t)
	resp := &http.Response{StatusCode: http.StatusUpgradeRequired, Header: http.Header{}}
	resp.Header.Set(HeaderMinVersion, "0.16.0")

	assert.True(t, CheckVersionResponse(resp))
	assert.Contains(t, buf.String(), "CLI Version No Longer Supported")
}
