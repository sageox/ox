package main

import (
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/ledger"
	oxruntime "github.com/sageox/ox/internal/runtime"
	"github.com/sageox/ox/internal/session"
)

// Two failures a real Amp orb showed after the agentx detection fix
// (sageox/ox#1425): the session-end push failed with nothing to retry it
// before the orb paused, and the orb's first prime ran before the Ledger was
// cloned, so it never recorded.

func TestUploadSessionBeforeHostEnds(t *testing.T) {
	transient := errors.New("commit and push: git push: ! [rejected] main -> main (fetch first)")
	expired := errors.New("LFS upload: HTTP 401 from the LFS server")

	for _, tc := range []struct {
		name           string
		daemonOutlives bool
		results        []error // what each attempt returns, in order
		wantCalls      int
		wantErr        error
	}{
		{"first attempt succeeds", false, []error{nil}, 1, nil},
		{"laptop: the daemon retries, not the stop", true, []error{transient}, 1, transient},
		{"orb: a failed push is retried and lands", false, []error{transient, nil}, 2, nil},
		{"orb: three attempts at most", false, []error{transient, transient, transient, nil}, 3, transient},
		{"orb: read-only access is never retried", false, []error{api.ErrReadOnly}, 1, api.ErrReadOnly},
		{"orb: expired credentials are never retried", false, []error{expired}, 1, expired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var slept []time.Duration
			err := uploadSessionBeforeHostEnds("2026-10-20T00-10-avery-OxTest1", tc.daemonOutlives,
				func() error { calls++; return tc.results[calls-1] },
				func(d time.Duration) { slept = append(slept, d) })

			if tc.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.wantErr)
			}
			assert.Equal(t, tc.wantCalls, calls, "upload attempts")
			require.Len(t, slept, tc.wantCalls-1, "one pause before each retry")
			for i, d := range slept {
				base := sessionUploadRetryWaits[i]
				assert.GreaterOrEqual(t, d, base/2, "pause %d below its jitter floor", i)
				assert.Less(t, d, base/2+base, "pause %d above its jitter ceiling", i)
			}
		})
	}
}

// TestSessionUploadRetries_FitInsideTheHookWait pins the budget: every pause
// at its jitter ceiling must leave the stop most of the SessionEnd hook's wait
// for the uploads themselves.
func TestSessionUploadRetries_FitInsideTheHookWait(t *testing.T) {
	var worst time.Duration
	for _, base := range sessionUploadRetryWaits {
		worst += base/2 + base
	}
	assert.Less(t, worst, sessionEndStopWait/4)
}

// TestRunSessionStopWith_FailedUploadKeepsTheReason: the stop exits 0 on a
// failed upload and says why only on stderr. In the orb that reason was lost
// when the hook deleted its temp files; the hook log must carry it.
func TestRunSessionStopWith_FailedUploadKeepsTheReason(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("a shell script stands in for ox")
	}
	dir := t.TempDir()
	printed := filepath.Join(dir, "printed")
	require.NoError(t, os.WriteFile(printed, []byte(
		"{\n  \"success\": true,\n  \"upload_warning\": \"Session saved locally but ledger upload failed. Run 'ox doctor' to retry.\"\n}\n"), 0o600))
	exe := filepath.Join(dir, "ox")
	script := "#!/bin/sh\ncat '" + printed + "'\n" +
		"echo 'warning: LFS upload failed (session saved locally): commit and push: git push rejected (fetch first)' >&2\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(exe, []byte(script), 0o755))

	err := runSessionStopWith(exe, t.TempDir(), "OxAb12", time.Minute)

	require.ErrorIs(t, err, errSessionUploadFailed)
	assert.Contains(t, err.Error(), "git push rejected (fetch first)", "the hook log must say why the upload failed")
}

func TestStartSessionRecording_OrbRecordsBeforeTheLedgerIsCloned(t *testing.T) {
	for _, tc := range []struct {
		name          string
		env           map[string]string
		wantRecording bool
	}{
		{"laptop: waits for the clone, as before", nil, false},
		{"Amp orb: records now; the clone adopts the cache", map[string]string{"AMP_ORB": "1"}, true},
		{"Amp orb with the daemon off: nothing would ever clone", map[string]string{"AMP_ORB": "1", "SAGEOX_DAEMON": "false"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectRoot, _ := setupTestProject(t)
			t.Chdir(projectRoot)
			clearFinalizeEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			oxruntime.Reset()
			require.False(t, ledger.Exists(""), "precondition: the Ledger is not cloned yet")
			const agentID = "OxOrbFirst"

			stat := startSessionRecording(projectRoot, agentID, "amp", "", "", "https://ampcode.com/threads/T-first-prime")

			require.NotNil(t, stat)
			assert.Equal(t, tc.wantRecording, stat.Recording, "recording")
			assert.Equal(t, !tc.wantRecording, stat.LedgerNeeded, "ledger needed")
			state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
			require.NoError(t, err)
			assert.Equal(t, tc.wantRecording, state != nil, "a live recording exists")
		})
	}
}
