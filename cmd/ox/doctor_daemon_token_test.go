package main

import (
	"os"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/daemon/testutil"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: `ox doctor` never reads the running daemon's token and so
// misses the mismatch, or reports one when no daemon runs. A fake daemon
// answers on a socket in a scratch XDG_RUNTIME_DIR: TestMain leaves that at the
// machine's real one, where the real daemons listen.
func TestCheckDaemonToken_ReadsTheRunningDaemon(t *testing.T) {
	t.Setenv("OX_XDG_DISABLE", "")                     // legacy mode would ignore XDG_RUNTIME_DIR
	runtimeDir, err := os.MkdirTemp("/tmp", "ox-tok-") // short: macOS caps a socket path at 104 bytes
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("SAGEOX_ENDPOINT", "https://sageox.ai")
	t.Setenv("SAGEOX_TOKEN", validTeamToken)

	assert.Equal(t, "daemon not running", checkDaemonToken().message)

	d := testutil.NewOxFaultDaemon(t, testutil.OxFaultConfig{StatusResponse: &daemon.StatusData{
		Running: true, EnvToken: "none", GlobalSyncEndpoint: "https://sageox.ai",
	}})
	d.Start()
	t.Cleanup(d.Stop)
	result := checkDaemonToken()
	assert.False(t, result.passed)
	assert.Equal(t, "daemon started without this shell's SAGEOX_TOKEN", result.message)
}

// Failure prevented: a daemon started before this shell's SAGEOX_TOKEN was set,
// or with another one, clones and syncs the Ledger without it while `ox status`
// and `ox doctor` report nothing wrong; or they raise an alarm about a daemon
// they cannot compare.
func TestDaemonTokenMismatch(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("SAGEOX_ENDPOINT", "https://sageox.ai")
	team := gitserver.BearerTokenFingerprint(validTeamToken)
	for _, tc := range []struct {
		name, daemonToken, shellToken, reason, verdict string
	}{
		{"daemon older than the field", "", validTeamToken, "", "skipped"},
		{"malformed token in this shell", "none", malformedTeamToken, "", "skipped"},
		{"same token", team, validTeamToken, "", "passed"},
		{"daemon without this shell's token", "none", validTeamToken, "daemon started without this shell's SAGEOX_TOKEN", "failed"},
		{"daemon with another token", team, validPersonalToken, "daemon started with a different SAGEOX_TOKEN than this shell's", "failed"},
		{"daemon with a token this shell lacks", team, "", "daemon started with a SAGEOX_TOKEN this shell lacks", "warning"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SAGEOX_TOKEN", tc.shellToken)
			ds := &daemon.StatusData{Running: true, EnvToken: tc.daemonToken, GlobalSyncEndpoint: "https://sageox.ai"}
			reason, remedy, _ := daemonTokenMismatch(ds)
			assert.Equal(t, tc.reason, reason)

			result := daemonTokenResult(ds)
			verdict := "passed"
			switch {
			case result.skipped:
				verdict = "skipped"
			case !result.passed:
				verdict = "failed"
			case result.warning:
				verdict = "warning"
			}
			assert.Equal(t, tc.verdict, verdict, "ox doctor")

			out := renderDaemonSyncSection(ds, nil, &config.LocalConfig{}, false, true)
			if tc.reason == "" {
				assert.NotContains(t, out, "SAGEOX_TOKEN", "ox status")
			} else {
				assert.Contains(t, out, tc.reason, "ox status")
				assert.Contains(t, out, remedy, "ox status")
			}
		})
	}
}
