package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/lfs"
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
			// the env layer outranks every config file, so neither a developer's
			// user config nor an inherited value can turn recording off here
			t.Setenv(config.EnvSessionRecording, config.SessionRecordingAuto)
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

// TestSessionEnd_AmpOrbUploadsATurnWithoutItsPrompt reproduces the real orb:
// prime starts recording after the thread's prompt, so the bridge sidecar the
// stop reads holds Amp's reply and no user message. The local summary is then
// only a stats line, which meta.json's writer rejected as a leaked failure
// stub, so every such upload failed. It must land, with an empty summary.
func TestSessionEnd_AmpOrbUploadsATurnWithoutItsPrompt(t *testing.T) {
	f := newOrbSessionFixture(t)
	t.Setenv("AMP_ORB", "1")
	state, err := session.LoadRecordingStateForAgent(f.projectRoot, f.agentID)
	require.NoError(t, err)
	require.NotNil(t, state)
	reply, err := json.Marshal(map[string]string{
		"type": "assistant", "content": orbReply,
		"timestamp": state.StartedAt.UTC().Add(2 * time.Second).Format(time.RFC3339Nano),
	})
	require.NoError(t, err)
	sidecar := filepath.Join(os.Getenv("HOME"), ".cache", "amp", "ox-sessions", nativeThreadKey(orbThreadURL)+".jsonl")
	require.NoError(t, os.WriteFile(sidecar, append(reply, '\n'), 0o600))

	f.sendSessionEnd(t)

	dirs := remoteSessionDirs(t, f.barePath)
	require.Len(t, dirs, 1, "a turn without its prompt must still reach the Ledger before the orb pauses")
	var meta lfs.SessionMeta
	require.NoError(t, json.Unmarshal([]byte(runGit(t, f.barePath, "show", "HEAD:sessions/"+dirs[0]+"/meta.json")), &meta))
	assert.Empty(t, meta.Summary, "a stats-only summary must not be published as the session's summary")
}

func TestPublishableSummary(t *testing.T) {
	assert.Equal(t, "", publishableSummary("0 user messages, 1 assistant responses. 3 tool calls. Tools: Bash"))
	assert.Equal(t, "", publishableSummary(""))
	withTopic := "Rename the retry helper\n\n1 user messages, 1 assistant responses"
	assert.Equal(t, withTopic, publishableSummary(withTopic), "a summary that names its topic is kept")
}
